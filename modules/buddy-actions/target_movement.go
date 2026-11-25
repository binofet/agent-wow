package buddyactions

import (
	"context"
	"encoding/hex"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// targetMovementIntent caches a caller-selected physical destination. It never
// selects enemies, spells, party roles, or whether combat should continue.
// Each fresh Lua decision advances the route once; Tick cannot author a move.
type targetMovementIntent struct {
	view             actionsv1.TargetMovementState
	until            time.Time
	nav              followNavigation
	constraintTarget position
}

func (s *Service) targetMovementSnapshotLocked() *actionsv1.TargetMovementState {
	if s.targetMovement.view.TargetGuid == 0 {
		return nil
	}
	out := proto.Clone(&s.targetMovement.view).(*actionsv1.TargetMovementState)
	if !s.now().Before(s.targetMovement.until) && !out.Ready {
		out.Phase = "awaiting_movement_decision"
	}
	return out
}

func (s *Service) targetMovementActiveLocked() bool {
	return s.targetMovement.view.TargetGuid != 0 && s.targetMovement.view.VisitId == s.encounterVisit.revision &&
		(s.hostEncounterKey == "" || s.hostEncounterActiveLocked()) && s.now().Before(s.targetMovement.until)
}

// ApproachTarget executes one ordinary ground-navigation step for an exact
// observed unit/corpse and requested geometric constraints. Range, LoS and
// behind are caller inputs, not a second Go combat/CC policy. Autoswing is
// deliberately preserved: closing on a runner must not cancel its attack.
func (s *Service) ApproachTarget(ctx context.Context, r *actionsv1.ApproachTargetRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetTargetGuid() == 0 || r.GetVisitId() == 0 ||
		!validTargetBandRequest(r) || math.IsNaN(float64(r.RangeYards)) || math.IsInf(float64(r.RangeYards), 0) || !r.Stop && r.RangeYards <= 0 {
		return nil, status.Error(codes.InvalidArgument, "current session, body, visit, exact target and finite positive range required")
	}
	r = proto.Clone(r).(*actionsv1.ApproachTargetRequest)
	token, supplied := modcb.IncomingPolicySpell(ctx)
	var tokenBytes [32]byte
	if n, err := hex.Decode(tokenBytes[:], []byte(token)); !supplied || err != nil || n != len(tokenBytes) || hex.EncodeToString(tokenBytes[:]) != token {
		return nil, status.Error(codes.PermissionDenied, "authenticated policy movement operation required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	w, err := s.snapshot(ctx, r.TargetGuid)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || r.VisitId != s.encounterVisit.revision || !s.admitsPolicyEncounterLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "current observed visit and its Lua writer required")
	}
	if safeSelfCombat(w, s.selfGUID) != "" || s.clientControlLost || s.clientControlRestoring || !s.refreshPositionLocked(w) || s.state.MotionUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "current controllable own ground position required")
	}
	ctx = modcb.ForwardPolicySpell(ctx)
	s.targetMovementExecuting = true
	defer func() { s.targetMovementExecuting = false }()
	movement := &s.targetMovement
	if movement.view.TargetGuid != r.TargetGuid || movement.view.VisitId != r.VisitId || movement.view.RangeYards != r.RangeYards || movement.view.Behind != r.Behind || movement.view.RequireLos != r.RequireLos || movement.view.MinRangeYards != r.MinRangeYards || movement.view.ClearanceYards != r.ClearanceYards || !sameClearanceTargets(movement.view.ClearanceTargetGuids, r.ClearanceTargetGuids) ||
		movement.nav.selfRevision != s.positionRevision || movement.nav.mapID != s.posMap {
		movement.nav.Reset()
	}
	movement.view = actionsv1.TargetMovementState{TargetGuid: r.TargetGuid, VisitId: r.VisitId, RangeYards: r.RangeYards, Behind: r.Behind, RequireLos: r.RequireLos, MinRangeYards: r.MinRangeYards, ClearanceYards: r.ClearanceYards, ClearanceTargetGuids: append([]uint64(nil), r.ClearanceTargetGuids...)}
	movement.until = s.now().Add(encounterLeaseDuration)
	if r.Stop {
		movement.nav.Reset()
		movement.view.Phase = "stopped"
		movement.view.Ready = s.stopLocked(ctx)
		return s.stateLocked(), nil
	}
	if s.bodyMoving || s.state.CastPending || s.state.Traveling || s.state.InteractionOwner != "" || s.recovery.state.Active {
		movement.view.Phase = "movement_occupied"
		return s.stateLocked(), nil
	}
	target, known := targetMovementPosition(w, r.TargetGuid, s.posMap)
	if !known {
		movement.view.Phase = "target_position_unavailable"
		s.stopLocked(ctx)
		return s.stateLocked(), nil
	}
	hazards, complete := targetClearancePositions(w, r.ClearanceTargetGuids, s.posMap)
	if !complete {
		movement.view.Phase = "clearance_target_unavailable"
		s.stopLocked(ctx)
		return s.stateLocked(), nil
	}
	constrained := r.MinRangeYards > 0 || len(hazards) > 0
	ready, los := distance(s.pos, target) <= float64(r.RangeYards) && distance(s.pos, target) >= float64(r.MinRangeYards) && targetClearanceReady(s.pos, hazards, float64(r.ClearanceYards)), true
	if r.RequireLos {
		los = s.crowdControlLineOfSightLocked(ctx, s.pos, target)
	}
	if r.Behind {
		ready = ready && targetMovementBehind(s.pos, target)
	}
	ready = ready && los
	if ready {
		movement.nav.Reset()
		movement.view.Phase = "in_position"
		if !s.stopLocked(ctx) {
			movement.view.Phase = "movement_stop_unconfirmed"
			return s.stateLocked(), nil
		}
		// Facing is part of executing the requested position; never ATTACKSTOP.
		facing := s.pos
		facing.o = followFacing(facing, target)
		if math.Abs(math.Remainder(float64(facing.o-s.pos.o), 2*math.Pi)) > .05 && !s.movementLocked(ctx, opcode.MSGMoveSetFacing, facing, false) {
			movement.view.Phase = "facing_unconfirmed"
			return s.stateLocked(), nil
		}
		if constrained {
			fresh, err := s.snapshot(ctx, r.TargetGuid)
			current, known := targetMovementPosition(fresh, r.TargetGuid, s.posMap)
			currentHazards, complete := targetClearancePositions(fresh, r.ClearanceTargetGuids, s.posMap)
			valid := err == nil && known && complete && s.matchesCurrentEncounterVisitLocked(fresh) && r.VisitId == s.encounterVisit.revision &&
				safeSelfCombat(fresh, s.selfGUID) == "" && distance(current, target) <= .05 && distance(s.pos, current) >= float64(r.MinRangeYards) && distance(s.pos, current) <= float64(r.RangeYards) && targetClearanceReady(s.pos, currentHazards, float64(r.ClearanceYards))
			if !valid {
				movement.view.Phase = "range_band_observation_changed"
				return s.stateLocked(), nil
			}
		}
		movement.view.Ready = true
		return s.stateLocked(), nil
	}
	goal := targetMovementGoal(s.pos, target, float64(r.RangeYards), r.Behind, los)
	if constrained {
		remaining := append([]position{s.pos}, movement.nav.path[min(movement.nav.index, len(movement.nav.path)):]...)
		valid := len(remaining) > 1 && movement.nav.selfRevision == s.positionRevision && movement.nav.mapID == s.posMap && distance(movement.nav.expected, s.pos) <= .05 && distance(movement.constraintTarget, target) <= .05 &&
			targetBandPathValid(remaining, s.pos, target, float64(r.MinRangeYards), float64(r.RangeYards), hazards, float64(r.ClearanceYards)) &&
			(!r.RequireLos || s.crowdControlLineOfSightLocked(ctx, remaining[len(remaining)-1], target))
		if !valid {
			path := s.targetBandPathLocked(ctx, target, float64(r.MinRangeYards), float64(r.RangeYards), r.RequireLos, hazards, float64(r.ClearanceYards))
			if len(path) < 2 {
				movement.nav.Reset()
				movement.view.Phase = "range_band_route_unavailable"
				s.stopLocked(ctx)
				return s.stateLocked(), nil
			}
			goal = path[len(path)-1]
			movement.nav = followNavigation{path: path, target: goal, expected: s.pos, selfRevision: s.positionRevision, mapID: s.posMap, arrival: goal, arrivalStop: .25, arrivalKnown: true}
			movement.constraintTarget = target
		} else {
			goal = movement.nav.target
		}
	}
	// Solve the caller's geometric constraints using the existing physical
	// route/LoS primitives. This chooses no target, spell or cast timing.
	if !constrained && (r.Behind || r.RequireLos && !los) {
		path := movement.nav.path
		valid := len(path) > 0 && movement.nav.selfRevision == s.positionRevision && movement.nav.mapID == s.posMap &&
			distance(movement.constraintTarget, target) <= 1.5 &&
			(!r.Behind || targetMovementBehind(path[len(path)-1], target))
		if valid {
			goal = movement.nav.target
		} else {
			plan := func(from, to position) []position {
				path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, from, to)
				if err != nil {
					return nil
				}
				return path
			}
			if r.Behind {
				path = sapFlankPath(s.pos, target, plan)
			} else {
				path = s.rangedPositionPathLocked(ctx, float64(r.RangeYards), target, plan)
			}
			if len(path) > 0 {
				goal = path[len(path)-1]
				movement.nav = followNavigation{path: path, target: goal, expected: s.pos, selfRevision: s.positionRevision, mapID: s.posMap,
					arrival: goal, arrivalStop: .25, arrivalKnown: true}
				movement.constraintTarget = target
			} else if r.Behind {
				// Never cut through the body when the requested rear route
				// cannot be proved. A ranged LoS request can still advance
				// along its ordinary verified route and re-observe LoS.
				movement.nav.Reset()
				movement.view.Phase = "positioning_route_unavailable"
				s.stopLocked(ctx)
				return s.stateLocked(), nil
			}
		}
	}
	if constrained {
		fresh, err := s.snapshot(ctx, r.TargetGuid)
		freshTarget, known := targetMovementPosition(fresh, r.TargetGuid, s.posMap)
		freshHazards, complete := targetClearancePositions(fresh, r.ClearanceTargetGuids, s.posMap)
		unchanged := err == nil && known && complete && s.matchesCurrentEncounterVisitLocked(fresh) && fresh.Self.PositionRevision == w.Self.PositionRevision && distance(freshTarget, target) <= .05
		if unchanged {
			for i := range hazards {
				if distance(hazards[i], freshHazards[i]) > .05 {
					unchanged = false
					break
				}
			}
		}
		if !unchanged {
			movement.nav.Reset()
			movement.view.Phase = "range_band_observation_changed"
			s.stopLocked(ctx)
			return s.stateLocked(), nil
		}
	}
	if constrained {
		prior, _ := ctx.Value(movementPrewriteGuardKey{}).(movementPrewriteGuard)
		boundStart := s.pos
		ctx = context.WithValue(ctx, movementPrewriteGuardKey{}, movementPrewriteGuard(func(call context.Context, current *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
			if prior != nil {
				var allowed bool
				current, allowed = prior(call, current)
				if !allowed {
					return current, false
				}
			}
			fresh, err := s.snapshot(call, r.TargetGuid)
			freshTarget, known := targetMovementPosition(fresh, r.TargetGuid, s.posMap)
			freshHazards, complete := targetClearancePositions(fresh, r.ClearanceTargetGuids, s.posMap)
			valid := err == nil && known && complete && s.matchesCurrentEncounterVisitLocked(fresh) && r.VisitId == s.encounterVisit.revision &&
				safeSelfCombat(fresh, s.selfGUID) == "" && s.admitsPolicyEncounterLocked(call, fresh) && distance(s.pos, boundStart) <= .05 && distance(freshTarget, target) <= .05
			if valid {
				for i := range hazards {
					if distance(hazards[i], freshHazards[i]) > .05 {
						valid = false
						break
					}
				}
			}
			if valid {
				route := append([]position{s.pos}, s.followNav.path[min(s.followNav.index, len(s.followNav.path)):]...)
				valid = targetBandPathValid(route, s.pos, freshTarget, float64(r.MinRangeYards), float64(r.RangeYards), freshHazards, float64(r.ClearanceYards))
			}
			if !valid {
				s.state.Reason = "range_band_observation_changed_before_write"
				return fresh, false
			}
			return fresh, true
		}))
	}
	// Use the same complete navmesh route and speed-limited movement transport
	// as travel, with a separate cache so formation cannot corrupt this route.
	previous := s.followNav
	s.followNav = movement.nav
	ok := s.followFormationStepLocked(ctx, goal, .25)
	movement.nav = s.followNav
	s.followNav = previous
	movement.view.Phase = "approaching"
	if !ok {
		movement.view.Phase = s.state.Reason
	}
	return s.stateLocked(), nil
}

func targetMovementBehind(self, target position) bool {
	dx, dy := float64(self.x-target.x), float64(self.y-target.y)
	return dx*math.Cos(float64(target.o))+dy*math.Sin(float64(target.o)) < 0
}

func targetMovementGoal(self, target position, radius float64, behind, los bool) position {
	// Leave room inside the requested range for the next observation. When LoS
	// is blocked, follow the complete route towards the unit until LoS opens.
	standoff := math.Max(0, radius-.75)
	if !los && !behind {
		standoff = 0
	}
	dx, dy := float64(self.x-target.x), float64(self.y-target.y)
	length := math.Hypot(dx, dy)
	if behind {
		dx, dy, length = -math.Cos(float64(target.o)), -math.Sin(float64(target.o)), 1
	}
	goal := target
	if length > 0 {
		goal.x += float32(dx / length * standoff)
		goal.y += float32(dy / length * standoff)
	}
	return goal
}

func targetMovementPosition(w *worldv1.SnapshotResponse, guid uint64, mapID uint32) (position, bool) {
	for _, p := range append([]*worldv1.PlayerState{w.GetSelf(), w.GetOwner()}, w.GetNearbyPlayers()...) {
		if p.GetGuid() == guid && p.GetPresent() && p.GetMapKnown() && p.GetMapId() == mapID && p.GetPositionKnown() {
			pos := playerPosition(p)
			return pos, finite(pos)
		}
	}
	for _, c := range append([]*worldv1.CorpseState{w.GetOwnCorpse()}, w.GetPlayerCorpses()...) {
		if c.GetGuid() == guid && c.GetPresent() && c.GetMapKnown() && c.GetMapId() == mapID && c.GetPositionKnown() {
			pos := position{c.X, c.Y, c.Z, c.Orientation}
			return pos, finite(pos)
		}
	}
	for _, c := range append([]*worldv1.CreatureState{w.GetRequestedCreature(), w.GetOwnerTarget()}, w.GetNearbyCreatures()...) {
		if c.GetGuid() != guid || !c.GetPresent() || !c.GetMapKnown() || c.GetMapId() != mapID || !c.GetPositionKnown() {
			continue
		}
		pos, known, _ := creaturePosition(c)
		return pos, known
	}
	return position{}, false
}
