package buddyactions

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

type followObservation struct {
	target                position
	guid, visit, revision uint64
	mapID                 uint32
}

func (s *Service) rememberFollowDestinationLocked(w *worldv1.SnapshotResponse) {
	o := w.GetOwner()
	if !s.matchesCurrentEncounterVisitLocked(w) || !o.GetPresent() {
		return
	}
	if o.GetGuid() != ownerGUID || !o.GetAliveKnown() || !o.GetAlive() || !o.GetMapKnown() || o.MapId != w.Self.MapId || !o.GetPositionKnown() || o.GetPositionInferred() || !finite(playerPosition(o)) || !o.GetMovementFlagsKnown() || !swimFollowCompatible(o.MovementFlags, o.ExtraMovementFlags) {
		s.lastSeenFollow = followObservation{}
		return
	}
	s.lastSeenFollow = followObservation{playerPosition(o), o.Guid, w.VisitRevision, s.followRevision, o.MapId}
}
func (s *Service) lastSeenCurrentLocked(ctx context.Context, d destinationOwner, w *worldv1.SnapshotResponse) bool {
	o := s.lastSeenFollow
	if !d.lastSeen || o.guid == 0 || d.guid != o.guid || o.target != d.target || o.revision != s.followRevision {
		return false
	}
	current := func(view *worldv1.SnapshotResponse) bool {
		me, owner := view.GetSelf(), view.GetOwner()
		if !d.localCurrent(s, s.now()) || !s.matchesCurrentEncounterVisitLocked(view) || view.VisitRevision != o.visit || me.MapId != o.mapID ||
			owner.GetPresent() || owner.GetGuid() != 0 && owner.Guid != d.guid ||
			d.mountedLastSeen != (me.UnitFlags&movementflags.Mounted != 0) || d.mountedLastSeen && s.swimming {
			return false
		}
		selfOK := func(f, x uint32) bool {
			return destinationSelfMode(view, f, x) || x == 0 && f == 0x1000 && s.routeProvesPriorFall(view, s.swimFollow.motion)
		}
		return safeQuestCharacterFlags(view, s.selfGUID, selfOK, s.groundTravelUnitFlags(me)) == ""
	}
	if !current(w) || !s.currentHealPartyLocked(ctx)[d.guid] {
		return false
	}
	fresh, err := s.readWorldSnapshot(ctx)
	return err == nil && current(fresh) && fresh.Self.PositionRevision == w.Self.PositionRevision
}

func (s *Service) followLastSeenLocked(ctx context.Context, w *worldv1.SnapshotResponse, now time.Time) bool {
	mounted := w.GetSelf().GetUnitFlags()&movementflags.Mounted != 0
	if w.GetOwner().GetPresent() {
		// Resume ordinary mounted formation as soon as the owner reappears.
		// The old last-seen ground path is not a current visible swim route.
		if s.swimFollow.owner.lastSeen && (s.swimFollow.owner.mountedLastSeen || mounted) {
			s.swimFollow = swimFollowRoute{}
		}
		return false
	}
	if !s.state.Following || s.navDataDir == "" || s.lastSeenFollow.guid == 0 {
		return false
	}
	last := s.lastSeenFollow
	d := destinationOwner{lastSeen: true, mountedLastSeen: mounted, guid: last.guid, followRevision: last.revision, target: last.target}
	if !s.lastSeenCurrentLocked(ctx, d, w) || !s.refreshPositionLocked(w) {
		return false
	}
	if distance(s.pos, last.target) <= 1.5 {
		s.lastStep = now
		if s.stopLocked(ctx) {
			s.state.Reason = "at_last_seen_follow_position"
		}
		return true
	}
	if s.planFollowCorridor != nil && !s.swimming && s.swimFollow.drop == nil && movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags) {
		s.lastStep = now
		call := context.WithValue(ctx, destinationOwnerKey{}, d)
		if s.followFormationStepLocked(call, last.target, 1.5, last.target) {
			if s.state.Reason == "within_follow_formation" {
				s.state.Reason = "at_last_seen_follow_position"
			} else if s.state.Reason == "following_formation" {
				s.state.Reason = "following_last_seen_position"
			}
		}
		return true
	}
	return s.followLastSeenSurfaceLocked(ctx, w, d, now, false)
}

func (s *Service) followLastSeenSurfaceLocked(ctx context.Context, w *worldv1.SnapshotResponse, d destinationOwner, now time.Time, mixedOnly bool) bool {
	r := &s.swimFollow
	if r.owner != d || len(r.path) == 0 || r.revision != s.positionRevision || r.mapID != s.posMap || distance(r.expected, s.pos) > .05 {
		if r.owner == d && now.Before(r.nextPlan) {
			s.state.Reason = "last_seen_follow_replan_wait"
			return true
		}
		*r = swimFollowRoute{owner: d, target: d.target, expected: s.pos, mapID: s.posMap, revision: s.positionRevision, nextPlan: now.Add(750 * time.Millisecond)}
		planCtx, cancel := destinationPlanningContext(ctx)
		defer cancel()
		var path []position
		var err error
		if !mixedOnly && !s.swimming {
			path, err = s.mapNavigationPath(planCtx, s.navDataDir, s.posMap, s.pos, d.target)
		}
		if !mixedOnly && err == nil && !s.swimming {
			r.path, r.wet = path, make([]bool, len(path))
		} else {
			if d.mountedLastSeen || !waterMovementAllowed(w) {
				s.stopLocked(ctx)
				s.state.Reason = "last_seen_follow_route_unavailable"
				return true
			}
			next, e := s.planMixedDestination(planCtx, s.navDataDir, s.posMap, s.pos, d.target, observedRunSpeed(w.Self), s.swimming, destinationGravitySource(w, s.pos), nil)
			if e != nil {
				s.stopLocked(ctx)
				s.state.Reason = "last_seen_follow_route_unavailable"
				s.state.NavReason = navigationDiagnostic(e, s.navDataDir)
				return true
			}
			r.path, r.wet, r.drop = next.path, next.wet, next.drop
		}
		s.state.NavAvailable = true
		if ctx.Err() != nil {
			s.state.Reason = "last_seen_destination_route_ready"
			return true
		}
	}
	s.lastStep = now
	if s.advanceDestinationLocked(ctx, r, now.Sub(s.posAt).Seconds()) {
		s.state.Reason = "following_last_seen_position"
	}
	return true
}
