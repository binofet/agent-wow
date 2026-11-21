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
	sessionID              string
	self, hostEpoch        uint64
	revision               uint64
	visit, follow, witness uint64
	until                  time.Time
	start                  position
	startRevision          uint64
	candidates             []position
}

// A plan or callback cannot replace the proposal that authorized an in-flight
// write. The monotonic revision also prevents an identical refresh from being
// mistaken for the original finite authority.
func samePortalGoal(a, b portalCatchupGoal) bool {
	if a.sessionID != b.sessionID || a.self != b.self || a.hostEpoch != b.hostEpoch || a.revision != b.revision ||
		a.visit != b.visit || a.follow != b.follow || a.witness != b.witness || a.until != b.until ||
		a.start != b.start || a.startRevision != b.startRevision || len(a.candidates) != len(b.candidates) {
		return false
	}
	for i := range a.candidates {
		if a.candidates[i] != b.candidates[i] {
			return false
		}
	}
	return true
}

func (s *Service) currentPortalGoalLocked(goal portalCatchupGoal, w *worldv1.SnapshotResponse) bool {
	return goal.revision != 0 && samePortalGoal(goal, s.portalGoal) && !s.closed &&
		goal.sessionID == s.sessionID && goal.self == s.selfGUID && goal.hostEpoch == s.hostEncounterEpoch &&
		goal.visit == s.encounterVisit.revision && goal.visit == w.GetVisitRevision() &&
		goal.follow == s.followRevision && goal.witness == s.followPortal.witness &&
		s.hostEncounterActiveLocked() && s.portalSourceWitnessCurrentLocked(w) && s.now().Before(goal.until)
}

// Retained observations are not movement authority. Capture either the exact
// finite proposal or the still-current legacy intent before dependency calls.
type portalIntent struct {
	sessionID                    string
	self, visit, follow, witness uint64
	hostEpoch                    uint64
	host, finite                 bool
	goal                         portalCatchupGoal
}

func (s *Service) capturePortalIntentLocked(w *worldv1.SnapshotResponse) (portalIntent, bool) {
	intent := portalIntent{sessionID: s.sessionID, self: s.selfGUID, visit: s.followPortal.sourceVisit,
		follow: s.followRevision, witness: s.followPortal.witness, hostEpoch: s.hostEncounterEpoch,
		host: s.hostEncounterActiveLocked()}
	if s.currentPortalGoalLocked(s.portalGoal, w) {
		intent.finite, intent.goal = true, s.portalGoal
		intent.goal.candidates = append([]position(nil), intent.goal.candidates...)
	} else if !s.legacyPortalIntentLocked(s.now()) {
		return portalIntent{}, false
	}
	return intent, s.portalIntentCurrentLocked(intent, w)
}

func (s *Service) portalIntentCurrentLocked(intent portalIntent, w *worldv1.SnapshotResponse) bool {
	if !s.portalSourceWitnessCurrentLocked(w) || intent.sessionID != s.sessionID || intent.self != s.selfGUID ||
		intent.visit != s.followPortal.sourceVisit || intent.follow != s.followRevision || intent.witness != s.followPortal.witness ||
		intent.hostEpoch != s.hostEncounterEpoch || intent.host != s.hostEncounterActiveLocked() {
		return false
	}
	if intent.finite {
		return s.currentPortalGoalLocked(intent.goal, w)
	}
	return s.legacyPortalIntentLocked(s.now())
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
	now := s.now()
	seenKnown, seenAge := portalElapsed(now, f.seenAt)
	lostKnown, lostAge := portalElapsed(now, f.lostAt)
	return &actionsv1.PortalFollowObservation{WitnessRevision: f.witness, SourceVisitId: f.sourceVisit,
		FollowRevision: f.followRevision, OwnerGuid: ownerGUID, SourceMapId: f.portal.source,
		DestinationMapId: f.portal.destination, TriggerId: f.portal.trigger,
		Ground: &actionsv1.PortalPoint{X: f.ground.x, Y: f.ground.y, Z: f.ground.z},
		Start:  s.portalStartLocked(w), CatchingUp: f.catchingUp, Notified: f.notified,
		OwnerAbsent:      w.GetOwner().GetGuid() == ownerGUID && !w.GetOwner().GetPresent(),
		SightingAgeKnown: seenKnown, SightingAgeMs: seenAge, DisappearanceAgeKnown: lostKnown, DisappearanceAgeMs: lostAge}
}

func (s *Service) SetPortalCatchup(ctx context.Context, r *actionsv1.PortalCatchupRequest) (*emptypb.Empty, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetOwnerGuid() != ownerGUID ||
		r.GetWitnessRevision() == 0 || r.GetStart().GetRevision() == 0 ||
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
	hostEpoch := s.hostEncounterEpoch
	current := func() bool {
		return !s.closed && !s.independent && s.hasPolicyEncounterAccessLocked(ctx) && s.hostEncounterEpoch == hostEpoch &&
			r.SessionId == s.sessionID && r.SelfGuid == s.selfGUID && r.VisitId == s.encounterVisit.revision &&
			r.FollowRevision == s.followRevision && r.WitnessRevision == s.followPortal.witness && !s.followPortal.notified
	}
	if !current() {
		return nil, status.Error(codes.FailedPrecondition, "current portal body, visit, follow intent and witness required")
	}
	w, err := s.readWorldSnapshot(ctx)
	if err != nil || !current() || !s.matchesCurrentEncounterVisitLocked(w) || !s.portalSourceWitnessCurrentLocked(w) ||
		!samePortalStart(r.Start, s.portalStartLocked(w)) {
		return nil, status.Error(codes.FailedPrecondition, "current witnessed entrance and exact controller pose required")
	}
	// The proposed goal supplies intent, not proof of a prior raw Follow toggle
	// or an already-admitted goal. Lua still owns eligibility and freshness.
	policy := s.portalPolicyWithIntentLocked(w, s.now(), true)
	fresh, err := s.readWorldSnapshot(ctx)
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err != nil || !policy.Valid || !policy.Eligible || !current() || !s.portalSourceWitnessCurrentLocked(fresh) ||
		fresh.GetSelf().GetPositionRevision() != w.GetSelf().GetPositionRevision() ||
		playerPosition(fresh.GetSelf()) != playerPosition(w.GetSelf()) || !samePortalStart(r.Start, s.portalStartLocked(fresh)) {
		return nil, status.Error(codes.FailedPrecondition, "current eligible witnessed entrance and exact controller pose required")
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
	if s.portalGoalRevision == ^uint64(0) {
		return nil, status.Error(codes.ResourceExhausted, "portal proposal revision exhausted")
	}
	s.portalGoalRevision++
	s.portalGoal = portalCatchupGoal{sessionID: r.SessionId, self: r.SelfGuid, hostEpoch: hostEpoch, revision: s.portalGoalRevision,
		visit: r.VisitId, follow: r.FollowRevision, witness: r.WitnessRevision,
		until: s.now().Add(encounterLeaseDuration), start: s.pos, startRevision: s.posRevision, candidates: candidates}
	return &emptypb.Empty{}, nil
}

func validPortalCandidate(candidate, start, ground position) bool {
	return finite(candidate) && (distance(candidate, ground) < .001 || distance(candidate, start) <= 24)
}

func (s *Service) portalCandidatesLocked(w *worldv1.SnapshotResponse, now time.Time) []position {
	if s.hostEncounterActiveLocked() {
		goal := s.portalGoal
		if s.currentPortalGoalLocked(goal, w) && goal.startRevision == s.posRevision && goal.start == s.pos {
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
