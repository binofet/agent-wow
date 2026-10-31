package buddyprofessions

import (
	"context"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPartySkinningUsesPositiveServerReadinessWithoutOwnNormalLoot(t *testing.T) {
	for _, mode := range []string{"joel_looted", "old_optional_own_receipt", "masked_lootable_without_skinnable", "other_party", "unknown_unit_flags", "unknown_dynamic_flags", "normal_loot_remains", "alive", "nonzero_health", "absent"} {
		t.Run(mode, func(t *testing.T) {
			s, f := skinningFixture(t)
			s.mu.Lock()
			c := s.view.raw.RequestedCreature
			// Keep the exact lookup and collection coherent, as they are for
			// an actual single cached creature in a server snapshot.
			s.view.raw.SkinnableCorpses[0] = c
			if mode == "joel_looted" {
				delete(s.fullLoot, c.Guid)
			} else if mode == "old_optional_own_receipt" {
				s.fullLoot[c.Guid] = s.now().Add(-61 * time.Second)
			}
			// Every negative case retains the own receipt. That diagnostic must
			// never override changed/unknown current flags or another party's tap.
			switch mode {
			case "masked_lootable_without_skinnable":
				c.UnitFlags = 0
			case "other_party":
				c.DynamicFlags = 4
			case "unknown_unit_flags":
				c.UnitFlagsKnown = false
			case "unknown_dynamic_flags":
				c.DynamicFlagsKnown = false
			case "normal_loot_remains":
				c.DynamicFlags |= 1
			case "alive":
				c.Alive = true
			case "nonzero_health":
				c.Health = 1
			case "absent":
				c.Present = false
			}
			ready := mode == "joel_looted" || mode == "old_optional_own_receipt"
			target, reason := s.skinningTargetLocked(c.Guid, c.PositionRevision, nil)
			rows := s.skinningTargetsLocked()
			s.mu.Unlock()
			if (target != nil && reason == "") != ready || len(rows) != 1 || rows[0].FullyLootedObserved != ready || rows[0].Ready != ready || rows[0].Approachable != ready {
				t.Fatal("current corpse proof and projection disagree", target, reason, rows)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 0 {
				t.Fatal("readiness inspection wrote gameplay packets")
			}
		})
	}
}

func TestPartySkinningFinalCastUsesFreshFlagsRatherThanOwnLootReceipt(t *testing.T) {
	s, f := skinningFixture(t)
	s.mu.Lock()
	delete(s.fullLoot, skinProtocolTarget)
	s.mu.Unlock()
	op := skinningDirect(t, s, f)
	attempted, err := s.skinWrite(context.Background(), op, opcode.CMSGCastSpell, skinCastPacket(9, skinProtocolTarget))
	if !attempted || err != nil {
		t.Fatal("party-cleared corpse blocked without own normal-loot receipt", attempted, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell {
		t.Fatal("unexpected party skinning write", f.packets)
	}
	// The normal cast write is only an attempt, never material confirmation.
	if op.receipt.OwnGoObserved || op.receipt.OutputInventoryObserved || op.receipt.LootReleaseObserved {
		t.Fatal("transport became successful skinning", op.receipt)
	}
}
