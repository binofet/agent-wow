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
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
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

func TestPracticeLifecycleSameSessionCASOwnsScenarioEligibility(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	target := f.world.NearbyCreatures[0]
	target.Entry, target.Level = 69, 2 // Synthetic observed scene; not a realm spawn claim.
	target.Guid = uint64(0xf130)<<48 | uint64(target.Entry)<<24 | 1
	ctx := context.Background()
	s.practiceTickLocked(ctx)
	if countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.PracticePulls != 0 {
		t.Fatal("default Lua admitted a different scenario", s.stateLocked(), f.packets)
	}
	source := strings.Replace(behaviors.StarterSpellSource, "target_entry=299,target_level=1", "target_entry=69,target_level=2", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("actual Lua scenario tuning missing")
	}
	revision := reloadPracticeLifecycleSource(t, s, source)
	sid, self, until := s.sessionID, s.selfGUID, s.practiceUntil
	s.practiceTickLocked(ctx)
	if lastCast(f) != fireball || s.state.PracticePulls != 1 || s.practiceTarget != target.Guid ||
		s.sessionID != sid || s.selfGUID != self || s.practiceUntil != until || s.starterSpells.revision != revision {
		t.Fatal("same-session Lua target rule remained blocked by a Go scenario", s.stateLocked(), s.starterSpells.lastError, f.packets)
	}
	// The tuned target's actual attack is part of the existing fight, not an
	// unexpected creature solely because its entry differs from old wolf299.
	f.world.Self.UnitFlags |= 0x80000
	target.AttackVictimKnown, target.AttackVictimGuid = true, self
	s.practiceTickLocked(ctx)
	if !s.state.Practicing || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("compiled unexpected-attacker rule shadowed Lua or replayed cast", s.stateLocked(), f.packets)
	}
	other := proto.Clone(target).(*worldv1.CreatureState)
	other.Entry, other.Guid = 299, uint64(0xf130)<<48|uint64(299)<<24|2
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, other)
	s.practiceTickLocked(ctx)
	if s.state.Practicing || s.state.PracticePhase != "unexpected_hostile_requires_help" || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("Lua did not own unexpected encounter admission", s.stateLocked(), f.packets)
	}
}

func TestPracticeLuaScenarioCannotOverridePhysicalCreatureAvailability(t *testing.T) {
	for _, mode := range []string{"guid", "map", "dead", "health_unknown", "attack_immune"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := practiceRig(t, 8)
			target := f.world.NearbyCreatures[0]
			start := strings.Index(behaviors.StarterSpellSource, "local function practice_target(c,tuning)")
			end := strings.Index(behaviors.StarterSpellSource[start:], "local function unclaimed(c)") + start
			if start < 0 || end < start {
				t.Fatal("actual scenario predicate missing")
			}
			source := behaviors.StarterSpellSource[:start] + "local function practice_target(c,tuning) return true end\n" + behaviors.StarterSpellSource[end:]
			reloadPracticeLifecycleSource(t, s, source)
			switch mode {
			case "guid":
				target.Guid = 3
			case "map":
				target.MapId = 1
			case "dead":
				target.Alive = false
			case "health_unknown":
				target.HealthKnown = false
			case "attack_immune":
				target.UnitFlags |= 0x100
			}
			s.practiceTickLocked(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.state.PracticePulls != 0 {
				t.Fatal("Lua scenario choice bypassed physical admission", s.stateLocked(), f.packets)
			}
		})
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
	if behavior.EvaluationLimit != 5*time.Millisecond || behavior.MaxJSONBytes != 32*1024 {
		t.Fatal("production budget changed")
	}
	// Keep the existing100ms semantic fixture for the native projection/source/
	// late-fact guards. Its result is not a native race-binary budget proof.
	// The ordinary exact-artifact CLI below independently executes Runtime
	// Reload+Evaluate at unchanged5ms/32KiB defaults on this identical full frame.
	runtime := s.starterSpells.runtime
	if runtime == nil || runtime.Status().Revision == "" {
		t.Fatal("fixture did not load the actual shared starter source")
	}
	revision := runtime.Status().Revision
	worldBefore := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	until, target, phase := s.practiceUntil, s.practiceTarget, s.state.PracticePhase
	var capturedFrame []byte
	var capturedOutput map[string]any
	reads := 0
	s.starterSpells.remote = func(ctx context.Context, frame map[string]any) (bool, string, map[string]any, error) {
		reads++
		serialized, marshalErr := json.Marshal(frame)
		rows, rowsKnown := frame["nearby"].([]any)
		quests, questsKnown := frame["questLog"].([]any)
		if marshalErr != nil || string(serialized) != string(raw) || !rowsKnown || len(rows) != 32 || !questsKnown || len(quests) != 25 {
			t.Fatal("native policy call changed the exact full32/25 frame", len(serialized), marshalErr)
		}
		capturedFrame = append([]byte(nil), serialized...)
		output, evaluateErr := runtime.EvaluateContext(ctx, frame)
		afterFrame, marshalErr := json.Marshal(frame)
		if marshalErr != nil || string(afterFrame) != string(capturedFrame) || runtime.Status().Revision != revision {
			t.Fatal("semantic evaluation mutated caller input or replaced its source", marshalErr)
		}
		encoded, encodeErr := json.Marshal(output)
		if encodeErr != nil || json.Unmarshal(encoded, &capturedOutput) != nil {
			t.Fatal("actual selector output could not be detached", encodeErr)
		}
		return true, revision, output, evaluateErr
	}
	selected, _, valid := s.practiceLifecycleDecisionLocked(context.Background(), f.world, "tick")
	if !valid || selected.kind != "select" || selected.target != last.Guid || len(f.packets) != 0 || reads != 1 ||
		selected.revision != revision || len(capturedOutput) != 6 || string(capturedFrame) != string(raw) ||
		!proto.Equal(worldBefore, f.world) || s.practiceUntil != until || s.practiceTarget != target || s.state.PracticePhase != phase || s.state.PracticePulls != 0 {
		t.Fatal("native semantic guards did not retain the exact32nd motion target and unmodified frame", len(raw), selected, s.starterSpells.lastError, reads)
	}
	practiceDefaultRuntimeCLIParity(t, behaviors.StarterSpellSource)(raw, capturedOutput)
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

type practiceMotionSession struct {
	*fakeSession
	snapshots      int
	beforeSnapshot func(int)
}

func (f *practiceMotionSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		f.snapshots++
		if f.beforeSnapshot != nil {
			f.beforeSnapshot(f.snapshots)
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, options...)
}

func practiceMovingFixture(t *testing.T) (*Service, *practiceMotionSession, *worldv1.CreatureState) {
	t.Helper()
	s, f, _ := practiceRig(t, 8)
	c := f.world.NearbyCreatures[0]
	c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 10000, AgeMs: 100,
		DestinationKnown: true, DestinationX: -8920, DestinationY: c.Y, DestinationZ: c.Z, Flags: 0x2000}
	fixture := &practiceMotionSession{fakeSession: f}
	s.session = fixture
	return s, fixture, c
}

func TestPracticeLifecycleMovingEstimateProgressUsesSameLuaChoiceAndPostStopProof(t *testing.T) {
	s, f, c := practiceMovingFixture(t)
	f.beforeSnapshot = func(int) { c.Trajectory.AgeMs += 100 }
	runtime := s.starterSpells.runtime
	reads := 0
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		reads++
		out, err := runtime.EvaluateContext(ctx, input)
		return true, runtime.Status().Revision, out, err
	}
	ctx := context.Background()
	selected, latest, valid := s.practiceLifecycleDecisionLocked(ctx, f.world, "tick")
	if !valid || selected.kind != "select" || selected.target != c.Guid || reads != 2 || f.snapshots != 2 || c.Trajectory.AgeMs != 300 ||
		latest.NearbyCreatures[0].Trajectory.AgeMs != 300 {
		t.Fatal("age-only progression starved or bypassed latest Lua choice", selected, reads, f.snapshots, s.starterSpells.lastError)
	}
	if !s.stopForPracticeLifecycleLocked(ctx, &selected) || reads != 4 || f.snapshots != 6 || len(f.packets) != 0 {
		t.Fatal("post-STOP validation starved or recursively reread moving estimate", reads, f.snapshots, f.packets)
	}
}

func TestPracticeLifecycleMovingSourceReplacementCannotAuthorizeOldChoice(t *testing.T) {
	for _, seam := range []int{1, 2} {
		for _, field := range []string{"endpoint", "duration", "flags", "position revision"} {
			t.Run(strconv.Itoa(seam)+"/"+field, func(t *testing.T) {
				s, f, c := practiceMovingFixture(t)
				f.beforeSnapshot = func(n int) {
					c.Trajectory.AgeMs += 100
					if n != seam {
						return
					}
					switch field {
					case "endpoint":
						c.Trajectory.DestinationX++
					case "duration":
						c.Trajectory.DurationMs++
					case "flags":
						c.Trajectory.Flags ^= 0x4000
					case "position revision":
						c.PositionRevision++
					}
				}
				selected, _, valid := s.practiceLifecycleDecisionLocked(context.Background(), f.world, "tick")
				if valid || f.snapshots != seam || len(f.packets) != 0 || s.practiceTarget != 0 || s.state.PracticePulls != 0 {
					t.Fatal("replaced raw motion source authorized old Lua choice", selected, f.snapshots, s.stateLocked(), f.packets)
				}
			})
		}
	}
}

func TestPracticeLifecycleMovingCompetitorRequiresFreshLuaChoice(t *testing.T) {
	s, f, first := practiceMovingFixture(t)
	first.X = -8932
	first.Trajectory.AgeMs, first.Trajectory.DurationMs, first.Trajectory.DestinationX = 0, 1000, -8833
	second := proto.Clone(first).(*worldv1.CreatureState)
	second.Guid, second.X, second.Trajectory = first.Guid+1, -8923, nil
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, second)
	f.beforeSnapshot = func(int) { first.Trajectory.AgeMs += 200 }
	runtime := s.starterSpells.runtime
	choices := []string{}
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		if id, ok := out["targetGuid"].(string); ok {
			choices = append(choices, id)
		}
		return true, runtime.Status().Revision, out, err
	}
	ctx := context.Background()
	selected, _, valid := s.practiceLifecycleDecisionLocked(ctx, f.world, "tick")
	if valid || len(choices) != 2 || choices[0] != strconv.FormatUint(first.Guid, 10) || choices[1] != strconv.FormatUint(second.Guid, 10) ||
		f.snapshots != 1 || len(f.packets) != 0 || s.practiceTarget != 0 {
		t.Fatal("native retained/reranked a superseded Lua choice", selected, choices, f.snapshots, f.packets)
	}
	// A later explicit observation may obtain its own current choice; the
	// refused call itself never silently substituted the other target.
	selected, _, valid = s.practiceLifecycleDecisionLocked(ctx, f.world, "tick")
	if !valid || selected.target != second.Guid || len(f.packets) != 0 {
		t.Fatal("fresh Lua choice could not progress", selected, choices, s.starterSpells.lastError)
	}
}

func TestPracticeLifecycleMovingControllerReplacementReceivesNoCleanup(t *testing.T) {
	s, f, advance := practiceRig(t, 4)
	f.world.NearbyCreatures[0].X = -8920
	s.practiceTickLocked(context.Background())
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStartForward || !s.state.Moving {
		t.Fatal("fixture has no real owned approach", f.packets, s.stateLocked())
	}
	advance(200 * time.Millisecond)
	runtime := s.starterSpells.runtime
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		if input["selection"] == practiceLifecycleSelection {
			s.state.InteractionOwner, s.interactionToken = "trade", "replacement-trade-token"
		}
		return true, runtime.Status().Revision, out, err
	}
	target, until, phase := s.practiceTarget, s.practiceUntil, s.state.PracticePhase
	s.practiceTickLocked(context.Background())
	if len(f.packets) != 1 || !s.state.Moving || s.state.InteractionOwner != "trade" || s.interactionToken != "replacement-trade-token" ||
		s.practiceTarget != target || s.practiceUntil != until || s.state.PracticePhase != phase || !s.state.Practicing {
		t.Fatal("old practice callback STOPped or changed replacement controller", s.stateLocked(), f.packets)
	}
}

func TestPracticeLifecycleAcceptedSourceCannotExceedExecutionLeaseEnvelope(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	until, target := s.practiceUntil, s.practiceTarget
	const source = `return {decide=function(input) return {kind="start",targetGuid="0",questGoal=false,phase="waiting_for_safe_pull",leaseMs=600001,finishing=false} end}`
	reloadPracticeLifecycleSource(t, s, source)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || s.practiceUntil != until || s.practiceTarget != target || !s.state.Practicing || len(f.packets) != 0 {
		t.Fatal("accepted Lua source escaped the finite native execution envelope", err, s.stateLocked(), f.packets)
	}
}
