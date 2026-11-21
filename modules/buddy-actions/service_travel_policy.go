package buddyactions

import (
	"context"
	"strconv"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// The raw World witness and the controller's route estimate are distinct.
// Advancing our own movement need not update the server's last observed pose.
type serviceTravelTarget struct {
	position, rawSelfPose                  position
	revision, body, visit, rawSelfRevision uint64
	mapID                                  uint32
}

func observedServiceTravelTarget(w *worldv1.SnapshotResponse, p position, revision uint64) *serviceTravelTarget {
	return &serviceTravelTarget{position: p, revision: revision, body: w.Self.Guid,
		visit: w.VisitRevision, rawSelfPose: playerPosition(w.Self), rawSelfRevision: w.Self.PositionRevision, mapID: w.Self.MapId}
}

// Only arrival is a Lua choice. Exact NPC admission, route geometry, lease and
// controller ownership remain with each existing journey and movement writer.
func (s *Service) serviceTravelArrivalLocked(ctx context.Context, kind string, r *actionsv1.ApproachQuestRequest, target *serviceTravelTarget, path []position, index int) (bool, string) {
	prefix := kind + "_travel_"
	owns, observe := s.ownsVendorTravel, s.vendorTravelContextLocked
	if kind == "trainer" {
		owns, observe = s.ownsTrainerTravel, s.trainerTravelContextLocked
	} else if kind != "vendor" {
		return false, prefix + "policy_unavailable"
	}
	if target == nil {
		return false, prefix + "policy_observation_changed"
	}
	from, mapID, revision := s.pos, s.posMap, s.positionRevision
	sessionID := s.sessionID
	phase, moving := s.state.Reason, s.state.Moving
	remaining := 0.0
	if path != nil {
		remaining = routeRemaining(from, path, index)
	}
	output := s.formation.decide(map[string]any{
		"mode": "service_travel", "nowMs": float64(s.now().UnixMilli()),
		"sessionId": s.sessionID, "selfGuid": strconv.FormatUint(s.selfGUID, 10), "classId": uint32(0),
		"self":  map[string]any{"x": from.x, "y": from.y, "z": from.z, "moving": moving},
		"owner": map[string]any{"x": target.position.x, "y": target.position.y, "z": target.position.z, "o": target.position.o, "moving": false},
		"travel": map[string]any{"kind": kind, "npcGuid": strconv.FormatUint(r.NpcGuid, 10),
			"npcPositionRevision": strconv.FormatUint(target.revision, 10), "mapId": mapID,
			"visitId": strconv.FormatUint(target.visit, 10), "routeKnown": path != nil, "remainingRoute": remaining},
	})
	arrived, valid := output["arrived"].(bool)
	// The pure host read is a dependency boundary. Revalidate the native
	// observation after it, including the raw witness, without another Lua call.
	if !owns(r) {
		return false, prefix + "lease_changed_or_expiring"
	}
	if ctx.Err() != nil {
		return false, prefix + "canceled_or_time_limit"
	}
	if s.state.Reason != phase || s.state.Moving != moving {
		return false, prefix + "interrupted_by_controller_change"
	}
	current, reason := observe(ctx, r)
	if reason != "" {
		return false, reason
	}
	if !owns(r) || s.interactionUntil.Sub(s.now()) < time.Second {
		return false, prefix + "lease_changed_or_expiring"
	}
	if ctx.Err() != nil {
		return false, prefix + "canceled_or_time_limit"
	}
	if s.state.Reason != phase || s.state.Moving != moving {
		return false, prefix + "interrupted_by_controller_change"
	}
	if s.sessionID != sessionID || s.pos != from || s.posMap != mapID || s.positionRevision != revision || current == nil || *current != *target {
		return false, prefix + "policy_observation_changed"
	}
	if !valid {
		if output != nil {
			s.formation.recordFailure("service_travel", "decision", s.formation.sourceRevision, "service travel requires an explicit boolean arrived")
		}
		return false, prefix + "policy_unavailable"
	}
	return arrived, ""
}
