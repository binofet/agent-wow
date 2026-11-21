package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Executes the selected near-owner goal using the existing complete route and
// bounded normal-speed packet integration. Lua supplies the destination/hold.
func (s *Service) nearOwnerFollowLocked(ctx context.Context, snapshot *worldv1.SnapshotResponse, now time.Time) {
	owner := playerPosition(snapshot.Owner)
	target := s.followFormationPlanLocked(now, snapshot, 0, owner, snapshot.Owner.MovementFlags&0x3f != 0)
	if !target.Valid || !finite(target.Position) {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "formation_position_invalid"
		return
	}
	if target.Hold {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = target.Phase
		return
	}
	d := distance(s.pos, target.Position)
	// Physical arrival remains enforceable between finite Lua samples.
	if d <= target.StopDistance {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "within_follow_distance"
		return
	}
	if !snapshot.Self.SpeedsKnown || math.Abs(float64(snapshot.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(snapshot.Self.RunSpeed)) {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "normal_run_speed_unknown"
		return
	}
	elapsed := now.Sub(s.lastStep).Seconds()
	s.lastStep = now
	if elapsed < 0 || elapsed > 0.6 {
		s.stopLocked(ctx)
		s.state.Reason = "follow_tick_late"
		return
	}
	plan, finish := context.WithTimeout(ctx, 250*time.Millisecond)
	path, planErr := s.mapNavigationPath(plan, s.navDataDir, s.posMap, s.pos, target.Position)
	if planErr == nil {
		planErr = plan.Err()
	}
	finish()
	if planErr != nil {
		s.stopLocked(ctx)
		s.state.NavAvailable = false
		s.state.NavReason = navigationDiagnostic(planErr, s.navDataDir)
		s.state.Reason = "follow_navigation_unavailable"
		return
	}
	s.state.NavAvailable = true
	p := s.pos
	op := uint32(opcode.MSGMoveStartForward)
	if s.state.Moving {
		var ok bool
		p, _, ok = followPathStep(s.pos, path, 0, math.Min(7*elapsed, d-target.StopDistance))
		if !ok || math.Abs(float64(p.z-s.pos.z)) > 2 {
			s.stopLocked(ctx)
			s.state.Reason = "follow_navigation_invalid_step"
			return
		}
		op = opcode.MSGMoveHeartbeat
	} else if len(path) > 1 {
		p.o = followFacing(p, path[1])
	}
	if s.movementLocked(ctx, op, p, true) {
		s.state.Reason = "following_client_navmesh_estimate"
	} else {
		s.stopLocked(ctx)
	}
}
