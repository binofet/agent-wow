package buddyactions

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
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
	selfOK := func(f, x uint32) bool {
		return destinationSelfMode(w, f, x) || x == 0 && f == 0x1000 && s.routeProvesPriorFall(w, s.swimFollow.motion)
	}
	if !d.lastSeen || !d.localCurrent(s, s.now()) || o.guid == 0 || d.guid != o.guid || o.target != d.target || o.revision != s.followRevision || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != o.visit || w.Self.MapId != o.mapID || w.GetOwner().GetPresent() || (w.GetOwner().GetGuid() != 0 && w.Owner.Guid != d.guid) || safeQuestCharacterMode(w, s.selfGUID, selfOK) != "" {
		return false
	}
	if !s.healingPartyLocked(ctx)[d.guid] {
		return false
	}
	fresh, err := s.readWorldSnapshot(ctx)
	return err == nil && !fresh.GetOwner().GetPresent() && (fresh.GetOwner().GetGuid() == 0 || fresh.Owner.Guid == d.guid) && s.matchesCurrentEncounterVisitLocked(fresh) && fresh.Self.PositionRevision == w.Self.PositionRevision && safeQuestCharacterMode(fresh, s.selfGUID, selfOK) == ""
}

func (s *Service) followLastSeenLocked(ctx context.Context, w *worldv1.SnapshotResponse, now time.Time) bool {
	if w.GetOwner().GetPresent() || !s.state.Following || s.navDataDir == "" || s.lastSeenFollow.guid == 0 {
		return false
	}
	last := s.lastSeenFollow
	d := destinationOwner{lastSeen: true, guid: last.guid, followRevision: last.revision, target: last.target}
	if !s.lastSeenCurrentLocked(ctx, d, w) || !s.refreshPositionLocked(w) {
		return false
	}
	if distance(s.pos, last.target) <= 1.5 {
		s.stopLocked(ctx)
		s.state.Reason = "at_last_seen_follow_position"
		s.lastStep = now
		return true
	}
	r := &s.swimFollow
	if r.owner != d || len(r.path) == 0 || r.revision != s.positionRevision || r.mapID != s.posMap || distance(r.expected, s.pos) > .05 {
		if r.owner == d && now.Before(r.nextPlan) {
			s.state.Reason = "last_seen_follow_replan_wait"
			return true
		}
		*r = swimFollowRoute{owner: d, target: last.target, expected: s.pos, mapID: s.posMap, revision: s.positionRevision, nextPlan: now.Add(750 * time.Millisecond)}
		path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, last.target)
		if err == nil && !s.swimming {
			r.path, r.wet = path, make([]bool, len(path))
		} else {
			if !waterMovementAllowed(w) {
				s.stopLocked(ctx)
				s.state.Reason = "last_seen_follow_route_unavailable"
				return true
			}
			next, e := s.planMixedDestination(ctx, s.navDataDir, s.posMap, s.pos, last.target, observedRunSpeed(w.Self), s.swimming, destinationGravitySource(w, s.pos), nil)
			if e != nil {
				s.stopLocked(ctx)
				s.state.Reason = "last_seen_follow_route_unavailable"
				s.state.NavReason = navigationDiagnostic(e, s.navDataDir)
				return true
			}
			r.path, r.wet, r.drop = next.path, next.wet, next.drop
		}
		s.state.NavAvailable = true
	}
	s.lastStep = now
	if s.advanceDestinationLocked(ctx, r, now.Sub(s.posAt).Seconds()) {
		s.state.Reason = "following_last_seen_position"
	}
	return true
}
