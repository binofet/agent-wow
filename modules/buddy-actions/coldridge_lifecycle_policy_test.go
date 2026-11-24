package buddyactions

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

var coldridgeGoalFixture struct {
	once    sync.Once
	mu      sync.Mutex
	runtime *behavior.Runtime
}

// The older structural-goal assertions use the same maintained Lua selector.
// These test-only wrappers are not a second production selector or fallback.
func coldridgeGoalFixtureDecision(w *worldv1.SnapshotResponse, self uint64, goal coldridgePracticeGoal, stage string) (coldridgePracticeGoal, bool, bool) {
	coldridgeGoalFixture.once.Do(func() {
		var err error
		coldridgeGoalFixture.runtime, err = behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
		if err != nil {
			panic(err)
		}
		if _, err = coldridgeGoalFixture.runtime.Reload(behaviors.StarterSpellSource, ""); err != nil {
			panic(err)
		}
	})
	refused := goal
	refused.needed, refused.neededEntries = false, [2]uint32{}
	if _, valid := goal.observationCounts(w, self); !valid {
		return refused, false, false
	}
	s := &Service{sessionID: "coldridge-goal-fixture", selfGUID: self, coldridgeGoal: goal}
	if w != nil && w.Self != nil {
		s.pos = playerPosition(w.Self)
	}
	input, valid := s.coldridgeLifecycleFacts(w, stage)
	if !valid {
		return refused, false, false
	}
	coldridgeGoalFixture.mu.Lock()
	output, err := coldridgeGoalFixture.runtime.EvaluateContext(context.Background(), input)
	coldridgeGoalFixture.mu.Unlock()
	out, valid := decodeColdridgeLifecycle(output)
	if err != nil || !valid {
		return refused, false, false
	}
	if stage == "start" && out.Kind != "start" {
		return coldridgePracticeGoal{}, false, false
	}
	selected := goal
	selected.questID = out.QuestID
	counts, valid := selected.observationCounts(w, self)
	if !valid || out.Active && [2]uint32{out.Counts[0], out.Counts[1]} != counts {
		return refused, false, false
	}
	selected.applyLifecycle(out)
	return selected, out.Active, out.Needed
}

func selectColdridgePracticeGoal(w *worldv1.SnapshotResponse, self uint64) (coldridgePracticeGoal, bool) {
	goal, _, needed := coldridgeGoalFixtureDecision(w, self, coldridgePracticeGoal{}, "start")
	return goal, needed
}

func (g *coldridgePracticeGoal) observe(w *worldv1.SnapshotResponse, self uint64) (bool, bool) {
	if g == nil {
		return false, false
	}
	goal, active, needed := coldridgeGoalFixtureDecision(w, self, *g, "observe")
	*g = goal
	return active, needed
}

func TestColdridgeLifecycleAcceptedLuaEditsGoalPriorityAndObjectiveNeed(t *testing.T) {
	s, f, _ := coldThreatRig(t, 707, 3, 0)
	ctx := context.Background()
	before := s.starterSpells.runtime.Status().Revision
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("fixture lacks its actual baseline")
	}
	initial, _, owned := s.coldridgeLifecycleLocked(ctx, f.world, "tick")
	if !owned || !initial.Valid || !initial.Needed || len(initial.NeededEntries) != 2 {
		t.Fatal(initial, owned)
	}
	changed := strings.Replace(behaviors.StarterSpellSource, "{entry=707,count=6}", "{entry=707,count=3}", 1)
	if changed == behaviors.StarterSpellSource {
		t.Fatal("actual objective tuning anchor missing")
	}
	revision, err := s.starterSpells.runtime.Reload(changed, before)
	if err != nil || revision == before {
		t.Fatal(revision, err)
	}
	selected, _, owned := s.coldridgeLifecycleLocked(ctx, f.world, "tick")
	if !owned || !selected.Valid || len(selected.NeededEntries) != 1 || selected.NeededEntries[0] != 724 || selected.Counts[0] != 3 || s.sessionID != f.party.SessionId {
		t.Fatal("same source edit failed to change need without rewriting progress", selected, owned)
	}
	s.coldridgeGoal.applyLifecycle(selected)
	if s.coldridgeGoal.targetNeeded(707) || !s.coldridgeGoal.targetNeeded(724) {
		t.Fatal(s.coldridgeGoal)
	}
	f.world.SelfQuestLog[1].QuestId = 179
	priority := strings.Replace(changed, "priority={179,170,183,182}", "priority={170,179,183,182}", 1)
	if priority == changed {
		t.Fatal("actual goal priority anchor missing")
	}
	if _, err := s.starterSpells.runtime.Reload(priority, revision); err != nil {
		t.Fatal(err)
	}
	chosen, _, owned := s.coldridgeLifecycleLocked(ctx, f.world, "start")
	if !owned || !chosen.Valid || chosen.Kind != "start" || chosen.QuestID != 170 {
		t.Fatal(chosen, owned)
	}
}

func TestColdridgeLifecycleFailureStopsOnlyItsCurrentApproachAndRetainsIntent(t *testing.T) {
	for _, mode := range []string{"missing", "malformed", "error", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldridgeRig(t)
			f.world.NearbyCreatures[0].X = -6200
			s.Tick(context.Background())
			s.Tick(context.Background())
			if !s.state.Moving {
				t.Fatal("fixture never started bounded approach")
			}
			target, until, pulls := s.practiceTarget, s.practiceUntil, s.state.PracticePulls
			before := countPackets(f, opcode.MSGMoveStop)
			s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
				if mode == "replacement" {
					s.selfGUID = 9
					f.world.Self.Guid = 9
					s.state.PracticePhase = "replacement"
				}
				if mode == "error" {
					return true, "current-cold-source", nil, errors.New("active cold selector failed")
				}
				if mode == "missing" {
					return false, "current-cold-source", nil, nil
				}
				return true, "current-cold-source", map[string]any{"kind": "select"}, nil
			}
			s.Tick(context.Background())
			if mode == "replacement" {
				if countPackets(f, opcode.MSGMoveStop) != before || s.state.PracticePhase != "replacement" {
					t.Fatal("old callback changed replacement", s.stateLocked())
				}
				return
			}
			if !s.state.Practicing || s.state.Moving || s.practiceTarget != target || s.practiceUntil != until || s.state.PracticePulls != pulls ||
				countPackets(f, opcode.MSGMoveStop) != before+1 || lastCast(f) != 0 {
				t.Fatal("policy failure left motion running or erased task intent", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestColdridgeLifecycleRetainsCounterHighwaterAndFencesLateStart(t *testing.T) {
	s, f, _ := coldThreatRig(t, 707, 3, 1)
	s.Tick(context.Background())
	before := s.coldridgeGoal.counts
	coldThreatQuest(f).ObjectiveCounts[0] = 2
	s.Tick(context.Background())
	if s.coldridgeGoal.counts != before || s.coldridgeGoal.needed || lastCast(f) != 0 {
		t.Fatal("regressed progress restored pull authority", s.coldridgeGoal)
	}
	s, f, _ = coldridgeRig(t)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		output := s.starterSpells.runtime
		decision, err := output.EvaluateContext(ctx, input)
		s.selfGUID = 9
		f.world.Self.Guid = 9
		s.state.PracticePhase = "replacement"
		return true, output.Status().Revision, decision, err
	}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || s.state.PracticePhase != "replacement" {
		t.Fatal("late start replaced current body state", err, s.stateLocked())
	}
}

func TestColdridgeLifecycleAll32CandidatesAndCompleteQuestLogFitUnchangedLimits(t *testing.T) {
	s, f, _ := coldridgeRig(t)
	for i := 1; i < 32; i++ {
		c := proto.Clone(f.world.NearbyCreatures[0]).(*worldv1.CreatureState)
		c.Guid = (coldridgeWolfGUID & ^uint64(0xffffff)) | uint64(1000+i)
		c.X += float32(i)
		f.world.NearbyCreatures = append(f.world.NearbyCreatures, c)
	}
	input, valid := s.coldridgeLifecycleFacts(f.world, "targets")
	if !valid || len(input["nearby"].([]any)) != 32 || len(input["questLog"].([]any)) != 25 {
		t.Fatal("factual projection omitted candidates or own slots", input)
	}
	packed, err := structpb.NewStruct(input)
	if err != nil || proto.Size(packed) > modcb.StarterSpellPolicyMaxBytes {
		t.Fatal("complete supported frame exceeds existing callback limit", proto.Size(packed), err)
	}
	out, err := s.starterSpells.runtime.EvaluateContext(context.Background(), input)
	selected, valid := decodeColdridgeLifecycle(out)
	if err != nil || !valid || selected.Kind != "select" {
		t.Fatal("complete supported frame failed default value/node limits", selected, err)
	}
}

func TestColdridgeLifecycleTargetFree182FrameKeepsNilAndReachesItsPlanner(t *testing.T) {
	s, f, _ := cold182Rig(t, 2, 4, 3)
	input, known := s.coldridgeLifecycleFacts(f.world, "tick")
	if !known || input["selected"] != nil || s.practiceTarget != 0 {
		t.Fatal("target-free frame fabricated an empty selected row", input["selected"], s.practiceTarget)
	}
	output, err := s.starterSpells.runtime.EvaluateContext(context.Background(), input)
	lifecycle, valid := decodeColdridgeLifecycle(output)
	if err != nil || !valid || lifecycle.Kind != "continue" || lifecycle.TargetGUID != "0" {
		t.Fatal("actual nil selection was interpreted as a stale target", lifecycle, err)
	}
	plans := 0
	s.planColdridge182Path = func(ctx context.Context, root string, from, to position) ([]position, error) {
		plans++
		return coldStraight(ctx, root, from, to)
	}
	s.Tick(context.Background())
	if plans != 1 || s.practiceTarget != cold182GoalGUID || lastCast(f) != 0 || s.practicePullStarted {
		t.Fatal("target-free accepted lifecycle did not reach native route qualification", plans, s.stateLocked(), s.starterSpells.lastError)
	}
}

type coldLifecycleWorldHook struct {
	*fakeSession
	beforeWorld func()
}

func (f *coldLifecycleWorldHook) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" && f.beforeWorld != nil {
		f.beforeWorld()
	}
	return f.fakeSession.InvokeModule(ctx, r, options...)
}

func TestColdridgeLifecycleMovingSourceUsesOneSameChoiceReevaluation(t *testing.T) {
	for _, mode := range []string{"same_source", "changed_source", "changed_final_source", "changed_choice", "changed_controller"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldridgeRig(t)
			creature := f.world.NearbyCreatures[0]
			creature.PositionRevision = 1
			creature.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, DestinationKnown: true,
				DestinationX: creature.X + 2, DestinationY: creature.Y, DestinationZ: creature.Z}
			if mode == "changed_choice" {
				creature.Trajectory.DestinationX = creature.X + 17
				other := proto.Clone(creature).(*worldv1.CreatureState)
				other.Guid = (other.Guid & ^uint64(0xffffff)) | 900
				other.X, other.Trajectory = creature.X+1, nil
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, other)
			}
			reads, calls := 0, 0
			s.session = &coldLifecycleWorldHook{fakeSession: f, beforeWorld: func() {
				reads++
				creature.Trajectory.AgeMs += 100
				if mode == "changed_source" {
					creature.Trajectory.DurationMs++
				}
				if mode == "changed_final_source" && reads == 2 {
					creature.Trajectory.DurationMs++
				}
				if mode == "changed_choice" {
					creature.Trajectory.AgeMs = 500
				}
			}}
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				calls++
				out, err := s.starterSpells.runtime.EvaluateContext(ctx, input)
				if mode == "changed_controller" {
					s.state.InteractionOwner, s.interactionToken = "trade", "replacement-token"
				}
				return true, s.starterSpells.runtime.Status().Revision, out, err
			}
			goal := s.coldridgeGoal
			selected, _, owned := s.coldridgeLifecycleLocked(context.Background(), f.world, "targets")
			if s.coldridgeGoal != goal || len(f.packets) != 0 {
				t.Fatal("pure moving-source admission mutated goal or wrote", s.coldridgeGoal, f.packets)
			}
			if mode == "same_source" {
				if !owned || !selected.Valid || selected.Kind != "select" || calls != 2 || reads != 2 {
					t.Fatal("same source did not use bounded same-choice recheck", selected, owned, calls, reads, s.starterSpells.lastError)
				}
			} else if mode == "changed_controller" {
				if owned || selected.Valid || reads != 0 || s.state.InteractionOwner != "trade" || s.interactionToken != "replacement-token" {
					t.Fatal("late controller acquired Coldridge mutation authority", selected, owned, reads, s.stateLocked())
				}
			} else {
				wantCalls := 1
				wantReads := 1
				if mode == "changed_choice" || mode == "changed_final_source" {
					wantCalls = 2
				}
				if mode == "changed_final_source" {
					wantReads = 2
				}
				if !owned || selected.Valid || calls != wantCalls || reads != wantReads {
					t.Fatal("changed source/choice was retried or admitted", selected, owned, calls, reads, s.starterSpells.lastError)
				}
			}
		})
	}
}
