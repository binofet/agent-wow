package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
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
}

const (
	portalApproachDistance = 45
	portalCatchUpWindow    = 75 * time.Second
)

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
}

func (s *Service) observeDungeonPortalLocked(w *worldv1.SnapshotResponse, now time.Time) {
	owner := w.GetOwner()
	if !s.state.Following || owner == nil || owner.Guid != ownerGUID {
		s.followPortal = dungeonPortalFollow{}
		return
	}
	if !owner.Present {
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
			s.followPortal = dungeonPortalFollow{
				portal:     portal,
				ground:     ground,
				seenAt:     now.Add(-time.Duration(owner.PositionAgeMs) * time.Millisecond),
				catchingUp: distance(playerPosition(w.Self), ground) > portalApproachDistance,
			}
			return
		}
	}
}

func (s *Service) portalSourceReadyLocked(w *worldv1.SnapshotResponse, now time.Time) bool {
	f := s.followPortal
	p, owner := w.GetSelf(), w.GetOwner()
	maxAge, maxDistance := 20*time.Second, float64(portalApproachDistance)
	if f.catchingUp {
		maxAge, maxDistance = portalCatchUpWindow, buddynav.MaxRouteLength
	}
	return s.state.Following && now.Before(s.followUntil) && !s.independent &&
		f.portal.trigger != 0 && !now.Before(f.seenAt) && now.Sub(f.seenAt) <= maxAge &&
		owner != nil && owner.Guid == ownerGUID && !owner.Present &&
		(!owner.MapKnown || owner.MapId == f.portal.source || owner.MapId == f.portal.destination) &&
		safeQuestCharacter(w, s.selfGUID) == "" && p.MapId == f.portal.source &&
		distance(playerPosition(p), f.ground) <= maxDistance
}

func (s *Service) portalMovementAllowedLocked(w *worldv1.SnapshotResponse) bool {
	return s.followPortal.movementInProgress && s.portalSourceReadyLocked(w, s.now())
}

// A single query can find a partial corridor between two floors that become
// connected after taking a turn. Use an intermediate point only when the
// physical planner verifies both the approach to it and the route to the portal.
func (s *Service) portalCatchUpTarget(ctx context.Context, ground position) (position, bool) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, ground); err == nil && len(path) != 0 {
		return ground, true
	}
	dx, dy := float64(ground.x-s.pos.x), float64(ground.y-s.pos.y)
	directionLength := math.Hypot(dx, dy)
	if directionLength <= portalApproachDistance {
		return position{}, false
	}
	dx, dy = dx/directionLength, dy/directionLength
	for _, ahead := range []float64{8, 12, 16} {
		for _, side := range []float64{-6, 6} {
			if ctx.Err() != nil {
				return position{}, false
			}
			bend := s.pos
			bend.x += float32(ahead*dx - side*dy)
			bend.y += float32(ahead*dy + side*dx)
			approach, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, bend)
			if err != nil || len(approach) == 0 {
				continue
			}
			bend = approach[len(approach)-1]
			if path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, bend, ground); err == nil && len(path) != 0 {
				return bend, true
			}
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
		s.followNav.Reset()
		s.lastStep = now
		s.state.Reason = "follow_instance_transfer_observed"
		return true
	}
	if !s.portalSourceReadyLocked(w, now) {
		return false
	}
	if f.lostAt.IsZero() {
		f.lostAt = now
		s.followNav.Reset()
	}
	s.lastStep = now
	if f.notified {
		s.state.Reason = "follow_waiting_for_instance_transfer"
		return true
	}
	if now.Sub(f.lostAt) < 500*time.Millisecond {
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
		if err != nil || !s.portalSourceReadyLocked(fresh, s.now()) ||
			!s.refreshPositionLocked(fresh) || distance(s.pos, f.portal.center) >= f.portal.radius-0.03 {
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
	if f.catchingUp && !f.catchUpTargetKnown {
		if now.Before(f.nextCatchUpPlan) {
			return true
		}
		f.nextCatchUpPlan = now.Add(750 * time.Millisecond)
		target, ok := s.portalCatchUpTarget(ctx, f.ground)
		if !ok {
			s.stopLocked(ctx)
			s.state.Reason = "follow_instance_catch_up_route_unavailable"
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
	s.followFormationStepLocked(ctx, target, .1)
	f.movementInProgress = false
	return true
}
