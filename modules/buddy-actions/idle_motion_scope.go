package buddyactions

import (
	"bytes"
	"context"
	"errors"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type idleMotionScopeKey struct{}

// Latest mechanical source for an already-owned idle lease, not a movement
// controller or operation ledger. Only actual known-written reports advance it.
type idleMotionScope struct {
	binding                                          idleInteractionBinding
	initial, current                                 *modv1.TransportScope
	rawPose, pose                                    position
	rawRevision, clientRevision, rawMovementRevision uint64
	mapID                                            uint32
	cleanupOnly, broken                              bool
	writes                                           uint64
	lastOpcode                                       uint32
	lastPayload                                      []byte
}

func idleMotionOpcode(op uint32) bool {
	return op == opcode.MSGMoveSetFacing || op == opcode.MSGMoveStartForward || op == opcode.MSGMoveHeartbeat || op == opcode.MSGMoveJump || op == opcode.MSGMoveFallLand || op == opcode.MSGMoveStop
}

func idleMotionPacketPose(payload []byte, self uint64) (position, bool) {
	id, n, err := readGUID(payload)
	if err != nil || id != self {
		return position{}, false
	}
	i, used, err := movementinfo.DecodePrefix(payload[n:])
	if err != nil || n+used != len(payload) || !i.AllFinite() {
		return position{}, false
	}
	return position{i.X, i.Y, i.Z, i.Orientation}, true
}

func (s *Service) idleMotionCurrentLocked(b *idleMotionScope, w *worldv1.SnapshotResponse, now time.Time) bool {
	if b == nil || b.broken || s.closed || s.sessionID != b.binding.sessionID || s.selfGUID != b.binding.selfGUID || s.idleInteraction != b.binding ||
		s.state.InteractionOwner != "exploration" || s.interactionToken != b.binding.token || s.state.InteractionHarvest ||
		!s.encounterVisit.bound || s.encounterVisit.revision != b.binding.visit || !s.matchesCurrentEncounterVisitLocked(w) || w.GetVisitRevision() != b.binding.visit ||
		!b.cleanupOnly && s.interactionUntil.Sub(now) < time.Second || s.clientControlLost || s.clientControlRestoring || s.state.MotionUnconfirmed ||
		!s.posKnown || !finite(s.pos) || s.pos != b.pose || s.posRevision != b.clientRevision || s.positionRevision != b.rawRevision || s.posMap != b.mapID ||
		w.GetSelf().GetPositionRevision() != b.rawRevision || playerPosition(w.GetSelf()) != b.rawPose || safeSelfCombat(w, s.selfGUID) != "" {
		return false
	}
	observed := portalObservedWorldScope(w, s.selfGUID)
	return samePortalCoreVisit(b.initial, observed) && samePortalCoreVisit(b.initial, b.current) && observed.MotionRevision >= b.initial.MotionRevision && observed.MotionRevision <= b.current.MotionRevision
}

func (s *Service) newIdleMotionScopeLocked(ctx context.Context, token string, w *worldv1.SnapshotResponse, cleanupOnly bool) (*idleMotionScope, error) {
	b := &idleMotionScope{binding: s.idleInteraction, initial: clonePortalScope(portalObservedWorldScope(w, s.selfGUID)),
		rawPose: playerPosition(w.GetSelf()), pose: s.pos, rawRevision: s.positionRevision, clientRevision: s.posRevision,
		rawMovementRevision: w.GetSelfMovementFacts().GetRevision(), mapID: s.posMap, cleanupOnly: cleanupOnly}
	b.current = clonePortalScope(b.initial)
	if token == "" || token != b.binding.token || b.clientRevision == 0 || b.initial == nil {
		return nil, modcb.PacketNotDispatchedError("idle motion source mapping unavailable")
	}
	prior, err := modcb.ReadOwnMovementReport(ctx, s.session, s.sessionID, s.selfGUID)
	needPose := s.posEstimated || w.GetSelf().GetPositionInferred()
	if err == nil {
		p, valid := idleMotionPacketPose(prior.Payload, s.selfGUID)
		if !valid || !idleMotionOpcode(prior.Opcode) || !samePortalCoreVisit(b.initial, prior.TransportScope) || prior.TransportScope.MotionRevision < b.initial.MotionRevision || needPose && p != b.pose {
			return nil, modcb.PacketNotDispatchedError("idle motion prior report no longer matches the captured body")
		}
		b.current = clonePortalScope(prior.TransportScope)
	} else if status.Code(err) != codes.FailedPrecondition || needPose {
		return nil, modcb.PacketNotDispatchedError("idle motion current prior report unavailable")
	}
	fresh, err := s.readWorldSnapshot(ctx)
	now := s.now()
	if err != nil || ctx.Err() != nil || !s.idleMotionCurrentLocked(b, fresh, now) {
		return nil, modcb.PacketNotDispatchedError("idle motion custody or parsed body changed")
	}
	s.idleMotion = b
	return b, nil
}

func (s *Service) idleMotionBeforeWriteLocked(ctx context.Context, b *idleMotionScope, op uint32, payload []byte) (*modv1.TransportScope, error) {
	if !idleMotionOpcode(op) || b.cleanupOnly && op != opcode.MSGMoveStop {
		return nil, modcb.PacketNotDispatchedError("packet outside idle motion scope")
	}
	if _, valid := idleMotionPacketPose(payload, b.binding.selfGUID); !valid {
		return nil, modcb.PacketNotDispatchedError("idle motion packet is not a full own movement image")
	}
	w, err := s.readWorldSnapshot(ctx)
	now := s.now()
	if err != nil || ctx.Err() != nil || b.clientRevision == ^uint64(0) || !s.idleMotionCurrentLocked(b, w, now) {
		return nil, modcb.PacketNotDispatchedError("idle motion custody or source changed before write")
	}
	return clonePortalScope(b.current), nil
}

func (s *Service) idleMotionAfterWriteLocked(ctx context.Context, b *idleMotionScope, op uint32, payload []byte) error {
	b.writes++
	b.lastOpcode, b.lastPayload = op, append([]byte(nil), payload...)
	r, err := modcb.ReadOwnMovementReport(ctx, s.session, s.sessionID, s.selfGUID)
	w, worldErr := s.readWorldSnapshot(ctx)
	now := s.now()
	post := *b
	post.current = r.GetTransportScope()
	if err != nil || worldErr != nil || ctx.Err() != nil || r.GetOpcode() != op || !bytes.Equal(r.GetPayload(), payload) ||
		!samePortalCoreVisit(b.initial, r.GetTransportScope()) || r.GetTransportScope().GetMotionRevision() <= b.current.MotionRevision ||
		!s.idleMotionCurrentLocked(&post, w, now) {
		b.broken = true
		return errors.New("idle motion known write lacks its exact current post-write source report")
	}
	observed := portalObservedWorldScope(w, s.selfGUID)
	if observed.MotionRevision > r.TransportScope.MotionRevision {
		b.broken = true
		return errors.New("idle motion parsed source superseded the written report")
	}
	p, valid := idleMotionPacketPose(payload, b.binding.selfGUID)
	if !valid {
		b.broken = true
		return errors.New("idle motion written packet could not be retained")
	}
	b.current, b.pose, b.clientRevision = clonePortalScope(r.TransportScope), p, b.clientRevision+1
	return nil
}

// A later actual stationary image may retire motion uncertainty, but never the
// Lua/native operation's UNKNOWN receipt. No packet or freshened pose is made.
func (s *Service) observeIdleMotionStoppedLocked(ctx context.Context) bool {
	b := s.idleMotion
	if b == nil || s.idleInteraction != b.binding || s.sessionID != b.binding.sessionID || s.selfGUID != b.binding.selfGUID || s.interactionToken != b.binding.token || s.state.InteractionOwner != "exploration" {
		return false
	}
	w, err := s.readWorldSnapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) {
		return false
	}
	raw := w.GetSelfMovementFacts()
	actual := raw.GetInfo()
	if raw.GetKnown() && raw.GetSelfGuid() == s.selfGUID && raw.GetVisitRevision() == w.VisitRevision && raw.GetRevision() > b.rawMovementRevision &&
		!w.GetSelf().GetPositionInferred() && w.GetSelf().GetPositionKnown() && actual != nil && movementflags.GroundCompatible(actual.Flags, actual.ExtraFlags) && actual.Flags&0x1ff == 0 &&
		raw.GetMapKnown() && raw.MapId == w.Self.MapId && samePortalCoreVisit(raw.TransportScope, portalObservedWorldScope(w, s.selfGUID)) && raw.TransportScope.ConnectionId == b.initial.ConnectionId && raw.TransportScope.MotionRevision > b.initial.MotionRevision &&
		(position{actual.X, actual.Y, actual.Z, actual.Orientation}) == playerPosition(w.Self) {
		s.refreshPositionLocked(w)
		s.lastWorld = w
		s.state.Moving, s.state.MotionUnconfirmed = false, false
		return true
	}
	if b.lastOpcode == opcode.MSGMoveStop && len(b.lastPayload) > 0 {
		r, err := modcb.ReadOwnMovementReport(ctx, s.session, s.sessionID, s.selfGUID)
		fresh, freshErr := s.readWorldSnapshot(ctx)
		if err == nil && freshErr == nil && ctx.Err() == nil && s.idleInteraction == b.binding && s.interactionToken == b.binding.token &&
			r.Opcode == opcode.MSGMoveStop && bytes.Equal(r.Payload, b.lastPayload) && samePortalCoreVisit(b.initial, r.TransportScope) &&
			samePortalCoreVisit(b.initial, portalObservedWorldScope(fresh, s.selfGUID)) {
			p, valid := idleMotionPacketPose(r.Payload, s.selfGUID)
			if valid && p == s.pos && s.positionRevision == fresh.Self.PositionRevision && fresh.VisitRevision == b.binding.visit {
				s.lastWorld = fresh
				s.state.Moving, s.state.MotionUnconfirmed = false, false
				return true
			}
		}
	}
	return false
}
