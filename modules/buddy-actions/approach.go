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
// observed eligible corpse or own Milly harvest crate. Only the current loot interaction token permits it;
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
}

func (s *Service) approachLootStepLocked(ctx context.Context, r *actionsv1.ApproachLootRequest, route *lootApproachNavigation) (*actionsv1.State, error) {
	s.expireInteractionLocked()
	if s.closed || s.state.InteractionOwner != "loot" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 {
		return nil, status.Error(codes.FailedPrecondition, "owned loot interaction is required")
	}
	pause := func(reason string) (*actionsv1.State, error) {
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
	coldridgeCorpse := false
	fargodeepCorpse := false
	coldridgeNavigationGoal := coldridgePracticeGoal{questID: 179}
	if r.GameObjectGuid != 0 {
		if s.navDataDir == "" {
			return pause("loot_approach_harvest_requires_verified_navigation")
		}
		if !ownHarvestQuest(w, s.selfGUID) {
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
		if object == nil || !object.Present || object.Guid>>48 != 0xf110 || object.Guid&0xffffff == 0 || uint32(object.Guid>>24&0xffffff) != 161557 || object.Entry != 161557 || !object.TypeKnown || object.GameObjectType != 3 || !object.MapKnown || object.MapId != w.Self.MapId || !object.PositionKnown || !object.FlagsKnown || object.Flags&(1|8|16) != 0 {
			return pause("loot_approach_harvest_object_not_eligible")
		}
		target = position{x: object.X, y: object.Y, z: object.Z, o: object.Orientation}
		targetRevision, targetKnown = object.PositionRevision, finite(target)
	} else {
		var corpse *worldv1.CreatureState
		if len(w.LootableCorpses) <= 64 {
			for _, c := range w.LootableCorpses {
				if c != nil && c.Guid == r.CorpseGuid {
					corpse = c
					break
				}
			}
		}
		if corpse == nil || !corpse.Present || !corpse.MapKnown || corpse.MapId != w.Self.MapId || !corpse.HealthKnown || corpse.Health != 0 || !corpse.AliveKnown || corpse.Alive || corpse.DynamicFlags&1 == 0 || corpse.Guid>>48 != 0xf130 && corpse.Guid>>48 != 0xf150 {
			return pause("loot_approach_corpse_not_eligible")
		}
		quest33, _ := ownWolfQuest(w)
		quest179, _ := ownColdridgeQuest(w, s.selfGUID)
		if s.coldridgeGoal.questID == 170 || s.coldridgeGoal.questID == 183 || s.coldridgeGoal.questID == 182 {
			// Another accepted quest must not replace the goal bound at start.
			quest179 = false
		}
		coldridgeCorpse = w.Self.MapId == 0 && quest179 && corpse.Entry == 705 && uint32(corpse.Guid>>24&0xffffff) == 705 && coldridge(s.pos)
		if s.ownColdridgeGoalCorpseLocked(w, corpse) {
			coldridgeCorpse = true
			coldridgeNavigationGoal = s.coldridgeGoal
		}
		if s.coldridgeGoal.questID == 182 && !coldridgeCorpse {
			return pause("loot_approach_bound_quest_182_scope_invalid")
		}
		fargodeepCorpse = s.ownFargodeepCorpseLocked(ctx, w, corpse)
		if (!quest33 || corpse.Entry != 299) && !coldridgeCorpse && !fargodeepCorpse {
			if reason := safePlayers(w, s.selfGUID); reason != "" {
				return pause("loot_approach_" + reason)
			}
			if w.Owner.UnitFlags&0x80000 != 0 || distance(s.pos, playerPosition(w.Owner)) > 30 {
				return pause("loot_approach_requires_nearby_owner_outside_combat")
			}
		}
		target, targetKnown, _ = creaturePosition(corpse)
		targetRevision = corpse.PositionRevision
	}
	d := distance(s.pos, target)
	if !targetKnown || d > 12 || math.Abs(float64(target.z-s.pos.z)) > 1.5 {
		return pause("loot_approach_target_unknown_far_or_vertical")
	}
	{
		inside := finite
		if coldridgeCorpse {
			inside = coldridgeNavigationGoal.contains
		} else if fargodeepCorpse {
			inside = fargodeepArea
		}
		if !inside(s.pos) || !inside(target) {
			return pause("loot_approach_outside_supported_navigation_area")
		}
		if route == nil {
			planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			var path []position
			var err error
			if coldridgeCorpse {
				path, err = s.coldridgeGoalNavigationPath(planCtx, coldridgeNavigationGoal, s.pos, target)
			} else {
				path, err = s.mapNavigationPath(planCtx, s.navDataDir, s.posMap, s.pos, target)
			}
			if err == nil {
				err = planCtx.Err()
			}
			cancel()
			if err == nil && (len(path) > 256 || distance(s.pos, path[0])+routeRemaining(path[0], path, 1) > 12) {
				err = errors.New("nearby loot route exceeds12yd or256points")
			}
			if err == nil && fargodeepCorpse {
				for _, p := range path {
					if !fargodeepArea(p) {
						err = errors.New("Fargodeep corpse route leaves its bound mission scene")
						break
					}
				}
			}
			if err != nil {
				s.state.NavAvailable = false
				s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
				return pause("loot_approach_navigation_unavailable")
			}
			route = &lootApproachNavigation{path: append([]position(nil), path...), start: s.pos, target: target, selfRevision: s.positionRevision, targetRevision: targetRevision, mapID: s.posMap}
			// Planning does not reserve a stale corpse, position, or lease.
			// This second validation cannot recurse into another plan.
			return s.approachLootStepLocked(ctx, r, route)
		}
		if route.mapID != s.posMap || route.selfRevision != s.positionRevision || distance(route.start, s.pos) > .05 || route.targetRevision != targetRevision || distance(route.target, target) > .01 {
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
		if coldridgeCorpse {
			// Preserve the explicit Coldridge practice region on its own exception.
			if !s.navigationStepWithinLocked(ctx, route.path, &index, coldridgeNavigationGoal.contains) {
				return pause("loot_approach_invalid_navigation_step")
			}
			if distance(s.pos, target) <= 3.01 && routeRemaining(s.pos, route.path, index) <= 3.01 {
				return pause("loot_approach_within_distance")
			}
			s.state.Reason = "loot_approaching_coldridge_client_navmesh_estimate"
			return s.stateLocked(), nil
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
		if fargodeepCorpse {
			previous := s.lootApproachGuard
			s.lootApproachGuard = func(call context.Context, current *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
				return s.fargodeepApproachCurrentLocked(call, r, route, current)
			}
			defer func() { s.lootApproachGuard = previous }()
		}
		if !s.movementLocked(ctx, op, p, true) {
			if fargodeepCorpse && s.state.InteractionOwner == "loot" && subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) == 1 {
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

// An incomplete own collection goal may approach an actually lootable party
// corpse. COMPLETE goals retain only the exact completed encounter exception.
func (s *Service) ownFargodeepCorpseLocked(ctx context.Context, w *worldv1.SnapshotResponse, corpse *worldv1.CreatureState) bool {
	if !s.fargodeepCorpseScopeLocked(w, corpse) {
		return false
	}
	return s.fargodeepCorpsePartySafeLocked(w, s.fargodeepCorpsePartyLocked(ctx, w, corpse))
}

func (s *Service) fargodeepCorpseScopeLocked(w *worldv1.SnapshotResponse, corpse *worldv1.CreatureState) bool {
	mission := &s.fargodeep
	active, incomplete := ownFargodeepQuests(w)
	retained := corpse != nil && !mission.active && mission.phase == "target_death_observed" && mission.target == corpse.Guid && mission.session != ""
	if s.independent || mission.active || !active || !incomplete && !retained || !fargodeepCharacter(w, s.selfGUID) || !fargodeepArea(s.pos) || corpse == nil || s.state.Practicing || s.state.GetRecovery().GetActive() {
		return false
	}
	p, known, _ := creaturePosition(corpse)
	if !known || !fargodeepArea(p) || corpse.Guid>>48 != 0xf130 || corpse.Guid&0xffffff == 0 || uint32(corpse.Guid>>24&0xffffff) != corpse.Entry || (corpse.Entry != 40 && corpse.Entry != 475) || !corpse.Present || !corpse.MapKnown || corpse.MapId != 0 || !corpse.HealthKnown || corpse.Health != 0 || corpse.MaxHealth == 0 || !corpse.AliveKnown || corpse.Alive || !corpse.LevelKnown || corpse.Level < 5 || corpse.Level > 7 || !corpse.UnitFlagsKnown || !corpse.DynamicFlagsKnown || corpse.DynamicFlags&9 != 9 {
		return false
	}
	return true
}

func (s *Service) fargodeepCorpsePartyLocked(ctx context.Context, w *worldv1.SnapshotResponse, corpse *worldv1.CreatureState) map[uint64]bool {
	_, incomplete := ownFargodeepQuests(w)
	mission := &s.fargodeep
	retained := corpse != nil && !mission.active && mission.phase == "target_death_observed" && mission.target == corpse.Guid && mission.session != ""
	if retained && !incomplete {
		p, _, _ := creaturePosition(corpse)
		online, reason := s.fargodeepPartyLocked(ctx, w, mission.session, false, p)
		if reason != "" {
			return nil
		}
		return online
	}
	return s.healingPartyLocked(ctx)
}

func (s *Service) fargodeepCorpsePartySafeLocked(w *worldv1.SnapshotResponse, online map[uint64]bool) bool {
	for _, guid := range []uint64{1, 4, 5} {
		player := observedHealingPlayer(w, guid)
		if !online[guid] || !safeHealingPlayer(player, 0) || !player.HealthKnown || player.MaxHealth == 0 || player.Health > player.MaxHealth || uint64(player.Health)*100 < uint64(player.MaxHealth)*85 || player.UnitFlags&0x80000 != 0 || player.PlayerFlags&2 != 0 || !player.ClassKnown || player.ClassId != map[uint64]uint32{1: 5, 4: 8, 5: 4}[guid] || !player.LevelKnown || player.Level < 4 || player.Level > 10 || guid != s.selfGUID && player.MovementFlags&0x1ff != 0 {
			return false
		}
		pos := playerPosition(player)
		if guid == s.selfGUID {
			pos = s.pos
		}
		if distance(s.pos, pos) > 35 {
			return false
		}
	}
	return true
}

// Membership is read before the last world snapshot, so a party callback cannot
// hide a newly observed combat/health/target change behind an older world read.
func (s *Service) fargodeepApproachCurrentLocked(ctx context.Context, r *actionsv1.ApproachLootRequest, route *lootApproachNavigation, current *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
	if current == nil || !s.fargodeepCorpseScopeLocked(current, approachCorpse(current, r.CorpseGuid)) {
		return nil, false
	}
	_, wasIncomplete := ownFargodeepQuests(current)
	online := s.fargodeepCorpsePartyLocked(ctx, current, approachCorpse(current, r.CorpseGuid))
	w, err := s.snapshot(ctx, r.CorpseGuid)
	if err != nil || w == nil || safeQuestCharacter(w, s.selfGUID) != "" || ctx.Err() != nil || s.closed || s.state.InteractionOwner != "loot" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 || !s.now().Before(s.interactionUntil) || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.autoTarget != 0 {
		return nil, false
	}
	_, incomplete := ownFargodeepQuests(w)
	corpse := approachCorpse(w, r.CorpseGuid)
	if wasIncomplete && !incomplete || !s.fargodeepCorpseScopeLocked(w, corpse) || !s.fargodeepCorpsePartySafeLocked(w, online) || w.Self.MapId != route.mapID || w.Self.PositionRevision != route.selfRevision || s.posMap != route.mapID || s.positionRevision != route.selfRevision || distance(s.pos, route.start) > .05 || corpse.PositionRevision != route.targetRevision {
		return nil, false
	}
	target, known, _ := creaturePosition(corpse)
	if !known || distance(target, route.target) > .01 || distance(s.pos, target) > 12 || math.Abs(float64(target.z-s.pos.z)) > 1.5 {
		return nil, false
	}
	return w, true
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

// Only corpses matching the explicitly bound170/183/182 combat goal enter this
// exception. The world's actual corpse retains the verified living profile;
// quest completion may precede looting.
func (s *Service) ownColdridgeGoalCorpseLocked(w *worldv1.SnapshotResponse, corpse *worldv1.CreatureState) bool {
	goal := &s.coldridgeGoal
	if goal.questID != 170 && goal.questID != 183 && goal.questID != 182 || !s.now().Before(s.practiceUntil) || w == nil || w.Self == nil || corpse == nil {
		return false
	}
	active, _ := goal.observe(w, s.selfGUID)
	if goal.questID == 182 {
		return active && matches182Profile(corpse) && goal.positionEvidenceWithin(corpse) && corpse.Guid>>48 == 0xf130 && corpse.Guid&0xffffff != 0 && uint32(corpse.Guid>>24&0xffffff) == corpse.Entry && w.Self.MapKnown && w.Self.MapId == 0 && goal.baselineContains(playerPosition(w.Self)) && goal.contains(s.pos)
	}
	level, maxHealth, faction := goal.creatureProfile()
	return active && goal.supportsEntry(corpse.Entry) && corpse.Guid>>48 == 0xf130 && corpse.Guid&0xffffff != 0 && uint32(corpse.Guid>>24&0xffffff) == corpse.Entry && corpse.MapKnown && corpse.MapId == 0 && w.Self.MapKnown && w.Self.MapId == 0 && coldridge(s.pos) && corpse.LevelKnown && corpse.Level == level && corpse.MaxHealth == maxHealth && (goal.questID != 183 || corpse.FactionTemplate == faction)
}

// This movement exception is bound to the acting character's actual own quest
// log, never to a party member's quests or an arbitrary game-object target.
func ownHarvestQuest(w *worldv1.SnapshotResponse, self uint64) bool {
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
		if q.QuestId == 3904 && !q.Complete && !q.Failed && q.State&3 == 0 {
			found = true
		}
	}
	return found
}
