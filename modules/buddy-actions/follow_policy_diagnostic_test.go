package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func assertFollowFailureText(t *testing.T, failure *actionsv1.PolicyDecisionFailure) {
	t.Helper()
	if failure == nil {
		t.Fatal("follow policy failure was absent")
	}
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"mode", failure.Mode, 64}, {"stage", failure.Stage, 32},
		{"source revision", failure.SourceRevision, 64}, {"error", failure.Error, 240},
	} {
		if len(field.value) > field.limit {
			t.Fatalf("%s exceeded its diagnostic byte bound: %d > %d", field.name, len(field.value), field.limit)
		}
		for _, value := range []byte(field.value) {
			if value < 32 || value > 126 {
				t.Fatalf("%s retained a control or non-ASCII byte: %q", field.name, field.value)
			}
		}
	}
}

func TestFollowPolicyRuntimeFailureReportsAttemptRevisionAndRetainsMemory(t *testing.T) {
	s, fake, _ := rig(t)
	// Isolate the error-format assertion from a watchdog timeout. Existing
	// formation and travel tests exercise their production execution budget.
	runtime, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	message := "diagnostic sentinel\n\t\r" + strings.Repeat("é", 20) + strings.Repeat("x", 600)
	source := fmt.Sprintf("return {decide=function(input) error(%q) end}", message)
	revision, err := runtime.Reload(source, "")
	if err != nil {
		t.Fatal(err)
	}
	memory := map[string]any{"guid": "4", "marker": "committed", "nested": map[string]any{"heading": float64(1)}}
	expectedMemory := map[string]any{"guid": "4", "marker": "committed", "nested": map[string]any{"heading": float64(1)}}
	s.formation.runtime, s.formation.memory = runtime, memory
	if out := s.formation.decide(map[string]any{"mode": "near_owner"}); out != nil {
		t.Fatal("failed current runtime produced a default decision", out)
	}
	if s.formation.runtime != nil || !reflect.DeepEqual(s.formation.memory, expectedMemory) {
		t.Fatal("runtime failure erased committed memory or retained a quarantined evaluator", s.formation.memory)
	}
	state, err := s.Status(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	failure := state.GetLastFollowPolicyFailure()
	assertFollowFailureText(t, failure)
	if failure.Mode != "near_owner" || failure.Stage != "evaluate" || failure.SourceRevision != revision ||
		!strings.Contains(failure.Error, "diagnostic sentinel") || len(failure.Error) != 240 {
		t.Fatal("status lost the failed attempt's revision or bounded underlying error", failure)
	}
	committedFailure := proto.Clone(failure).(*actionsv1.PolicyDecisionFailure)
	failure.Error, failure.SourceRevision = "caller changed snapshot", "caller changed revision"
	again, err := s.Status(context.Background(), &emptypb.Empty{})
	if err != nil || !proto.Equal(again.GetLastFollowPolicyFailure(), committedFailure) {
		t.Fatal("status exposed mutable internal failure storage", again, err)
	}

	current, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	currentRevision, err := current.Reload("return {decide=function(input)return {arrived=true} end}", "")
	if err != nil {
		t.Fatal(err)
	}
	s.formation.runtime = current
	out := s.formation.decide(map[string]any{"mode": "service_travel"})
	if out["arrived"] != true || s.formation.sourceRevision != currentRevision ||
		!reflect.DeepEqual(s.formation.memory, expectedMemory) {
		t.Fatal("later current-source success lost its result or committed memory", out, s.formation.sourceRevision, s.formation.memory)
	}
	if snapshot := s.stateLocked(); !proto.Equal(snapshot.GetLastFollowPolicyFailure(), committedFailure) {
		t.Fatal("success erased the last observed policy failure", snapshot.GetLastFollowPolicyFailure())
	}
	if len(fake.packets) != 0 {
		t.Fatal("diagnostic reads wrote gameplay packets", fake.packets)
	}
}

func TestFollowPolicyRemoteFailureUsesOnlyReturnedAttemptRevision(t *testing.T) {
	s, _, _ := rig(t)
	s.formation.memory = map[string]any{"guid": "4", "marker": "committed"}
	s.formation.sourceRevision = strings.Repeat("b", 64)
	attemptRevision := strings.Repeat("a", 64)
	s.formation.remote = func(map[string]any) (bool, string, map[string]any, error) {
		return true, attemptRevision, nil, errors.New("remote policy sentinel\n" + strings.Repeat("detail", 60))
	}
	for _, revision := range []string{attemptRevision, ""} {
		attemptRevision = revision
		if out := s.formation.decide(map[string]any{"mode": "service_travel"}); out != nil {
			t.Fatal("callback error produced a builtin decision", out)
		}
		failure := s.stateLocked().GetLastFollowPolicyFailure()
		assertFollowFailureText(t, failure)
		if failure.Mode != "service_travel" || failure.Stage != "callback" || failure.SourceRevision != revision ||
			!strings.Contains(failure.Error, "remote policy sentinel") || s.formation.runtime != nil ||
			s.formation.memory["marker"] != "committed" {
			t.Fatal("callback failure reused an unrelated revision or discarded its cause", failure)
		}
	}
	lastFailure := proto.Clone(s.formation.lastFailure).(*actionsv1.PolicyDecisionFailure)
	s.formation.remote = func(map[string]any) (bool, string, map[string]any, error) {
		return true, strings.Repeat("c", 64), map[string]any{"arrived": true}, nil
	}
	if out := s.formation.decide(map[string]any{"mode": "service_travel"}); out["arrived"] != true ||
		!proto.Equal(s.stateLocked().GetLastFollowPolicyFailure(), lastFailure) {
		t.Fatal("later callback success erased an unknown-revision failure", out, s.formation.lastFailure)
	}
}

func TestFollowPolicyFailureSanitizesEveryFieldAndSnapshotsAreDetached(t *testing.T) {
	s, _, _ := rig(t)
	s.formation.recordFailure(strings.Repeat("m", 80)+"\n", strings.Repeat("s", 40)+"\t",
		strings.Repeat("r", 70)+"é", "first\x00\n\t\r"+strings.Repeat("é", 150)+strings.Repeat("x", 300))
	stored := proto.Clone(s.formation.lastFailure).(*actionsv1.PolicyDecisionFailure)
	assertFollowFailureText(t, stored)
	if len(stored.Mode) != 64 || len(stored.Stage) != 32 || len(stored.SourceRevision) != 64 || len(stored.Error) != 240 {
		t.Fatal("diagnostic fields were not bounded independently", stored)
	}
	snapshot := s.stateLocked()
	snapshot.LastFollowPolicyFailure.Mode = "mutated"
	snapshot.LastFollowPolicyFailure.Stage = "mutated"
	snapshot.LastFollowPolicyFailure.SourceRevision = "mutated"
	snapshot.LastFollowPolicyFailure.Error = "mutated"
	if !proto.Equal(s.formation.lastFailure, stored) || !proto.Equal(s.stateLocked().GetLastFollowPolicyFailure(), stored) {
		t.Fatal("stateLocked returned the stored diagnostic message")
	}
}

func TestTrainerTravelMalformedPolicyDecisionKeepsUnderlyingFailureAndDoesNotMove(t *testing.T) {
	for _, callbackError := range []bool{false, true} {
		name := "malformed decision"
		if callbackError {
			name = "callback failure"
		}
		t.Run(name, func(t *testing.T) {
			s, fake, _, request := trainerTravelRig(t)
			fake.world.NearbyCreatures[0].X = fake.world.Self.X + 2
			revision := strings.Repeat("d", 64)
			s.formation.memory = map[string]any{"marker": "committed"}
			s.formation.remote = func(map[string]any) (bool, string, map[string]any, error) {
				if callbackError {
					return true, revision, nil, errors.New("trainer callback sentinel")
				}
				return true, revision, map[string]any{"arrived": "true"}, nil
			}
			s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				t.Fatal("unusable policy decision reached route planning")
				return nil, nil
			}
			got, err := s.TravelTrainer(context.Background(), request)
			if err != nil || got == nil || got.Reason != "trainer_travel_policy_unavailable" ||
				got.Moving || got.Traveling || len(fake.packets) != 0 || s.formation.memory["marker"] != "committed" {
				t.Fatal("unusable service policy moved or erased committed state", got, err, fake.packets)
			}
			failure := got.GetLastFollowPolicyFailure()
			assertFollowFailureText(t, failure)
			stage, cause := "decision", "arrived"
			if callbackError {
				stage, cause = "callback", "trainer callback sentinel"
			}
			if failure.Mode != "service_travel" || failure.Stage != stage || failure.SourceRevision != revision ||
				!strings.Contains(failure.Error, cause) {
				t.Fatal("service arrival overwrote or hid the actual failure stage", failure)
			}
		})
	}
}

func TestFollowPolicyFailurePublishedStateContractMatchesGeneratedAPI(t *testing.T) {
	generated := (&actionsv1.State{}).ProtoReflect().Descriptor()
	field := generated.Fields().ByNumber(121)
	if field == nil || field.Name() != "last_follow_policy_failure" ||
		field.Message().FullName() != "warcraftbuddy.actions.v1.PolicyDecisionFailure" {
		t.Fatal("State field 121 does not publish follow policy diagnostics", field)
	}
	diagnostic := (&actionsv1.PolicyDecisionFailure{}).ProtoReflect().Descriptor()
	for i, name := range []protoreflect.Name{"mode", "stage", "source_revision", "error"} {
		got := diagnostic.Fields().ByName(name)
		if got == nil || got.Number() != protoreflect.FieldNumber(i+1) || got.Kind() != protoreflect.StringKind {
			t.Fatal("diagnostic field lost its published string contract", name, got)
		}
	}
	// Keep the imported Actions contract aligned throughout the descriptor
	// cohort, including the State exposed by CorpseObservation.actions.
	for _, path := range []string{"module.pb", "../buddy-inventory/module.pb", "../buddy-loot/module.pb"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		set := &descriptorpb.FileDescriptorSet{}
		if err := proto.Unmarshal(body, set); err != nil {
			t.Fatal(err)
		}
		files, err := protodesc.NewFiles(set)
		if err != nil {
			t.Fatal(err)
		}
		published, err := files.FindDescriptorByName(generated.FullName())
		if err != nil {
			t.Fatal(err)
		}
		publishedState := published.(protoreflect.MessageDescriptor)
		publishedField := publishedState.Fields().ByNumber(121)
		if publishedField == nil || !proto.Equal(protodesc.ToDescriptorProto(generated), protodesc.ToDescriptorProto(publishedState)) ||
			!proto.Equal(protodesc.ToFieldDescriptorProto(field), protodesc.ToFieldDescriptorProto(publishedField)) ||
			!proto.Equal(protodesc.ToDescriptorProto(diagnostic), protodesc.ToDescriptorProto(publishedField.Message())) {
			t.Fatal(path, "and generated binary API disagree on the follow failure diagnostic")
		}
	}
}
