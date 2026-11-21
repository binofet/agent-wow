package modrt

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
)

func TestPolicyRollPacketBindsOfferSlotAndVote(t *testing.T) {
	op := &policySpell{target: packagedRollGUID, roll: &policyRoll{slot: 2, choice: "disenchant", wireChoice: 3}}
	payload := binary.LittleEndian.AppendUint64(nil, packagedRollGUID)
	payload = append(binary.LittleEndian.AppendUint32(payload, 2), 3)
	if !op.matchesActionPacket(opcode.CMSGLootRoll, payload) || op.moduleName() != inventoryModule {
		t.Fatal("exact Inventory vote was not admitted")
	}
	for _, at := range []int{0, 8, 12} {
		wrong := append([]byte(nil), payload...)
		wrong[at]++
		if op.matchesActionPacket(opcode.CMSGLootRoll, wrong) {
			t.Fatalf("unbound vote field at %d", at)
		}
	}
	if op.matchesActionPacket(opcode.CMSGCastSpell, payload) || op.matchesActionPacket(opcode.CMSGLootRoll, append(payload, 0)) {
		t.Fatal("unexpected vote opcode or trailing data admitted")
	}
}

func TestPolicyRollConfirmsOnlyExactOwnNativeVoteReceipt(t *testing.T) {
	op := &policySpell{capability: "loot_roll", target: packagedRollGUID,
		roll: &policyRoll{self: 11, visit: 1, revision: 4, slot: 2, choice: "need", requestID: "exact-request"}}
	for _, test := range []struct {
		name string
		edit func(*inventoryv1.RollReceipt)
		want behavior.ActionOutcome
	}{
		{"native own echo", func(*inventoryv1.RollReceipt) {}, behavior.ActionConfirmed},
		{"missing echo", func(row *inventoryv1.RollReceipt) { row.ServerVoteObserved = false }, ""},
		{"other request", func(row *inventoryv1.RollReceipt) { row.RequestId += "-other" }, ""},
		{"other offer", func(row *inventoryv1.RollReceipt) { row.RollGuid++ }, ""},
		{"other slot", func(row *inventoryv1.RollReceipt) { row.LootSlot++ }, ""},
		{"old visit", func(row *inventoryv1.RollReceipt) { row.VisitId++ }, ""},
		{"new revision", func(row *inventoryv1.RollReceipt) { row.OfferRevision++ }, ""},
		{"other choice", func(row *inventoryv1.RollReceipt) { row.Choice = "disenchant" }, ""},
		{"deadline", func(row *inventoryv1.RollReceipt) {
			row.Phase = "outcome_unconfirmed_no_retry"
			row.ServerVoteObserved = false
		}, behavior.ActionSettledUnverified},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &inventoryv1.RollReceipt{RequestId: "exact-request", RollGuid: packagedRollGUID,
				OfferRevision: 4, VisitId: 1, LootSlot: 2, Choice: "need", Phase: "server_vote_observed",
				ServerVoteObserved: true, TransportAttempted: true, TransportWritten: true}
			test.edit(native)
			raw, err := proto.Marshal(native)
			if err != nil {
				t.Fatal(err)
			}
			outcome, payload, err := op.decodeRollEffect(raw)
			if err != nil || outcome != test.want {
				t.Fatalf("receipt outcome = %q, payload %+v, error %v", outcome, payload, err)
			}
		})
	}
}

func TestPolicyRollFinalNativeRefusalDoesNotStrandTheLedger(t *testing.T) {
	ctx := context.Background()
	manager := New(packagedRollRegistry(t), nil, nil)
	t.Cleanup(manager.cancel)
	instance := manager.instances[inventoryModule]
	instance.healthy = true
	conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(_ context.Context, method string, request, response any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			switch method {
			case grpc_health_v1.Health_Check_FullMethodName:
				response.(*grpc_health_v1.HealthCheckResponse).Status = grpc_health_v1.HealthCheckResponse_SERVING
			case "/warcraftbuddy.inventory.v1.BuddyInventory/Roll":
				var exact inventoryv1.RollRequest
				raw, err := proto.Marshal(request.(proto.Message))
				if err != nil {
					return err
				}
				if err := proto.Unmarshal(raw, &exact); err != nil {
					return err
				}
				// The native final-world check refused before any callback write.
				*response.(*inventoryv1.RollState) = inventoryv1.RollState{
					SessionId: "body", SelfGuid: policyMageGUID,
					Receipts: []*inventoryv1.RollReceipt{{
						RequestId: exact.RequestId, RollGuid: exact.RollGuid,
						OfferRevision: exact.ExpectedOfferRevision, VisitId: exact.VisitId,
						Choice: exact.Choice, LootSlot: exact.ExpectedLootSlot, Phase: "not_dispatched",
					}},
				}
			default:
				t.Fatalf("unexpected native method %q", method)
			}
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	instance.conn = conn
	source := fmt.Sprintf(`return {
 decide=function(frame)
  if frame.input.cast then return {action={capability="loot_roll",sessionId=frame.session_id,
    selfGuid="11",visitId="1",rollGuid="%d",offerRevision="4",lootSlot=2,choice="need"}} end
  return {}
 end,
 on_event=function(event) return true end,
 export_state=function() return {version=1,data={}} end,
 import_state=function(saved) return true end
}`, packagedRollGUID)
	vm, err := behavior.NewSession(ctx, "body", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	host := &SessionPolicy{vm: vm, manager: manager, body: "11", lifetime: manager.ctx}
	_, receipt, err := host.Step(ctx, map[string]any{"cast": true}, nil)
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionNotDispatched ||
		receipt.Payload["phase"] != "not_dispatched" {
		t.Fatalf("native refusal receipt = %+v, error %v", receipt, err)
	}
	if _, _, err := host.Step(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if vm.Status().Pending != 0 || len(manager.policySpells.active) != 0 {
		t.Fatalf("native refusal left pending action: %+v", vm.Status())
	}
}
