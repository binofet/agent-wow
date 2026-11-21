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
	view  actionsv1.TargetMovementState
	until time.Time
	nav   followNavigation
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
		math.IsNaN(float64(r.RangeYards)) || math.IsInf(float64(r.RangeYards), 0) || !r.Stop && r.RangeYards <= 0 {
		return nil, status.Error(codes.InvalidArgument, "current session, body, visit, exact target and finite positive range required")
	}
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
	if movement.view.TargetGuid != r.TargetGuid || movement.view.VisitId != r.VisitId || movement.view.RangeYards != r.RangeYards || movement.view.Behind != r.Behind || movement.view.RequireLos != r.RequireLos ||
		movement.nav.selfRevision != s.positionRevision || movement.nav.mapID != s.posMap {
		movement.nav.Reset()
	}
	movement.view = actionsv1.TargetMovementState{TargetGuid: r.TargetGuid, VisitId: r.VisitId, RangeYards: r.RangeYards, Behind: r.Behind, RequireLos: r.RequireLos}
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
	ready, los := distance(s.pos, target) <= float64(r.RangeYards), true
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
		movement.view.Ready = true
		return s.stateLocked(), nil
	}
	goal := targetMovementGoal(s.pos, target, float64(r.RangeYards), r.Behind, los)
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
