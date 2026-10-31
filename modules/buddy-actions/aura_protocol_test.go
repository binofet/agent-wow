package buddyactions

import (
	"context"
	"encoding/binary"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
	"time"
)

func TestBuffAuraProtocolAtomicPrefixesFullAndDelta(t *testing.T) {
	s, _, advance := buffRig(t, 5, 1243)
	full := buffAura(1, 3, 2, 1243, 600000)
	full = append(full, buffAura(1, 1, 9, 588, 10000)[len(packedGUID(1)):]...)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: full})
	set := s.buffAuras[1]
	if set == nil || !set.complete || set.slots[2].caster != 3 || set.slots[9].caster != 1 {
		t.Fatal(set)
	}
	rev := s.buffAuraRevision
	for i := 0; i < len(full); i++ {
		if i == len(packedGUID(1)) || i == len(buffAura(1, 3, 2, 1243, 600000)) {
			continue
		}
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: full[:i]})
		if s.buffAuraRevision != rev {
			t.Fatal("partial frame committed", i)
		}
	}
	advance(time.Second)
	if set.slots[9].remaining(s.now()) != 9000 {
		t.Fatal("duration not measured from actual observation")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 9, 0, 0)})
	if set.slots[9].spell != 0 || set.slots[2].spell != 1243 {
		t.Fatal("wrong removal")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: packedGUID(1)})
	if !s.buffAuras[1].complete || s.buffAuras[1].slots[2].spell != 0 {
		t.Fatal("empty full baseline not honored")
	}
}
func TestBuffAuraRejectsMalformedFlagsDurationSlotsAndForeignKinds(t *testing.T) {
	base := buffAura(1, 1, 2, 1243, 600000)
	for _, kind := range []string{"unsupported flags", "invalid slot", "duration", "duplicate", "trailing"} {
		p := append([]byte(nil), base...)
		switch kind {
		case "unsupported flags":
			p[len(packedGUID(1))+5] |= 0x40
		case "invalid slot":
			p[len(packedGUID(1))] = 255
		case "duration":
			binary.LittleEndian.PutUint32(p[len(p)-4:], 600001)
		case "duplicate":
			p = append(p, base[len(packedGUID(1)):]...)
		case "trailing":
			p = append(p, 1)
		}
		if _, _, err := decodeBuffAuras(p, true); err == nil {
			t.Fatal("accepted", kind)
		}
	}
}
func TestBuffAuraNormalCreatureFramesAreIgnoredWithoutMalformedClaims(t *testing.T) {
	s, _, _ := buffRig(t, 5, 1243)
	before := s.state.MalformedPackets
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(0xf130000000000001, 1, 2, 1243, 600000)})
	if len(s.buffAuras) != 0 || s.state.MalformedPackets != before {
		t.Fatal("normal NPC aura treated as malformed/player authority")
	}
}
func TestBuffAuraCacheBoundAndLifecycle(t *testing.T) {
	s, _, _ := buffRig(t, 5, 1243)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(1, 1, 2, 1243, 600000)})
	for g := uint64(10); g < 100; g++ {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(g, g, 2, 1243, 600000)})
	}
	if len(s.buffAuras) != 64 || s.buffAuras[1] == nil {
		t.Fatal("unbounded or own eviction")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGTransferPending, Payload: make([]byte, 4)})
	if len(s.buffAuras) != 0 {
		t.Fatal("old world aura retained")
	}
}
func FuzzBuffAuraAndSpellbook(f *testing.F) {
	f.Add(buffAura(1, 1, 2, 1243, 600000))
	f.Add(buffInitial(1243, 168))
	f.Fuzz(func(t *testing.T, p []byte) {
		decodeBuffAuras(p, false)
		decodeBuffAuras(p, true)
		completeSpellbook(p)
	})
}
