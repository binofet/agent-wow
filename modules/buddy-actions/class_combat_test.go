package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
	"time"
)

func learnSpells(s *Service, spells ...uint32) {
	p := []byte{0, byte(len(spells)), byte(len(spells) >> 8)}
	for _, spell := range spells {
		p = binary.LittleEndian.AppendUint32(p, spell)
		p = append(p, 0, 0)
	}
	p = append(p, 0, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: p})
}
func countPackets(f *fakeSession, op uint32) int {
	n := 0
	for _, p := range f.packets {
		if p.Opcode == op {
			n++
		}
	}
	return n
}
func lastCast(f *fakeSession) uint32 {
	for i := len(f.packets) - 1; i >= 0; i-- {
		if f.packets[i].Opcode == opcode.CMSGCastSpell {
			return binary.LittleEndian.Uint32(f.packets[i].Payload[1:])
		}
	}
	return 0
}

func TestInitialSpellbookAcceptsOnlyWholeServerFilteredCooldowns(t *testing.T) {
	for _, tc := range []struct {
		name              string
		declaredCooldowns uint16
		cooldownBytes     int
		wantSpellbook     bool
	}{
		{"one filtered", 2, 16, true},
		{"all filtered", 2, 0, true},
		{"partial record", 2, 15, false},
		{"extra record", 1, 32, false},
		{"over count bound", 4097, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := rig(t)
			p := buffInitial(autoAttack, fireball)
			binary.LittleEndian.PutUint16(p[len(p)-2:], tc.declaredCooldowns)
			p = append(p, make([]byte, tc.cooldownBytes)...)
			if tc.cooldownBytes >= 16 {
				binary.LittleEndian.PutUint32(p[len(p)-tc.cooldownBytes:], fireball)
			}
			known, err := initialSpellKnown(p, fireball)
			if known != tc.wantSpellbook || (err == nil) != tc.wantSpellbook {
				t.Fatalf("direct decoder known=%t error=%v", known, err)
			}
			_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: p})
			if s.spellbookKnown != tc.wantSpellbook || s.state.KnownAutoAttack != tc.wantSpellbook || s.state.KnownFireball != tc.wantSpellbook {
				t.Fatalf("spellbook=%t auto attack=%t fireball=%t", s.spellbookKnown, s.state.KnownAutoAttack, s.state.KnownFireball)
			}
			if s.spellCooldownBaselineKnown != tc.wantSpellbook {
				t.Fatalf("cooldown baseline known=%t", s.spellCooldownBaselineKnown)
			}
		})
	}
}

func TestInitialFilteredCooldownsKeepSerializedDurations(t *testing.T) {
	s, _, _ := rig(t)
	p := buffInitial(autoAttack, fireball)
	binary.LittleEndian.PutUint16(p[len(p)-2:], 2) // One cooldown was filtered by the server.
	p = binary.LittleEndian.AppendUint32(p, fireball)
	p = binary.LittleEndian.AppendUint16(p, 0) // Cast item.
	p = binary.LittleEndian.AppendUint16(p, 7) // Category.
	p = binary.LittleEndian.AppendUint32(p, 1500)
	p = binary.LittleEndian.AppendUint32(p, 3000)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: p})
	end := s.now().Add(3 * time.Second)
	if !s.spellbookKnown || !s.spellCooldownBaselineKnown || !s.spellReady[fireball].Equal(end) || !s.categoryReady[7].Equal(end) {
		t.Fatalf("spellbook=%t baseline=%t spell end=%v category end=%v", s.spellbookKnown, s.spellCooldownBaselineKnown, s.spellReady[fireball], s.categoryReady[7])
	}
}

func TestMageUsesActualClassKnownFireballAndBaseManaWithoutHealReserve(t *testing.T) {
	s, f, _ := assistRig(t)
	f.world.Self.ClassId = 8
	f.world.Self.BaseManaKnown = true
	f.world.Self.BaseMana = 100
	f.world.Self.Power = 8
	f.world.Owner.Health = 40
	learnSpells(s, fireball, autoAttack)
	if _, err := castSelectedForTest(s, fireball, enemyGUID); err != nil {
		t.Fatal(err)
	}
	if lastCast(f) != 133 || state(t, s).CombatStyle != "mage_fireball" || state(t, s).HealAttempts != 0 || !state(t, s).KnownFireball {
		t.Fatal(state(t, s), f.packets)
	}
	if state(t, s).ServerAssistGo != 0 || state(t, s).FireballDamageEvents != 0 {
		t.Fatal("transport claimed damage")
	}
}
func TestActuallyLearnedHealDoesNotRequirePriestClassWhitelist(t *testing.T) {
	s, f, _ := assistRig(t)
	f.world.Self.ClassId = 8
	learnSpells(s, fireball, lesserHeal)
	f.world.Owner.Health = 40
	r, err := testExactHeal(s, 2050, ownerGUID)
	if err != nil || r.HealAttempts != 1 || lastCast(f) != lesserHeal {
		t.Fatal(r, err)
	}
}

// Full ItemHandler.cpp response, including fixed trailing fields. The item
// category proof is independent of the descriptive name.
func weaponQueryFixture(entry, subclass, inventory uint32) []byte {
	p := binary.LittleEndian.AppendUint32(nil, entry)
	p = binary.LittleEndian.AppendUint32(p, 2)
	p = binary.LittleEndian.AppendUint32(p, subclass)
	p = append(p, 0, 0, 0, 0)
	p = append(p, []byte("Dagger\x00\x00\x00\x00")...)
	fields := make([]byte, 88)
	binary.LittleEndian.PutUint32(fields[24:], inventory)
	p = append(p, fields...)
	p = append(p, make([]byte, 8+24+28+12+120+4)...)
	p = append(p, 0)
	p = append(p, make([]byte, 116)...)
	return p
}
func rogueRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	s, f, advance := assistRig(t)
	self := f.world.Self
	self.ClassId = 4
	self.PowerType = 3
	self.Power = 100
	self.MaxPower = 100
	self.BaseAttackTimeKnown = true
	self.BaseAttackTimeMs = 2000
	self.CombatReachKnown = true
	self.CombatReach = 1.5
	self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: 0x4000000000000042, MetadataKnown: true, Entry: 2092, DurabilityKnown: true, Durability: 10, MaxDurability: 10}
	f.world.OwnerTarget.X = 3
	f.world.OwnerTarget.CombatReachKnown = true
	f.world.OwnerTarget.CombatReach = 1.5
	learnSpells(s, autoAttack, sinisterStrike)
	return s, f, advance
}
func verifyWeapon(t *testing.T, s *Service, f *fakeSession) {
	t.Helper()
	s.requestWeaponLocked(context.Background(), f.world.Self)
	if countPackets(f, opcode.CMSGItemQuerySingle) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal("weapon query did not precede attack", f.packets)
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemQuerySingleResponse, Payload: weaponQueryFixture(2092, 15, 13)})
}
func TestRogueRequiresOwnedUsableServerVerifiedWeaponAndActualEnergyCost(t *testing.T) {
	s, f, r := instantStrikeFixture(t, 1752)
	learnSpells(s, 1752)
	f.world.Self.Power = 44
	if _, err := s.CastOffense(context.Background(), r); err == nil || len(f.packets) != 0 {
		t.Fatal("insufficient energy admitted", err, f.packets)
	}
	f.world.Self.Power = 45
	out, err := s.CastOffense(context.Background(), r)
	if err != nil || lastCast(f) != 1752 || !out.GetAutoAttacking() || out.GetSinisterStrikeEnergyCost() != 45 || out.GetSinisterStrikeDamageEvents() != 0 {
		t.Fatal(out, err, f.packets)
	}
}
func TestKnownImprovedSinisterStrikeReducesOnlyVerifiedTalentCost(t *testing.T) {
	for _, tc := range []struct{ talent, cost uint32 }{{13732, 42}, {13863, 40}} {
		s, f, r := instantStrikeFixture(t, 1752)
		learnSpells(s, 1752, tc.talent)
		f.world.Self.Power = tc.cost - 1
		if _, err := s.CastOffense(context.Background(), r); err == nil {
			t.Fatal("talent admitted insufficient energy")
		}
		f.world.Self.Power = tc.cost
		out, err := s.CastOffense(context.Background(), r)
		if err != nil || out.GetSinisterStrikeEnergyCost() != tc.cost || lastCast(f) != 1752 {
			t.Fatal(out, err, f.packets)
		}
	}
}

func TestWeaponQueryCompleteFramingAndTruncations(t *testing.T) {
	p := weaponQueryFixture(2092, 15, 13)
	proof, err := decodeWeaponQuery(p)
	if err != nil || !proof.valid || proof.entry != 2092 || proof.subclass != 15 {
		t.Fatal(proof, err)
	}
	for n := 0; n < len(p); n++ {
		if _, err := decodeWeaponQuery(p[:n]); err == nil {
			t.Fatal("accepted truncated weapon query", n)
		}
	}
	if _, err := decodeWeaponQuery(append(bytes.Clone(p), 0)); err == nil {
		t.Fatal("accepted trailing weapon query")
	}
}
