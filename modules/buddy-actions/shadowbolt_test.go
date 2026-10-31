package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestShadowBoltNormalCastPacketGolden(t *testing.T) {
	golden, err := hex.DecodeString("01ae0200000002000000c9013330f1")
	if err != nil || !bytes.Equal(spellPacket(1, shadowBolt, enemyGUID), golden) {
		t.Fatal(err, spellPacket(1, shadowBolt, enemyGUID), golden)
	}
}

func TestWarlockShadowBoltUsesVerifiedRankManaAndActualClass(t *testing.T) {
	s, f, _ := assistRig(t)
	f.world.Self.ClassId, f.world.Self.BaseManaKnown, f.world.Self.BaseMana, f.world.Self.Power = 9, true, 100, 10
	f.world.Owner.Health = 1 // Warlock does not reserve or promise priest healing.
	learnSpells(s, shadowBolt)
	s.Tick(context.Background())
	got := state(t, s)
	if lastCast(f) != 686 || !got.KnownShadowBolt || got.ClassName != "warlock" || got.CombatStyle != "warlock_shadow_bolt" || got.CombatSpellId != 686 || got.HealAttempts != 0 || got.ShadowBoltDamageEvents != 0 {
		t.Fatal(got, f.packets)
	}
	rank := s.selectedSpellLocked(shadowBolt)
	if rank.id != 686 || rank.rank != 1 || rank.manaPercent != 10 || rank.castTime != 1700*time.Millisecond {
		t.Fatal(rank)
	}
}

func TestShadowBoltRejectsUnknownHigherRankWrongClassManaAndRange(t *testing.T) {
	for _, mode := range []string{"unknown", "higher_rank", "class_unknown", "mana", "range", "silenced"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := assistRig(t)
			f.world.Self.ClassId, f.world.Self.BaseManaKnown, f.world.Self.BaseMana, f.world.Self.Power = 9, true, 100, 10
			if mode != "unknown" && mode != "higher_rank" {
				learnSpells(s, shadowBolt)
			}
			if mode == "unknown" {
				learnSpells(s)
			}
			switch mode {
			case "higher_rank":
				learnSpells(s, 695)
			case "wrong_class":
				f.world.Self.ClassId = 8
			case "class_unknown":
				f.world.Self.ClassKnown = false
			case "mana":
				f.world.Self.Power = 9
			case "range":
				f.world.OwnerTarget.X = f.world.Self.X + 28.01
			case "silenced":
				f.world.Self.UnitFlags |= 0x2000
			}
			s.Tick(context.Background())
			if lastCast(f) != 0 {
				t.Fatal(mode, state(t, s), f.packets)
			}
		})
	}
}

func TestShadowBoltKnowledgeRemovalAndDamageAreActualOwnPositiveEvidence(t *testing.T) {
	s, f, _ := assistRig(t)
	f.world.Self.ClassId = 9
	learnSpells(s, shadowBolt)
	removed := binary.LittleEndian.AppendUint32(nil, shadowBolt)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGRemovedSpell, Payload: removed})
	if state(t, s).KnownShadowBolt || s.selectedSpellLocked(shadowBolt).id != 0 {
		t.Fatal(state(t, s))
	}
	learned := append(binary.LittleEndian.AppendUint32(nil, shadowBolt), 0, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: learned})
	if !state(t, s).KnownShadowBolt {
		t.Fatal(state(t, s))
	}
	for _, mode := range []string{"own", "foreign", "zero", "truncated"} {
		packet := damageGolden(t)
		binary.LittleEndian.PutUint32(packet[7:], shadowBolt)
		packet[19] = 0x20 // Actual Shadow school mask; remaining frame is ordinary.
		switch mode {
		case "foreign":
			packet[6] = 3
		case "zero":
			binary.LittleEndian.PutUint32(packet[11:], 0)
		case "truncated":
			packet = packet[:len(packet)-1]
		}
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: packet})
	}
	got := state(t, s)
	if got.ShadowBoltDamageEvents != 1 || got.ShadowBoltDamageTotal != 9 || got.AssistDamageEvents != 1 || got.AssistDamageTotal != 9 || got.FireballDamageEvents != 0 || got.SmiteDamageEvents != 0 || got.MalformedPackets != 1 {
		t.Fatal(got)
	}
}
