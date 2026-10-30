package buddysupplies

import (
	"math"
	"testing"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func TestWorldRequiresCompleteOwnWireEvidence(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"othercharacter":   func(w *worldv1.SnapshotResponse) { w.Self.Guid = 7 },
		"partialbag":       func(w *worldv1.SnapshotResponse) { w.Backpack = w.Backpack[:15] },
		"duplicatebagslot": func(w *worldv1.SnapshotResponse) { w.Backpack[1].Slot = 0 },
		"unknownmetadata":  func(w *worldv1.SnapshotResponse) { w.Backpack[0].ItemGuid = poleGUID },
		"partialskills":    func(w *worldv1.SnapshotResponse) { w.SelfSkills = w.SelfSkills[:127] },
		"duplicateskill": func(w *worldv1.SnapshotResponse) {
			w.SelfSkills[10] = &worldv1.SkillState{Slot: 10, SkillId: 356, Value: 1, MaxValue: 75}
		},
		"invalidskillrank": func(w *worldv1.SnapshotResponse) { w.SelfSkills[0].Value = 76 },
		"unknownwallet":    func(w *worldv1.SnapshotResponse) { w.SelfCopperKnown = false },
		"walletrevision":   func(w *worldv1.SnapshotResponse) { w.SelfCopperRevision = 0 },
		"dead":             func(w *worldv1.SnapshotResponse) { w.Self.Alive = false },
		"combat":           func(w *worldv1.SnapshotResponse) { w.Self.UnitFlags = 0x80000 },
		"nan":              func(w *worldv1.SnapshotResponse) { w.Self.X = float32(math.NaN()) },
		"unknownchannel":   func(w *worldv1.SnapshotResponse) { w.Self.ChannelObjectKnown = false },
		"mainhandinbag": func(w *worldv1.SnapshotResponse) {
			w.Backpack[0] = &worldv1.BackpackSlot{Slot: 0, ItemGuid: weaponGUID, Entry: 35, StackCount: 1, MetadataKnown: true}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, f := newFixture(t)
			change(f.world)
			if _, e := validateWorld(f.world, 6); e == nil {
				t.Fatal("accepted incomplete/inconsistent actual observations")
			}
		})
	}
	_, f := newFixture(t)
	v, e := validateWorld(f.world, 6)
	if e != nil || v.free != 16 || v.copper != 1000 || len(v.skills) != 6 {
		t.Fatal(v, e)
	}
}
func TestPurchaseProofRejectsUnrelatedOrRedistributedInventory(t *testing.T) {
	_, f := newFixture(t)
	f.world.Backpack[0] = &worldv1.BackpackSlot{Slot: 0, ItemGuid: poleGUID, Entry: 2320, StackCount: 3, MetadataKnown: true}
	f.world.Backpack[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: poleGUID + 1, Entry: 2320, StackCount: 2, MetadataKnown: true}
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID + 2, Entry: 2589, StackCount: 1, MetadataKnown: true}
	before, e := validateWorld(f.world, 6)
	if e != nil {
		t.Fatal(e)
	}
	afterRaw := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	afterRaw.BackpackRevision++
	afterRaw.Backpack[0].StackCount++
	after, e := validateWorld(afterRaw, 6)
	if e != nil || !exactPurchaseIncrease(before, after, 2320, 1) {
		t.Fatal("valid exact increase rejected", e)
	}
	for name, change := range map[string]func(*worldv1.SnapshotResponse){"unrelatedmove": func(w *worldv1.SnapshotResponse) { w.Backpack[2].StackCount = 2 }, "netincreasebutshrink": func(w *worldv1.SnapshotResponse) { w.Backpack[0].StackCount = 2; w.Backpack[1].StackCount = 4 }, "targetidentity": func(w *worldv1.SnapshotResponse) { w.Backpack[0].ItemGuid = poleGUID + 10 }, "oldrevision": func(w *worldv1.SnapshotResponse) { w.BackpackRevision-- }} {
		t.Run(name, func(t *testing.T) {
			w := proto.Clone(afterRaw).(*worldv1.SnapshotResponse)
			change(w)
			v, e := validateWorld(w, 6)
			if e != nil {
				t.Fatal(e)
			}
			if exactPurchaseIncrease(before, v, 2320, 1) {
				t.Fatal("unrelated or redistributed change counted as purchase")
			}
		})
	}
}
func TestEquipmentProofRequiresExactEntrySwapAndObservedEmptyOffhand(t *testing.T) {
	_, f := newFixture(t)
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6256, StackCount: 1, MetadataKnown: true}
	before, e := validateWorld(f.world, 6)
	if e != nil {
		t.Fatal(e)
	}
	w := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	w.BackpackRevision++
	w.Self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: poleGUID, Entry: 6256, MetadataKnown: true}
	w.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: weaponGUID, Entry: 35, StackCount: 1, MetadataKnown: true}
	after, e := validateWorld(w, 6)
	if e != nil || !exactEquipmentSwap(before, after, 2, poleGUID, weaponGUID, 6256, 35) {
		t.Fatal("valid swap rejected", e)
	}
	for name, change := range map[string]func(*worldv1.SnapshotResponse){"wrongentry": func(w *worldv1.SnapshotResponse) { w.Self.Mainhand.Entry = 7005 }, "unknownoffhand": func(w *worldv1.SnapshotResponse) { w.Self.Offhand = nil }, "occupiedoffhand": func(w *worldv1.SnapshotResponse) {
		w.Self.Offhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: poleGUID + 9, Entry: 222, MetadataKnown: true}
	}, "missingoriginal": func(w *worldv1.SnapshotResponse) { w.Backpack[2] = &worldv1.BackpackSlot{Slot: 2} }} {
		t.Run(name, func(t *testing.T) {
			raw := proto.Clone(w).(*worldv1.SnapshotResponse)
			change(raw)
			v, e := validateWorld(raw, 6)
			if e != nil {
				t.Fatal(e)
			}
			if exactEquipmentSwap(before, v, 2, poleGUID, weaponGUID, 6256, 35) {
				t.Fatal("incomplete equipment evidence accepted")
			}
		})
	}
}
func TestRequirementsUsePureSkillAndRejectUnknownRaceRestrictions(t *testing.T) {
	_, f := newFixture(t)
	v, e := validateWorld(f.world, 6)
	if e != nil {
		t.Fatal(e)
	}
	i := proto.Clone(f.items[6256]).(*tradev1.ItemDetails)
	i.RequiredSkill = 356
	i.RequiredSkillRank = 2
	f.world.SelfSkills[0].PermanentBonus = 100
	if itemRequirements(v, i) == "" {
		t.Fatal("skill bonus met pure requirement")
	}
	i.RequiredSkillRank = 1
	if itemRequirements(v, i) != "" {
		t.Fatal("valid actual pure rank rejected")
	}
	i.AllowableRace = 0
	if itemRequirements(v, i) == "" {
		t.Fatal("zero race mask accepted")
	}
	i.AllowableRace = 1
	if itemRequirements(v, i) == "" {
		t.Fatal("unobserved race accepted")
	}
}
