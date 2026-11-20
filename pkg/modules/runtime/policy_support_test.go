package modrt

import (
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestPolicyResurrectionBindsPatientToExactCorpsePacketAndEffect(t *testing.T) {
	const corpse = uint64(0xf101000000000035)
	op := &policySpell{capability: "resurrect", spell: 2006, target: 3, castTarget: corpse}
	packet := []byte{1}
	packet = binary.LittleEndian.AppendUint32(packet, 2006)
	packet = append(packet, 0)
	packet = binary.LittleEndian.AppendUint32(packet, 0x8000)
	packet = append(packet, 0xc1, 0x35, 0x01, 0xf1)
	if !op.matchesActionPacket(opcode.CMSGCastSpell, packet) {
		t.Fatal("owned corpse wire rejected", packet)
	}
	bad := append([]byte(nil), packet...)
	bad[len(bad)-3]++
	if op.matchesActionPacket(opcode.CMSGCastSpell, bad) {
		t.Fatal("foreign corpse admitted")
	}
	for _, test := range []struct {
		target   uint64
		phase    string
		observed bool
		want     behavior.ActionOutcome
	}{
		{corpse, "confirmed_alive", true, behavior.ActionConfirmed},
		{corpse, "server_cast_go_effect_unverified", false, behavior.ActionSettledUnverified},
		{corpse + 1, "confirmed_alive", true, ""},
	} {
		raw, _ := proto.Marshal(&actionsv1.SpellOperation{Capability: "resurrect", SpellId: 2006, TargetGuid: test.target, Phase: test.phase, TransportWritten: true, ServerCastGo: true, EffectObserved: test.observed})
		got, payload, err := op.decodeEffect(raw)
		if err != nil || got != test.want {
			t.Fatal(got, payload, err)
		}
		if got != "" && payload["target_guid"] != "3" {
			t.Fatal("receipt lost patient identity", payload)
		}
	}
}
