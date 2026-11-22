package buddyactions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const practiceGoalSelection = "practice_goal"

type practiceGoalDecision struct {
	Kind          string          `json:"kind"`
	Phase         string          `json:"phase"`
	WriterLeaseMs int64           `json:"writerLeaseMs"`
	Task          json.RawMessage `json:"task"`
}

func (s *Service) revokeReplacedPracticeGoalLocked(w *worldv1.SnapshotResponse) bool {
	var binding struct {
		SessionID string `json:"sessionId"`
		SelfGUID  string `json:"selfGuid"`
		VisitID   string `json:"visitId"`
	}
	if s.practiceGoalID == "" || json.Unmarshal(s.practiceGoal, &binding) != nil || w == nil || w.Self == nil {
		return false
	}
	if binding.SessionID == s.sessionID && binding.SelfGUID == strconv.FormatUint(s.selfGUID, 10) && binding.VisitID == strconv.FormatUint(w.VisitRevision, 10) {
		return false
	}
	// Revoke the old writer's metadata without sending its cleanup packets to
	// a replacement body/visit or claiming any old operation has settled.
	s.state.Practicing, s.state.PracticePhase = false, "practice_goal_body_or_visit_replaced"
	s.practiceTarget, s.state.PracticeTargetGuid, s.practicePullStarted = 0, 0, false
	s.practiceRoute, s.practiceUntil = nil, time.Time{}
	s.practiceGoalRevision++
	return true
}

// This is the existing named task's opaque Lua continuation, not a second
// controller. Reads can inspect an expired writer lease; only a current Lua
// decision can authorize its next bounded interval or choose completion.
func (s *Service) practiceGoalCurrentLocked(ctx context.Context) func() bool {
	session, self, id, goal := s.sessionID, s.selfGUID, s.practiceGoalID, string(s.practiceGoal)
	active, until, target, pulled, generation := s.state.Practicing, s.practiceUntil, s.practiceTarget, s.practicePullStarted, s.practiceGoalRevision
	return func() bool {
		return ctx.Err() == nil && !s.closed && s.sessionID == session && s.selfGUID == self &&
			s.practiceGoalID == id && string(s.practiceGoal) == goal && s.state.Practicing == active &&
			s.practiceUntil == until && s.practiceTarget == target && s.practicePullStarted == pulled &&
			s.practiceGoalRevision == generation &&
			s.hostEncounterKey == "" && !s.fargodeep.active && !s.questApproaching && !s.recovery.state.Active
	}
}

func (s *Service) practiceGoalFactsLocked(w *worldv1.SnapshotResponse, intent, id string) (map[string]any, bool) {
	input, known := s.practiceTargetFactsLocked(w)
	if !known || s.practiceClockOrigin.IsZero() {
		return nil, false
	}
	now := s.now().Sub(s.practiceClockOrigin).Milliseconds()
	if now < 0 {
		return nil, false
	}
	input["selection"], input["intent"], input["intentId"], input["nowMonotonicMs"] = practiceGoalSelection, intent, id, now
	input["pullStarted"], input["castPending"], input["castOutcomeUnknown"] = s.practicePullStarted, s.state.CastPending, s.starterSpells.uncertainStrike != nil
	if intent != "begin" {
		var task map[string]any
		if json.Unmarshal(s.practiceGoal, &task) != nil || task == nil {
			return nil, false
		}
		input["task"] = task
	}
	obs := input["observation"].(map[string]any)
	player := obs["players"].(map[string]any)[strconv.FormatUint(s.selfGUID, 10)].(map[string]any)
	player["classKnown"], player["classId"], player["levelKnown"], player["level"] = w.Self.ClassKnown, w.Self.ClassId, w.Self.LevelKnown, w.Self.Level
	player["unitFlagsKnown"], player["unitFlags"] = w.Self.UnitFlagsKnown, w.Self.UnitFlags
	quests := make([]any, 0, len(w.SelfQuestLog))
	for _, q := range w.SelfQuestLog {
		if q == nil {
			quests = append(quests, nil)
			continue
		}
		row, valid := starterJSON(q)
		if !valid {
			return nil, false
		}
		quests = append(quests, row)
	}
	obs["questLogKnown"], obs["quests"] = w.SelfQuestLogKnown, quests
	return input, true
}

func (s *Service) practiceGoalDecisionLocked(ctx context.Context, w *worldv1.SnapshotResponse, intent, id string, current func() bool) (practiceGoalDecision, *worldv1.SnapshotResponse, bool) {
	input, known := s.practiceGoalFactsLocked(w, intent, id)
	if !known || !current() {
		return practiceGoalDecision{}, nil, false
	}
	output := s.starterSpells.decide(ctx, input, s.behavior)
	if !current() {
		return practiceGoalDecision{}, nil, false
	}
	raw, err := json.Marshal(output)
	var selected practiceGoalDecision
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || len(output) != 4 || decoder.Decode(&selected) != nil ||
		(selected.Kind != "continue" && selected.Kind != "finish_current" && selected.Kind != "finish" && selected.Kind != "advance" && selected.Kind != "wait") ||
		selected.Phase == "" || len(selected.Phase) > 128 || selected.WriterLeaseMs < 0 ||
		selected.WriterLeaseMs > int64((1<<63-1)/time.Millisecond) || selected.Kind != "finish" && selected.WriterLeaseMs == 0 {
		if s.starterSpells.lastError == "" {
			s.starterSpells.lastError = "invalid current Practice goal decision"
		}
		return practiceGoalDecision{}, nil, false
	}
	if selected.Kind == "finish_current" && s.practiceTarget == 0 || selected.Kind == "advance" &&
		(s.practiceTarget == 0 || s.state.CastPending || s.starterSpells.uncertainStrike != nil) {
		return practiceGoalDecision{}, nil, false
	}
	var binding struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
		SelfGUID  string `json:"selfGuid"`
		VisitID   string `json:"visitId"`
	}
	if json.Unmarshal(selected.Task, &binding) != nil || binding.ID != id || binding.SessionID != s.sessionID ||
		binding.SelfGUID != strconv.FormatUint(s.selfGUID, 10) || binding.VisitID != strconv.FormatUint(w.VisitRevision, 10) {
		return practiceGoalDecision{}, nil, false
	}
	fresh, err := s.snapshot(ctx, s.practiceTarget)
	if err != nil || !current() || safePracticeCharacter(fresh, s.selfGUID) != "" || !s.refreshPositionLocked(fresh) {
		return practiceGoalDecision{}, nil, false
	}
	after, known := s.practiceGoalFactsLocked(fresh, intent, id)
	if !known || after["nowMonotonicMs"].(int64) < input["nowMonotonicMs"].(int64) {
		return practiceGoalDecision{}, nil, false
	}
	delete(input, "nowMonotonicMs")
	delete(after, "nowMonotonicMs")
	if !samePracticeTargetFacts(input, after) || !current() {
		return practiceGoalDecision{}, nil, false
	}
	return selected, fresh, true
}

func (s *Service) commitPracticeGoalLocked(id string, selected practiceGoalDecision) {
	s.practiceGoalID, s.practiceGoal = id, append([]byte(nil), selected.Task...)
	s.practiceGoalRevision++
	// This is finite physical writer custody, not a gameplay deadline. The
	// logical goal and any explicitly supplied deadline remain opaque Lua data.
	s.practiceUntil = s.now().Add(time.Duration(selected.WriterLeaseMs) * time.Millisecond)
}

// Execute only the selected lifecycle edge. The Lua frame cannot clear a
// physical reservation, and failed/unknown STOP never proves a target advance.
func (s *Service) applyPracticeGoalBoundaryLocked(ctx context.Context, selected practiceGoalDecision) bool {
	if selected.Kind == "continue" || selected.Kind == "finish_current" {
		return false
	}
	s.commitPracticeGoalLocked(s.practiceGoalID, selected)
	if selected.Kind == "finish" {
		s.stopPracticeLocked(ctx, selected.Phase)
		return true
	}
	stopped := s.stopLocked(ctx)
	attackStopped := s.stopAutoAttackLocked(ctx)
	if selected.Kind == "advance" {
		if !stopped || !attackStopped {
			s.state.PracticePhase = "waiting_for_owned_practice_stop"
			return true
		}
		s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
		s.practiceRoute, s.practicePullStarted = nil, false
	}
	s.state.PracticePhase = selected.Phase
	return true
}

func newPracticeGoalID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("Practice intent identity unavailable: %w", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}
