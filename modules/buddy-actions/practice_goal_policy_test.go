package buddyactions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func practiceGoalFrame(t *testing.T, s *Service) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(s.practiceGoal, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func installExplicitPracticeGoalDeadline(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Practice(ctx, &actionsv1.ToggleRequest{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	const before = "local function result(kind,phase,stage)\n        task.stage=stage or task.stage\n        task.phase=phase"
	const after = "local function result(kind,phase,stage)\n        task.stage=stage or task.stage\n        if input.intent==\"begin\" then task.deadlineMonotonicMs=input.nowMonotonicMs+1000 end\n        task.phase=phase"
	if strings.Count(behaviors.StarterSpellSource, before) != 1 {
		t.Fatal("missing actual owner-editable goal result")
	}
	source := strings.Replace(behaviors.StarterSpellSource, before, after, 1)
	if _, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: source}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Practice(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPracticeGoalSameBodyLuaControlsCompletionWithoutDefaultTimeout(t *testing.T) {
	ctx := context.Background()
	s, f, advance := practiceRig(t, 8)
	id, sid, body := s.practiceGoalID, s.sessionID, s.selfGUID
	advance(11 * time.Minute) // Exceed the removed implicit ten-minute goal abort.
	s.Tick(ctx)
	if !s.state.Practicing || s.practiceGoalID != id || countPackets(f, opcode.CMSGCastSpell) != 1 || !s.now().Before(s.practiceUntil) {
		t.Fatal("logical goal expired with its old writer lease or lost the original frame", s.stateLocked(), practiceGoalFrame(t, s), f.packets)
	}
	finishPracticeCast(s)
	f.world.Self.Level = 2
	defaultChoice, _, valid := s.practiceGoalDecisionLocked(ctx, f.world, "tick", id, s.practiceGoalCurrentLocked(ctx))
	if !valid || defaultChoice.Kind != "finish" {
		t.Fatal("default Lua did not observe its actual level goal", defaultChoice, valid)
	}
	source := strings.Replace(behaviors.StarterSpellSource, "xp_finish_level=2,writer_lease_ms=3000", "xp_finish_level=3,writer_lease_ms=3000", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("missing actual shared goal tuning")
	}
	accepted, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{SessionId: sid, SelfGuid: body, Source: source})
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	s.Tick(ctx)
	if !s.state.Practicing || s.practiceGoalID != id || s.sessionID != sid || s.selfGUID != body || practiceGoalFrame(t, s)["stage"] == "completed" {
		t.Fatal("native level choice overrode accepted same-body Lua", s.stateLocked(), practiceGoalFrame(t, s))
	}
	f.world.Self.Level = 3
	s.Tick(ctx)
	if s.state.Practicing || s.state.PracticePhase != "xp_level_goal_observed" || practiceGoalFrame(t, s)["stage"] != "completed" || s.practiceGoalID != id {
		t.Fatal("observed edited level goal did not complete the same task", s.stateLocked(), practiceGoalFrame(t, s))
	}
}

func TestPracticeGoalUnknownQuestAndKnownCompletionKeepUnknownPhysicalCast(t *testing.T) {
	s, f, _, _, advance := practiceCasterRig(t, 5)
	s.session = starterWeaponAmbiguousSession{f}
	s.Tick(context.Background())
	unknown, id := s.starterSpells.uncertainStrike, s.practiceGoalID
	if unknown == nil || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("missing actual unknown first cast", s.stateLocked(), f.packets)
	}
	f.world.SelfQuestLogKnown = false
	advance(11 * time.Minute)
	s.Tick(context.Background())
	if !s.state.Practicing || s.state.PracticePhase != "waiting_for_observed_quest_goal" || s.practiceGoalID != id || s.starterSpells.uncertainStrike != unknown {
		t.Fatal("unknown quest or elapsed time completed a goal or cleared physical custody", s.stateLocked(), practiceGoalFrame(t, s))
	}
	f.world.SelfQuestLogKnown, f.world.SelfQuestLog[0].Complete = true, true
	s.Tick(context.Background())
	if s.state.Practicing || practiceGoalFrame(t, s)["stage"] != "completed" || s.starterSpells.uncertainStrike != unknown ||
		unknown.view.ServerCastGo || unknown.view.EffectObserved || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("known goal completion erased or replayed its unknown cast", s.stateLocked(), unknown.view, f.packets)
	}
}

func TestPracticeGoalExplicitStopAndNewBeginNeverReplayUnknownCast(t *testing.T) {
	ctx := context.Background()
	s, f, advance := practiceRig(t, 8)
	s.session = starterWeaponAmbiguousSession{f}
	s.Tick(ctx)
	unknown, original := s.starterSpells.uncertainStrike, s.practiceGoalID
	if unknown == nil {
		t.Fatal("missing actual ambiguous cast")
	}
	if _, err := s.Practice(ctx, &actionsv1.ToggleRequest{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if s.state.Practicing || practiceGoalFrame(t, s)["stage"] != "cancelled" || s.starterSpells.uncertainStrike != unknown {
		t.Fatal("explicit cancellation discarded physical uncertainty", s.stateLocked(), unknown.view)
	}
	advance(11 * time.Minute)
	s.Tick(ctx) // Normal expiry may retire the physical pending flag, never its unknown receipt.
	if _, err := s.Practice(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s.Tick(ctx)
	}
	if s.practiceGoalID == original || s.starterSpells.uncertainStrike != unknown || countPackets(f, opcode.CMSGCastSpell) != 1 || unknown.view.ServerCastGo || unknown.view.EffectObserved {
		t.Fatal("new logical intent guessed or replayed an old physical outcome", s.stateLocked(), unknown.view, f.packets)
	}
}

func TestPracticeGoalReplacedVisitRevokesOnlyOldMetadataWithoutCleanupPackets(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	id, before := s.practiceGoalID, len(f.packets)
	f.world.VisitRevision++
	s.Tick(context.Background())
	if s.state.Practicing || s.state.PracticePhase != "practice_goal_body_or_visit_replaced" || len(f.packets) != before ||
		s.practiceGoalID != id || practiceGoalFrame(t, s)["stage"] == "completed" {
		t.Fatal("old task wrote cleanup into the new visit or claimed completion", s.stateLocked(), practiceGoalFrame(t, s), f.packets)
	}
}
