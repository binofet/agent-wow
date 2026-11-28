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
	sessionID                               string
	self                                    uint64
	visit, owner, followRevision, hostEpoch uint64
	until                                   time.Time
	target                                  formationTarget
}

func (s *Service) SetFollowFormation(ctx context.Context, r *actionsv1.FollowFormationRequest) (*emptypb.Empty, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetOwnerGuid() == 0 ||
		r.GetOwnerPositionRevision() == 0 || !finite(position{r.X, r.Y, r.Z, r.Orientation}) ||
		math.IsNaN(r.StartDistance) || math.IsInf(r.StartDistance, 0) || r.StartDistance < .2 || r.StartDistance > 12 ||
		math.IsNaN(r.StopDistance) || math.IsInf(r.StopDistance, 0) || r.StopDistance < .1 || r.StopDistance > 4 || r.StartDistance < r.StopDistance {
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
	current := func() bool {
		return s.hasPolicyEncounterAccessLocked(ctx) && !s.closed && !s.independent && r.SessionId == s.sessionID && r.SelfGuid == s.selfGUID &&
			r.VisitId == s.encounterVisit.revision && r.FollowRevision == s.followRevision
	}
	if !current() {
		return nil, status.Error(codes.FailedPrecondition, "current owned body, follow revision and observed visit required")
	}
	w, err := s.readWorldSnapshot(ctx)
	owner := w.GetOwner()
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if err != nil || !current() || !s.matchesCurrentEncounterVisitLocked(w) || owner.GetGuid() != r.OwnerGuid || !owner.GetPresent() ||
		!owner.GetAliveKnown() || !owner.GetAlive() || !owner.GetMapKnown() || owner.MapId != w.Self.MapId || !owner.PositionKnown ||
		owner.PositionRevision != r.OwnerPositionRevision || !finite(playerPosition(owner)) {
		return nil, status.Error(codes.FailedPrecondition, "current living followed owner pose required")
	}
	if r.SurfaceMode != "" {
		return s.setSurfaceFormationLocked(ctx, r, w)
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
	if r.Phase != "" {
		switch r.Phase {
		case "formation_reaction_delay", "within_personal_follow_space", "within_follow_distance",
			"follow_target_too_far_or_vertical", "following_formation":
			phase = r.Phase
		default:
			return nil, status.Error(codes.InvalidArgument, "known formation phase required")
		}
	}
	s.surfaceFollow.goal = nil
	s.followGoal = followFormationGoal{sessionID: r.SessionId, self: r.SelfGuid, visit: r.VisitId, owner: r.OwnerGuid,
		followRevision: r.FollowRevision, hostEpoch: s.hostEncounterEpoch,
		until: s.now().Add(encounterLeaseDuration), target: formationTarget{
			Position: target, StartDistance: r.StartDistance, StopDistance: r.StopDistance, Ready: r.Ready, Hold: r.Hold, Phase: phase, Valid: true}}
	return &emptypb.Empty{}, nil
}

// A finite exact goal is voluntary movement authority, separate from the
// legacy Follow toggle. Every packet rechecks this after dependency callbacks.
func (s *Service) currentFollowFormationGoalLocked(goal followFormationGoal, w *worldv1.SnapshotResponse) bool {
	owner := w.GetOwner()
	return goal == s.followGoal && goal.target.Valid && s.now().Before(goal.until) && !s.independent &&
		goal.sessionID == s.sessionID && goal.self == s.selfGUID &&
		goal.hostEpoch == s.hostEncounterEpoch && goal.followRevision == s.followRevision &&
		goal.visit == s.encounterVisit.revision && s.hostEncounterActiveLocked() && s.matchesCurrentEncounterVisitLocked(w) &&
		goal.owner == owner.GetGuid() && owner.GetPresent() && owner.GetAliveKnown() && owner.GetAlive() &&
		owner.GetMapKnown() && owner.GetMapId() == w.GetSelf().GetMapId() && owner.GetPositionKnown() && finite(playerPosition(owner))
}

func (s *Service) followFormationPlanLocked(now time.Time, w *worldv1.SnapshotResponse, classID uint32, owner position, moving bool) formationTarget {
	goal := s.followGoal
	if s.currentFollowFormationGoalLocked(goal, w) {
		return goal.target
	}
	if !goal.until.IsZero() {
		s.followGoal = followFormationGoal{}
		s.formation.Reset()
	}
	if s.navDataDir == "" {
		return s.formation.NearOwner(now, s.selfGUID, owner, moving, s.pos, s.state.Moving, w.GetOwner().GetPlayerFlags())
	}
	return s.formation.Plan(now, s.selfGUID, classID, owner, moving, s.pos, s.state.Moving, s.followNav.ownerArrival, w.GetOwner().GetPlayerFlags())
}
