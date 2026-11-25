package buddyactions

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ApproachLoot advances at most one ordinary ground-movement step toward an
// observed eligible corpse or own verified quest chest. Only the current loot interaction token permits it;
// callers cannot select coordinates or an arbitrary living target.
func (s *Service) ApproachLoot(ctx context.Context, r *actionsv1.ApproachLootRequest) (result *actionsv1.State, err error) {
	if r == nil || r.Token == "" || (r.CorpseGuid == 0) == (r.GameObjectGuid == 0) {
		return nil, status.Error(codes.InvalidArgument, "loot token and exactly one corpse or game-object GUID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// An owned approach already in motion still owes a STOP when its caller
	// cancels. Use fresh bounded transport only for this exact active token.
	defer func() {
		if ctx.Err() != nil && !s.closed && s.state.InteractionOwner == "loot" && subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) == 1 && s.state.Moving {
			stop, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
			s.stopLocked(stop)
			cancel()
			result = s.stateLocked()
		}
	}()
	if e := ctx.Err(); e != nil {
		return nil, status.FromContextError(e).Err()
	}
	return s.approachLootStepLocked(ctx, r, nil)
}

type lootApproachNavigation struct {
	path                         []position
	start, target                position
	selfRevision, targetRevision uint64
	mapID                        uint32
	session                      string
	self, visit                  uint64
}

func (s *Service) approachLootStepLocked(ctx context.Context, r *actionsv1.ApproachLootRequest, route *lootApproachNavigation) (*actionsv1.State, error) {
	s.expireInteractionLocked()
	if s.closed || s.state.InteractionOwner != "loot" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 {
		return nil, status.Error(codes.FailedPrecondition, "owned loot interaction is required")
	}
	pause := func(reason string) (*actionsv1.State, error) {
		if route != nil && (route.session != s.sessionID || route.self != s.selfGUID || route.visit != s.lastWorld.GetVisitRevision()) {
			return s.stateLocked(), nil
		}
		if s.stopLocked(ctx) {
			s.state.Reason = reason
		}
		return s.stateLocked(), nil
	}
	if ctx.Err() != nil || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.autoTarget != 0 || s.interactionUntil.Sub(s.now()).Milliseconds() < 1000 {
		return pause("loot_approach_state_or_lease_unavailable")
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return pause("loot_approach_observations_unavailable")
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return pause("loot_approach_" + reason)
	}
	if !s.refreshPositionLocked(w) {
		return pause("loot_approach_authoritative_position_changed")
	}
	if !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(w.Self.RunSpeed)) {
		return pause("loot_approach_normal_run_speed_unknown")
	}
	var target position
	var targetRevision uint64
	var targetKnown bool
	if r.GameObjectGuid != 0 {
		if s.navDataDir == "" {
			return pause("loot_approach_harvest_requires_verified_navigation")
		}
		entry := uint32(r.GameObjectGuid >> 24 & 0xffffff)
		if !ownHarvestQuest(w, s.selfGUID, entry) {
			return pause("loot_approach_harvest_quest_not_incomplete")
		}
		var object *worldv1.GameObjectState
		if len(w.QuestObjects) <= 32 {
			for _, g := range w.QuestObjects {
				if g != nil && g.Guid == r.GameObjectGuid {
					if object != nil {
						return pause("loot_approach_harvest_observation_ambiguous")
					}
					object = g
				}
			}
		}
		if object == nil || !object.Present || object.Guid>>48 != 0xf110 || object.Guid&0xffffff == 0 || uint32(object.Guid>>24&0xffffff) != entry || object.Entry != entry || !object.TypeKnown || object.GameObjectType != 3 || !object.MapKnown || object.MapId != w.Self.MapId || !object.PositionKnown || !object.FlagsKnown || object.Flags&(1|8|16) != 0 {
			return pause("loot_approach_harvest_object_not_eligible")
		}
		target = position{x: object.X, y: object.Y, z: object.Z, o: object.Orientation}
		targetRevision, targetKnown = object.PositionRevision, finite(target)
	} else {
		corpse := approachCorpse(w, r.CorpseGuid)
		if !lootApproachCorpseEligible(w, corpse) {
			return pause("loot_approach_corpse_not_eligible")
		}
		target, targetKnown, _ = creaturePosition(corpse)
		targetRevision = corpse.PositionRevision
	}
	d := distance(s.pos, target)
	if !targetKnown || d > 12 || math.Abs(float64(target.z-s.pos.z)) > 1.5 {
		return pause("loot_approach_target_unknown_far_or_vertical")
	}
	{
		if !finite(s.pos) || !finite(target) {
			return pause("loot_approach_outside_supported_navigation_area")
		}
		if route == nil {
			route = &lootApproachNavigation{start: s.pos, target: target, selfRevision: s.positionRevision, targetRevision: targetRevision, mapID: s.posMap, session: s.sessionID, self: s.selfGUID, visit: w.VisitRevision}
			planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			path, err := s.mapNavigationPath(planCtx, s.navDataDir, s.posMap, s.pos, target)
			if err == nil {
				err = planCtx.Err()
			}
			cancel()
			if err == nil && (len(path) > 256 || distance(s.pos, path[0])+routeRemaining(path[0], path, 1) > 12) {
				err = errors.New("nearby loot route exceeds12yd or256points")
			}
			if err != nil {
				s.state.NavAvailable = false
				s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
				return pause("loot_approach_navigation_unavailable")
			}
			route.path = append([]position(nil), path...)
			// Planning does not reserve a stale corpse, position, or lease.
			// This second validation cannot recurse into another plan.
			return s.approachLootStepLocked(ctx, r, route)
		}
		if route.session != s.sessionID || route.self != s.selfGUID || route.visit != w.VisitRevision || route.mapID != s.posMap || route.selfRevision != s.positionRevision || distance(route.start, s.pos) > .05 || route.targetRevision != targetRevision || distance(route.target, target) > .01 {
			return pause("loot_approach_position_changed_after_planning")
		}
		s.state.NavAvailable = true
		s.state.NavReason = "client_navmesh_route_verified"
		if d <= 3.01 && routeRemaining(s.pos, route.path, 0) <= 3.01 {
			return pause("loot_approach_within_distance")
		}
		index := 0
		for index < len(route.path) && distance(s.pos, route.path[index]) <= .005 {
			index++
		}
		if index == len(route.path) {
			return pause("loot_approach_route_ended_outside_range")
		}
		p := s.pos
		op := uint32(opcode.MSGMoveStartForward)
		if s.state.Moving {
			elapsed := s.now().Sub(s.posAt).Seconds()
			if elapsed < 0 || elapsed > .6 {
				return pause("loot_approach_tick_late")
			}
			if elapsed < .15 {
				s.state.Reason = "loot_approach_waiting_for_next_step"
				return s.stateLocked(), nil
			}
			var ok bool
			p, index, ok = followPathStep(s.pos, route.path, index, 7*elapsed)
			if !ok || math.Abs(float64(p.z-s.pos.z)) > 2 || distance(s.pos, p) > 7*elapsed+.01 {
				return pause("loot_approach_invalid_navigation_step")
			}
			op = opcode.MSGMoveHeartbeat
		} else {
			p.o = followFacing(p, route.path[index])
		}
		previous := s.lootApproachGuard
		s.lootApproachGuard = func(call context.Context, current *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
			if r.GameObjectGuid != 0 {
				return current, call.Err() == nil && s.harvestApproachCurrentLocked(r, route, current)
			}
			return current, s.corpseApproachCurrentLocked(call, r, route, current)
		}
		defer func() { s.lootApproachGuard = previous }()
		if !s.movementLocked(ctx, op, p, true) {
			if s.state.InteractionOwner == "loot" && subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) == 1 {
				return pause(s.state.Reason)
			}
			return s.stateLocked(), nil
		}
		if distance(s.pos, target) <= 3.01 && routeRemaining(s.pos, route.path, index) <= 3.01 {
			return pause("loot_approach_within_distance")
		}
		s.state.Reason = "loot_approaching_client_navmesh_estimate"
		return s.stateLocked(), nil
	}
}

// The clock callback may receive a quest completion, combat or object update.
// Recheck this exact recipe/source before START/HEARTBEAT; STOP needs no chest.
func (s *Service) harvestApproachCurrentLocked(r *actionsv1.ApproachLootRequest, route *lootApproachNavigation, w *worldv1.SnapshotResponse) bool {
	entry := uint32(r.GameObjectGuid >> 24 & 0xffffff)
	if route == nil || safeQuestCharacter(w, s.selfGUID) != "" || !ownHarvestQuest(w, s.selfGUID, entry) || len(w.QuestObjects) > 32 || s.closed || s.state.InteractionOwner != "loot" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 || s.interactionUntil.Sub(s.now()) < time.Second || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.autoTarget != 0 || w.Self.MapId != route.mapID || w.Self.PositionRevision != route.selfRevision || s.posMap != route.mapID || s.positionRevision != route.selfRevision || distance(s.pos, route.start) > .05 {
		return false
	}
	var object *worldv1.GameObjectState
	for _, g := range w.QuestObjects {
		if g != nil && g.Guid == r.GameObjectGuid {
			if object != nil {
				return false
			}
			object = g
		}
	}
	if object == nil || !object.Present || object.Guid>>48 != 0xf110 || object.Guid&0xffffff == 0 || uint32(object.Guid>>24&0xffffff) != entry || object.Entry != entry || !object.TypeKnown || object.GameObjectType != 3 || !object.MapKnown || object.MapId != route.mapID || !object.PositionKnown || !object.FlagsKnown || object.Flags&(1|8|16) != 0 || object.PositionRevision != route.targetRevision {
		return false
	}
	target := position{x: object.X, y: object.Y, z: object.Z, o: object.Orientation}
	return finite(target) && distance(target, route.target) <= .01 && distance(s.pos, target) <= 12 && math.Abs(float64(target.z-s.pos.z)) <= 1.5
}

// Lua selects the exact offered corpse. This final native check grants no
// quest/party exception: only the same live body, route and loot lease can move.
func (s *Service) corpseApproachCurrentLocked(ctx context.Context, r *actionsv1.ApproachLootRequest, route *lootApproachNavigation, w *worldv1.SnapshotResponse) bool {
	now := s.now()
	if route == nil || ctx.Err() != nil || s.closed || route.session != s.sessionID || route.self != s.selfGUID || w.GetVisitRevision() != route.visit ||
		safeQuestCharacter(w, s.selfGUID) != "" || s.state.InteractionOwner != "loot" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 ||
		!now.Before(s.interactionUntil) || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.autoTarget != 0 ||
		w.Self.MapId != route.mapID || w.Self.PositionRevision != route.selfRevision || s.posMap != route.mapID || s.positionRevision != route.selfRevision || distance(s.pos, route.start) > .05 {
		return false
	}
	corpse := approachCorpse(w, r.CorpseGuid)
	if !lootApproachCorpseEligible(w, corpse) || corpse.PositionRevision != route.targetRevision {
		return false
	}
	target, known, _ := creaturePosition(corpse)
	return known && distance(target, route.target) <= .01 && distance(s.pos, target) <= 12 && math.Abs(float64(target.z-s.pos.z)) <= 1.5
}

func lootApproachCorpseEligible(w *worldv1.SnapshotResponse, corpse *worldv1.CreatureState) bool {
	return w.GetSelf() != nil && corpse != nil && corpse.Present && corpse.MapKnown && corpse.MapId == w.Self.MapId &&
		corpse.HealthKnown && corpse.Health == 0 && corpse.AliveKnown && !corpse.Alive && corpse.DynamicFlags&1 != 0 &&
		(corpse.Guid>>48 == 0xf130 || corpse.Guid>>48 == 0xf150)
}

func approachCorpse(w *worldv1.SnapshotResponse, guid uint64) *worldv1.CreatureState {
	if w == nil || len(w.LootableCorpses) > 64 {
		return nil
	}
	var found *worldv1.CreatureState
	for _, c := range w.LootableCorpses {
		if c != nil && c.Guid == guid {
			if found != nil {
				return nil
			}
			found = c
		}
	}
	return found
}

// This movement exception is bound to the acting character's actual own quest
// log, never to a party member's quests or an arbitrary game-object target.
func ownHarvestQuest(w *worldv1.SnapshotResponse, self uint64, objectEntry uint32) bool {
	questID := uint32(0)
	switch objectEntry {
	case 161557:
		questID = 3904
	case 176793:
		questID = 5545
	default:
		return false
	}
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false
	}
	var seen [25]bool
	found := false
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || seen[q.Slot] {
			return false
		}
		seen[q.Slot] = true
		if q.QuestId == questID && !q.Complete && !q.Failed && q.State&3 == 0 {
			found = true
		}
	}
	return found
}
