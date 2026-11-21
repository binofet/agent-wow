package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ApproachCorpse executes one verified physical route step toward this death's
// exact server-queried corpse location. Lua chooses range, continuation, retry,
// stopping and whether to reclaim; this method never runs a recovery loop.
func (s *Service) ApproachCorpse(ctx context.Context, q *actionsv1.CorpseApproachRequest) (*actionsv1.State, error) {
	if q == nil || q.SessionId == "" || q.SelfGuid == 0 || q.DeathEpoch == 0 || q.ExpectedPositionRevision == 0 ||
		!q.Stop && (q.CorpseQueryRevision == 0 || math.IsNaN(float64(q.RangeYards)) || math.IsInf(float64(q.RangeYards), 0) || q.RangeYards <= 0) {
		return nil, status.Error(codes.InvalidArgument, "current body/death/pose and exact query/range required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || q.SessionId != s.sessionID || q.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "corpse approach body/session changed")
	}
	w, err := s.snapshot(ctx)
	if err != nil || !recoverySelf(w, s.selfGUID) || q.DeathEpoch != s.recovery.state.DeathEpoch || q.ExpectedPositionRevision != w.Self.PositionRevision {
		return nil, status.Error(codes.FailedPrecondition, "current observed death and pose required")
	}
	r := &s.recovery
	if s.state.InteractionOwner != "" && !s.recoveryOwnsLocked() {
		return nil, status.Error(codes.FailedPrecondition, "another physical interaction owns this body")
	}
	s.refreshPositionLocked(w)
	if s.state.MotionUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "actual fresh stopped pose required after unknown movement")
	}
	if !q.Stop && (!r.state.GhostMovementSupported || !ghostSelf(w, s.selfGUID) || !r.state.CorpseQueryKnown || !r.state.CorpseFound ||
		q.CorpseQueryRevision != r.state.CorpseQueryRevision || r.state.CorpseLocation == nil || w.Self.MapId != r.state.CorpseLocation.MapId || w.Self.MapId != r.state.CorpseMapId) {
		return nil, status.Error(codes.FailedPrecondition, "same-map actual ghost and exact current corpse query required")
	}
	if r.token == "" {
		r.token = fmt.Sprintf("death-motion-%d", q.DeathEpoch)
		s.interactionToken = r.token
		s.state.InteractionOwner = "recovery"
	}
	r.state.Active = true
	// A short execution lease prevents movement continuing without another Lua
	// input. This is the same physical writer horizon used by encounter actions.
	s.interactionUntil = s.now().Add(encounterLeaseDuration)
	call := modcb.ForwardPolicySpell(ctx)
	if q.Stop {
		s.finishRecoveryMotionLocked(call, "stopped", "explicit_Lua_corpse_route_stop")
		return s.stateLocked(), nil
	}
	to := position{x: r.state.CorpseLocation.X, y: r.state.CorpseLocation.Y, z: r.state.CorpseLocation.Z}
	if distance(s.pos, to) <= float64(q.RangeYards) {
		s.finishRecoveryMotionLocked(call, "within_requested_range", "observed_pose_within_requested_range")
		return s.stateLocked(), nil
	}
	if r.path != nil && (w.Self.MapId != r.mapID || w.Self.PositionRevision != r.revision || distance(s.pos, r.expected) > .05) {
		if !s.stopLocked(call) {
			r.state.Reason = "route_pose_changed_waiting_for_stop"
			return s.stateLocked(), nil
		}
		r.path = nil
	}
	if r.path == nil {
		from, revision := s.pos, s.positionRevision
		var path []position
		var planErr error
		if s.planRecoveryPath != nil {
			path, planErr = s.planRecoveryPath(ctx, s.navDataDir, w.Self.MapId, from, to, float64(q.RangeYards))
		} else {
			path, planErr = s.mapNavigationPath(ctx, s.navDataDir, w.Self.MapId, from, to)
		}
		if planErr == nil && (len(path) < 2 || !validMapNavigationPath(path, from, path[len(path)-1])) {
			planErr = errors.New("corpse route failed finite geometry/endpoint validation")
		}
		if planErr != nil {
			s.state.NavReason = navigationDiagnostic(planErr, s.navDataDir)
			s.finishRecoveryMotionLocked(call, "route_unavailable", s.state.NavReason)
			return s.stateLocked(), status.Error(codes.FailedPrecondition, "verified corpse route unavailable")
		}
		fresh, e := s.snapshot(ctx)
		if e != nil || !ghostSelf(fresh, s.selfGUID) || fresh.Self.MapId != w.Self.MapId || fresh.Self.PositionRevision != revision || r.state.DeathEpoch != q.DeathEpoch || r.state.CorpseQueryRevision != q.CorpseQueryRevision || s.clientControlLost || s.clientControlRestoring {
			r.path = nil
			return s.stateLocked(), status.Error(codes.FailedPrecondition, "ghost/query changed during route planning")
		}
		r.path, r.index, r.mapID, r.revision, r.expected = path, 0, w.Self.MapId, revision, from
		r.state.VerifiedLegs++
	}
	if routeRemaining(s.pos, r.path, r.index) <= .75 {
		s.finishRecoveryMotionLocked(call, "route_leg_complete", "measured_verified_leg_endpoint")
		return s.stateLocked(), nil
	}
	r.state.Phase = "following_verified_ghost_route"
	index := r.index
	if !s.navigationStepAtSpeedLocked(call, r.path, &r.index, finite, float64(w.Self.RunSpeed)) {
		r.index = index
		r.state.MovementUnconfirmed = s.state.MotionUnconfirmed
		r.state.Reason = "route_step_not_completed"
		return s.stateLocked(), nil
	}
	r.expected = s.pos
	return s.stateLocked(), nil
}

// ReclaimCorpse is one normal exact reclaim request. It never selects a corpse,
// waits on a policy timer or interprets transport as resurrection success.
func (s *Service) ReclaimCorpse(ctx context.Context, q *actionsv1.ReclaimCorpseRequest) (*actionsv1.State, error) {
	if q == nil || q.SessionId == "" || q.SelfGuid == 0 || q.DeathEpoch == 0 || q.ExpectedPositionRevision == 0 || q.CorpseGuid == 0 || q.ExpectedCorpseRevision == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact current body/death/pose/corpse required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || q.SessionId != s.sessionID || q.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "reclaim body/session changed")
	}
	w, err := s.snapshot(ctx)
	r := &s.recovery
	if err != nil || !ghostSelf(w, s.selfGUID) || q.DeathEpoch != r.state.DeathEpoch || q.ExpectedPositionRevision != w.Self.PositionRevision || !r.state.GhostMovementSupported || s.clientControlLost || s.clientControlRestoring {
		return nil, status.Error(codes.FailedPrecondition, "actual supported current ghost/death/pose required")
	}
	s.refreshPositionLocked(w)
	if s.state.Moving || s.state.MotionUnconfirmed || s.state.InteractionOwner != "" && !s.recoveryOwnsLocked() {
		return nil, status.Error(codes.FailedPrecondition, "stopped current body required before reclaim")
	}
	if r.state.ReclaimAttempted {
		return s.stateLocked(), nil
	}
	c := w.OwnCorpse
	location := r.state.CorpseLocation
	if !r.state.CorpseQueryKnown || !r.state.CorpseFound || location == nil || c == nil || c.Guid != q.CorpseGuid || c.PositionRevision != q.ExpectedCorpseRevision || c.Guid>>48 != 0xf101 || c.OwnerGuid != s.selfGUID || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.PositionKnown || !finite(position{x: c.X, y: c.Y, z: c.Z}) || !c.FlagsKnown || c.Flags&1 != 0 ||
		location.MapId != c.MapId || r.state.CorpseMapId != c.MapId || distance(position{x: c.X, y: c.Y, z: c.Z}, position{x: location.X, y: location.Y, z: location.Z}) > 2 || distance(s.pos, position{x: c.X, y: c.Y, z: c.Z}) > 35 {
		return nil, status.Error(codes.FailedPrecondition, "exact actual own corpse in legal reclaim range required")
	}
	if r.state.ReclaimDelayKnown && s.now().Before(r.delayUntil) {
		return nil, status.Error(codes.FailedPrecondition, "observed server reclaim delay remains")
	}
	r.state.CorpseGuid = c.Guid
	r.state.ReclaimAttempted = true
	r.state.Phase = "reclaim_requested"
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGReclaimCorpse, binary.LittleEndian.AppendUint64(nil, c.Guid))
	r.state.ReclaimWritten = err == nil
	if modcb.PacketWasNotDispatched(err) {
		r.state.ReclaimAttempted = false
		r.state.Phase = "reclaim_not_dispatched"
		return s.stateLocked(), err
	}
	if err != nil {
		r.state.Reason = "reclaim_transport_unconfirmed_no_retry"
	}
	return s.stateLocked(), nil
}
