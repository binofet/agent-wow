package buddyactions

import (
	"context"
	"encoding/binary"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type dungeonPortal struct {
	trigger, source, destination uint32
	center                       position
	radius                       float64
}

// The geometry is from build 12340 AreaTrigger.dbc, with destination maps
// from AzerothCore's areatrigger_teleport table. The companion approaches the
// owner's observed ground pose, never a guessed height at the sphere center.
var dungeonEntrances = [...]dungeonPortal{
	{trigger: 78, source: 0, destination: 36, center: position{x: -11208.5, y: 1685.339966, z: 25.7612}, radius: 7},
	{trigger: 228, source: 1, destination: 43, center: position{x: -753.596008, y: -2212.780029, z: 21.5403}, radius: 13},
	{trigger: 324, source: 0, destination: 90, center: position{x: -5161.33, y: 939.623, z: 258.288}, radius: 10},
	{trigger: 322, source: 90, destination: 0, center: position{x: -312.205, y: -4.63256, z: -148.657}, radius: 14},
	{trigger: 45, source: 0, destination: 189, center: position{x: 2924.38, y: -798.429, z: 161.611}, radius: 8}, // Scarlet Monastery Graveyard
}

const portalApproachDistance = 45

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
	sourceVisit           uint64
	followRevision        uint64
	ownerPoseRevision     uint64
	witness               uint64
	catchUpHost           bool
	movementWitness       uint64
	movementVisit         uint64
	movementFollow        uint64
	movementHost          bool
}

func (s *Service) observeDungeonPortalLocked(w *worldv1.SnapshotResponse, now time.Time) {
	owner := w.GetOwner()
	if !s.state.Following || owner == nil || owner.Guid != ownerGUID {
		s.followPortal = dungeonPortalFollow{}
		s.portalGoal = portalCatchupGoal{}
		return
	}
	if s.followPortal.portal.trigger != 0 && (s.followPortal.followRevision != s.followRevision ||
		s.followPortal.sourceVisit != w.VisitRevision && !s.followPortal.notified) {
		s.followPortal = dungeonPortalFollow{}
		s.portalGoal = portalCatchupGoal{}
	}
	if !owner.Present {
		if s.followPortal.portal.trigger != 0 && s.followPortal.lostAt.IsZero() {
			s.followPortal.lostAt = now
		}
		return
	}
	f := s.followPortal
	if f.notified && w.GetSelf().GetMapKnown() && w.GetSelf().GetMapId() == f.portal.destination {
		return // Let the follow tick observe the transfer before discarding its witness.
	}
	if f.approaching || f.notified {
		s.followNav.Reset()
	}
	// Reappearance ends this crossing attempt. A later disappearance requires
	// a new sighting at an entrance, even if the character remains nearby.
	s.followPortal = dungeonPortalFollow{}
	if safePlayers(w, s.selfGUID) != "" || owner.PositionAgeMs > 2000 {
		return
	}
	ground := playerPosition(owner)
	for _, portal := range dungeonEntrances {
		if owner.MapId == portal.source && distance(ground, portal.center) < portal.radius {
			witness := f.witness
			if witness == 0 || f.portal.trigger != portal.trigger || f.sourceVisit != w.VisitRevision ||
				f.followRevision != s.followRevision || f.ownerPoseRevision != owner.PositionRevision ||
				f.ground != ground || !f.lostAt.IsZero() || f.notified {
				s.portalWitnessRevision++
				witness = s.portalWitnessRevision
				s.portalGoal = portalCatchupGoal{}
			}
			s.followPortal = dungeonPortalFollow{
				portal:      portal,
				ground:      ground,
				seenAt:      now.Add(-time.Duration(owner.PositionAgeMs) * time.Millisecond),
				catchingUp:  distance(playerPosition(w.Self), ground) > portalApproachDistance,
				sourceVisit: w.VisitRevision, followRevision: s.followRevision, ownerPoseRevision: owner.PositionRevision, witness: witness,
			}
			return
		}
	}
}

func (s *Service) portalSourceCurrentLocked(w *worldv1.SnapshotResponse, now time.Time) bool {
	f := s.followPortal
	p, owner := w.GetSelf(), w.GetOwner()
	maxDistance := float64(portalApproachDistance)
	if f.catchingUp {
		maxDistance = buddynav.MaxRouteLength
	}
	return s.state.Following && now.Before(s.followUntil) && !s.independent &&
		f.sourceVisit == w.GetVisitRevision() && f.followRevision == s.followRevision &&
		(f.sourceVisit == 0 || s.matchesCurrentEncounterVisitLocked(w)) &&
		f.portal.trigger != 0 &&
		owner != nil && owner.Guid == ownerGUID && !owner.Present &&
		(!owner.MapKnown || owner.MapId == f.portal.source || owner.MapId == f.portal.destination) &&
		safeQuestCharacter(w, s.selfGUID) == "" && p.MapId == f.portal.source &&
		distance(playerPosition(p), f.ground) <= maxDistance
}

func (s *Service) portalSourceReadyLocked(w *worldv1.SnapshotResponse, now time.Time) bool {
	if !s.portalSourceCurrentLocked(w, now) {
		return false
	}
	policy := s.portalPolicyLocked(w, now)
	return policy.Valid && policy.Eligible
}

func (s *Service) portalMovementCurrentLocked(w *worldv1.SnapshotResponse) bool {
	f := s.followPortal
	return f.movementInProgress && s.portalSourceCurrentLocked(w, s.now()) &&
		f.witness == f.movementWitness && f.sourceVisit == f.movementVisit && s.followRevision == f.movementFollow &&
		f.movementHost == s.hostEncounterActiveLocked() && (!f.movementHost ||
		!f.catchingUp || s.now().Before(s.portalGoal.until) && s.portalGoal.witness == f.witness)
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
	f := &s.followPortal
	if f.portal.trigger == 0 || !s.state.Following {
		return false
	}
	if f.notified && w.GetSelf().GetMapKnown() && w.GetSelf().GetMapId() == f.portal.destination {
		s.followPortal = dungeonPortalFollow{}
		s.portalGoal = portalCatchupGoal{}
		s.followNav.Reset()
		s.lastStep = now
		s.state.Reason = "follow_instance_transfer_observed"
		return true
	}
	if !s.portalSourceCurrentLocked(w, now) {
		return false
	}
	policy := s.portalPolicyLocked(w, now)
	if !policy.Valid {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "follow_instance_policy_unavailable"
		return true
	}
	if !policy.Eligible {
		return false
	}
	s.lastStep = now
	if f.notified {
		s.state.Reason = "follow_waiting_for_instance_transfer"
		return true
	}
	if !policy.Ready {
		s.stopLocked(ctx)
		s.state.Reason = "follow_waiting_at_instance_entrance"
		return true
	}
	if !s.healingPartyLocked(ctx)[ownerGUID] || !s.refreshPositionLocked(w) ||
		!s.stopAutoAttackLocked(ctx) || s.state.AutoAttackUnconfirmed {
		s.stopLocked(ctx)
		s.state.Reason = "follow_instance_party_or_pose_unavailable"
		return true
	}
	if distance(s.pos, f.portal.center) < f.portal.radius-0.03 {
		if !s.stopLocked(ctx) {
			return true
		}
		// STOP and the party lookup may race an owner reappearance, combat, or
		// correction. Bind entry to the fresh source map and current tracker.
		fresh, err := s.snapshot(ctx)
		if err != nil || !s.portalSourceCurrentLocked(fresh, s.now()) ||
			!s.refreshPositionLocked(fresh) || distance(s.pos, f.portal.center) >= f.portal.radius-0.03 {
			s.state.Reason = "follow_instance_source_changed"
			return true
		}
		current := s.portalPolicyLocked(fresh, s.now())
		if !current.Valid || !current.Eligible || !current.Ready {
			s.state.Reason = "follow_instance_policy_changed"
			return true
		}
		// The pure source read can race a new World observation. Recheck
		// physical entry after it, with no further policy callback before send.
		latest, err := s.snapshot(ctx)
		if err != nil || !s.portalSourceCurrentLocked(latest, s.now()) || !s.refreshPositionLocked(latest) ||
			distance(s.pos, f.portal.center) >= f.portal.radius-0.03 {
			s.state.Reason = "follow_instance_source_changed"
			return true
		}
		f.notified = true // Do not replay an ambiguous write.
		if s.sendLocked(ctx, opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, f.portal.trigger)) {
			s.state.Reason = "follow_waiting_for_instance_transfer"
		}
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
	if f.catchingUp {
		host := s.hostEncounterActiveLocked()
		if f.catchUpHost != host {
			f.catchUpTargetKnown = false
			f.catchUpHost = host
			s.followNav.Reset()
		}
		if host && (!now.Before(s.portalGoal.until) || s.portalGoal.witness != f.witness) {
			f.catchUpTargetKnown = false
			s.stopLocked(ctx)
			s.state.Reason = "follow_instance_waiting_for_policy"
			return true
		}
	}
	if f.catchingUp && !f.catchUpTargetKnown {
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
		witness, follow, visit, host := f.witness, f.followRevision, f.sourceVisit, s.hostEncounterActiveLocked()
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
		if err != nil || !s.portalSourceReadyLocked(fresh, s.now()) ||
			f.witness != witness || f.followRevision != follow || f.sourceVisit != visit ||
			!samePortalStart(start, s.portalStartLocked(fresh)) || host &&
			(!s.hostEncounterActiveLocked() || !s.now().Before(s.portalGoal.until) || s.portalGoal.witness != witness) {
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
	f.movementWitness, f.movementVisit, f.movementFollow = f.witness, f.sourceVisit, s.followRevision
	f.movementHost = s.hostEncounterActiveLocked()
	s.followFormationStepLocked(ctx, target, .1)
	f.movementInProgress = false
	return true
}
