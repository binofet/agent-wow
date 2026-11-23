package buddyactions

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
)

type dungeonPortal struct {
	trigger, source, destination uint32
	center                       position
	radius                       float64
	reference                    areatriggerreference.Trigger
}

func portalReference(t areatriggerreference.Trigger) dungeonPortal {
	return dungeonPortal{trigger: t.ID, source: t.SourceMap, destination: t.DestinationMap,
		center: position{x: float32(t.X), y: float32(t.Y), z: float32(t.Z)}, radius: t.Radius, reference: t}
}

func (p dungeonPortal) contains(mapID uint32, point position, margin float64) bool {
	return p.reference.Contains(mapID, float64(point.x), float64(point.y), float64(point.z), margin)
}

// References identify a unique volume, not realm admission or a route. Never
// choose between overlapping volumes: Lua's observed-owner intent is not proof
// of which ambiguous trigger the realm would accept.
func witnessedPortal(mapID uint32, point position) (dungeonPortal, bool) {
	var found dungeonPortal
	for _, reference := range areatriggerreference.All() {
		if reference.Contains(mapID, float64(point.x), float64(point.y), float64(point.z), 0) {
			if found.trigger != 0 {
				return dungeonPortal{}, false
			}
			found = portalReference(reference)
		}
	}
	return found, found.trigger != 0
}

type dungeonPortalFollow struct {
	portal                dungeonPortal
	ground                position
	seenAt, lostAt        time.Time
	approaching, notified bool
	movementInProgress    bool
	catchingUp            bool
	catchUpTarget         position
	catchUpTargetKnown    bool
	nextCatchUpPlan       time.Time
	sourceSession         string
	sourceSelf            uint64
	sourceVisit           uint64
	sourceScope           *modv1.TransportScope
	followRevision        uint64
	ownerPoseRevision     uint64
	witness               uint64
	catchUpHost           bool
	movementIntent        portalIntent
}

func (s *Service) observeDungeonPortalLocked(w *worldv1.SnapshotResponse, now time.Time) {
	owner := w.GetOwner()
	if s.followPortal.portal.trigger != 0 && (s.followPortal.sourceSession != s.sessionID ||
		s.followPortal.sourceSelf != s.selfGUID || !s.followPortal.notified &&
		(s.followPortal.followRevision != s.followRevision || s.followPortal.sourceVisit != w.VisitRevision)) {
		s.followPortal = dungeonPortalFollow{}
		s.portalGoal = portalCatchupGoal{}
	}
	// An attempted crossing is a tombstone, not renewable movement authority.
	// Neither lease expiry nor a reappearing owner proves that its packet was
	// rejected. Only the actual destination observation below settles it.
	if s.followPortal.notified {
		return
	}
	if owner == nil || owner.Guid != ownerGUID {
		s.followPortal = dungeonPortalFollow{}
		s.portalGoal = portalCatchupGoal{}
		return
	}
	if !owner.Present {
		if s.followPortal.portal.trigger != 0 && s.followPortal.lostAt.IsZero() {
			s.followPortal.lostAt = now
		}
		return
	}
	// Capture a sighting while ordinary following or a finite Lua formation
	// goal authorizes this visible owner. Its data can outlive that authority.
	if !s.legacyPortalIntentLocked(now) && !s.currentFollowFormationGoalLocked(s.followGoal, w) {
		return
	}
	f := s.followPortal
	if f.approaching {
		s.followNav.Reset()
	}
	// Reappearance ends this crossing attempt. A later disappearance requires
	// a new sighting at an entrance, even if the character remains nearby.
	s.followPortal = dungeonPortalFollow{}
	if safePlayers(w, s.selfGUID) != "" || owner.PositionAgeMs > 2000 {
		return
	}
	ground := playerPosition(owner)
	if portal, found := witnessedPortal(owner.MapId, ground); found {
		sourceScope := clonePortalScope(portalObservedWorldScope(w, s.selfGUID))
		catchingUp, valid := s.portalSightingPolicyLocked(w, now)
		if !valid || !s.legacyPortalIntentLocked(s.now()) && !s.currentFollowFormationGoalLocked(s.followGoal, w) {
			s.portalGoal = portalCatchupGoal{}
			return
		}
		witness := f.witness
		if witness == 0 || f.portal.trigger != portal.trigger || f.sourceVisit != w.VisitRevision ||
			f.followRevision != s.followRevision || f.ownerPoseRevision != owner.PositionRevision ||
			f.ground != ground || f.catchingUp != catchingUp || !f.lostAt.IsZero() || f.notified || !samePortalCoreVisit(f.sourceScope, sourceScope) {
			s.portalWitnessRevision++
			witness = s.portalWitnessRevision
			s.portalGoal = portalCatchupGoal{}
		}
		s.followPortal = dungeonPortalFollow{
			portal:        portal,
			ground:        ground,
			seenAt:        now.Add(-time.Duration(owner.PositionAgeMs) * time.Millisecond),
			catchingUp:    catchingUp,
			sourceSession: s.sessionID, sourceSelf: s.selfGUID,
			sourceVisit: w.VisitRevision, followRevision: s.followRevision, ownerPoseRevision: owner.PositionRevision, witness: witness,
			sourceScope: clonePortalScope(sourceScope),
		}
		return
	}
}

func (s *Service) legacyPortalIntentLocked(now time.Time) bool {
	return !s.closed && !s.independent && s.state.Following && now.Before(s.followUntil)
}

// Physical witness validity is independent of the short-lived intent which
// may consume it. In particular, the first portal goal cannot require itself.
func (s *Service) portalSourceWitnessCurrentLocked(w *worldv1.SnapshotResponse) bool {
	f := s.followPortal
	p, owner := w.GetSelf(), w.GetOwner()
	return !s.closed && !s.independent && f.sourceSession == s.sessionID && f.sourceSelf == s.selfGUID &&
		f.sourceVisit == w.GetVisitRevision() && f.followRevision == s.followRevision &&
		(f.sourceVisit == 0 || s.matchesCurrentEncounterVisitLocked(w)) &&
		samePortalCoreVisit(f.sourceScope, portalObservedWorldScope(w, s.selfGUID)) &&
		f.portal.trigger != 0 &&
		owner != nil && owner.Guid == ownerGUID && !owner.Present &&
		(!owner.MapKnown || owner.MapId == f.portal.source || owner.MapId == f.portal.destination) &&
		safeQuestCharacter(w, s.selfGUID) == "" && p.MapId == f.portal.source &&
		distance(playerPosition(p), f.ground) <= buddynav.MaxRouteLength
}

func (s *Service) portalSourceCurrentLocked(w *worldv1.SnapshotResponse, now time.Time) bool {
	return s.portalSourceWitnessCurrentLocked(w) &&
		(s.legacyPortalIntentLocked(now) || s.currentPortalGoalLocked(s.portalGoal, w))
}

func (s *Service) portalSourceReadyLocked(w *worldv1.SnapshotResponse, now time.Time) bool {
	intent, current := s.capturePortalIntentLocked(w)
	if !current {
		return false
	}
	policy := s.portalPolicyLocked(w, now)
	return policy.Valid && policy.Eligible && s.portalIntentCurrentLocked(intent, w)
}

func (s *Service) portalMovementCurrentLocked(w *worldv1.SnapshotResponse) bool {
	f := s.followPortal
	return f.movementInProgress && !f.notified && s.portalIntentCurrentLocked(f.movementIntent, w)
}

func (s *Service) portalMovementAllowedLocked(w *worldv1.SnapshotResponse) bool {
	if !s.portalMovementCurrentLocked(w) {
		return false
	}
	policy := s.portalPolicyLocked(w, s.now())
	return policy.Valid && policy.Eligible && policy.Ready
}

// A single query can find a partial corridor between two floors that become
// connected after taking a turn. Use an intermediate point only when the
// physical planner verifies both the approach to it and the route to the portal.
func (s *Service) portalCatchUpTarget(ctx context.Context, ground position, candidates []position) (position, bool) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	for _, candidate := range candidates {
		if ctx.Err() != nil || !validPortalCandidate(candidate, s.pos, ground) {
			return position{}, false
		}
		approach, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, candidate)
		if err != nil || len(approach) == 0 {
			continue
		}
		if distance(candidate, ground) < .001 {
			return ground, ctx.Err() == nil
		}
		bend := approach[len(approach)-1]
		if path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, bend, ground); err == nil && len(path) != 0 && ctx.Err() == nil {
			return bend, true
		}
	}
	return position{}, false
}

// Called after combat, casting and exclusive interactions have yielded.
// Disappearance at the portal is a cue to try the normal transfer once. The
// realm decides eligibility and which copy this party is allowed to enter.
func (s *Service) followDungeonPortalLocked(ctx context.Context, w *worldv1.SnapshotResponse, now time.Time) bool {
	if s.areaTrigger.operation.GetPending() {
		s.state.Reason = "area_trigger_waiting_for_destination_observation"
		return true
	}
	f := &s.followPortal
	if f.portal.trigger == 0 {
		return false
	}
	self := w.GetSelf()
	if f.notified && f.sourceSession == s.sessionID && f.sourceSelf == s.selfGUID &&
		self.GetGuid() == f.sourceSelf && self.GetPresent() && self.GetMapKnown() && self.GetMapId() == f.portal.destination &&
		f.sourceVisit != 0 && w.GetVisitRevision() > f.sourceVisit && s.matchesCurrentEncounterVisitLocked(w) {
		s.followPortal = dungeonPortalFollow{}
		s.portalGoal = portalCatchupGoal{}
		s.followNav.Reset()
		s.lastStep = now
		s.state.Reason = "follow_instance_transfer_observed"
		return true
	}
	if f.notified {
		s.lastStep = now
		s.state.Reason = "follow_waiting_for_instance_transfer"
		return true
	}
	intent, current := s.capturePortalIntentLocked(w)
	if !current {
		return false
	}
	policy := s.portalPolicyLocked(w, now)
	if !policy.Valid {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "follow_instance_policy_unavailable"
		return true
	}
	if !s.portalIntentCurrentLocked(intent, w) {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "follow_instance_source_changed"
		return true
	}
	if !policy.Eligible {
		return false
	}
	s.lastStep = now
	if !policy.Ready {
		s.stopLocked(ctx)
		s.state.Reason = "follow_waiting_at_instance_entrance"
		return true
	}
	if !s.healingPartyLocked(ctx)[ownerGUID] || !s.refreshPositionLocked(w) ||
		!s.stopAutoAttackLocked(ctx) || s.state.AutoAttackUnconfirmed || !s.portalIntentCurrentLocked(intent, w) {
		s.stopLocked(ctx)
		s.state.Reason = "follow_instance_party_or_pose_unavailable"
		return true
	}
	if f.portal.contains(s.posMap, s.pos, .03) {
		s.enterWitnessedPortalLocked(ctx, w, intent)
		return true
	}
	if s.navDataDir == "" {
		s.stopLocked(ctx)
		s.state.Reason = "follow_instance_navigation_unavailable"
		return true
	}
	if !f.approaching {
		s.followNav.Reset()
		f.approaching = true
	}
	useCandidates := f.catchingUp || intent.finite
	if useCandidates {
		host := s.hostEncounterActiveLocked()
		if f.catchUpHost != host {
			f.catchUpTargetKnown = false
			f.catchUpHost = host
			s.followNav.Reset()
		}
		if host && !s.currentPortalGoalLocked(s.portalGoal, w) {
			f.catchUpTargetKnown = false
			s.stopLocked(ctx)
			s.state.Reason = "follow_instance_waiting_for_policy"
			return true
		}
	}
	if useCandidates && !f.catchUpTargetKnown {
		if now.Before(f.nextCatchUpPlan) {
			return true
		}
		candidates := s.portalCandidatesLocked(w, now)
		if len(candidates) == 0 {
			s.stopLocked(ctx)
			s.state.Reason = "follow_instance_waiting_for_policy"
			return true
		}
		f.nextCatchUpPlan = now.Add(750 * time.Millisecond)
		start := s.portalStartLocked(w)
		if start == nil {
			s.stopLocked(ctx)
			s.state.Reason = "follow_instance_source_changed"
			return true
		}
		target, ok := s.portalCatchUpTarget(ctx, f.ground, candidates)
		if !ok {
			s.stopLocked(ctx)
			s.state.Reason = "follow_instance_catch_up_route_unavailable"
			return true
		}
		fresh, err := s.snapshot(ctx)
		if err != nil || !s.portalIntentCurrentLocked(intent, fresh) || !s.portalSourceReadyLocked(fresh, s.now()) ||
			!s.portalIntentCurrentLocked(intent, fresh) || !samePortalStart(start, s.portalStartLocked(fresh)) {
			s.stopLocked(ctx)
			s.state.Reason = "follow_instance_source_changed"
			return true
		}
		f.catchUpTarget, f.catchUpTargetKnown = target, true
	}
	if f.catchUpTargetKnown && f.catchUpTarget != f.ground && followAtTarget(s.pos, f.catchUpTarget, .1) {
		f.catchUpTarget = f.ground
		s.followNav.Reset()
	}
	target := f.ground
	if f.catchUpTargetKnown {
		target = f.catchUpTarget
	}
	f.movementInProgress = true
	f.movementIntent = intent
	s.followFormationStepLocked(ctx, target, .1)
	f.movementInProgress = false
	f.movementIntent = portalIntent{}
	return true
}
