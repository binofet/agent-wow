package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func cleanseRig(t *testing.T, classID uint32, spells ...uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	f.world.Self.ClassId = classID
	f.world.Self.Level, f.world.Self.LevelKnown = 24, true
	f.world.Self.Power, f.world.Self.MaxPower = 200, 200
	f.world.Self.BaseMana, f.world.Self.BaseManaKnown = 100, true
	f.world.Self.ShapeshiftFormKnown = true
	f.world.Owner.Health = f.world.Owner.MaxHealth
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: buffInitial(spells...)})
	return s, f, advance
}

func cleanseAuraPacket(target uint64, spell uint32, harmful bool) []byte {
	aura := buffAura(target, 0xf130000001000001, 1, spell, 15000)
	if harmful {
		aura[len(packedGUID(target))+5] ^= 0x90 // AFLAG_POSITIVE to AFLAG_NEGATIVE.
	}
	return aura
}

func dispelLog(target, caster uint64, cast, aura uint32) []byte {
	data := append(packedGUID(target), packedGUID(caster)...)
	data = binary.LittleEndian.AppendUint32(data, cast)
	data = append(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 1)
	data = binary.LittleEndian.AppendUint32(data, aura)
	return append(data, 0)
}

func exactCleanseRequest(s *Service, target uint64, spell uint32) *actionsv1.CastCleanseRequest {
	aura := s.buffAuras[target].slots[1]
	return &actionsv1.CastCleanseRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: target,
		SpellId: spell, AuraSlot: 1, AuraSpellId: aura.spell, AuraRevision: aura.revision}
}

func TestExactCleanseExecutesChosenRankAndRequiresObservedRemoval(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		class, debuff, spell uint32
	}{{"curse", 8, 980, 475}, {"disease", 5, 3150, 528}, {"magic", 5, 172, 527}} {
		t.Run(tt.name, func(t *testing.T) {
			s, f, advance := cleanseRig(t, tt.class, tt.spell)
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(ownerGUID, tt.debuff, true)})
			request := exactCleanseRequest(s, ownerGUID, tt.spell)
			if _, err := s.CastCleanse(context.Background(), request); err != nil || lastCast(f) != tt.spell || healingCastTarget(f) != ownerGUID {
				t.Fatal("exact cleanse did not execute caller's choice", err, s.stateLocked())
			}
			goPacket := smiteEvent(true, s.castCount, ownerGUID)
			_, _, _, offset, _ := castEvent(goPacket)
			binary.LittleEndian.PutUint32(goPacket[offset-4:], tt.spell)
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: goPacket})
			if s.supportOp.view.EffectObserved || s.supportOp.view.Phase != "waiting_cleanse_effect" {
				t.Fatal("GO invented a dispel effect", s.supportOp.view.String())
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpelldispellog, Payload: dispelLog(ownerGUID, 2, tt.spell, tt.debuff)})
			if s.supportOp.cleanseLogObserved {
				t.Fatal("unrelated caster credited removal")
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpelldispellog, Payload: dispelLog(ownerGUID, s.selfGUID, tt.spell, tt.debuff)})
			if s.supportOp.view.EffectObserved {
				t.Fatal("log without requested slot removal claimed exact effect")
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(ownerGUID, 0, 1, 0, 0)})
			if !s.supportOp.view.ServerCastGo || !s.supportOp.view.EffectObserved || s.supportOp.view.Phase != "confirmed_effect" {
				t.Fatal("missing correlated actual removal", s.supportOp.view.String())
			}
			advance(2 * time.Second)
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("native Tick chose another cleanse after exact execution")
			}
		})
	}
}

func TestExactCleanseRejectsStaleAuraAndInvalidRequestWithoutWrite(t *testing.T) {
	for _, mode := range []string{"foreign session", "foreign body", "changed slot", "changed revision", "unlearned", "positive", "wrong type", "expired", "out of range", "unknown friendly", "silenced", "channel", "resources"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := cleanseRig(t, 5, 528)
			aura, harmful := uint32(3150), true
			if mode == "positive" {
				harmful = false
			}
			if mode == "wrong type" {
				aura = 980
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(ownerGUID, aura, harmful)})
			request := exactCleanseRequest(s, ownerGUID, 528)
			switch mode {
			case "foreign session":
				request.SessionId = "other"
			case "foreign body":
				request.SelfGuid++
			case "changed slot":
				request.AuraSlot = 2
			case "changed revision":
				request.AuraRevision++
			case "unlearned":
				s.setKnownSpell(528, false)
			case "expired":
				advance(16 * time.Second)
			case "out of range":
				f.world.Owner.X = 100
			case "unknown friendly":
				f.party = nil
				f.world.Owner.RaceKnown = false
			case "silenced":
				f.world.Self.UnitFlags |= 0x2000
			case "channel":
				f.world.Self.ChannelSpellId = 12051
			case "resources":
				f.world.Self.Power = 0
			}
			if _, err := s.CastCleanse(context.Background(), request); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("invalid exact cleanse wrote a game packet", mode, err, f.packets)
			}
		})
	}
}

func TestExactCleanseKeepsInstantMovementAndAutoswingAndHonorsSelectedFriendly(t *testing.T) {
	s, f, _ := cleanseRig(t, 5, 528)
	f.world.Self.RaceKnown, f.world.Self.RaceId = true, 1
	other := proto.Clone(f.world.Owner).(*worldv1.PlayerState)
	other.Guid, other.RaceKnown, other.RaceId = 9, true, 1
	f.world.NearbyPlayers = append(f.world.NearbyPlayers, other)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(ownerGUID, 3150, true)})
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(9, 3150, true)})
	s.state.Moving, s.state.AutoAttacking, s.state.AutoAttackRequested = true, true, true
	s.autoTarget = 0xf130000000000001
	f.world.Self.MovementFlags = 1
	if _, err := s.CastCleanse(context.Background(), exactCleanseRequest(s, 9, 528)); err != nil || healingCastTarget(f) != 9 {
		t.Fatal("native harness replaced the selected friendly or rejected a legal instant", err)
	}
	if !s.state.AutoAttacking || !s.state.Moving || countPackets(f, opcode.CMSGAttackstop) != 0 || countPackets(f, opcode.MSGMoveStop) != 0 {
		t.Fatal("instant cleanse authored a movement/swing policy", s.stateLocked(), f.packets)
	}
}

func TestNativeTickAndEmergencyNeverChooseCleanse(t *testing.T) {
	s, f, _ := cleanseRig(t, 5, 528)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(ownerGUID, 3150, true)})
	s.Tick(context.Background())
	enemy := encounterTestCreature(0xf130000000000001, 0)
	enemy.AttackVictimKnown, enemy.AttackVictimGuid, enemy.TargetKnown, enemy.TargetGuid = true, s.selfGUID, true, s.selfGUID
	enemy.UnitFlags = 0x80000
	f.world.NearbyCreatures = []*worldv1.CreatureState{enemy}
	f.world.Self.UnitFlags |= 0x80000
	s.emergencyDefenseLocked(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 0 || s.supportOp != nil {
		t.Fatal("Go retained autonomous cleanse spell/patient selection", s.stateLocked(), f.packets)
	}
}

func TestExactCleanseConfirmsActualCompatibleRemovalWhenServerChoosesAnotherAura(t *testing.T) {
	s, f, _ := cleanseRig(t, 5, 527)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(ownerGUID, 172, true)})
	other := buffAura(ownerGUID, 0xf130000001000001, 2, 589, 15000)
	other[len(packedGUID(ownerGUID))+5] ^= 0x90
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: other})
	request := exactCleanseRequest(s, ownerGUID, 527)
	if _, err := s.CastCleanse(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(s.supportOp.cleanseBaseline) != 2 {
		t.Fatal("fixture did not expose two compatible aura applications")
	}
	goPacket := smiteEvent(true, s.castCount, ownerGUID)
	_, _, _, offset, _ := castEvent(goPacket)
	binary.LittleEndian.PutUint32(goPacket[offset-4:], 527)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: goPacket})
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpelldispellog, Payload: dispelLog(ownerGUID, s.selfGUID, 527, 589)})
	if s.supportOp.view.EffectObserved {
		t.Fatal("log alone claimed actual removal")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(ownerGUID, 0, 2, 0, 0)})
	if !s.supportOp.view.EffectObserved || s.supportOp.view.Phase != "confirmed_effect" || s.buffAuras[ownerGUID].slots[1].spell != 172 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("server-selected compatible effect was lost or requested aura removal was invented", s.supportOp.view.String())
	}
}
