package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	crowdControlLease = 25 * time.Second
	crowdControlReach = 60 // Bounds the exact selected target and owned route.
)

type crowdControlIntent struct {
	active, executing, stealthSent bool
	target                         uint64
	rank, class, mapID             uint32
	kind                           spellcatalog.Kind
	phase                          string
	until, nextPlan                time.Time
	origin, goal, plannedTarget    position
	path                           []position
	index                          int
}

func (c *crowdControlIntent) matches(rank uint32, target uint64, kind spellcatalog.Kind) bool {
	return c.target == target && c.rank == rank && c.kind == kind
}

func (c *crowdControlIntent) snapshot(now time.Time) *actionsv1.CrowdControlState {
	if c.target == 0 {
		return nil
	}
	out := &actionsv1.CrowdControlState{Active: c.active, TargetGuid: c.target, SpellId: c.rank, Phase: c.phase}
	if c.active && now.Before(c.until) {
		out.LeaseRemainingMs = uint64(c.until.Sub(now).Milliseconds())
	}
	return out
}

func (s *Service) endCrowdControlLocked(ctx context.Context, phase string) {
	if !s.crowdControl.active {
		return
	}
	s.crowdControl.active, s.crowdControl.phase, s.crowdControl.path = false, phase, nil
	s.stopLocked(ctx)
}

func (s *Service) requestCrowdControlApproachLocked(ctx context.Context, rank uint32, target uint64, kind spellcatalog.Kind, class uint32, approach, cancel bool) (*actionsv1.State, error) {
	c := &s.crowdControl
	if c.active {
		if !c.matches(rank, target, kind) {
			return nil, status.Error(codes.FailedPrecondition, "a different crowd control preparation owns movement")
		}
		if cancel {
			s.endCrowdControlLocked(ctx, "canceled")
		} else if !approach && c.phase == "ready" {
			s.commitCrowdControlLocked(ctx)
		}
		return s.stateLocked(), nil
	}
	if cancel || !approach {
		return s.stateLocked(), nil
	}
	spell, known := s.learnedContractsLocked()[rank]
	if !s.spellbookKnown || !known || spell.Capability().Kind != kind {
		return nil, status.Error(codes.FailedPrecondition, "exact learned crowd-control rank required")
	}
	if s.state.InteractionOwner != "" || s.state.Practicing || s.engage.active || s.fargodeep.active || s.state.Traveling ||
		s.recovery.state.Active || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed ||
		s.autoTarget != 0 || s.state.AutoAttackRequested || s.state.AutoAttacking || s.autoStopping {
		return nil, status.Error(codes.FailedPrecondition, "crowd control preparation needs available movement custody")
	}
	w, err := s.snapshot(ctx, target)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) ||
		!w.Self.ClassKnown || w.Self.ClassId != class || !crowdControlTarget(w, target) {
		return nil, status.Error(codes.FailedPrecondition, "current caster and exact selected living NPC required")
	}
	if !s.stopLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "prior movement could not be stopped")
	}
	*c = crowdControlIntent{active: true, target: target, rank: rank, class: class, kind: kind,
		mapID: w.Self.MapId, origin: s.pos, until: s.now().Add(crowdControlLease), phase: "waiting_for_target"}
	return s.stateLocked(), nil
}

func crowdControlTarget(w *worldv1.SnapshotResponse, guid uint64) bool {
	c := w.GetRequestedCreature()
	return c != nil && c.Guid == guid && c.Present && c.MapKnown && c.MapId == w.GetSelf().GetMapId() &&
		c.AliveKnown && c.Alive && c.HealthKnown && c.Health != 0 && c.UnitFlagsKnown &&
		c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 && c.UnitFlags_2&1 == 0 && c.DynamicFlags&0x20 == 0
}

// Current finite spline estimates support both approach and legal target casts.
func crowdControlPose(c *worldv1.CreatureState) (position, bool, bool) {
	p, known, _ := creaturePosition(c)
	// A target may walk throughout a cast. Estimated current spline poses are
	// suitable for range/LoS checks; only the caster must finish positioning.
	return p, known, known
}

func (s *Service) crowdControlMovementValidLocked(w *worldv1.SnapshotResponse) bool {
	c := &s.crowdControl
	if !c.active || !s.now().Before(c.until) || safeSelfCombat(w, s.selfGUID) != "" || w.Self.MapId != c.mapID ||
		w.Self.PositionRevision != s.positionRevision || !crowdControlTarget(w, c.target) ||
		w.Self.ChannelSpellId != 0 || s.selfAttackerLocked(w) != nil {
		return false
	}
	if c.kind == spellcatalog.Sap && (!s.stealthedLocked() || (w.Self.UnitFlags|w.RequestedCreature.UnitFlags)&0x80000 != 0) {
		return false
	}
	p, known, _ := crowdControlPose(w.RequestedCreature)
	return known && distance(c.origin, p) <= crowdControlReach && distance(c.plannedTarget, p) <= 1.5 &&
		(c.kind != spellcatalog.Sap || behindEnemy(c.goal, p))
}

func (s *Service) observeCrowdControlLocked(ctx context.Context) (*worldv1.SnapshotResponse, spellcatalog.Spell, position, bool, bool) {
	c := &s.crowdControl
	if !s.now().Before(c.until) {
		s.endCrowdControlLocked(ctx, "expired")
		return nil, spellcatalog.Spell{}, position{}, false, false
	}
	w, err := s.snapshot(ctx, c.target)
	phase := ""
	switch {
	case err != nil || safeSelfCombat(w, s.selfGUID) != "":
		phase = "caster_observation_unavailable"
	case w.Self.MapId != c.mapID:
		phase = "caster_map_changed"
	case !s.refreshPositionLocked(w) || s.state.MotionUnconfirmed:
		phase = "caster_position_unconfirmed"
	case w.Self.ClassId != c.class:
		phase = "caster_class_changed"
	case w.Self.ChannelSpellId != 0:
		phase = "caster_channeling"
	case s.selfAttackerLocked(w) != nil:
		phase = "caster_under_attack"
	}
	if phase != "" {
		s.endCrowdControlLocked(ctx, phase)
		return nil, spellcatalog.Spell{}, position{}, false, false
	}
	if !crowdControlTarget(w, c.target) {
		s.endCrowdControlLocked(ctx, "target_lost")
		return nil, spellcatalog.Spell{}, position{}, false, false
	}
	spell, learned := s.learnedContractsLocked()[c.rank]
	if !learned || spell.Capability().Kind != c.kind {
		s.endCrowdControlLocked(ctx, "spell_unavailable")
		return nil, spellcatalog.Spell{}, position{}, false, false
	}
	p, known, settled := crowdControlPose(w.RequestedCreature)
	if known && distance(c.origin, p) > crowdControlReach {
		s.endCrowdControlLocked(ctx, "target_outside_approach")
		return nil, spellcatalog.Spell{}, position{}, false, false
	}
	return w, spell, p, known, settled
}

func (s *Service) crowdControlLineOfSightLocked(ctx context.Context, from, to position) bool {
	ray := s.crowdControlSight
	if ray == nil {
		ray = func(ctx context.Context, root string, mapID uint32, a, b position) (bool, error) {
			return buddynav.LineOfSightContext(ctx, root, mapID,
				buddynav.Point{X: a.x, Y: a.y, Z: a.z + 1.5}, buddynav.Point{X: b.x, Y: b.y, Z: b.z + 1.5})
		}
	}
	clear, err := ray(ctx, s.navDataDir, s.posMap, from, to)
	if err == nil {
		return clear
	}
	// Missing static collision data permits only a verified direct ground
	// corridor as a casting heuristic. The server still checks actual LOS.
	path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, from, to)
	return err == nil && directRemainingRoute(from, to, path, 0)
}

func (s *Service) crowdControlReadyLocked(ctx context.Context, spell spellcatalog.Spell, to position, settled bool) bool {
	if !settled || !s.casterRequirementsLocked(spell) || s.now().Before(s.spellReady[spell.ID]) {
		return false
	}
	r := distance(s.pos, to)
	if r < spell.MinHostile || r > spell.MaxHostile-.5 ||
		s.crowdControl.kind == spellcatalog.Sap && (!s.stealthedLocked() || !behindEnemy(s.pos, to)) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	return s.crowdControlLineOfSightLocked(ctx, s.pos, to)
}

func (s *Service) crowdControlRouteLocked(ctx context.Context, spell spellcatalog.Spell, to position) {
	c := &s.crowdControl
	if s.now().Before(c.nextPlan) {
		return
	}
	c.nextPlan = s.now().Add(500 * time.Millisecond)
	planCtx, cancel := context.WithTimeout(ctx, 350*time.Millisecond)
	defer cancel()
	plan := func(from, to position) []position {
		path, err := s.mapNavigationPath(planCtx, s.navDataDir, c.mapID, from, to)
		if err != nil {
			return nil
		}
		return path
	}
	var path []position
	if c.kind == spellcatalog.Sap {
		path = sapFlankPath(s.pos, to, plan)
	} else {
		path = s.rangedCastPathLocked(planCtx, spell, to, plan, func(position) bool { return true })
	}
	if len(path) != 0 && routeRemaining(s.pos, path, 0) <= crowdControlReach {
		c.path, c.index, c.goal, c.plannedTarget = path, 0, path[len(path)-1], to
	}
}

func (s *Service) rangedCastPathLocked(ctx context.Context, spell spellcatalog.Spell, target position, plan func(position, position) []position, usable func(position) bool) []position {
	// Leave room for target movement and navmesh endpoint projection. Try the
	// caster's side of the target first, then angles around a possible obstacle.
	minRange, maxRange := max(4, spell.MinHostile), spell.MaxHostile-.5
	radius := min(maxRange-1, max(minRange+1, distance(s.pos, target)))
	heading := math.Atan2(float64(s.pos.y-target.y), float64(s.pos.x-target.x))
	canCast := func(p position) bool {
		d := distance(p, target)
		return ctx.Err() == nil && d >= minRange && d <= maxRange && usable(p) && s.crowdControlLineOfSightLocked(ctx, p, target)
	}
	for _, offset := range []float64{0, -math.Pi / 4, math.Pi / 4} {
		for _, scale := range []float64{1, .75, .5} {
			if ctx.Err() != nil {
				return nil
			}
			r := max(minRange+1, radius*scale)
			goal := position{x: target.x + float32(r*math.Cos(heading+offset)), y: target.y + float32(r*math.Sin(heading+offset)), z: target.z}
			path := plan(s.pos, goal)
			if len(path) > 0 && routeRemaining(s.pos, path, 0) <= crowdControlReach && canCast(path[len(path)-1]) {
				return path
			}
		}
	}
	// In narrow or uneven corridors the radial positions can miss walkable
	// ground. Use a stand-off waypoint from the verified route in that case.
	if ctx.Err() == nil {
		path := plan(s.pos, target)
		for i := 1; i < len(path); i++ {
			if canCast(path[i]) {
				return path[:i+1]
			}
		}
	}
	return nil
}

func sapFlankPath(from, target position, plan func(position, position) []position) []position {
	relative := func(angle, radius float64) position {
		heading := float64(target.o) + angle
		return position{target.x + float32(radius*math.Cos(heading)), target.y + float32(radius*math.Sin(heading)), target.z, target.o}
	}
	for _, offset := range []float64{0, -.6, .6} {
		rear := relative(math.Pi+offset, 2.5)
		if path := plan(from, rear); sapRearPathValid(from, target, path) {
			return path
		}
	}
	rear := relative(math.Pi, 2.5)
	for _, side := range []float64{-math.Pi / 2, math.Pi / 2} {
		first := plan(from, relative(side, 5))
		if len(first) == 0 {
			continue
		}
		if second := plan(first[len(first)-1], rear); len(second) != 0 {
			if path := append(first, second...); sapRearPathValid(from, target, path) {
				return path
			}
		}
	}
	return nil
}

func sapRearPathValid(from, target position, path []position) bool {
	if len(path) == 0 || !behindEnemy(path[len(path)-1], target) || distance(path[len(path)-1], target) > 4.5 {
		return false
	}
	// Check whole segments, including any planner start normalization, so an
	// apparent rear route cannot cut through the mob on the way to its flank.
	for _, next := range path {
		dx, dy := float64(next.x-from.x), float64(next.y-from.y)
		ax, ay := float64(target.x-from.x), float64(target.y-from.y)
		length2 := dx*dx + dy*dy
		if length2 > 0 {
			t := max(0, min(1, (ax*dx+ay*dy)/length2))
			ax, ay = ax-t*dx, ay-t*dy
		}
		if math.Hypot(ax, ay) < 1.8 {
			return false
		}
		from = next
	}
	return true
}

func (s *Service) crowdControlTickLocked(ctx context.Context) bool {
	c := &s.crowdControl
	if !c.active {
		return false
	}
	w, spell, to, known, settled := s.observeCrowdControlLocked(ctx)
	if w == nil {
		return false // Emergency defense and ordinary controls can resume.
	}
	s.lastStep = s.now()
	if c.kind == spellcatalog.Sap {
		ready, phase, err := s.sapReadyLocked(ctx, w, spell)
		if err != nil {
			s.endCrowdControlLocked(ctx, phase)
			return false
		}
		if !ready {
			c.phase = "waiting_for_requirements"
			s.stopLocked(ctx)
			return true
		}
		if !s.stealthedLocked() {
			c.phase = "waiting_for_stealth"
			if !s.stopLocked(ctx) {
				s.endCrowdControlLocked(ctx, "caster_stop_unconfirmed")
				return false
			}
			if !c.stealthSent && !s.state.CastPending && !s.now().Before(s.nextAssist) {
				c.stealthSent = s.prepareSapStealthLocked(ctx, c.target) == nil
			}
			return true
		}
	}
	if !known {
		c.phase, c.path = "waiting_for_target", nil
		s.stopLocked(ctx)
		return true
	}
	if s.state.CastPending || !w.Self.PowerKnown || int32(w.Self.PowerType) != spell.Power || uint64(w.Self.Power) < spellCost(spell, w.Self) {
		c.phase = "waiting_for_requirements"
		s.stopLocked(ctx)
		return true
	}
	if c.phase == "ready" || s.crowdControlReadyLocked(ctx, spell, to, settled) && s.stopLocked(ctx) {
		c.phase = "ready"
		return true
	}
	if len(c.path) == 0 || c.index >= len(c.path) || distance(c.plannedTarget, to) > 1.5 ||
		c.kind == spellcatalog.Sap && !behindEnemy(c.goal, to) {
		c.path = nil
		s.crowdControlRouteLocked(ctx, spell, to)
	}
	speed := observedRunSpeed(w.Self)
	if len(c.path) == 0 || speed == 0 {
		c.phase = "approaching"
		s.stopLocked(ctx)
		return true
	}
	c.executing = true
	moved := s.navigationStepAtSpeedLocked(ctx, c.path, &c.index,
		func(p position) bool { return finite(p) && distance(c.origin, p) <= crowdControlReach }, speed)
	c.executing = false
	if !moved {
		c.path = nil
		s.stopLocked(ctx)
	}
	c.phase = "approaching"
	return true
}

func (s *Service) commitCrowdControlLocked(ctx context.Context) {
	c := &s.crowdControl
	w, spell, to, known, settled := s.observeCrowdControlLocked(ctx)
	if w == nil {
		return
	}
	if s.state.Moving || s.state.CastPending || s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist) ||
		w.Self.MovementFlags&0xff != 0 || !w.Self.PowerKnown || int32(w.Self.PowerType) != spell.Power ||
		uint64(w.Self.Power) < spellCost(spell, w.Self) || !known || !s.crowdControlReadyLocked(ctx, spell, to, settled) {
		c.phase = "waiting_for_target"
		return
	}
	if c.kind == spellcatalog.Sap {
		ready, phase, err := s.sapReadyLocked(ctx, w, spell)
		if err != nil || !ready {
			s.endCrowdControlLocked(ctx, phase)
			return
		}
	}
	// A facing write can coincide with target movement. Rebind the exact
	// creature again before spending the one prepared cast.
	if !s.faceEnemyLocked(ctx, to) {
		s.endCrowdControlLocked(ctx, "caster_facing_unconfirmed")
		return
	}
	castFrom := s.pos
	w, spell, fresh, known, settled := s.observeCrowdControlLocked(ctx)
	if w == nil {
		return
	}
	if !known || !settled || distance(to, fresh) > .35 || distance(castFrom, s.pos) > .05 || !s.now().Before(c.until) || ctx.Err() != nil ||
		!w.Self.PowerKnown || int32(w.Self.PowerType) != spell.Power || uint64(w.Self.Power) < spellCost(spell, w.Self) ||
		w.Self.MovementFlags&0xff != 0 || !s.casterRequirementsLocked(spell) ||
		c.kind == spellcatalog.Sap && (!s.stealthedLocked() || !s.weaponReady(w.Self) || !behindEnemy(s.pos, fresh) || (w.Self.UnitFlags|w.RequestedCreature.UnitFlags)&0x80000 != 0) {
		c.phase = "waiting_for_target"
		return
	}
	if !s.stopLocked(ctx) {
		s.endCrowdControlLocked(ctx, "caster_stop_unconfirmed")
		return
	}
	s.endCrowdControlLocked(ctx, "cast_dispatched")
	s.castSupportLocked(ctx, spell, c.target, 0)
	if !s.supportOp.view.TransportWritten {
		c.phase = "cast_unconfirmed"
	}
}
