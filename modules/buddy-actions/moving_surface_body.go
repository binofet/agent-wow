package buddyactions

import (
	"bytes"
	"context"
	"errors"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"google.golang.org/protobuf/proto"
)

type surfaceOwnPacketKey struct{}

// A current exact physical write is the client's own input state. The realm
// normally rebroadcasts it to peers, excluding this sender. Keep it distinct
// from received World observations, and never let it beat a later own pose.
func (s *Service) currentSurfaceReportLocked(ctx context.Context, w *worldv1.SnapshotResponse) (*modv1.WorldPacket, error) {
	b := s.surfaceFollow.body
	if b == nil || b.uncertain || b.packet == nil || s.surfaceFollow.lastAttempt == "" ||
		!s.matchesCurrentEncounterVisitLocked(w) || b.visit != w.VisitRevision ||
		b.rawPositionRevision != w.GetSelf().GetPositionRevision() {
		return nil, errors.New("current exact own surface frame required")
	}
	if b.info.TransportPresent {
		hull := movingHull(w, b.info.Transport.GUID)
		if hull == nil || b.hullObjectRevision == 0 || hull.MovingTransport.ObjectRevision != b.hullObjectRevision {
			return nil, errors.New("written passenger belongs to a replaced or absent hull")
		}
	}
	current, err := modcb.ReadBodyInputReport(ctx, s.session, s.sessionID, s.selfGUID, s.surfaceFollow.lastAttempt, true)
	if err != nil {
		return nil, err
	}
	if current.Opcode != b.packet.Opcode || !bytes.Equal(current.Payload, b.packet.Payload) ||
		!samePortalCoreVisit(current.TransportScope, b.packet.TransportScope) ||
		!samePortalCoreVisit(current.TransportScope, portalObservedWorldScope(w, s.selfGUID)) {
		return nil, errors.New("own surface input was superseded by a different physical writer or body")
	}
	return current, nil
}

func (s *Service) recordSurfaceFrameLocked(ctx context.Context, w *worldv1.SnapshotResponse, op uint32, payload []byte, attempt string) error {
	info, valid := movementinfo.OwnFrame(payload, s.selfGUID)
	scope := portalObservedWorldScope(w, s.selfGUID)
	if !valid || !modcb.ValidMovementAttempt(attempt) || !s.matchesCurrentEncounterVisitLocked(w) || w.GetSelf().GetPositionRevision() == 0 ||
		scope == nil {
		return errors.New("complete written own movement and its attempt required")
	}
	var hullRevision uint64
	if info.TransportPresent {
		hull := movingHull(w, info.Transport.GUID)
		if hull == nil || hull.MovingTransport.ObjectRevision == 0 {
			return errors.New("written passenger requires its original observed hull lifetime")
		}
		hullRevision = hull.MovingTransport.ObjectRevision
	}
	s.surfaceFollow.body = &movingSurfaceBody{visit: w.VisitRevision, rawPositionRevision: w.Self.PositionRevision,
		hullObjectRevision: hullRevision, info: info, packet: &modv1.WorldPacket{Opcode: op, Payload: append([]byte(nil), payload...), TransportScope: proto.Clone(scope).(*modv1.TransportScope)}}
	s.surfaceFollow.lastAttempt = attempt
	return nil
}

func (s *Service) surfaceMovementWorldLocked(ctx context.Context, raw *worldv1.SnapshotResponse) *worldv1.SnapshotResponse {
	b := s.surfaceFollow.body
	if b == nil || b.uncertain || raw.GetSelf().GetPositionRevision() != b.rawPositionRevision || raw.VisitRevision != b.visit ||
		raw.GetSelf().GetMovementFlags() == b.info.Flags && raw.GetSelf().GetExtraMovementFlags() == uint32(b.info.ExtraFlags) {
		return raw
	}
	if _, err := s.currentSurfaceReportLocked(ctx, raw); err != nil {
		return raw
	}
	// Native movement admission sees the verified flags it actually wrote.
	// Raw coordinates, ages, revisions, unit flags, passenger observations and
	// the public buddy-world snapshot all retain their received provenance.
	result := proto.Clone(raw).(*worldv1.SnapshotResponse)
	result.Self.MovementFlagsKnown = true
	result.Self.MovementFlags, result.Self.ExtraMovementFlags = b.info.Flags, uint32(b.info.ExtraFlags)
	result.Self.MovementTime = b.info.Time
	return result
}

// This variant is only used under the native actuator's existing mutex.
// The unlocked public encounter feed continues to read the raw World frame.
func (s *Service) readMovementWorldSnapshotLocked(ctx context.Context, guid ...uint64) (*worldv1.SnapshotResponse, error) {
	w, err := s.readWorldSnapshot(ctx, guid...)
	if err != nil {
		return nil, err
	}
	return s.surfaceMovementWorldLocked(ctx, w), nil
}
