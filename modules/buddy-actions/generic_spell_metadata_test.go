package buddyactions

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestToolkitSeparatesGenericCastFromAutomaticClassification(t *testing.T) {
	s, _, _ := rig(t)
	s.sessionID = "mage-new-spell"
	// Simulate a spell learned after login, including an unfamiliar catalog ID.
	s.applySpellbookLocked(map[uint32]bool{8423: true, 8427: true, 8439: true, 987654: true})
	book, err := s.SpellToolkit(context.Background(), &emptypb.Empty{})
	if err != nil || len(book.GetSpells()) != 4 {
		t.Fatal(book, err)
	}
	for _, spell := range book.Spells {
		if !spell.GenericCastSupported || spell.BlockedReason != "" {
			t.Fatalf("learned active ID %d still claims a cast handler is required: %+v", spell.SpellId, spell)
		}
		if spell.MetadataKnown && (spell.Mechanics == nil || len(spell.Mechanics.Effects) == 0) {
			t.Fatal("script cannot inspect targeting/effect facts", spell)
		}
	}
}

func TestReflectedSpellRequirementsAndEffectsAreIndependentFacts(t *testing.T) {
	spell := spellcatalog.Spell{ID: 987654, Stances: 4, StancesNot: 8, TargetMask: 0x40,
		Focus: 17, Facing: 2, CostPerLevel: 3, PerSecond: 4, CastMinMs: 5, CastPerLevel: -6,
		TimingKnown: true, RangeKnown: true, MinHostile: 2, MaxHostile: 30, MinFriendly: 1,
		MaxFriendly: 40, CooldownMs: 7, Category: 8, CategoryCooldownMs: 9, GcdMs: 10,
		DurationIndex: 11, InventoryMask: 12, RuneCost: 13, Reagents: []spellcatalog.Reagent{{ID: 14, Count: 2}},
		Totems: []uint32{15}, Effects: []spellcatalog.Effect{{Kind: 12345, A: 15, B: 22, Aura: 23,
			Trigger: 42, PeriodMs: 1000, Chain: 3, Radius: 8, ItemType: 99, Points: -1, Dice: 2, Misc: -3, PerLevel: 1.5}}}
	spell.Attrs[1], spell.Conditions[2], spell.FamilyFlags[1] = 0x40, 16, 17
	got := reflectedSpellMechanics(spell)
	if got.TargetMask != 0x40 || got.Attributes[1] != 0x40 || got.Conditions[2] != 16 ||
		got.FamilyFlags[1] != 17 || got.Reagents[0].ItemId != 14 || got.Reagents[0].Count != 2 ||
		got.Totems[0] != 15 || got.RequiredInventoryMask != 12 || got.RuneCost != 13 ||
		got.Effects[0].TargetSelectorA != 15 || got.Effects[0].TargetSelectorB != 22 ||
		got.Effects[0].Points != -1 || got.Effects[0].PointsPerLevel != 1.5 || got.MaxHostileYards != 30 {
		t.Fatal("raw requirements/effects were classified away", got)
	}
	got.Attributes[1], got.Totems[0] = 0, 0
	if spell.Attrs[1] != 0x40 || spell.Totems[0] != 15 {
		t.Fatal("reflected facts alias catalog storage")
	}
}
