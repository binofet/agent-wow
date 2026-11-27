package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/harvestguard"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

// One short, ground-only detour under the normal movement writer. A new Lua
// decision is required for each step. Opening and inventory writes use Loot.
type questObjectRoute struct {
	guid, visit, selfRevision                        uint64
	objectRevision, questRevision, inventoryRevision uint64
	followRevision, ownerGUID, moveSequence          uint64
	arrivalPositionRevision                          uint64
	questID, itemID                                  uint32
	owner, target                                    position
	moving                                           bool
	strip                                            *travelStrip
}

func observedQuestObject(w *worldv1.SnapshotResponse, guid uint64) *worldv1.GameObjectState {
	if w.GetSelf() == nil || guid>>48 != 0xf110 || guid&0xffffff == 0 || len(w.QuestObjects) > 32 {
		return nil
	}
	var found *worldv1.GameObjectState
	for _, g := range w.QuestObjects {
		if g.GetGuid() != guid {
			continue
		}
		if found != nil || !g.Present || !g.TypeKnown || g.GameObjectType != 3 || g.Entry != uint32(guid>>24&0xffffff) ||
			!g.MapKnown || g.MapId != w.Self.MapId || !g.PositionKnown || g.PositionRevision == 0 ||
			!g.FlagsKnown || g.Flags&(1|8|16) != 0 || !finite(position{g.X, g.Y, g.Z, g.Orientation}) {
			return nil
		}
		found = g
	}
	return found
}

func stationaryQuestOwner(w *worldv1.SnapshotResponse, guid uint64) bool {
	p := w.GetOwner()
	return p.GetGuid() == guid && p.GetPresent() && p.MapKnown && p.MapId == w.GetSelf().GetMapId() &&
		p.AliveKnown && p.Alive && p.UnitFlagsKnown && p.UnitFlags&0x80000 == 0 && p.PositionKnown && !p.PositionInferred &&
		p.MovementFlagsKnown && movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) && p.MovementFlags&0xff == 0 && finite(playerPosition(p))
}

func (b *questObjectRoute) matches(r *actionsv1.ApproachTargetRequest) bool {
	return b != nil && b.guid == r.TargetGuid && b.visit == r.VisitId && b.questID == r.QuestId && b.itemID == r.QuestItemId &&
		b.objectRevision == r.QuestObjectRevision && b.questRevision == r.QuestLogRevision && b.inventoryRevision == r.QuestInventoryRevision &&
		b.followRevision == r.QuestFollowRevision && b.ownerGUID == r.QuestOwnerGuid
}

func (b *questObjectRoute) ownsMotion(s *Service) bool {
	return b != nil && b.moving && s.state.Moving && !s.state.MotionUnconfirmed && b.moveSequence == s.movementSequence &&
		b.followRevision == s.followRevision && b.visit == s.encounterVisit.revision && !s.bodyMoving && s.state.InteractionOwner == "" && !s.state.Traveling
}

func (s *Service) questObjectRouteCurrent(w *worldv1.SnapshotResponse, b *questObjectRoute) bool {
	g := observedQuestObject(w, b.guid)
	if g == nil || w.VisitRevision != b.visit || !s.matchesCurrentEncounterVisitLocked(w) || safeQuestCharacter(w, s.selfGUID) != "" ||
		!ownQuestContainerObjective(w, s.selfGUID, g.Entry, b.questID, b.itemID) || !w.InventoryKnown || w.InventoryRevision != b.inventoryRevision ||
		w.SelfQuestLogRevision != b.questRevision || w.Self.PositionRevision != b.selfRevision || g.PositionRevision != b.objectRevision ||
		distance(b.target, position{g.X, g.Y, g.Z, g.Orientation}) > .01 || s.followRevision != b.followRevision ||
		s.state.AutoAttacking || s.state.AutoAttackRequested || s.state.AutoAttackUnconfirmed || s.state.MotionUnconfirmed ||
		s.bodyMoving || s.state.CastPending || s.state.Traveling || s.state.InteractionOwner != "" || s.recovery.state.Active {
		return false
	}
	if b.ownerGUID != 0 {
		return !s.independent && stationaryQuestOwner(w, b.ownerGUID) && distance(playerPosition(w.Owner), b.owner) <= 1.5
	}
	return !s.state.Following && (s.independent || w.GetOwner().GetGuid() == 0)
}

func (s *Service) stepToQuestObjectLocked(ctx context.Context, r *actionsv1.ApproachTargetRequest, w *worldv1.SnapshotResponse) *actionsv1.State {
	m := &s.targetMovement
	refuse := func(reason string) *actionsv1.State {
		m.nav.Reset()
		m.view.Phase = reason
		if m.quest.ownsMotion(s) && !s.stopLocked(ctx) {
			m.view.Phase = "movement_stop_unconfirmed"
		}
		m.until = s.now()
		return s.stateLocked()
	}
	g := observedQuestObject(w, r.TargetGuid)
	if g == nil || r.RangeYards != 3 || !r.RequireLos || r.Behind || r.MinRangeYards != 0 || len(r.ClearanceTargetGuids) != 0 || len(s.currentMovementPointsLocked()) != 0 ||
		r.QuestId == 0 || r.QuestItemId == 0 || r.QuestObjectRevision == 0 || r.QuestLogRevision == 0 || r.QuestInventoryRevision == 0 {
		return refuse("quest_object_unavailable")
	}
	b := m.quest
	if b == nil || b.guid != g.Guid || b.visit != w.VisitRevision {
		if s.state.Moving {
			return refuse("quest_object_existing_movement")
		}
		b = &questObjectRoute{guid: g.Guid, visit: w.VisitRevision, selfRevision: w.Self.PositionRevision, objectRevision: r.QuestObjectRevision,
			questRevision: r.QuestLogRevision, inventoryRevision: r.QuestInventoryRevision, followRevision: r.QuestFollowRevision,
			questID: r.QuestId, itemID: r.QuestItemId, ownerGUID: r.QuestOwnerGuid,
			target: position{g.X, g.Y, g.Z, g.Orientation}}
		if b.ownerGUID != 0 {
			if !stationaryQuestOwner(w, b.ownerGUID) {
				return refuse("quest_object_group_moved")
			}
			b.owner = playerPosition(w.Owner)
			if distance(b.owner, s.pos) > 18 {
				return refuse("quest_object_group_moved")
			}
		}
		m.quest = b
		m.nav.Reset()
	}
	if !b.matches(r) || b.moving && !b.ownsMotion(s) {
		return refuse("quest_object_movement_superseded")
	}
	// A new authoritative body image is a new start for the retained strip.
	// It must remain unchanged from this decision through its physical write.
	b.selfRevision = w.GetSelf().GetPositionRevision()
	if !s.questObjectRouteCurrent(w, b) {
		return refuse("quest_object_observation_changed")
	}
	protected, reason := s.harvestPartyLocked(ctx)
	if reason != "" || harvestguard.WorldReason(w, s.selfGUID, protected) != "" {
		return refuse("quest_object_party_unavailable_or_busy")
	}
	speed := observedRunSpeed(w.Self)
	if speed <= 0 {
		return refuse("quest_object_ground_speed_unknown")
	}
	if len(m.nav.path) == 0 || m.nav.index >= len(m.nav.path) || m.nav.selfRevision != s.positionRevision || distance(m.nav.expected, s.pos) > .05 ||
		distance(s.pos, m.nav.target) <= .25 && distance(m.nav.target, b.target) > 3 {
		plan, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		scope := s.travelMapLocked(s.posMap)
		goal := b.target
		if b.strip != nil {
			goal = b.strip.goal
		}
		strip, path, err := prepareTravelStrip(plan, s.openTravel, scope, s.pos, goal, b.strip)
		if err != nil && b.strip == nil && plan.Err() == nil && distance(s.pos, b.target) <= buddynav.MaxRouteLength+3 {
			// A chest may occupy a hole in the player mesh. Only select a
			// nearby standing place already verified by the interaction planner.
			standoff, e := s.interactionNavigationPath(plan, s.navDataDir, s.posMap, s.pos, b.target)
			if e == nil && len(standoff) > 0 {
				goal = standoff[len(standoff)-1]
				strip, path, err = prepareTravelStrip(plan, s.openTravel, scope, s.pos, goal, nil)
			}
		}
		if err != nil || len(path) < 2 {
			return refuse("quest_object_ground_route_unavailable")
		}
		end := navPosition(strip.surface.Destination())
		if !finite(end) || distance(end, b.target) > 3 || math.Abs(float64(end.z-b.target.z)) > 1.5 || !s.questObjectLineOfSight(plan, end, b.target) {
			return refuse("quest_object_endpoint_unreachable")
		}
		b.strip = strip
		next := path[len(path)-1]
		m.nav = followNavigation{path: path, target: next, expected: s.pos, selfRevision: s.positionRevision, mapID: s.posMap, arrival: next, arrivalStop: .25, arrivalKnown: true}
	}
	remaining := routeRemaining(s.pos, m.nav.path, m.nav.index)
	if remaining <= 3 && distance(s.pos, b.target) <= 3 && math.Abs(float64(s.pos.z-b.target.z)) <= 1.5 && s.questObjectLineOfSight(ctx, s.pos, b.target) {
		if !s.stopLocked(ctx) {
			m.view.Phase = "movement_stop_unconfirmed"
			return s.stateLocked()
		}
		fresh, err := s.snapshot(ctx)
		if err != nil || !s.questObjectRouteCurrent(fresh, b) {
			return refuse("quest_object_observation_changed")
		}
		m.view.Phase, m.view.Ready = "in_position", true
		b.moving = false
		b.moveSequence, b.arrivalPositionRevision = s.movementSequence, s.posRevision
		m.view.QuestArrivalWorldRevision = b.selfRevision
		return s.stateLocked()
	}
	if remaining <= .25 {
		return refuse("quest_object_endpoint_unreachable")
	}
	ctx = withMovementPrewriteGuard(ctx, func(call context.Context, fresh *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
		allowed := call.Err() == nil && s.questObjectRouteCurrent(fresh, b) && observedRunSpeed(fresh.GetSelf()) == speed && len(s.currentMovementPointsLocked()) == 0 && harvestguard.WorldReason(fresh, s.selfGUID, protected) == ""
		if !allowed {
			s.state.Reason = "quest_object_observation_changed_before_write"
		}
		return fresh, allowed
	})
	previous := s.followNav
	s.followNav = m.nav
	ok := s.followFormationStepLocked(ctx, m.nav.target, .25)
	m.nav = s.followNav
	s.followNav = previous
	if !ok {
		return refuse(s.state.Reason)
	}
	b.moving = s.state.Moving
	b.moveSequence = s.movementSequence
	m.view.Phase = "approaching"
	return s.stateLocked()
}

func (s *Service) questObjectLineOfSight(ctx context.Context, from, to position) bool {
	if s.crowdControlSight != nil {
		clear, err := s.crowdControlSight(ctx, s.navDataDir, s.posMap, from, to)
		return err == nil && clear
	}
	from.z += 1.5
	to.z += 1.5
	clear, err := buddynav.LineOfSightContext(ctx, s.navDataDir, s.posMap, swimPoint(from), swimPoint(to))
	return err == nil && clear
}
