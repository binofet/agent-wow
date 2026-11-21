package buddyactions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

var recoveryPolicies sync.Map

func deathPolicySource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "death-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local recovery=(function()\n" + string(src) + "\nend)();local obs,seq\nreturn {on_event=function(e) recovery.on_event(e); if e.kind=='encounter_observation' then obs=e.value;seq=e.sequence end return true end,decide=function(f)return recovery.decide(f,obs,seq)end,export_state=recovery.export_state,import_state=recovery.import_state}"
}

// Existing corpse-route fixtures invoke the real shared release/query policy.
// Native maintenance alone is explicitly tested not to author those packets.
func tickDeathLua(t *testing.T, s *Service) {
	t.Helper()
	raw, ok := recoveryPolicies.Load(s)
	if !ok {
		vm, err := behavior.NewSessionWithOptions(context.Background(), s.sessionID, deathPolicySource(t), behavior.SessionOptions{ExecutionLimit: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		recoveryPolicies.Store(s, vm)
		raw = vm
		t.Cleanup(func() { vm.Close(); recoveryPolicies.Delete(s) })
	}
	vm := raw.(*behavior.Session)
	view, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var obs map[string]any
	if err = json.Unmarshal(data, &obs); err != nil {
		t.Fatal(err)
	}
	seq, err := vm.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if err != nil {
		t.Fatal(err)
	}
	out, err := vm.Decide(context.Background(), map[string]any{"observationSequence": strconv.FormatUint(seq, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if action, ok := out.Output["action"].(map[string]any); ok {
		epoch, _ := strconv.ParseUint(action["deathEpoch"].(string), 10, 64)
		request := &actionsv1.DeathActionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: epoch}
		id, err := vm.ReserveAction(out)
		if err != nil {
			t.Fatal(err)
		}
		var callErr error
		parse := func(key string) uint64 { v, _ := action[key].(string); n, _ := strconv.ParseUint(v, 10, 64); return n }
		switch action["capability"] {
		case "release_spirit":
			_, callErr = s.ReleaseSpirit(context.Background(), request)
		case "query_corpse":
			_, callErr = s.QueryCorpse(context.Background(), request)
		case "approach_corpse", "stop_corpse":
			radius, _ := action["rangeYards"].(float64)
			_, callErr = s.ApproachCorpse(context.Background(), &actionsv1.CorpseApproachRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: epoch,
				ExpectedPositionRevision: parse("expectedPositionRevision"), CorpseQueryRevision: parse("corpseQueryRevision"), RangeYards: float32(radius), Stop: action["capability"] == "stop_corpse"})
		case "reclaim_corpse":
			_, callErr = s.ReclaimCorpse(context.Background(), &actionsv1.ReclaimCorpseRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: epoch,
				ExpectedPositionRevision: parse("expectedPositionRevision"), CorpseGuid: parse("corpseGuid"), ExpectedCorpseRevision: parse("expectedCorpseRevision")})
		default:
			t.Fatal("unknown recovery action", action)
		}
		outcome := behavior.ActionSettledUnverified
		if status.Code(callErr) == codes.FailedPrecondition {
			outcome = behavior.ActionNotDispatched
		}
		if callErr != nil && outcome != behavior.ActionNotDispatched {
			t.Fatal(callErr)
		}
		_, err = vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: s.sessionID, ActionID: id, Outcome: outcome, Payload: map[string]any{"capability": action["capability"], "self_guid": action["selfGuid"], "death_epoch": action["deathEpoch"]}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Tick(context.Background())
}

func TestDeathReleaseQueryNativeTickIsPassiveAndEpochIsObservedWithoutEnable(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if countOpcode(f, opcode.CMSGRepopRequest) != 0 {
		t.Fatal("native maintenance selected release", f.packets)
	}
	s.EncounterObservation(context.Background(), &emptypb.Empty{})
	r := &actionsv1.DeathActionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: s.recovery.state.DeathEpoch}
	if _, err := s.Recovery(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseSpirit(context.Background(), r); err != nil {
		t.Fatal("primitive inherited policy-enable gate", err)
	}
	if countOpcode(f, opcode.CMSGRepopRequest) != 1 {
		t.Fatal(f.packets)
	}
	ghostAt(f, 0)
	s.Tick(context.Background())
	if countOpcode(f, opcode.MSGCorpseQuery) != 0 {
		t.Fatal("native maintenance selected query")
	}
	if _, err := s.QueryCorpse(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !s.recovery.snapshot(s.now()).CorpseQueryPending || countOpcode(f, opcode.MSGCorpseQuery) != 1 {
		t.Fatal(s.stateLocked())
	}
}
func TestDeathActionsValidateIdentityAndNeverReplayUnknown(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.Recovery(context.Background(), &actionsv1.ToggleRequest{})
	s.EncounterObservation(context.Background(), &emptypb.Empty{})
	r := &actionsv1.DeathActionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, DeathEpoch: s.recovery.state.DeathEpoch}
	for _, bad := range []*actionsv1.DeathActionRequest{{SessionId: "foreign", SelfGuid: 1, DeathEpoch: r.DeathEpoch}, {SessionId: r.SessionId, SelfGuid: 2, DeathEpoch: r.DeathEpoch}, {SessionId: r.SessionId, SelfGuid: 1, DeathEpoch: r.DeathEpoch + 1}} {
		if _, err := s.ReleaseSpirit(context.Background(), bad); status.Code(err) != codes.FailedPrecondition {
			t.Fatal(bad, err)
		}
	}
	if len(f.packets) != 0 {
		t.Fatal(f.packets)
	}
	f.failSend = true
	for range 3 {
		if _, err := s.ReleaseSpirit(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if countOpcode(f, opcode.CMSGRepopRequest) != 1 || !s.recovery.state.ReleaseAttempted || s.recovery.state.ReleaseWritten {
		t.Fatal(s.stateLocked(), f.packets)
	}
	ghostAt(f, 0)
	for range 3 {
		if _, err := s.QueryCorpse(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if countOpcode(f, opcode.MSGCorpseQuery) != 1 {
		t.Fatal("unknown query replayed", f.packets)
	}
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	if !s.recovery.state.CorpseQueryKnown || s.recovery.queryPending {
		t.Fatal(s.stateLocked())
	}
	f.world.Self.Alive = true
	f.world.Self.Health = 100
	f.world.Self.PlayerFlags = 0
	s.EncounterObservation(context.Background(), &emptypb.Empty{})
	f.world.Self.Alive = false
	f.world.Self.Health = 0
	s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if s.recovery.state.DeathEpoch != r.DeathEpoch+1 {
		t.Fatal("new death did not retire old reservations", s.stateLocked())
	}
	if _, err := s.ReleaseSpirit(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("old epoch revived", err)
	}
	r.DeathEpoch++
	if _, err := s.ReleaseSpirit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if countOpcode(f, opcode.CMSGRepopRequest) != 2 {
		t.Fatal(f.packets)
	}
}
