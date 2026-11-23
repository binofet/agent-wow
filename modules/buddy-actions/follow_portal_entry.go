package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type portalStopGuardKey struct{}
type portalStopGuard func(*worldv1.SnapshotResponse, position) bool

type portalEntryProposal struct {
	intent                      portalIntent
	pose, rawPose               position
	mapID                       uint32
	clientRevision, rawRevision uint64
}

// This last admission is pure: all clock/World/policy callbacks have returned.
// In particular, checking a finite lease must not call s.now after comparing it.
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

func (s *Service) portalEntryCurrentLocked(ctx context.Context, p portalEntryProposal, w *worldv1.SnapshotResponse, revision uint64, now time.Time) bool {
	self := w.GetSelf()
	return ctx.Err() == nil && p.intent.visit != 0 && s.matchesCurrentEncounterVisitLocked(w) && s.portalIntentCurrentAtLocked(p.intent, w, now) &&
		!s.bodyMoving && !s.clientControlLost && !s.clientControlRestoring && !s.state.MotionUnconfirmed &&
		!s.state.AutoAttackUnconfirmed && !s.state.AutoAttackRequested && !s.state.AutoAttacking && !s.state.CastPending &&
		!now.Before(s.castUntil) && s.state.InteractionOwner == "" && !s.state.Traveling && !s.state.Practicing &&
		s.movementOwner.owner == "" && !(s.targetMovement.view.TargetGuid != 0 && now.Before(s.targetMovement.until)) &&
		self.GetChannelSpellKnown() && self.GetChannelObjectKnown() && self.GetChannelSpellId() == 0 && self.GetChannelObjectGuid() == 0 &&
		s.posKnown && finite(s.pos) && s.pos == p.pose && s.posRevision == revision && s.posMap == p.mapID &&
		s.positionRevision == p.rawRevision && self.GetPositionRevision() == p.rawRevision &&
		playerPosition(self) == p.rawPose && s.followPortal.portal.contains(p.mapID, p.pose, .03)
}

// An ID-only area-trigger packet has no pose. Bind it instead to an exact
// successful normal STOP written by this Core connection, then let Core check
// that immutable scope immediately at its owner-loop transport boundary.
func (s *Service) enterWitnessedPortalLocked(ctx context.Context, w *worldv1.SnapshotResponse, intent portalIntent) {
	p := portalEntryProposal{intent: intent, pose: s.pos, rawPose: playerPosition(w.GetSelf()), mapID: s.posMap,
		clientRevision: s.posRevision, rawRevision: s.positionRevision}
	if p.clientRevision == ^uint64(0) || !s.portalEntryCurrentLocked(ctx, p, w, p.clientRevision, s.now()) {
		s.state.Reason = "follow_instance_source_changed"
		return
	}
	if s.posEstimated && !s.state.Moving {
		// A clock cannot freshen an old estimate. Its last successful own
		// packet must still be Core's current report before another STOP can
		// use that controller position. Incoming supersession makes it unknown.
		previous, err := modcb.ReadOwnMovementReport(ctx, s.session, s.sessionID, s.selfGUID)
		if err != nil || !portalMovementReportMatches(previous, s.selfGUID, p.mapID, p.pose, false) {
			s.state.Reason = "follow_instance_movement_report_unavailable"
			return
		}
	}
	guard := portalStopGuard(func(current *worldv1.SnapshotResponse, stop position) bool {
		now := s.now()
		return stop == p.pose && s.portalEntryCurrentLocked(ctx, p, current, p.clientRevision, now)
	})
	stopCtx := context.WithValue(ctx, portalStopGuardKey{}, guard)
	// stopLocked deliberately does nothing when idle. A fresh actual report is
	// required here even in that case; use the same normal encoder/executor.
	stopped := false
	if s.state.Moving {
		stopped = s.stopLocked(stopCtx)
	} else {
		stopped = s.movementLocked(stopCtx, opcode.MSGMoveStop, p.pose, false)
	}
	if !stopped {
		return
	}
	revision := p.clientRevision + 1
	fresh, err := s.snapshot(ctx)
	if err != nil || !s.portalEntryCurrentLocked(ctx, p, fresh, revision, s.now()) {
		s.state.Reason = "follow_instance_source_changed"
		return
	}
	policy := s.portalPolicyLocked(fresh, s.now())
	if !policy.Valid || !policy.Eligible || !policy.Ready {
		s.state.Reason = "follow_instance_policy_changed"
		return
	}
	report, err := modcb.ReadOwnMovementReport(ctx, s.session, s.sessionID, s.selfGUID)
	if err != nil || !portalStopReportMatches(report, s.selfGUID, p.mapID, p.pose) {
		s.state.Reason = "follow_instance_movement_report_unavailable"
		return
	}
	latest, err := s.snapshot(ctx)
	now := s.now() // Last local callback; only pure comparisons follow.
	if err != nil || !s.portalEntryCurrentLocked(ctx, p, latest, revision, now) || s.state.Moving {
		s.state.Reason = "follow_instance_source_changed"
		return
	}
	s.followPortal.notified = true // The outcome may be unknown; never replay.
	err = s.sendPacketLocked(ctx, opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, s.followPortal.portal.trigger), report.TransportScope)
	if modcb.PacketWasNotDispatched(err) {
		// Core can reject a superseded scope before transport. Its typed
		// zero-write receipt is retained in LastTransportFailure; a fresh
		// report and current Lua intent may qualify a later explicit entry.
		// Plain errors retain the tombstone because their outcome is unknown.
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
