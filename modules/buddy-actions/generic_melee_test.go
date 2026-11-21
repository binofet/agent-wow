package buddyactions

import (
	"encoding/binary"
	"fmt"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func TestGenericMeleeExactStartWorksForEveryKnownClass(t *testing.T) {
	for _, class := range []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 11} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, r, _ := exactMeleeFixture(t)
			f.world.Self.ClassId = class
			out, err := s.Melee(exactMeleeContext(), r)
			if err != nil || !out.GetAutoAttackRequested() || out.GetAutoAttacking() || countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal(out, err, f.packets)
			}
			packet := f.packets[len(f.packets)-1]
			if len(packet.Payload) != 8 || binary.LittleEndian.Uint64(packet.Payload) != r.TargetGuid {
				t.Fatal("different creature attacked", packet)
			}
		})
	}
}
