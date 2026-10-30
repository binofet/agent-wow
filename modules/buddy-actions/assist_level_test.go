package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestAssistKnownEnemyMoreThanThreeLevelsAboveSelfDoesNotStartOffense(t *testing.T) {
	for _, class := range []uint32{4, 5, 8, 9} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, _ := assistRig(t)
			f.world.Self.ClassId = class
			f.world.Self.LevelKnown, f.world.Self.Level = true, 5
			f.world.Owner.LevelKnown, f.world.Owner.Level = true, 80
			f.world.OwnerTarget.LevelKnown, f.world.OwnerTarget.Level = true, 11
			learnSpells(s, smite, fireball, shadowBolt, sinisterStrike, autoAttack, lesserHeal)
			s.Tick(context.Background())
			got := state(t, s)
			if !got.Assisting || got.AssistReason != "assist_target_level_exceeds_self_plus_three" || got.AssistAttempts != 0 || got.Moving || len(f.packets) != 0 {
				t.Fatal(got, f.packets)
			}
		})
	}
}

func TestAssistKnownLevelBoundaryAndUnknownLevelBehavior(t *testing.T) {
	for _, mode := range []string{"three_above", "own_unknown", "target_unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := assistRig(t)
			f.world.Self.LevelKnown, f.world.Self.Level = true, 5
			f.world.OwnerTarget.LevelKnown, f.world.OwnerTarget.Level = true, 8
			if mode == "own_unknown" {
				f.world.Self.LevelKnown = false
				f.world.OwnerTarget.Level = 11
			} else if mode == "target_unknown" {
				f.world.OwnerTarget.LevelKnown, f.world.OwnerTarget.Level = false, 11
			}
			s.Tick(context.Background())
			if got := state(t, s); got.AssistAttempts != 1 || lastCast(f) != smite || !got.Assisting {
				t.Fatal(got, f.packets)
			}
		})
	}
}

func TestAssistLevelLimitPreservesHealingPriorityAndIntent(t *testing.T) {
	s, f, advance := assistRig(t)
	f.world.Self.LevelKnown, f.world.Self.Level = true, 5
	f.world.OwnerTarget.LevelKnown, f.world.OwnerTarget.Level = true, 11
	f.world.Owner.Health = 50
	if _, err := s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if got := state(t, s); got.HealAttempts != 1 || got.AssistAttempts != 0 || !got.Healing || lastCast(f) != lesserHeal {
		t.Fatal(got, f.packets)
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: serverSpellPacket(true, 1)})
	f.world.Owner.Health = 100
	advance(4 * time.Second)
	s.Tick(context.Background())
	if got := state(t, s); !got.Healing || !got.Assisting || got.AssistAttempts != 0 || got.AssistReason != "assist_target_level_exceeds_self_plus_three" || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal(got, f.packets)
	}
}

func TestAssistLevelLimitStopsExistingRogueAutoAttack(t *testing.T) {
	s, f, _ := rogueRig(t)
	verifyWeapon(t, s, f)
	f.world.Self.LevelKnown, f.world.Self.Level = true, 5
	f.world.OwnerTarget.LevelKnown, f.world.OwnerTarget.Level = true, 8
	f.world.Self.Power = 0
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal("fixture did not start normal attack", state(t, s), f.packets)
	}
	f.world.OwnerTarget.Level = 11
	s.Tick(context.Background())
	if got := state(t, s); !got.Assisting || got.AssistReason != "assist_target_level_exceeds_self_plus_three" || !got.AutoAttackUnconfirmed || countPackets(f, opcode.CMSGAttackstop) != 1 || countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal(got, f.packets)
	}
	ack := append(packedGUID(s.selfGUID), packedGUID(enemyGUID)...)
	ack = binary.LittleEndian.AppendUint32(ack, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackstop, Payload: ack})
	if got := state(t, s); got.AutoAttackUnconfirmed || got.AutoAttacking || got.AssistDamageEvents != 0 {
		t.Fatal(got)
	}
}

func TestAssistLevelLimitStopsOwnedRogueApproach(t *testing.T) {
	s, f, advance := rogueRig(t)
	verifyWeapon(t, s, f)
	f.world.Self.LevelKnown, f.world.Self.Level = true, 5
	f.world.OwnerTarget.LevelKnown, f.world.OwnerTarget.Level = true, 8
	f.world.OwnerTarget.X = 10
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !state(t, s).Moving {
		t.Fatal("fixture did not start normal approach", state(t, s))
	}
	f.world.OwnerTarget.Level = 11
	s.Tick(context.Background())
	if got := state(t, s); got.Moving || !got.Assisting || got.AssistReason != "assist_target_level_exceeds_self_plus_three" || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal(got, f.packets)
	}
}
