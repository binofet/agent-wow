package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
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
	"google.golang.org/protobuf/proto"
)

type portalStopGuardKey struct{}
type portalStopGuard func(*worldv1.SnapshotResponse, position) bool
type portalStopAdmission struct {
	guard portalStopGuard
	scope *modv1.TransportScope
}

// A physical entry proposal contains no owner location or gameplay decision.
// Each caller supplies its own immutable intent/custody admission predicate.
type areaTriggerProposal struct {
	sessionID                   string
	self, visit                 uint64
	pose, rawPose               position
	mapID                       uint32
	clientRevision, rawRevision uint64
	worldScope                  *modv1.TransportScope
}

// World and Core count visits independently. This is an actual parsed packet
// envelope tagged with World's own visit; spline-authority Known is irrelevant
// to ordinary ground movement. Never turn a later report into this mapping.
func portalObservedWorldScope(w *worldv1.SnapshotResponse, self uint64) *modv1.TransportScope {
	a, p := w.GetSelfMotionAuthority(), w.GetSelf()
	scope := a.GetTransportScope()
	if self == 0 || w.GetVisitRevision() == 0 || a.GetSelfGuid() != self || a.GetVisitRevision() != w.GetVisitRevision() ||
		p.GetGuid() != self || !p.GetPresent() || !p.GetMapKnown() || scope == nil || scope.ConnectionId == "" ||
		scope.SelfGuid != self || !scope.VisitBound || scope.VisitRevision == 0 || scope.MotionRevision == 0 || scope.MapId != p.MapId {
		return nil
	}
	return scope
}

func clonePortalScope(scope *modv1.TransportScope) *modv1.TransportScope {
	if scope == nil {
		return nil
	}
	return proto.Clone(scope).(*modv1.TransportScope)
}

func samePortalCoreVisit(a, b *modv1.TransportScope) bool {
	return a != nil && b != nil && a.ConnectionId != "" && a.ConnectionId == b.ConnectionId && a.SelfGuid != 0 &&
		a.SelfGuid == b.SelfGuid && a.VisitBound && b.VisitBound && a.VisitRevision != 0 && a.VisitRevision == b.VisitRevision && a.MapId == b.MapId
}

func areaTriggerWorldCurrent(p areaTriggerProposal, w *worldv1.SnapshotResponse) bool {
	current := portalObservedWorldScope(w, p.self)
	return samePortalCoreVisit(p.worldScope, current) && current.MotionRevision >= p.worldScope.MotionRevision
}

func areaTriggerReportWorldMatches(p areaTriggerProposal, w *worldv1.SnapshotResponse, report *modv1.WorldPacket) bool {
	current, scope := portalObservedWorldScope(w, p.self), report.GetTransportScope()
	return samePortalCoreVisit(p.worldScope, scope) && samePortalCoreVisit(current, scope) &&
		p.worldScope.MotionRevision <= scope.MotionRevision && current.MotionRevision <= scope.MotionRevision
}

// This last admission is pure: all clock/World/policy callbacks have returned.
func (s *Service) portalIntentCurrentAtLocked(intent portalIntent, w *worldv1.SnapshotResponse, now time.Time) bool {
	host := s.hostEncounterKey != "" && !s.closed && s.encounterVisit.bound && s.hostEncounterVisit != 0 &&
		s.hostEncounterVisit == s.encounterVisit.revision && now.Before(s.hostEncounterUntil)
	if !s.portalSourceWitnessCurrentLocked(w) || s.followPortal.notified || intent.sessionID != s.sessionID ||
		intent.self != s.selfGUID || intent.visit != s.followPortal.sourceVisit || intent.follow != s.followRevision ||
		intent.witness != s.followPortal.witness || intent.hostEpoch != s.hostEncounterEpoch || intent.host != host {
		return false
	}
	if !intent.finite {
		return s.legacyPortalIntentLocked(now)
	}
	g := intent.goal
	return host && g.revision != 0 && samePortalGoal(g, s.portalGoal) && g.sessionID == s.sessionID &&
		g.self == s.selfGUID && g.hostEpoch == s.hostEncounterEpoch && g.visit == s.encounterVisit.revision &&
		g.visit == w.GetVisitRevision() && g.follow == s.followRevision && g.witness == s.followPortal.witness && now.Before(g.until)
}

func (s *Service) areaTriggerProposalLocked(w *worldv1.SnapshotResponse) areaTriggerProposal {
	return areaTriggerProposal{sessionID: s.sessionID, self: s.selfGUID, visit: w.GetVisitRevision(),
		pose: s.pos, rawPose: playerPosition(w.GetSelf()), mapID: s.posMap, clientRevision: s.posRevision,
		rawRevision: s.positionRevision, worldScope: clonePortalScope(portalObservedWorldScope(w, s.selfGUID))}
}

func (s *Service) areaTriggerPoseCurrentLocked(ctx context.Context, p areaTriggerProposal, w *worldv1.SnapshotResponse, revision uint64, now time.Time) bool {
	return s.areaTriggerPhysicalPoseCurrentLocked(ctx, p, w, revision, now, safeQuestCharacter(w, p.self) == "")
}

func (s *Service) areaTriggerPhysicalPoseCurrentLocked(ctx context.Context, p areaTriggerProposal, w *worldv1.SnapshotResponse, revision uint64, now time.Time, bodyAllowed bool) bool {
	self := w.GetSelf()
	return ctx.Err() == nil && !s.closed && p.sessionID == s.sessionID && p.self == s.selfGUID && p.visit != 0 &&
		w.GetVisitRevision() == p.visit && s.matchesCurrentEncounterVisitLocked(w) && areaTriggerWorldCurrent(p, w) &&
		bodyAllowed && !s.bodyMoving && !s.clientControlLost && !s.clientControlRestoring && !s.state.MotionUnconfirmed &&
		!s.state.AutoAttackUnconfirmed && !s.state.AutoAttackRequested && !s.state.AutoAttacking && !s.state.CastPending &&
		!now.Before(s.castUntil) && !s.state.Traveling && !s.state.Practicing &&
		s.movementOwner.owner == "" && !(s.targetMovement.view.TargetGuid != 0 && now.Before(s.targetMovement.until)) &&
		self.GetChannelSpellKnown() && self.GetChannelObjectKnown() && self.GetChannelSpellId() == 0 && self.GetChannelObjectGuid() == 0 &&
		s.posKnown && finite(s.pos) && s.pos == p.pose && s.posRevision == revision && s.posMap == p.mapID &&
		s.positionRevision == p.rawRevision && self.GetPositionRevision() == p.rawRevision && playerPosition(self) == p.rawPose
}

type areaTriggerAdmission func(*worldv1.SnapshotResponse, uint64, time.Time) bool

// The shared physical path writes only the normal scoped STOP and returns its
// current exact report. It does not choose an entrance or send its trigger.
// A refusal here means no AREA_TRIGGER was sent; an earlier STOP can still have
// a written or unknown outcome, retained by the normal movement bookkeeping.
func (s *Service) prepareAreaTriggerLocked(ctx context.Context, w *worldv1.SnapshotResponse, p areaTriggerProposal, current areaTriggerAdmission, qualify func(*worldv1.SnapshotResponse) bool) (*modv1.WorldPacket, error) {
	refuse := func(reason string) (*modv1.WorldPacket, error) {
		s.state.Reason = reason
		return nil, errors.New(reason)
	}
	if p.clientRevision == ^uint64(0) || !current(w, p.clientRevision, s.now()) {
		return refuse("follow_instance_source_changed")
	}
	stopScope := clonePortalScope(p.worldScope)
	needsPriorPose := s.posEstimated && !s.state.Moving || w.GetSelf().GetPositionInferred()
	previous, reportErr := modcb.ReadOwnMovementReport(ctx, s.session, s.sessionID, s.selfGUID)
	if status.Code(reportErr) == codes.FailedPrecondition && needsPriorPose && !s.state.Moving {
		// An earlier known STOP can still establish the exact stationary body
		// for a new entry decision. The new STOP reserves the freshly observed
		// source; the final trigger requires its own exact physical receipt.
		if settled, err := modcb.ReadSettledBodySource(ctx, s.session, s.sessionID, s.selfGUID); err == nil && settledPortalPose(settled, stopScope, p.pose) {
			reportErr = nil
			previous = nil
		}
	}
	if reportErr == nil && previous != nil {
		if !areaTriggerReportWorldMatches(p, w, previous) || needsPriorPose &&
			!portalMovementReportMatches(previous, s.selfGUID, p.mapID, p.pose, false) {
			return refuse("follow_instance_movement_report_unavailable")
		}
		stopScope = clonePortalScope(previous.TransportScope)
	} else if reportErr != nil && (status.Code(reportErr) != codes.FailedPrecondition || needsPriorPose) {
		return refuse("follow_instance_movement_report_unavailable")
	}
	guard := portalStopGuard(func(w *worldv1.SnapshotResponse, stop position) bool {
		now := s.now()
		return stop == p.pose && current(w, p.clientRevision, now)
	})
	stopCtx := context.WithValue(ctx, portalStopGuardKey{}, portalStopAdmission{guard: guard, scope: stopScope})
	stopped := false
	if s.state.Moving {
		stopped = s.stopLocked(stopCtx)
	} else {
		stopped = s.movementLocked(stopCtx, opcode.MSGMoveStop, p.pose, false)
	}
	if !stopped {
		return refuse("follow_instance_stop_unavailable: " + s.state.Reason)
	}
	revision := p.clientRevision + 1
	fresh, err := s.snapshot(ctx)
	if err != nil || !current(fresh, revision, s.now()) {
		return refuse("follow_instance_source_changed")
	}
	if qualify != nil && !qualify(fresh) {
		return refuse("follow_instance_policy_changed")
	}
	report, err := modcb.ReadOwnMovementReport(ctx, s.session, s.sessionID, s.selfGUID)
	if err != nil || !portalStopReportMatches(report, s.selfGUID, p.mapID, p.pose) {
		return refuse("follow_instance_movement_report_unavailable")
	}
	latest, err := s.snapshot(ctx)
	now := s.now() // Last local callback; only pure admission follows.
	if err != nil || !current(latest, revision, now) || !areaTriggerReportWorldMatches(p, latest, report) || s.state.Moving {
		return refuse("follow_instance_source_changed")
	}
	return report, nil
}

func settledPortalPose(report *modv1.WorldPacket, current *modv1.TransportScope, pose position) bool {
	return samePortalCoreVisit(report.GetTransportScope(), current) && report.TransportScope.MotionRevision <= current.MotionRevision &&
		portalStopReportMatches(report, current.SelfGuid, current.MapId, pose)
}

// Automatic following supplies its actual owner witness and Lua eligibility;
// explicit destination intent uses the same physical executor without either.
func (s *Service) enterWitnessedPortalLocked(ctx context.Context, w *worldv1.SnapshotResponse, intent portalIntent) {
	p := s.areaTriggerProposalLocked(w)
	current := areaTriggerAdmission(func(w *worldv1.SnapshotResponse, revision uint64, now time.Time) bool {
		return s.areaTriggerPoseCurrentLocked(ctx, p, w, revision, now) && s.portalIntentCurrentAtLocked(intent, w, now) &&
			samePortalCoreVisit(p.worldScope, s.followPortal.sourceScope) && p.worldScope.MotionRevision >= s.followPortal.sourceScope.MotionRevision &&
			s.state.InteractionOwner == "" && !s.areaTrigger.operation.GetPending() && s.followPortal.portal.contains(p.mapID, p.pose, .03)
	})
	qualify := func(w *worldv1.SnapshotResponse) bool {
		policy := s.portalPolicyLocked(w, s.now())
		return policy.Valid && policy.Eligible && policy.Ready && (!s.followPortal.requiresPartyZone || s.portalPartyCurrentLocked(ctx))
	}
	report, err := s.prepareAreaTriggerLocked(ctx, w, p, current, qualify)
	if err != nil {
		return
	}
	s.followPortal.notified = true
	err = s.sendPacketLocked(ctx, opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, s.followPortal.portal.trigger), report.TransportScope)
	if modcb.PacketWasNotDispatched(err) {
		s.followPortal.notified = false
		s.state.Reason = "follow_instance_trigger_not_dispatched"
	} else if err == nil {
		s.state.Reason = "follow_waiting_for_instance_transfer"
	}
}

func portalStopReportMatches(report *modv1.WorldPacket, self uint64, mapID uint32, pose position) bool {
	return portalMovementReportMatches(report, self, mapID, pose, true)
}

func portalMovementReportMatches(report *modv1.WorldPacket, self uint64, mapID uint32, pose position, stop bool) bool {
	if report.GetTransportScope() == nil || stop && report.GetOpcode() != opcode.MSGMoveStop ||
		!stop && report.GetOpcode() != opcode.MSGMoveStop && report.GetOpcode() != opcode.MSGMoveStartForward && report.GetOpcode() != opcode.MSGMoveHeartbeat {
		return false
	}
	scope := report.TransportScope
	if scope.ConnectionId == "" || scope.SelfGuid != self || !scope.VisitBound || scope.VisitRevision == 0 ||
		scope.MotionRevision == 0 || scope.MapId != mapID {
		return false
	}
	guid, n, err := readGUID(report.Payload)
	if err != nil || guid != self || !bytes.Equal(report.Payload[:n], packedGUID(self)) {
		return false
	}
	info, consumed, err := movementinfo.DecodePrefix(report.Payload[n:])
	return err == nil && n+consumed == len(report.Payload) && info.AllFinite() &&
		movementflags.GroundCompatible(info.Flags, uint32(info.ExtraFlags)) && (!stop || info.Flags&0x1ff == 0) &&
		(position{x: info.X, y: info.Y, z: info.Z, o: info.Orientation}) == pose
}
