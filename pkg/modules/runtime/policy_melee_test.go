package modrt

import (
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestPolicyMeleeCallbackLimitsWireToExactOperation(t *testing.T) {
	const target = uint64(0xf130000000000023)
	start := &policySpell{target: target, melee: &policyMelee{}}
	stop := &policySpell{target: target, melee: &policyMelee{stop: true}}
	body := binary.LittleEndian.AppendUint64(nil, target)
	for _, test := range []struct {
		name string
		op   *policySpell
		code uint32
		body []byte
		want bool
	}{
		{"exact target", start, opcode.CMSGAttackswing, body, true},
		{"other target", start, opcode.CMSGAttackswing, binary.LittleEndian.AppendUint64(nil, target+1), false},
		{"trailing bytes", start, opcode.CMSGAttackswing, append(body, 0), false},
		{"stop with start token", start, opcode.CMSGAttackstop, nil, false},
		{"swing with stop token", stop, opcode.CMSGAttackswing, body, false},
		{"nonempty stop", stop, opcode.CMSGAttackstop, body, false},
		{"bodyless stop", stop, opcode.CMSGAttackstop, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.op.matchesActionPacket(test.code, test.body); got != test.want {
				t.Fatalf("opcode %#x, body %x admitted = %v", test.code, test.body, got)
			}
		})
	}
}

func TestPolicyMeleeEffectMatchesBodyTargetVisitAndDirection(t *testing.T) {
	op := &policySpell{capability: "melee_stop", target: 7, melee: &policyMelee{self: 9, visit: 3, stop: true}}
	for _, test := range []struct {
		name    string
		self    uint64
		target  uint64
		visit   uint64
		enabled bool
		phase   string
		want    behavior.ActionOutcome
	}{
		{"matching", 9, 7, 3, false, "confirmed_attack_stop", behavior.ActionConfirmed},
		{"other body", 8, 7, 3, false, "confirmed_attack_stop", ""},
		{"other target", 9, 8, 3, false, "confirmed_attack_stop", ""},
		{"old visit", 9, 7, 2, false, "confirmed_attack_stop", ""},
		{"other direction", 9, 7, 3, true, "confirmed_attack_stop", ""},
		{"wrong acknowledgement", 9, 7, 3, false, "confirmed_attack_start", ""},
		{"timeout", 9, 7, 3, false, "outcome_unconfirmed_no_retry", behavior.ActionSettledUnverified},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := proto.Marshal(&actionsv1.MeleeOperation{
				SelfGuid: test.self, TargetGuid: test.target, VisitId: test.visit, Enabled: test.enabled,
				Phase: test.phase, TransportWritten: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			outcome, payload, err := op.decodeMeleeEffect(raw)
			if err != nil || outcome != test.want {
				t.Fatalf("outcome = %q, payload = %+v, error = %v", outcome, payload, err)
			}
			if outcome != "" && (payload["effect_observed"] != (outcome == behavior.ActionConfirmed) ||
				payload["visit_id"] != "3" || payload["target_guid"] != "7" || payload["capability"] != "melee_stop") {
				t.Fatalf("wrong effect payload: %+v", payload)
			}
		})
	}
}
