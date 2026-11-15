package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func cleanseRig(t *testing.T, classID uint32, spells ...uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
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

func TestAutoCleanseSelectsMatchingLearnedSpellFromExactHarmfulAura(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		class, debuff, spell uint32
	}{
		{"Mira removes curse", 8, 980, 475},
		{"Arlen removes disease", 5, 3150, 528},
		{"Arlen dispels magic", 5, 172, 527},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			learned := []uint32{tt.spell}
			if tt.class == 5 {
				learned = []uint32{527, 528}
			}
			s, f, advance := cleanseRig(t, tt.class, learned...)
			f.world.Owner.PlayerFlags = 2 // AFK party members still receive support.
			send := func(code uint32, data []byte) {
				t.Helper()
				s.OnPacket(ctx, &modv1.WorldPacket{Opcode: code, Payload: data})
			}
			// A single-slot update can prove the positive presence of this
			// harmful aura even before a full party aura snapshot arrives.
			send(opcode.SMSGAuraUpdate, cleanseAuraPacket(ownerGUID, tt.debuff, true))
			s.Tick(ctx)
			if lastCast(f) != tt.spell || healingCastTarget(f) != ownerGUID ||
				s.supportOp == nil || s.supportOp.view.EffectObserved {
				t.Fatal("missing exact learned cleanse", state(t, s))
			}
			goPacket := smiteEvent(true, s.castCount, ownerGUID)
			_, _, _, offset, _ := castEvent(goPacket)
			binary.LittleEndian.PutUint32(goPacket[offset-4:], tt.spell)
			send(opcode.SMSGSpellGo, goPacket)
			send(opcode.SMSGSpelldispellog, dispelLog(ownerGUID, 2, tt.spell, tt.debuff))
			if !s.supportOp.view.ServerCastGo || s.supportOp.view.EffectObserved {
				t.Fatal("unrelated log or cast completion credited removal", s.supportOp.view.String())
			}
			send(opcode.SMSGSpelldispellog, dispelLog(ownerGUID, s.selfGUID, tt.spell, tt.debuff))
			if !s.supportOp.view.EffectObserved || s.supportOp.view.Phase != "confirmed_effect" {
				t.Fatal("missing correlated dispel receipt", s.supportOp.view.String())
			}
			send(opcode.SMSGAuraUpdate, buffAura(ownerGUID, 0, 1, 0, 0))
			advance(2 * time.Second)
			s.Tick(ctx)
			if countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("removed aura was targeted again", state(t, s))
			}
		})
	}
}

func TestAutoCleanseSkipsNonmembersUnlearnedOrIneligibleAurasAndYieldsToHeal(t *testing.T) {
	for _, mode := range []string{"nonmember", "unlearned", "positive", "wrong type", "expired", "out of range", "urgent heal"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, f, advance := cleanseRig(t, 5, 528, lesserHeal)
			debuff, harmful := uint32(3150), true
			var spell uint32
			switch mode {
			case "nonmember":
				f.party = nil
			case "unlearned":
				s.setKnownSpell(528, false)
			case "positive":
				harmful = false
			case "wrong type":
				debuff = 980
			case "out of range":
				f.world.Owner.X = 100
			case "urgent heal":
				f.world.Self.Health = 25
				if _, err := s.Healing(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
					t.Fatal(err)
				}
				spell = lesserHeal
			}
			s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: cleanseAuraPacket(ownerGUID, debuff, harmful)})
			if mode == "expired" {
				advance(16 * time.Second)
			}
			s.Tick(ctx)
			if lastCast(f) != spell {
				t.Fatal("incorrect cast selection", state(t, s), f.packets)
			}
		})
	}
}
