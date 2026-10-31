package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
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

func TestMageUsesActualClassKnownFireballAndBaseManaWithoutHealReserve(t *testing.T) {
	s, f, _ := assistRig(t)
	f.world.Self.ClassId = 8
	f.world.Self.BaseManaKnown = true
	f.world.Self.BaseMana = 100
	f.world.Self.Power = 8
	f.world.Owner.Health = 40
	learnSpells(s, fireball, autoAttack)
	s.Tick(context.Background())
	if lastCast(f) != 133 || state(t, s).CombatStyle != "mage_fireball" || state(t, s).HealAttempts != 0 || !state(t, s).KnownFireball {
		t.Fatal(state(t, s), f.packets)
	}
	if state(t, s).ServerAssistGo != 0 || state(t, s).FireballDamageEvents != 0 {
		t.Fatal("transport claimed damage")
	}
}
func TestClassUnknownOrAbsentLearnedDamageCannotCast(t *testing.T) {
	for _, change := range []func(*Service, *fakeSession){
		func(s *Service, f *fakeSession) { f.world.Self.ClassKnown = false },
		func(s *Service, f *fakeSession) { f.world.Self.ClassId = 8; learnSpells(s, lesserHeal) },
		func(s *Service, f *fakeSession) { f.world.Self.ClassId = 5; learnSpells(s, lesserHeal) },
	} {
		s, f, _ := assistRig(t)
		change(s, f)
		s.Tick(context.Background())
		if countPackets(f, opcode.CMSGCastSpell) != 0 {
			t.Fatal(state(t, s), f.packets)
		}
	}
}
func TestActuallyLearnedHealDoesNotRequirePriestClassWhitelist(t *testing.T) {
	s, f, _ := assistRig(t)
	f.world.Self.ClassId = 8
	learnSpells(s, fireball, lesserHeal)
	f.world.Owner.Health = 40
	r, err := s.HealOnce(context.Background(), &emptypb.Empty{})
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
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGItemQuerySingle) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal("weapon query did not precede attack", f.packets)
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemQuerySingleResponse, Payload: weaponQueryFixture(2092, 15, 13)})
}
func TestRogueRequiresOwnedUsableServerVerifiedWeaponAndActualEnergyCost(t *testing.T) {
	s, f, _ := rogueRig(t)
	verifyWeapon(t, s, f)
	f.world.Self.Power = 44
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackswing) != 1 || lastCast(f) != 0 || state(t, s).AutoAttacking || !state(t, s).AutoAttackRequested || state(t, s).SinisterStrikeEnergyCost != 45 {
		t.Fatal(state(t, s), f.packets)
	}
	attack := binary.LittleEndian.AppendUint64(nil, s.selfGUID)
	attack = binary.LittleEndian.AppendUint64(attack, f.world.OwnerTarget.Guid)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackstart, Payload: attack})
	if !state(t, s).AutoAttacking {
		t.Fatal("server attack start not observed")
	}
	f.world.Self.Power = 45
	s.Tick(context.Background())
	if lastCast(f) != 1752 {
		t.Fatal(state(t, s), f.packets)
	}
	if state(t, s).MeleeDamageEvents != 0 || state(t, s).SinisterStrikeDamageEvents != 0 {
		t.Fatal("attack transport claimed damage")
	}
}
func TestKnownImprovedSinisterStrikeReducesOnlyVerifiedTalentCost(t *testing.T) {
	for _, tc := range []struct{ talent, cost uint32 }{{13732, 42}, {13863, 40}} {
		s, f, _ := rogueRig(t)
		learnSpells(s, autoAttack, sinisterStrike, tc.talent)
		verifyWeapon(t, s, f)
		f.world.Self.Power = tc.cost
		s.Tick(context.Background())
		if state(t, s).SinisterStrikeEnergyCost != tc.cost || lastCast(f) != 1752 {
			t.Fatal(state(t, s), f.packets)
		}
	}
}
func TestRogueNeverAttacksWithoutRangeWeaponOrActualHostilityProof(t *testing.T) {
	for _, change := range []func(*Service, *fakeSession){
		func(s *Service, f *fakeSession) { f.world.OwnerTarget.AttackVictimKnown = false },
		func(s *Service, f *fakeSession) { f.world.Self.Mainhand.Durability = 0 },
		func(s *Service, f *fakeSession) { f.world.Self.Mainhand.MetadataKnown = false },
		func(s *Service, f *fakeSession) { f.world.Self.BaseAttackTimeKnown = false },
		func(s *Service, f *fakeSession) { f.world.OwnerTarget.X = 13 },
		func(s *Service, f *fakeSession) {
			s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 20, inventory: 17, valid: true}
		},
	} {
		s, f, _ := rogueRig(t)
		s.weaponQueries[2092] = true
		s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 15, inventory: 13, valid: true}
		change(s, f)
		s.Tick(context.Background())
		if countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
			t.Fatal(state(t, s), f.packets)
		}
	}
}
func TestRogueApproachesOnlyAlreadyEngagedNearbyFlatGroundTarget(t *testing.T) {
	s, f, advance := rogueRig(t)
	verifyWeapon(t, s, f)
	f.world.OwnerTarget.X = 10
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal(state(t, s), f.packets)
	}
	_, n, _ := readGUID(f.packets[len(f.packets)-1].Payload)
	if len(f.packets[len(f.packets)-1].Payload[n:]) != 30 {
		t.Fatal("not normal movement")
	}
}
func TestAutoAttackStopsOnOwnerTargetChangeAndBeforeInteraction(t *testing.T) {
	for _, mode := range []string{"target", "disable", "interaction"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rogueRig(t)
			verifyWeapon(t, s, f)
			f.world.Self.Power = 0
			s.Tick(context.Background())
			switch mode {
			case "target":
				f.world.Owner.TargetGuid = 0
				s.Tick(context.Background())
			case "disable":
				s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			case "interaction":
				if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot"}); err == nil {
					t.Fatal("lease granted before attack stop")
				}
			}
			if countPackets(f, opcode.CMSGAttackstop) != 1 || !state(t, s).AutoAttackUnconfirmed {
				t.Fatal(state(t, s), f.packets)
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGAttackstop) != 1 {
				t.Fatal("ambiguous stop retried")
			}
			p := append(packedGUID(s.selfGUID), packedGUID(f.world.OwnerTarget.Guid)...)
			p = append(p, 0, 0, 0, 0)
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackstop, Payload: p})
			if state(t, s).AutoAttackUnconfirmed || state(t, s).AutoAttacking {
				t.Fatal(state(t, s))
			}
		})
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

func TestAssistOnlyRogueStopsOwnedApproachOnDisableExpiryAndLostProof(t *testing.T) {
	for _, mode := range []string{"disable", "expiry", "target", "victim", "immune", "weapon", "class", "known_attack", "pacified", "reach", "attack_time"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rogueRig(t)
			verifyWeapon(t, s, f)
			f.world.OwnerTarget.X = 10
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if !state(t, s).Moving || state(t, s).Following {
				t.Fatal("fixture did not start assist-only approach", state(t, s))
			}
			switch mode {
			case "disable":
				s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			case "expiry":
				advance(lease + time.Second)
			case "target":
				f.world.Owner.TargetGuid = 0
			case "victim":
				f.world.OwnerTarget.AttackVictimKnown = false
			case "immune":
				f.world.OwnerTarget.UnitFlags = 2
			case "weapon":
				f.world.Self.Mainhand.MetadataKnown = false
			case "class":
				f.world.Self.ClassKnown = false
			case "known_attack":
				s.state.KnownAutoAttack = false
			case "pacified":
				f.world.Self.UnitFlags |= 0x20000
			case "reach":
				f.world.Self.CombatReachKnown = false
			case "attack_time":
				f.world.Self.BaseAttackTimeKnown = false
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if state(t, s).Moving || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 1 {
				t.Fatal("owned approach did not stop exactly once", state(t, s), f.packets)
			}
		})
	}
}

func TestAutoAttackStopRequestedByFailedAssistGateBlocksSameTickFollow(t *testing.T) {
	s, f, _ := rogueRig(t)
	verifyWeapon(t, s, f)
	f.world.Self.Power = 0
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal("fixture did not request ordinary attack")
	}
	toggle(t, s, true)
	// Still the same living target/victim, but the target is now unattackable.
	// This fails inside assistLocked after the early autoTarget identity gate.
	f.world.OwnerTarget.UnitFlags = 2
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackstop) != 1 || !state(t, s).AutoAttackUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != 0 || !state(t, s).Following {
		t.Fatal("follow moved before server attack-stop confirmation", state(t, s), f.packets)
	}
}
