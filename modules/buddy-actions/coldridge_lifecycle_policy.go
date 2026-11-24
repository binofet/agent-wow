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
	Kind             string   `json:"kind"`
	Phase            string   `json:"phase"`
	QuestID          uint32   `json:"questId"`
	Counts           []uint32 `json:"counts"`
	Active           bool     `json:"active"`
	Needed           bool     `json:"needed"`
	NeededEntries    []uint32 `json:"neededEntries"`
	TargetGUID       string   `json:"targetGuid"`
	Finishing        bool     `json:"finishing"`
	LeaseMS          uint32   `json:"leaseMs"`
	Valid            bool     `json:"valid"`
	source           string
	binding          practiceLifecycleBinding
	motionSources    any
	facts            map[string]any
	questDataInvalid bool
	invalidGoal      coldridgePracticeGoal
}

func sameColdridgeLifecycleChoice(a, b coldridgeLifecycleDecision) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right) // Exactly the eleven public decoded fields.
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
	// A typed nil map becomes an empty Lua table. Absence must remain nil so
	// the policy cannot confuse target-free planning with a selected creature.
	var selected any
	if creature := practiceTargetObservation(w, s.practiceTarget); creature != nil {
		selected = project(creature)
	}
	return map[string]any{"selection": coldridgeLifecycleSelection, "stage": stage, "sessionId": s.sessionID,
		"selfGuid": strconv.FormatUint(s.selfGUID, 10), "visitId": strconv.FormatUint(w.VisitRevision, 10), "self": self,
		"questLogKnown": w.SelfQuestLogKnown, "questLog": quests,
		"goal":       map[string]any{"questId": goal.questID, "counts": []any{goal.counts[0], goal.counts[1]}, "neededEntries": entries},
		"targetGuid": strconv.FormatUint(s.practiceTarget, 10), "pullStarted": s.practicePullStarted,
		"clientPosition": map[string]any{"x": s.pos.x, "y": s.pos.y, "z": s.pos.z}, "nearby": nearby,
		"selected": selected}, true
}

// The callback chooses policy; only this unchanged native task and identical
// complete observations may consume its result. No failed source falls back.
func (s *Service) coldridgeLifecycleLocked(ctx context.Context, w *worldv1.SnapshotResponse, stage string) (coldridgeLifecycleDecision, *worldv1.SnapshotResponse, bool) {
	binding := s.practiceLifecycleBindingLocked(w)
	invalid := coldridgeLifecycleDecision{binding: binding}
	sid, self, visit, goal, target, pulled, until, practicing := s.sessionID, s.selfGUID, s.encounterVisit, s.coldridgeGoal, s.practiceTarget, s.practicePullStarted, s.practiceUntil, s.state.Practicing
	owner, token, motion, cast, travel, body, approach, control, restoring, epoch := s.state.InteractionOwner, s.interactionToken, s.state.MotionUnconfirmed, s.state.CastPending, s.state.Traveling, s.bodyMoving, s.questApproaching, s.clientControlLost, s.clientControlRestoring, s.hostEncounterEpoch
	recovery, recoveryToken, phase := s.recovery.state.Active, s.recovery.token, s.state.PracticePhase
	current := func() bool {
		now := s.now()
		return binding.sameTask(s, ctx) && ctx.Err() == nil && !s.closed && s.sessionID == sid && s.selfGUID == self && s.encounterVisit == visit &&
			s.practiceUntil == until && s.state.Practicing == practicing && s.coldridgeGoal == goal && s.practiceTarget == target && s.practicePullStarted == pulled &&
			s.state.InteractionOwner == owner && s.interactionToken == token && s.state.MotionUnconfirmed == motion && s.state.CastPending == cast &&
			s.state.Traveling == travel && s.bodyMoving == body && s.questApproaching == approach && s.clientControlLost == control && s.clientControlRestoring == restoring &&
			s.hostEncounterEpoch == epoch && s.recovery.state.Active == recovery && s.recovery.token == recoveryToken && s.state.PracticePhase == phase &&
			(stage == "start" || until.IsZero() || now.Before(until))
	}
	if stage != "observe" && (owner != "" || motion || travel || body || approach || control || restoring || recovery) || stage == "start" && cast {
		return invalid, nil, false
	}
	structural := goal
	if stage == "start" {
		structural = coldridgePracticeGoal{}
	}
	invalidQuestData := func(observed *worldv1.SnapshotResponse) (coldridgeLifecycleDecision, *worldv1.SnapshotResponse, bool) {
		if stage != "start" {
			s.coldridgeGoal.needed, s.coldridgeGoal.neededEntries = false, [2]uint32{}
			invalid.questDataInvalid, invalid.invalidGoal = true, s.coldridgeGoal
		}
		s.starterSpells.lastError = "Coldridge own quest frame is malformed or regressed"
		return invalid, observed, true
	}
	frameCurrent := func(observed *worldv1.SnapshotResponse) bool {
		return observed.GetVisitRevision() == binding.visit && observed.GetSelf().GetGuid() == self &&
			observed.GetSelf().GetPresent() && observed.GetSelf().GetMapKnown() && observed.GetSelf().GetMapId() == s.posMap &&
			(!binding.encounter.bound || s.matchesCurrentEncounterVisitLocked(observed))
	}
	if !current() || !frameCurrent(w) {
		return invalid, nil, false
	}
	// Validate before projection: a nil quest row is invalid observed data,
	// rather than a missing or broken Lua export. Neither restores pull authority.
	if _, valid := structural.observationCounts(w, self); !valid {
		return invalidQuestData(w)
	}
	input, known := s.coldridgeLifecycleFacts(w, stage)
	motionSources := practiceLifecycleMotionSources(w, target)
	if !known || !current() {
		return invalid, nil, current()
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
	if err == nil && frameCurrent(fresh) {
		if _, valid := structural.observationCounts(fresh, self); !valid {
			return invalidQuestData(fresh)
		}
	}
	now, valid := s.coldridgeLifecycleFacts(fresh, stage)
	if err != nil || !frameCurrent(fresh) || !valid || !reflect.DeepEqual(motionSources, practiceLifecycleMotionSources(fresh, target)) || !reflect.DeepEqual(practiceLifecycleStable(input), practiceLifecycleStable(now)) || !s.practiceLifecycleSourceCurrentLocked(source) {
		s.starterSpells.lastError = "Coldridge lifecycle observations or source changed after policy read"
		return invalid, nil, true
	}
	if !reflect.DeepEqual(practiceLifecycleMotionPositions(input), practiceLifecycleMotionPositions(now)) {
		repeated := s.starterSpells.decide(ctx, now, s.behavior)
		if !current() {
			return invalid, nil, false
		}
		again, valid := decodeColdridgeLifecycle(repeated)
		if !valid || s.starterSpells.revision != source || !s.practiceLifecycleSourceCurrentLocked(source) || !sameColdridgeLifecycleChoice(out, again) {
			s.starterSpells.lastError = "Coldridge moving-source policy choice changed"
			return invalid, nil, true
		}
		latest, e := s.snapshot(ctx, target)
		if !current() {
			return invalid, nil, false
		}
		if e == nil && frameCurrent(latest) {
			if _, valid := structural.observationCounts(latest, self); !valid {
				return invalidQuestData(latest)
			}
		}
		last, known := s.coldridgeLifecycleFacts(latest, stage)
		if e != nil || !frameCurrent(latest) || !known || !reflect.DeepEqual(motionSources, practiceLifecycleMotionSources(latest, target)) ||
			!reflect.DeepEqual(practiceLifecycleStable(now), practiceLifecycleStable(last)) || !s.practiceLifecycleSourceCurrentLocked(source) {
			s.starterSpells.lastError = "Coldridge moving-source final observations changed"
			return invalid, nil, true
		}
		fresh, now = latest, last
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
	out.source, out.binding, out.motionSources, out.facts = source, binding, motionSources, now
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

func (s *Service) rejectColdridgeLifecycleLocked(ctx context.Context, out coldridgeLifecycleDecision) {
	if !out.questDataInvalid {
		s.pauseColdridgeLifecycleLocked(ctx, out.binding)
		return
	}
	// Authoritative quest knowledge was lost or contradicted its high-water
	// marks. Retire only this old task; uncertain movement, attack and spell
	// receipts remain owned by their native operations.
	b := out.binding
	phase, epoch := s.state.PracticePhase, s.hostEncounterEpoch
	current := func() bool {
		return b.sameTask(s, ctx) && s.coldridgeGoal == out.invalidGoal && s.state.PracticePhase == phase && s.hostEncounterEpoch == epoch &&
			s.state.InteractionOwner == "" && !s.bodyMoving && !s.state.Traveling && !s.fargodeep.active && !s.questApproaching &&
			!s.clientControlLost && !s.clientControlRestoring && !s.recovery.state.Active
	}
	observed := func() bool {
		w, err := s.snapshot(ctx, b.target)
		return err == nil && current() && w.GetVisitRevision() == b.visit && w.GetSelf().GetGuid() == b.self &&
			w.GetSelf().GetPresent() && w.GetSelf().GetMapKnown() && w.GetSelf().GetMapId() == s.posMap &&
			(!b.encounter.bound || s.matchesCurrentEncounterVisitLocked(w))
	}
	if !current() || !observed() {
		return
	}
	s.stopLocked(ctx)
	if !current() || !observed() {
		return
	}
	s.stopAutoAttackLocked(ctx)
	if !current() {
		return
	}
	now := s.now()
	if !current() || !observed() {
		return
	}
	s.state.Practicing = false
	s.state.PracticePhase, s.state.Reason = "coldridge_own_quest_data_invalid", "practice_coldridge_own_quest_data_invalid"
	s.practiceRoute, s.practiceTarget, s.state.PracticeTargetGuid, s.practicePullStarted = nil, 0, 0, false
	s.lastStep = now
}
