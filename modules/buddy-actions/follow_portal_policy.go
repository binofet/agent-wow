package buddyactions

import (
	"strconv"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

type portalFollowPolicy struct {
	Eligible, Ready, Valid bool
}

// These are elapsed observations, not a policy timeout. Preserve unknownness
// when no sighting/disappearance was observed or the clock precedes it.
func portalElapsed(now, observed time.Time) (bool, uint64) {
	if observed.IsZero() || now.Before(observed) {
		return false, 0
	}
	return true, uint64(now.Sub(observed).Milliseconds())
}

func (s *Service) portalPositionPolicyInputLocked(w *worldv1.SnapshotResponse, now time.Time, mode string, ground position) map[string]any {
	self, owner := w.GetSelf(), w.GetOwner()
	if self == nil || owner == nil {
		return nil
	}
	classID := uint32(0)
	if self.ClassKnown {
		classID = self.ClassId
	}
	return map[string]any{
		"mode": mode, "nowMs": float64(now.UnixMilli()),
		"sessionId": s.sessionID, "selfGuid": strconv.FormatUint(s.selfGUID, 10), "classId": classID,
		"self": map[string]any{"x": self.X, "y": self.Y, "z": self.Z, "moving": self.MovementFlags&0xff != 0},
		"owner": map[string]any{"x": ground.x, "y": ground.y, "z": ground.z,
			"o": ground.o, "moving": false, "playerFlags": owner.PlayerFlags},
	}
}

func (s *Service) portalSightingPolicyLocked(w *worldv1.SnapshotResponse, now time.Time) (bool, bool) {
	if w.GetOwner() == nil {
		return false, false
	}
	input := s.portalPositionPolicyInputLocked(w, now, "portal_sighting", playerPosition(w.GetOwner()))
	if input == nil {
		return false, false
	}
	output := s.formation.decide(input)
	catchingUp, valid := output["catchingUp"].(bool)
	return catchingUp, valid
}

func (s *Service) portalPolicyLocked(w *worldv1.SnapshotResponse, now time.Time) portalFollowPolicy {
	intent := s.legacyPortalIntentLocked(now) || s.currentPortalGoalLocked(s.portalGoal, w)
	return s.portalPolicyWithIntentLocked(w, now, intent)
}

func (s *Service) portalPolicyWithIntentLocked(w *worldv1.SnapshotResponse, now time.Time, intent bool) portalFollowPolicy {
	f := s.followPortal
	input := s.portalPositionPolicyInputLocked(w, now, "portal_follow", f.ground)
	if input == nil || f.portal.trigger == 0 {
		return portalFollowPolicy{}
	}
	seenKnown, seenAge := portalElapsed(now, f.seenAt)
	lostKnown, lostAge := portalElapsed(now, f.lostAt)
	input["portal"] = map[string]any{
		"sourceVisitId": strconv.FormatUint(f.sourceVisit, 10), "currentVisitId": strconv.FormatUint(w.VisitRevision, 10),
		"followRevision": strconv.FormatUint(f.followRevision, 10), "currentFollowRevision": strconv.FormatUint(s.followRevision, 10),
		"ownerGuid": strconv.FormatUint(w.GetOwner().Guid, 10), "sourceMapId": f.portal.source,
		"destinationMapId": f.portal.destination, "currentMapId": w.GetSelf().MapId, "triggerId": f.portal.trigger,
		"following": intent, "ownerAbsent": !w.GetOwner().Present, "catchingUp": f.catchingUp,
		"sightingAgeKnown": seenKnown, "sightingAgeMs": seenAge,
		"disappearanceAgeKnown": lostKnown, "disappearanceAgeMs": lostAge,
	}
	output := s.formation.decide(input)
	eligible, a := output["eligible"].(bool)
	ready, b := output["ready"].(bool)
	if !a || !b || ready && !eligible {
		return portalFollowPolicy{}
	}
	return portalFollowPolicy{Eligible: eligible, Ready: ready, Valid: true}
}
