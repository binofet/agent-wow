package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestGenericLearnedWeaponMeleeDefendsEveryKnownClass(t *testing.T) {
	for _, class := range []uint32{1, 2, 3, 6, 7, 8, 11} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, c, _ := selfDefenseRig(t, 4)
			s.selfGUID, f.world.Self.Guid, c.AttackVictimGuid = 87, 87, 87
			f.world.Self.ClassId, f.world.Self.Power = class, 0
			f.world.Self.Mainhand.Entry = 36
			s.weapons[36] = weaponProof{entry: 36, class: 2, subclass: 4, inventory: 13, valid: true}
			learnSpells(s, autoAttack)
			s.state.Assisting = false
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 || !s.state.AutoAttackRequested {
				t.Fatal("known equipped weapon did not defend", s.stateLocked(), f.packets)
			}
			for _, p := range f.packets {
				if p.Opcode == opcode.CMSGAttackswing && (len(p.Payload) != 8 || binary.LittleEndian.Uint64(p.Payload) != c.Guid) {
					t.Fatal("attack was not bound to actual attacker", p)
				}
			}
		})
	}
}

func TestGenericMeleeRetainsLearnedWeaponAndFinalAttackerGuards(t *testing.T) {
	for _, mode := range []string{"unknown_spell", "broken_weapon", "unverified_weapon", "removed_attacker", "removed_spell"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, _ := selfDefenseRig(t, 4)
			f.world.Self.ClassId = 2
			learnSpells(s, autoAttack)
			switch mode {
			case "unknown_spell":
				learnSpells(s)
			case "broken_weapon":
				f.world.Self.Mainhand.MaxDurability, f.world.Self.Mainhand.Durability = 10, 0
			case "unverified_weapon":
				delete(s.weapons, f.world.Self.Mainhand.Entry)
			case "removed_attacker":
				f.onClock = func() { c.AttackVictimKnown = false }
			case "removed_spell":
				f.onClock = func() { s.setKnownSpell(autoAttack, false) }
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("invalid fallback emitted offense", s.stateLocked(), f.packets)
			}
		})
	}
}
