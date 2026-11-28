package buddyworld

import (
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNaturalTaxiLandingRetainsOnlyUninterruptedOwnFlagTransition(t *testing.T) {
	for _, event := range []string{"clear", "unchanged positive flags", "foreign clear", "death and restore", "retakeoff and clear", "malformed", "same map reentry", "new own source"} {
		t.Run(event, func(t *testing.T) {
			x := newCompletionRig(t)
			before := ownMovementSnapshot(t, x.s)
			rawRev := before.SelfMovementFacts.Revision
			clearBody := deltaFixture(1, map[int]uint32{unitFlags: 8})
			// A busy map may provide only bystander updates until the final
			// own flag edge. No actor movement or client completion is used.
			x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(2, map[int]uint32{unitHealth: 50}))
			switch event {
			case "foreign clear":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(2, map[int]uint32{unitFlags: 8}))
			case "unchanged positive flags":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0x100000}))
			default:
				x.deliver(t, opcode.SMSGCompressedUpdateObject, compressed(t, clearBody))
				switch event {
				case "death and restore", "retakeoff and clear":
					f, mask, after := unitHealth, uint32(0), uint32(70)
					if event == "retakeoff and clear" {
						f, mask, after = unitFlags, 0x100008, 8
					}
					one, two := deltaFixture(1, map[int]uint32{f: mask}), deltaFixture(1, map[int]uint32{f: after})
					binary.LittleEndian.PutUint32(one, 2)
					x.deliver(t, opcode.SMSGUpdateObject, append(one, two[4:]...))
				case "malformed":
					x.deliver(t, opcode.SMSGUpdateObject, []byte{1})
				case "same map reentry":
					x.deliver(t, opcode.SMSGNewWorld, login())
				case "new own source":
					x.start(t, completionFlyingHeader(5, 36), 18)
				}
			}
			got := ownMovementSnapshot(t, x.s)
			want := event == "clear"
			if positive := got.SelfMotionAuthority.NaturalTaxiLandingRevision != 0; positive != want {
				t.Fatal(event, got.SelfMotionAuthority)
			}
			if want && (got.SelfMotionAuthority.Known || got.SelfMotionAuthority.TaxiOnMotionRevision == 0 ||
				got.SelfMotionAuthority.UnitFlagsSourceOpcode != opcode.SMSGCompressedUpdateObject || got.SelfMovementFacts.Revision != rawRev ||
				got.SplineCompletion.Operation != nil || len(x.f.packets) != 0) {
				t.Fatal("a receive-only flag edge altered unrelated evidence", got)
			}
		})
	}
}
