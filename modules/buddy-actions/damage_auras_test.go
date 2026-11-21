package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestWarlockUsesAnInstantAfterInterruptionWithoutRefreshingActiveCurse(t *testing.T) {
	s, f, target, advance := partyDefenseRig(t, 9)
	const agony, bolt = 980, 705
	learnSpells(s, agony, bolt)
	self := f.world.Self
	self.BaseManaKnown, self.BaseMana, self.Power = true, 100, 100
	s.state.LastCastFailure, s.state.LastCastTargetGuid = spellFailedInterrupted, target.Guid
	pos, _, _ := creaturePosition(target)
	choose := func(health uint32) uint32 {
		return s.starterBestDamageLocked(self, pos, health, target.Guid).ID
	}
	if got := choose(0); got != agony {
		t.Fatal("expected a learned instant on the healthy attacker after interruption", got)
	}
	if got := choose(10); got != bolt {
		t.Fatal("expected direct damage to finish a nearly dead enemy", got)
	}
	packet := curseAura(target.Guid, s.selfGUID, agony)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: packet})
	if got := choose(0); got != bolt {
		t.Fatal("a still-active curse should not be refreshed", got)
	}
	advance(24 * time.Second)
	if got := choose(0); got != agony {
		t.Fatal("expired curse should be eligible again", got)
	}
}

func curseAura(target, caster uint64, spell uint32) []byte {
	aura := buffAura(target, caster, 3, spell, 24000)
	if spell != 0 {
		aura[len(packedGUID(target))+5] ^= 0x10 | 0x80 // negative aura
	}
	return aura
}

func TestDamageAuraFollowsCasterAndTargetLifecycle(t *testing.T) {
	s, _, target, _ := partyDefenseRig(t, 9)
	const agony = 980
	learnSpells(s, agony)
	packet := func(op uint32, payload []byte) {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: payload})
	}
	caster := s.selfGUID + 30
	packet(opcode.SMSGAuraUpdate, curseAura(target.Guid, caster, agony))
	if s.hasOwnDamageAuraLocked(target.Guid, agony) {
		t.Fatal("another warlock's curse was counted as ours")
	}
	apply := func() { packet(opcode.SMSGAuraUpdateAll, curseAura(target.Guid, s.selfGUID, agony)) }
	apply()
	if !s.hasOwnDamageAuraLocked(target.Guid, agony) {
		t.Fatal("own curse missing from complete aura update")
	}
	packet(opcode.SMSGAuraUpdate, curseAura(target.Guid, s.selfGUID, 0))
	if s.hasOwnDamageAuraLocked(target.Guid, agony) {
		t.Fatal("empty aura slot failed to clear curse")
	}
	apply()
	packet(opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, target.Guid), 0))
	if s.hasOwnDamageAuraLocked(target.Guid, agony) {
		t.Fatal("despawning target retained a curse")
	}
	apply()
	packet(opcode.SMSGTransferPending, make([]byte, 4))
	if s.hasOwnDamageAuraLocked(target.Guid, agony) {
		t.Fatal("world transfer retained a curse")
	}
}
