package modrt

import (
	"context"
	"strconv"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func TestPolicyObservationConsumesTerminalReceiptWithoutPacketWorker(t *testing.T) {
	const sid = "quiet-world"
	ctx := context.Background()
	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	t.Cleanup(manager.cancel)
	vm, err := behavior.NewSession(ctx, sid, `local state={settled=0}; return {
  on_event=function(e) if e.receipt and e.receipt.outcome=="settled_unverified" then state.settled=state.settled+1;state.observed=e.receipt.payload.effect_observed end;return true end,
  decide=function() if not state.proposed then state.proposed=true;return {action={capability="heal"}} end;return state end,
  export_state=function() return {version=1,data=state} end,
  import_state=function(s) state=s.data;return true end
 }`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	decision, err := vm.Decide(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := vm.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	op := &policySpell{vm: vm, capability: "heal", spell: 2050, target: 3, attempted: true, token: token,
		origin: modsession.WriteOrigin{SessionID: sid, Module: actionsModule, Writer: "lua", ActionID: strconv.FormatUint(id, 10)}}
	manager.policySpells.active = map[string]*policySpell{token: op}
	host := &SessionPolicy{vm: vm, manager: manager}
	if err = host.recordSpellTransport(op, &behavior.ActionReceipt{SessionID: sid, ActionID: id, Outcome: behavior.ActionTransportAccepted}); err != nil {
		t.Fatal(err)
	}
	if err = vm.Drain(ctx); err != nil || vm.Status().Pending != 1 {
		t.Fatal("fixture not waiting for native result", vm.Status(), err)
	}
	native := &actionsv1.SpellOperation{Capability: "heal", SpellId: 2050, TargetGuid: 3, Phase: "outcome_unconfirmed_no_retry", TransportWritten: true}
	effect, err := proto.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	// A real gRPC client call option delivers the same metadata trailer that
	// Actions' periodic read publishes. No packet queue or world event runs.
	conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(_ context.Context, method string, _, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			if method != "/warcraftbuddy.actions.v1.BuddyActions/EncounterObservation" {
				t.Fatalf("unexpected request %s", method)
			}
			for _, option := range opts {
				if trailer, ok := option.(grpc.TrailerCallOption); ok {
					*trailer.TrailerAddr = metadata.Pairs("agentwow-policy-spell", token, "agentwow-policy-spell-receipt-bin", string(effect))
				}
			}
			raw, _ := proto.Marshal(&actionsv1.EncounterObservationResponse{SessionId: sid, SelfGuid: 11, HostRequested: proto.Bool(true)})
			return proto.Unmarshal(raw, reply.(proto.Message))
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	instance := manager.instances[actionsModule]
	instance.conn, instance.healthy = conn, true
	for poll := 0; poll < 3; poll++ {
		if _, err = manager.readPolicyObservation(ctx, sid, encounterObservationMethod); err != nil {
			t.Fatal(err)
		}
		if err = vm.Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	final, err := vm.Decide(ctx, nil)
	if err != nil || vm.Status().Pending != 0 || final.Output["settled"] != float64(1) || final.Output["observed"] != false {
		t.Fatalf("quiet poll did not release exactly once without inventing success: %+v %+v %v", vm.Status(), final.Output, err)
	}
	if len(manager.policySpells.active) != 0 {
		t.Fatal("settled token retained")
	}
	// Keep the shared metadata codec in this assertion: a misspelled test key
	// must not silently prove a different receipt path than packet delivery.
	trailers := metadata.Pairs("agentwow-policy-spell", token, "agentwow-policy-spell-receipt-bin", string(effect))
	if _, _, ok := modcb.PolicySpellEffect(trailers); !ok {
		t.Fatal("fixture receipt metadata invalid")
	}
}
