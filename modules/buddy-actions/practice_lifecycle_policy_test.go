package buddyactions

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func reloadPracticeLifecycleSource(t *testing.T, s *Service, source string) string {
	t.Helper()
	expected := ""
	if s.behavior != nil {
		expected = s.behavior.Status().Revision
	}
	out, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: expected, Source: source})
	if err != nil || out == nil || out.Revision == "" {
		t.Fatal("actual native source CAS failed", out, err)
	}
	return out.Revision
}

func TestPracticeLifecycleSameSessionCASOwnsReadinessAndGoalContinuation(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	ctx := context.Background()
	f.world.Self.Health, f.world.Self.Power = 94, 35
	before, _, valid := s.practiceLifecycleDecisionLocked(ctx, f.world, "tick")
	if !valid || before.kind != "wait" || before.phase != "recovering_before_next_pull" {
		t.Fatal(before, s.starterSpells.lastError)
	}
	source := strings.Replace(behaviors.StarterSpellSource,
		"M.practice_pull={minimum_health=95,priest_mana=45,mage_mana=40", "M.practice_pull={minimum_health=90,priest_mana=45,mage_mana=30", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("practice tuning anchor missing")
	}
	revision := reloadPracticeLifecycleSource(t, s, source)
	sid, self, until := s.sessionID, s.selfGUID, s.practiceUntil
	s.practiceTickLocked(ctx)
	if lastCast(f) != fireball || s.state.PracticePulls != 1 || s.sessionID != sid || s.selfGUID != self || s.practiceUntil != until || s.starterSpells.revision != revision {
		t.Fatal("same-session Lua readiness edit was overridden by native thresholds", s.stateLocked(), s.starterSpells.lastError, f.packets)
	}

	// A separate body fixture proves the completion rule, without pretending
	// that the first fixture's still-pending cast has already settled.
	s, f, _ = practiceRig(t, 8)
	f.world.Self.Level = 2
	before, _, valid = s.practiceLifecycleDecisionLocked(ctx, f.world, "tick")
	if !valid || before.kind != "stop" || before.phase != "level_two_observed" {
		t.Fatal(before, s.starterSpells.lastError)
	}
	source = strings.Replace(behaviors.StarterSpellSource, "self.level==1 and self.classId~=5", "self.level<=2 and self.classId~=5", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("XP lifecycle anchor missing")
	}
	reloadPracticeLifecycleSource(t, s, source)
	s.practiceTickLocked(ctx)
	if lastCast(f) != fireball || !s.state.Practicing || s.state.PracticePulls != 1 {
		t.Fatal("compiled XP completion survived the Lua edit", s.stateLocked(), s.starterSpells.lastError, f.packets)
	}
}

func TestPracticeLifecycleSameSessionCASOwnsRankingAndFiniteLease(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	second := proto.Clone(f.world.NearbyCreatures[0]).(*worldv1.CreatureState)
	second.Guid, second.X = second.Guid+1, -8920
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, second)
	ctx := context.Background()
	before, _, valid := s.practiceLifecycleDecisionLocked(ctx, f.world, "tick")
	if !valid || before.kind != "select" || before.target != enemyGUID {
		t.Fatal(before, s.starterSpells.lastError)
	}
	source := strings.Replace(behaviors.StarterSpellSource, "lease_ms=600000,prefer_nearest=true", "lease_ms=2000,prefer_nearest=false", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("rank/lease tuning anchor missing")
	}
	reloadPracticeLifecycleSource(t, s, source)
	selected, _, valid := s.practiceLifecycleDecisionLocked(ctx, f.world, "tick")
	if !valid || selected.kind != "select" || selected.target != second.Guid {
		t.Fatal("native nearest choice survived Lua edit", selected, s.starterSpells.lastError)
	}
	now, sid, self := s.now(), s.sessionID, s.selfGUID
	if _, err := s.Practice(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err, s.starterSpells.lastError)
	}
	if s.practiceUntil.Sub(now) != 2*time.Second || s.sessionID != sid || s.selfGUID != self || len(f.packets) != 0 {
		t.Fatal("finite duration was not supplied by the same accepted Lua", s.practiceUntil.Sub(now), s.stateLocked(), f.packets)
	}
}

func TestPracticeLifecycleActualDefaultBudgetKeepsAll32CandidatesAndMotionPoint(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	base := f.world.NearbyCreatures[0]
	f.world.NearbyCreatures = nil
	for i := 0; i < 32; i++ {
		c := proto.Clone(base).(*worldv1.CreatureState)
		c.Guid, c.X = base.Guid+uint64(i), -8830
		f.world.NearbyCreatures = append(f.world.NearbyCreatures, c)
	}
	last := f.world.NearbyCreatures[31]
	last.PositionKnown, last.X = false, -8700
	last.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 100, AgeMs: 100,
		DestinationKnown: true, DestinationX: -8930, DestinationY: -136, DestinationZ: 81}
	f.world.SelfQuestLogKnown = true
	for i := uint32(0); i < 25; i++ {
		f.world.SelfQuestLog = append(f.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: i, ObjectiveCounts: []uint32{0, 0, 0, 0}})
	}
	input, known := s.practiceLifecycleFactsLocked(f.world, "tick")
	raw, err := json.Marshal(input)
	if !known || err != nil || len(raw) > behavior.MaxJSONBytes || len(input["nearby"].([]any)) != 32 {
		t.Fatal("supported raw frame exceeded unchanged input budget or lost candidates", len(raw), err)
	}
	if input["nearby"].([]any)[31].(map[string]any)["motionPositionKnown"] != true {
		t.Fatal("existing trajectory position mechanic disappeared")
	}
	// Actual production lazy construction/initialization and5ms pure evaluator;
	// this fixture never substitutes a larger budget or retries a failure.
	s.starterSpells.runtime = nil
	if behavior.EvaluationLimit != 5*time.Millisecond {
		t.Fatal("production budget changed")
	}
	selected, _, valid := s.practiceLifecycleDecisionLocked(context.Background(), f.world, "tick")
	if !valid || selected.kind != "select" || selected.target != last.Guid || len(f.packets) != 0 {
		t.Fatal("actual default selector did not retain/select the32nd observed candidate", len(raw), selected, s.starterSpells.lastError)
	}
}

func TestPracticeLifecycleLatePolicyChangesCannotReplaceTaskOrDispatch(t *testing.T) {
	for _, mode := range []string{"session", "body", "visit", "lease", "target", "controller", "raw pose", "health", "claim", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := practiceRig(t, 8)
			runtime := s.starterSpells.runtime
			changed := false
			newUntil := s.practiceUntil.Add(time.Minute)
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				if input["selection"] == practiceLifecycleSelection && input["stage"] == "tick" && !changed {
					changed = true
					switch mode {
					case "session":
						s.sessionID = "replacement-session"
					case "body":
						s.selfGUID, f.world.Self.Guid = 8, 8
					case "visit":
						s.encounterVisit = encounterVisit{revision: 2, mapID: 0, bound: true}
						f.world.VisitRevision = 2
					case "lease":
						s.practiceUntil = newUntil
					case "target":
						s.practiceTarget, s.state.PracticeTargetGuid = enemyGUID+9, enemyGUID+9
					case "controller":
						s.state.InteractionOwner = "trade"
					case "raw pose":
						f.world.Self.PositionRevision++
						f.world.Self.X++
					case "health":
						f.world.Self.Health = 50
					case "claim":
						f.world.NearbyCreatures[0].AttackVictimKnown = true
						f.world.NearbyCreatures[0].AttackVictimGuid = 3
					case "expiry":
						advance(11 * time.Minute)
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			s.practiceTickLocked(context.Background())
			if !changed || len(f.packets) != 0 || s.state.PracticePulls != 0 || !s.state.Practicing {
				t.Fatal("late facts wrote or retired replacement task", mode, s.stateLocked(), f.packets)
			}
			if mode == "lease" && s.practiceUntil != newUntil || mode == "target" && s.practiceTarget != enemyGUID+9 || mode == "controller" && s.state.InteractionOwner != "trade" {
				t.Fatal("old callback mutated replacement custody", mode, s.stateLocked())
			}
		})
	}
}

func TestPracticeLifecycleUnknownCastAndSourceFailureNeverReplay(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	f.failSend, f.failSendOpcode = true, opcode.CMSGCastSpell
	s.practiceTickLocked(context.Background())
	op := s.starterSpells.uncertainStrike
	if op == nil || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("missing ambiguous actual cast", s.stateLocked(), f.packets)
	}
	target := s.practiceTarget
	s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
		return false, "broken-current-source", nil, errors.New("current Lua unavailable")
	}
	for i := 0; i < 2; i++ {
		s.practiceTickLocked(context.Background())
	}
	if s.starterSpells.uncertainStrike != op || s.practiceTarget != target || !s.state.Practicing || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("policy failure discarded or replayed an uncertain cast", s.stateLocked(), f.packets)
	}
}

func TestPracticeLifecycleOwnedFightAgeBoundaryCannotCarryAcrossFreshRead(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	s.practiceTarget, s.state.PracticeTargetGuid, s.practicePullStarted = enemyGUID, enemyGUID, true
	f.world.Self.Level, f.world.Self.UnitFlags = 2, 0x80000
	c := f.world.NearbyCreatures[0]
	c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, s.selfGUID, 30000
	runtime := s.starterSpells.runtime
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		c.AttackAgeMs = 30001
		return true, runtime.Status().Revision, out, err
	}
	selected, _, valid := s.practiceLifecycleDecisionLocked(context.Background(), f.world, "tick")
	if valid || len(f.packets) != 0 {
		t.Fatal("stale owned-fight evidence retained finishing authority", selected, f.packets)
	}
	if reflectAge := practiceLifecycleStable(map[string]any{"attackAgeMs": strconv.FormatUint(c.AttackAgeMs, 10)}).(map[string]any)["attackAgeMs"]; reflectAge != false {
		t.Fatal(reflectAge)
	}
}
