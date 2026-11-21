package modrt

import (
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestPolicyBuffBindsSelectedSpellFriendlyAndObservedAura(t *testing.T) {
	const enemy = uint64(3)
	op := &policySpell{capability: "buff", spell: 1244, target: enemy}
	packet := []byte{1}
	packet = binary.LittleEndian.AppendUint32(packet, 1244)
	packet = append(packet, 0)
	packet = binary.LittleEndian.AppendUint32(packet, 2)
	packet = append(packet, 1, 3)
	if !op.matchesActionPacket(opcode.CMSGCastSpell, packet) {
		t.Fatal("exact selected interrupt packet was rejected")
	}
	other := append([]byte(nil), packet...)
	other[11]++
	if op.matchesActionPacket(opcode.CMSGCastSpell, other) {
		t.Fatal("foreign enemy packet was admitted")
	}
	for _, tc := range []struct {
		phase  string
		effect bool
		target uint64
		want   behavior.ActionOutcome
	}{
		{"confirmed_aura", true, enemy, behavior.ActionConfirmed},
		{"server_cast_go_effect_unverified", false, enemy, behavior.ActionSettledUnverified},
		{"transport_unconfirmed_no_retry", false, enemy, behavior.ActionUnknown},
		{"server_rejected_no_replay", false, enemy, behavior.ActionFailed},
		{"confirmed_aura", true, enemy + 1, ""},
	} {
		raw, _ := proto.Marshal(&actionsv1.SpellOperation{Capability: "buff", SpellId: 1244, TargetGuid: tc.target, Phase: tc.phase, TransportWritten: true, ServerCastGo: true, EffectObserved: tc.effect})
		got, _, err := op.decodeEffect(raw)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
}
