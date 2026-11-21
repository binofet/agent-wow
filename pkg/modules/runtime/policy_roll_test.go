package modrt

import (
	"encoding/binary"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
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
