package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type followFormationGoal struct {
	visit, owner uint64
	until        time.Time
	target       formationTarget
}

func (s *Service) SetFollowFormation(ctx context.Context, r *actionsv1.FollowFormationRequest) (*emptypb.Empty, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetOwnerGuid() == 0 ||
		r.GetOwnerPositionRevision() == 0 || !finite(position{r.X, r.Y, r.Z, r.Orientation}) ||
		math.IsNaN(r.StartDistance) || math.IsInf(r.StartDistance, 0) || r.StartDistance < .2 || r.StartDistance > 12 ||
		math.IsNaN(r.StopDistance) || math.IsInf(r.StopDistance, 0) || r.StopDistance < .1 || r.StopDistance > 4 || r.StartDistance <= r.StopDistance {
		return nil, status.Error(codes.InvalidArgument, "exact current body, visit, owner pose and bounded formation radii required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.trustedEncounterHostLocked(ctx) {
		return nil, status.Error(codes.PermissionDenied, "current attached encounter host required")
	}
	if !s.hasPolicyEncounterAccessLocked(ctx) || s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || !s.hostEncounterActiveLocked() ||
		r.VisitId != s.encounterVisit.revision || r.FollowRevision != s.followRevision || !s.state.Following || s.navDataDir == "" {
		return nil, status.Error(codes.FailedPrecondition, "current following body and observed visit required")
	}
	w, err := s.readWorldSnapshot(ctx)
	owner := w.GetOwner()
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || owner.GetGuid() != r.OwnerGuid || !owner.GetPresent() ||
		!owner.GetAliveKnown() || !owner.GetAlive() || !owner.GetMapKnown() || owner.MapId != w.Self.MapId || !owner.PositionKnown ||
		owner.PositionRevision != r.OwnerPositionRevision || !finite(playerPosition(owner)) {
		return nil, status.Error(codes.FailedPrecondition, "current living followed owner pose required")
	}
	target := position{r.X, r.Y, r.Z, r.Orientation}
	// This caps a caller's physical destination envelope. The role and
	// spacing within it come entirely from Lua.
	if distance(target, playerPosition(owner)) > 16 {
		return nil, status.Error(codes.FailedPrecondition, "formation destination exceeds the observed owner envelope")
	}
	phase := "formation_reaction_delay"
	if r.Ready {
		phase = "within_personal_follow_space"
	}
	s.followGoal = followFormationGoal{visit: r.VisitId, owner: r.OwnerGuid,
		until: s.now().Add(encounterLeaseDuration), target: formationTarget{
			Position: target, StartDistance: r.StartDistance, StopDistance: r.StopDistance, Ready: r.Ready, Hold: r.Hold, Phase: phase, Valid: true}}
	return &emptypb.Empty{}, nil
}

func (s *Service) followFormationPlanLocked(now time.Time, w *worldv1.SnapshotResponse, classID uint32, owner position, moving bool) formationTarget {
	goal := s.followGoal
	if now.Before(goal.until) && goal.visit == s.encounterVisit.revision && s.encounterVisit.bound &&
		goal.owner == w.GetOwner().GetGuid() && s.hostEncounterActiveLocked() {
		return goal.target
	}
	if !goal.until.IsZero() {
		s.followGoal = followFormationGoal{}
		s.formation.Reset()
	}
	return s.formation.Plan(now, s.selfGUID, classID, owner, moving, s.pos, s.state.Moving, s.followNav.ownerArrival)
}
