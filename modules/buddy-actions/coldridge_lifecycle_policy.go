package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const coldridgeLifecycleSelection = "coldridge_lifecycle"

type coldridgeLifecycleDecision struct {
	Kind          string   `json:"kind"`
	Phase         string   `json:"phase"`
	QuestID       uint32   `json:"questId"`
	Counts        []uint32 `json:"counts"`
	Active        bool     `json:"active"`
	Needed        bool     `json:"needed"`
	NeededEntries []uint32 `json:"neededEntries"`
	TargetGUID    string   `json:"targetGuid"`
	Finishing     bool     `json:"finishing"`
	LeaseMS       uint32   `json:"leaseMs"`
	Valid         bool     `json:"valid"`
	source        string
	binding       practiceLifecycleBinding
}

func decodeColdridgeLifecycle(output map[string]any) (coldridgeLifecycleDecision, bool) {
	var out coldridgeLifecycleDecision
	// Lua's empty table has no array/object distinction. Normalize only that
	// exact empty value; missing, null and nonempty objects remain malformed.
	normalized := make(map[string]any, len(output))
	for key, value := range output {
		normalized[key] = value
	}
	if empty, ok := normalized["neededEntries"].(map[string]any); ok && len(empty) == 0 {
		normalized["neededEntries"] = []any{}
	}
	if normalized["neededEntries"] == nil {
		return out, false
	}
	raw, err := json.Marshal(normalized)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err != nil || len(output) != 11 || d.Decode(&out) != nil || !out.Valid || len(out.Counts) != 2 || len(out.NeededEntries) > 2 || len(out.Phase) > 128 || strings.IndexFunc(out.Phase, func(r rune) bool { return r < 32 || r > 126 }) >= 0 {
		return out, false
	}
	for _, key := range []string{"kind", "phase", "questId", "counts", "active", "needed", "neededEntries", "targetGuid", "finishing", "leaseMs", "valid"} {
		if _, present := output[key]; !present {
			return out, false
		}
	}
	switch out.Kind {
	case "start", "stop", "wait", "continue", "select", "target_dead", "clear_target":
	default:
		return out, false
	}
	guid, err := strconv.ParseUint(out.TargetGUID, 10, 64)
	if err != nil || strconv.FormatUint(guid, 10) != out.TargetGUID || (out.Kind == "select") != (guid != 0) && out.Kind != "continue" && out.Kind != "target_dead" && out.Kind != "clear_target" && out.Kind != "stop" {
		return out, false
	}
	if out.Kind == "start" && (out.LeaseMS == 0 || out.LeaseMS > 600000) || out.Kind != "start" && out.LeaseMS != 0 {
		return out, false
	}
	goal := coldridgePracticeGoal{questID: out.QuestID}
	for i, entry := range out.NeededEntries {
		if !goal.supportsEntry(entry) || i > 0 && entry == out.NeededEntries[0] {
			return out, false
		}
	}
	if out.Needed != (len(out.NeededEntries) != 0) || out.Needed && !out.Active {
		return out, false
	}
	return out, true
}

func (s *Service) coldridgeLifecycleFacts(w *worldv1.SnapshotResponse, stage string) (map[string]any, bool) {
	if w == nil || w.Self == nil || s.sessionID == "" || w.Self.Guid != s.selfGUID {
		return nil, false
	}
	self := practiceLifecyclePlayerFacts(w.Self)
	if self == nil {
		return nil, false
	}
	quests := make([]any, 0, len(w.SelfQuestLog))
	for _, q := range w.SelfQuestLog {
		if q == nil {
			return nil, false
		}
		row, valid := starterJSON(q)
		if !valid {
			return nil, false
		}
		quests = append(quests, row)
	}
	goal := s.coldridgeGoal
	if stage == "start" {
		goal = coldridgePracticeGoal{}
	}
	project := func(c *worldv1.CreatureState) map[string]any {
		if c == nil {
			return nil
		}
		row := practiceLifecycleCreatureFacts(c)
		if row == nil {
			return nil
		}
		p, known, _ := creaturePosition(c)
		row["eligible"], row["positionWithin"], row["unclaimed"] = goal.eligible(c, w.Self), known && goal.contains(p) && goal.positionEvidenceWithin(c), unclaimedColdridgePull(c)
		return row
	}
	nearby := make([]any, 0, len(w.NearbyCreatures))
	if len(w.NearbyCreatures) > 32 {
		return nil, false
	}
	for _, c := range w.NearbyCreatures {
		if c != nil {
			nearby = append(nearby, project(c))
		}
	}
	entries := make([]any, 0, 2)
	for _, entry := range goal.neededEntries {
		if entry != 0 {
			entries = append(entries, entry)
		}
	}
	return map[string]any{"selection": coldridgeLifecycleSelection, "stage": stage, "sessionId": s.sessionID,
		"selfGuid": strconv.FormatUint(s.selfGUID, 10), "visitId": strconv.FormatUint(w.VisitRevision, 10), "self": self,
		"questLogKnown": w.SelfQuestLogKnown, "questLog": quests,
		"goal":       map[string]any{"questId": goal.questID, "counts": []any{goal.counts[0], goal.counts[1]}, "neededEntries": entries},
		"targetGuid": strconv.FormatUint(s.practiceTarget, 10), "pullStarted": s.practicePullStarted,
		"clientPosition": map[string]any{"x": s.pos.x, "y": s.pos.y, "z": s.pos.z}, "nearby": nearby,
		"selected": project(practiceTargetObservation(w, s.practiceTarget))}, true
}

// The callback chooses policy; only this unchanged native task and identical
// complete observations may consume its result. No failed source falls back.
func (s *Service) coldridgeLifecycleLocked(ctx context.Context, w *worldv1.SnapshotResponse, stage string) (coldridgeLifecycleDecision, *worldv1.SnapshotResponse, bool) {
	binding := s.practiceLifecycleBindingLocked(w)
	invalid := coldridgeLifecycleDecision{binding: binding}
	sid, self, visit, goal, target, pulled, until, practicing := s.sessionID, s.selfGUID, s.encounterVisit, s.coldridgeGoal, s.practiceTarget, s.practicePullStarted, s.practiceUntil, s.state.Practicing
	current := func() bool {
		now := s.now()
		return binding.sameTask(s, ctx) && ctx.Err() == nil && !s.closed && s.sessionID == sid && s.selfGUID == self && s.encounterVisit == visit &&
			s.practiceUntil == until && s.state.Practicing == practicing && s.coldridgeGoal == goal && s.practiceTarget == target && s.practicePullStarted == pulled &&
			(stage == "start" || until.IsZero() || now.Before(until))
	}
	input, known := s.coldridgeLifecycleFacts(w, stage)
	if !known || !current() {
		return invalid, nil, current()
	}
	structural := goal
	if stage == "start" {
		structural = coldridgePracticeGoal{}
	}
	if _, valid := structural.observationCounts(w, self); !valid {
		if stage != "start" {
			s.coldridgeGoal.needed, s.coldridgeGoal.neededEntries = false, [2]uint32{}
		}
		s.starterSpells.lastError = "Coldridge own quest frame is malformed or regressed"
		return invalid, nil, true
	}
	output := s.starterSpells.decide(ctx, input, s.behavior)
	source := s.starterSpells.revision
	if !current() {
		return invalid, nil, false
	}
	out, valid := decodeColdridgeLifecycle(output)
	if !valid {
		if s.starterSpells.lastError == "" {
			s.starterSpells.lastError = "invalid Coldridge lifecycle decision"
		}
		return invalid, nil, true
	}
	allowed := false
	switch stage {
	case "start":
		allowed = out.Kind == "start" || out.Kind == "stop" || out.Kind == "wait"
	case "observe":
		allowed = out.Kind == "continue"
	case "tick":
		allowed = out.Kind == "continue" || out.Kind == "stop" || out.Kind == "wait" || out.Kind == "target_dead" || out.Kind == "clear_target"
	case "targets":
		allowed = out.Kind == "select" || out.Kind == "stop" || out.Kind == "wait"
	}
	if !allowed || stage == "tick" && out.Kind == "continue" && (out.TargetGUID != strconv.FormatUint(target, 10) || !out.Active) {
		return invalid, nil, true
	}
	fresh, err := s.snapshot(ctx, target)
	if !current() {
		return invalid, nil, false
	}
	now, valid := s.coldridgeLifecycleFacts(fresh, stage)
	if err != nil || !valid || !reflect.DeepEqual(practiceLifecycleStable(input), practiceLifecycleStable(now)) || !s.practiceLifecycleSourceCurrentLocked(source) {
		s.starterSpells.lastError = "Coldridge lifecycle observations or source changed after policy read"
		return invalid, nil, true
	}
	selectedGoal := goal
	if stage == "start" {
		selectedGoal = coldridgePracticeGoal{questID: out.QuestID}
	} else if out.QuestID != goal.questID {
		return invalid, nil, true
	}
	counts, valid := selectedGoal.observationCounts(fresh, self)
	if !valid {
		if stage != "start" {
			s.coldridgeGoal.needed, s.coldridgeGoal.neededEntries = false, [2]uint32{}
		}
		return invalid, nil, true
	}
	if !out.Active {
		counts = selectedGoal.counts
	}
	if out.QuestID != 0 && (!valid || out.Counts[0] != counts[0] || out.Counts[1] != counts[1]) {
		return invalid, nil, true
	}
	out.source, out.binding = source, binding
	return out, fresh, true
}

func (g *coldridgePracticeGoal) applyLifecycle(out coldridgeLifecycleDecision) {
	g.questID, g.counts, g.needed = out.QuestID, [2]uint32{out.Counts[0], out.Counts[1]}, out.Needed
	g.neededEntries = [2]uint32{}
	copy(g.neededEntries[:], out.NeededEntries)
}

func (s *Service) observeColdridgeGoalLocked(ctx context.Context, w *worldv1.SnapshotResponse) (bool, bool) {
	out, _, owned := s.coldridgeLifecycleLocked(ctx, w, "observe")
	if !owned || !out.Valid {
		return false, false
	}
	s.coldridgeGoal.applyLifecycle(out)
	return out.Active, out.Needed
}

func (s *Service) pauseColdridgeLifecycleLocked(ctx context.Context, binding practiceLifecycleBinding) {
	s.pausePracticeLifecycleLocked(ctx, binding)
}
