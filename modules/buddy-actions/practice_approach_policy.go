package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strconv"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const practiceApproachSelection = "practice_approach"

type practiceRouteObservation struct {
	points []position
	index  int
	target position
}

type practiceApproachDecision struct {
	kind       string
	standYards float64
	facts      map[string]any
	route      practiceRouteObservation
}

func (s *Service) practiceApproachCurrentLocked(ctx context.Context, target uint64) func() bool {
	session, self, until := s.sessionID, s.selfGUID, s.practiceUntil
	pulled, generation, host := s.practicePullStarted, s.practiceGoalRevision, s.hostEncounterKey
	return func() bool {
		return ctx.Err() == nil && s.sessionID == session && s.selfGUID == self && !s.closed &&
			!s.clientControlLost && !s.clientControlRestoring && s.state.Practicing &&
			s.practiceUntil == until && s.now().Before(until) && s.practiceTarget == target && s.state.PracticeTargetGuid == target &&
			s.practicePullStarted == pulled && s.practiceGoalRevision == generation && s.hostEncounterKey == host && host == "" &&
			!s.fargodeep.active && !s.questApproaching && !s.recovery.state.Active && !s.state.Traveling && s.state.InteractionOwner == ""
	}
}

func (s *Service) practiceApproachAuthorityLocked(ctx context.Context, target uint64, visit uint64, current func() bool) (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
	w, creature, point, valid := s.practiceTaskObservationsLocked(ctx, target, current)
	if !valid || w.VisitRevision != visit || s.state.MotionUnconfirmed || s.targetProtection.blocks(target, s.now()) ||
		!s.practicePullStarted && w.Self.UnitFlags&0x80000 != 0 {
		if current() {
			s.stopPracticeLocked(ctx, "practice_approach_authority_changed")
		}
		return nil, nil, position{}, false
	}
	return w, creature, point, true
}

func (s *Service) observePracticeRouteLocked() practiceRouteObservation {
	return practiceRouteObservation{points: append([]position(nil), s.practiceRoute...), index: s.practiceRouteIndex, target: s.practiceRouteTarget}
}

func (s *Service) samePracticeRouteLocked(route practiceRouteObservation, cursor bool) bool {
	return reflect.DeepEqual(route.points, s.practiceRoute) && route.target == s.practiceRouteTarget && (!cursor || route.index == s.practiceRouteIndex)
}

func (s *Service) practiceApproachFactsLocked(w *worldv1.SnapshotResponse, target *worldv1.CreatureState, point position, route practiceRouteObservation) (map[string]any, bool) {
	self := w.GetSelf()
	if self == nil || target == nil || s.sessionID == "" || self.Guid != s.selfGUID || !s.posKnown || !finite(s.pos) || !finite(point) ||
		!s.samePracticeRouteLocked(route, false) {
		return nil, false
	}
	known := s.practiceRoute != nil
	remaining, direct := float64(0), false
	if known {
		if len(route.points) < 2 || len(route.points) > 2048 || route.index < 0 || route.index > len(route.points) {
			return nil, false
		}
		remaining = routeRemaining(s.pos, route.points, route.index)
		direct = directRemainingRoute(s.pos, point, route.points, route.index)
	}
	if math.IsNaN(remaining) || math.IsInf(remaining, 0) || remaining < 0 {
		return nil, false
	}
	guid := strconv.FormatUint(s.selfGUID, 10)
	player := map[string]any{"guid": guid, "present": self.Present, "classKnown": self.ClassKnown, "classId": self.ClassId,
		"mapKnown": self.MapKnown, "mapId": self.MapId, "positionKnown": self.PositionKnown,
		"x": s.pos.x, "y": s.pos.y, "z": s.pos.z, "orientation": s.pos.o, "moving": s.state.Moving}
	creature := map[string]any{"guid": strconv.FormatUint(target.Guid, 10), "present": target.Present,
		"mapKnown": target.MapKnown, "mapId": target.MapId, "positionKnown": target.PositionKnown,
		"x": point.x, "y": point.y, "z": point.z, "orientation": point.o}
	return map[string]any{"sessionId": s.sessionID, "selfGuid": guid, "selection": practiceApproachSelection,
		"autoAttackKnown": s.state.KnownAutoAttack, "target": creature,
		"observation": map[string]any{"sessionId": s.sessionID, "selfGuid": guid, "mapId": self.MapId,
			"visitId": strconv.FormatUint(w.VisitRevision, 10), "players": map[string]any{guid: player}},
		"baseline": map[string]any{"revision": strconv.FormatUint(self.PositionRevision, 10),
			"x": self.X, "y": self.Y, "z": self.Z},
		"route": map[string]any{"known": known, "remainingYards": remaining, "direct": direct}}, true
}

func (s *Service) practiceApproachDecisionLocked(ctx context.Context, target uint64, visit uint64, current func() bool, proposed *practiceRouteObservation) (practiceApproachDecision, *worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
	w, creature, point, valid := s.practiceApproachAuthorityLocked(ctx, target, visit, current)
	if !valid {
		return practiceApproachDecision{}, nil, nil, position{}, false
	}
	route := s.observePracticeRouteLocked()
	cursor := s.practiceRouteIndex
	if proposed != nil {
		// The executor can advance its tentative cursor while constructing a
		// packet. Geometry still starts at the last written pose/pre-step cursor.
		route = *proposed
	}
	input, valid := s.practiceApproachFactsLocked(w, creature, point, route)
	if !valid {
		return practiceApproachDecision{}, nil, nil, position{}, false
	}
	output := s.starterSpells.decide(ctx, input, s.behavior)
	if !current() {
		return practiceApproachDecision{}, nil, nil, position{}, false
	}
	var selected struct {
		Kind       string  `json:"kind"`
		StandYards float64 `json:"standYards"`
	}
	raw, err := json.Marshal(output)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || output == nil || len(output) != 2 || decoder.Decode(&selected) != nil ||
		(selected.Kind != "approach" && selected.Kind != "arrived" && selected.Kind != "wait") ||
		math.IsNaN(selected.StandYards) || math.IsInf(selected.StandYards, 0) || selected.StandYards < 0 || selected.StandYards > 240 ||
		(selected.Kind == "wait") != (selected.StandYards == 0) {
		return practiceApproachDecision{}, nil, nil, position{}, false
	}
	w, creature, point, valid = s.practiceApproachAuthorityLocked(ctx, target, visit, current)
	now, observed := s.practiceApproachFactsLocked(w, creature, point, route)
	if !valid || !observed || !current() || s.practiceRouteIndex != cursor || !reflect.DeepEqual(input, now) {
		return practiceApproachDecision{}, nil, nil, position{}, false
	}
	return practiceApproachDecision{kind: selected.Kind, standYards: selected.StandYards, facts: input, route: route}, w, creature, point, true
}

func (s *Service) pausePracticeApproachLocked(ctx context.Context, current func() bool) {
	if current() {
		s.stopLocked(ctx)
		if current() {
			s.state.PracticePhase = "waiting_for_current_practice_approach"
			s.state.Reason = "practice_approach_policy_unavailable_or_changed"
		}
	}
}

func (s *Service) practiceApproachArrivedLocked(selected practiceApproachDecision, point position) bool {
	return distance(s.pos, point) <= selected.standYards && (s.practiceRoute == nil ||
		routeRemaining(s.pos, s.practiceRoute, s.practiceRouteIndex) <= selected.standYards &&
			directRemainingRoute(s.pos, point, s.practiceRoute, s.practiceRouteIndex))
}

func (s *Service) practiceApproachLocked(ctx context.Context, original *worldv1.SnapshotResponse, target *worldv1.CreatureState, point position) (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
	guid, visit := target.Guid, original.VisitRevision
	current := s.practiceApproachCurrentLocked(ctx, guid)
	refuse := func() (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
		s.pausePracticeApproachLocked(ctx, current)
		return nil, nil, position{}, false
	}
	selected, w, target, point, valid := s.practiceApproachDecisionLocked(ctx, guid, visit, current, nil)
	if !valid || selected.kind == "wait" {
		return refuse()
	}
	if !s.practicePullStarted || selected.kind == "approach" {
		if s.practiceRoute == nil || distance(s.practiceRouteTarget, point) > 2 {
			if !s.stopLocked(ctx) || !current() {
				return refuse()
			}
			from, to := s.pos, point
			path, err := s.navigationPath(ctx, s.navDataDir, from, to)
			if !current() {
				return nil, nil, position{}, false
			}
			if err != nil {
				s.state.NavAvailable, s.state.NavReason = false, navigationDiagnostic(err, s.navDataDir)
				s.stopPracticeLocked(ctx, "navigation_unavailable")
				return nil, nil, position{}, false
			}
			w, target, point, valid = s.practiceApproachAuthorityLocked(ctx, guid, visit, current)
			if !valid || distance(s.pos, from) > .05 || distance(point, to) > 2 {
				return refuse()
			}
			s.practiceRoute, s.practiceRouteIndex, s.practiceRouteTarget = path, 0, to
			s.practiceReplans++
			s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
			selected, w, target, point, valid = s.practiceApproachDecisionLocked(ctx, guid, visit, current, nil)
			if !valid || selected.kind == "wait" {
				return refuse()
			}
		}
	}
	if selected.kind == "approach" {
		if !s.stopAutoAttackLocked(ctx) || !current() {
			return nil, nil, position{}, false
		}
		if s.practiceRoute == nil || s.practiceRouteIndex >= len(s.practiceRoute) {
			s.stopPracticeLocked(ctx, "navigation_ended_outside_range")
			return nil, nil, position{}, false
		}
		blocked := false
		guard := movementPrewriteGuard(func(call context.Context, _ *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
			latest, world, _, _, allowed := s.practiceApproachDecisionLocked(call, guid, visit, current, &selected.route)
			allowed = allowed && latest.kind == "approach" && latest.standYards == selected.standYards && reflect.DeepEqual(latest.facts, selected.facts)
			blocked = !allowed
			return world, allowed
		})
		written := s.navigationStepLocked(withMovementPrewriteGuard(ctx, guard), s.practiceRoute, &s.practiceRouteIndex)
		if !current() {
			return nil, nil, position{}, false
		}
		if !written {
			if blocked {
				if s.samePracticeRouteLocked(selected.route, false) {
					s.practiceRouteIndex = selected.route.index
				}
				return refuse()
			}
			s.stopPracticeLocked(ctx, "navigation_step_failed")
			return nil, nil, position{}, false
		}
		s.state.PracticePhase = "approaching_observed_wolf_on_navmesh"
		return nil, nil, position{}, false
	}
	// Arrival is a claim about the selected spatial plan, not authority to skip
	// the complete verified path or cast through its remaining bend.
	if !s.practiceApproachArrivedLocked(selected, point) {
		return refuse()
	}
	if !s.stopLocked(ctx) || !current() {
		if current() {
			s.stopPracticeLocked(ctx, "movement_stop_unconfirmed")
		}
		return nil, nil, position{}, false
	}
	latest, w, target, point, valid := s.practiceApproachDecisionLocked(ctx, guid, visit, current, nil)
	if !valid || latest.kind != "arrived" || latest.standYards != selected.standYards || !s.practiceApproachArrivedLocked(latest, point) {
		return refuse()
	}
	s.practiceRoute = nil
	return w, target, point, true
}
