package buddyactions

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type portalCatchupGoal struct {
	visit, follow, witness uint64
	until                  time.Time
	start                  position
	startRevision          uint64
	candidates             []position
}

// Require the controller's current pose revision as well as World's baseline.
// Own successful movement writes need not be echoed by the realm.
func (s *Service) portalStartLocked(w *worldv1.SnapshotResponse) *actionsv1.ClientPosition {
	p := w.GetSelf()
	if !s.posKnown || !finite(s.pos) || !p.GetPositionKnown() || !p.GetMapKnown() || p.GetGuid() != s.selfGUID ||
		s.posMap != p.MapId || s.positionRevision != p.PositionRevision || s.posRevision == 0 || s.state.MotionUnconfirmed {
		return nil
	}
	return &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: s.posMap,
		X: s.pos.x, Y: s.pos.y, Z: s.pos.z, Orientation: s.pos.o, Revision: s.posRevision, Estimated: s.posEstimated}
}

func samePortalStart(a, b *actionsv1.ClientPosition) bool {
	return a.GetKnown() && b.GetKnown() && a.GetMapKnown() && b.GetMapKnown() && a.MapId == b.MapId &&
		a.Revision == b.Revision && a.X == b.X && a.Y == b.Y && a.Z == b.Z && a.Orientation == b.Orientation
}

func (s *Service) portalObservationLocked(w *worldv1.SnapshotResponse) *actionsv1.PortalFollowObservation {
	f := s.followPortal
	if f.portal.trigger == 0 || f.sourceVisit == 0 || f.witness == 0 {
		return nil
	}
	return &actionsv1.PortalFollowObservation{WitnessRevision: f.witness, SourceVisitId: f.sourceVisit,
		FollowRevision: f.followRevision, OwnerGuid: ownerGUID, SourceMapId: f.portal.source,
		DestinationMapId: f.portal.destination, TriggerId: f.portal.trigger,
		Ground: &actionsv1.PortalPoint{X: f.ground.x, Y: f.ground.y, Z: f.ground.z},
		Start:  s.portalStartLocked(w), CatchingUp: f.catchingUp, Notified: f.notified,
		OwnerAbsent: w.GetOwner().GetGuid() == ownerGUID && !w.GetOwner().GetPresent()}
}

func (s *Service) SetPortalCatchup(ctx context.Context, r *actionsv1.PortalCatchupRequest) (*emptypb.Empty, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetOwnerGuid() != ownerGUID ||
		r.GetFollowRevision() == 0 || r.GetWitnessRevision() == 0 || r.GetStart().GetRevision() == 0 ||
		len(r.GetCandidates()) == 0 || len(r.GetCandidates()) > 7 {
		return nil, status.Error(codes.InvalidArgument, "exact portal body, visit, witness, starting pose and bounded candidates required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.trustedEncounterHostLocked(ctx) {
		return nil, status.Error(codes.PermissionDenied, "current attached encounter host required")
	}
	if !s.hasPolicyEncounterAccessLocked(ctx) || !s.hostEncounterActiveLocked() ||
		r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || r.VisitId != s.encounterVisit.revision ||
		r.FollowRevision != s.followRevision || r.WitnessRevision != s.followPortal.witness {
		return nil, status.Error(codes.FailedPrecondition, "current portal body, visit, follow intent and witness required")
	}
	w, err := s.readWorldSnapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || !s.portalSourceReadyLocked(w, s.now()) ||
		!s.followPortal.catchingUp || s.followPortal.notified || !samePortalStart(r.Start, s.portalStartLocked(w)) {
		return nil, status.Error(codes.FailedPrecondition, "current witnessed entrance and exact controller pose required")
	}
	candidates := make([]position, 0, len(r.Candidates))
	for _, point := range r.Candidates {
		if point == nil {
			return nil, status.Error(codes.InvalidArgument, "finite portal candidate required")
		}
		candidate := position{x: point.X, y: point.Y, z: point.Z}
		// Only the witnessed ground or a bounded local bend may be proposed.
		// Their preference order belongs entirely to the caller's Lua.
		if !validPortalCandidate(candidate, s.pos, s.followPortal.ground) {
			return nil, status.Error(codes.InvalidArgument, "portal candidate exceeds the physical planning envelope")
		}
		candidates = append(candidates, candidate)
	}
	s.portalGoal = portalCatchupGoal{visit: r.VisitId, follow: r.FollowRevision, witness: r.WitnessRevision,
		until: s.now().Add(encounterLeaseDuration), start: s.pos, startRevision: s.posRevision, candidates: candidates}
	return &emptypb.Empty{}, nil
}

func validPortalCandidate(candidate, start, ground position) bool {
	return finite(candidate) && (distance(candidate, ground) < .001 || distance(candidate, start) <= 24)
}

func (s *Service) portalCandidatesLocked(w *worldv1.SnapshotResponse, now time.Time) []position {
	if s.hostEncounterActiveLocked() {
		goal := s.portalGoal
		if now.Before(goal.until) && goal.visit == w.VisitRevision && goal.follow == s.followRevision &&
			goal.witness == s.followPortal.witness && goal.startRevision == s.posRevision && goal.start == s.pos {
			return goal.candidates
		}
		return nil // An active host must supply its own current proposal.
	}
	// This fallback uses the same source; its publication remains tied to the
	// native artifact until the builtin source is exposed to the edit surface.
	input := map[string]any{"mode": "portal_catch_up", "nowMs": float64(now.UnixMilli()),
		"selfGuid": strconv.FormatUint(s.selfGUID, 10), "classId": 0,
		"self": map[string]any{"x": s.pos.x, "y": s.pos.y, "z": s.pos.z, "moving": s.state.Moving},
		"owner": map[string]any{"x": s.followPortal.ground.x, "y": s.followPortal.ground.y,
			"z": s.followPortal.ground.z, "o": s.followPortal.ground.o, "moving": false, "playerFlags": w.GetOwner().GetPlayerFlags()}}
	output := s.formation.decide(input)
	var result struct{ Candidates []*actionsv1.PortalPoint }
	body, err := json.Marshal(output)
	if err != nil || json.Unmarshal(body, &result) != nil || len(result.Candidates) == 0 || len(result.Candidates) > 7 {
		return nil
	}
	candidates := make([]position, 0, len(result.Candidates))
	for _, point := range result.Candidates {
		if point == nil {
			return nil
		}
		candidate := position{x: point.X, y: point.Y, z: point.Z}
		if !validPortalCandidate(candidate, s.pos, s.followPortal.ground) {
			return nil
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}
