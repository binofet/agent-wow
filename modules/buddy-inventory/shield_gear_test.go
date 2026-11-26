package buddyinventory

import (
	"context"
	"encoding/binary"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const crestEntry = 10835
const maceEntry = 42948

func fixtureShield(t *testing.T, main, off bool) (*Service, *fakeInventory, *inventoryv1.EquipGearRequest) {
	t.Helper()
	s, f := fixtureGear(t, false)
	f.w.Self.ClassId, f.w.Self.Level = 2, 50
	f.w.SelfSkills[0].SkillId = 433
	f.w.InventoryBags[0].Slots[2].Entry = crestEntry
	// Crest of Supremacy has an on-equip effect. Its physical destination
	// must also be available to an explicit equip outside upgrade scoring.
	s.metadata[crestEntry] = itemMetadata{known: true, entry: crestEntry, class: 4, subclass: 6, inventoryType: 14,
		requiredLevel: 50, allowableClass: ^uint32(0), allowableRace: ^uint32(0), hasSpell: true, stackable: 1}
	s.queried[crestEntry] = true
	if main {
		f.w.SelfEquipment[mainhandSlot] = &worldv1.EquipmentSlotState{Slot: mainhandSlot, SlotKnown: true, ItemGuid: otherHandGUID,
			MetadataKnown: true, Entry: maceEntry, StackCount: 1, EnhancementsKnown: true, HasEnhancements: true,
			EnhancementDetails: &worldv1.ItemEnhancementState{IdKnownMask: 1, Enchantments: []*worldv1.ItemEnchantmentSlot{{Slot: 0, Id: 2669}}}}
		s.metadata[maceEntry] = itemMetadata{known: true, entry: maceEntry, class: 2, subclass: 4, inventoryType: 21}
		s.queried[maceEntry] = true
	}
	if off {
		const oldShield = 9403
		f.w.SelfEquipment[offhandSlot] = &worldv1.EquipmentSlotState{Slot: offhandSlot, SlotKnown: true,
			ItemGuid: oldPantsGUID, MetadataKnown: true, Entry: oldShield, StackCount: 1, EnhancementsKnown: true}
		s.metadata[oldShield] = itemMetadata{known: true, entry: oldShield, class: 4, subclass: 6, inventoryType: 14}
		s.queried[oldShield] = true
	}
	st := stateInventory(t, s)
	return s, f, &inventoryv1.EquipGearRequest{SessionId: st.SessionId, RequestId: "equip-shield", ExpectedRevision: st.Revision,
		ExpectedInventoryRevision: st.InventoryRevision, ExpectedEquipmentRevision: st.EquipmentRevision, EquipmentSlot: offhandSlot,
		ExpectedEquippedItemGuid: st.Equipment[offhandSlot].ItemGuid, Source: &inventoryv1.ItemRef{Slot: 2, ItemGuid: breechesGUID, Entry: crestEntry, StackCount: 1}}
}

func TestShieldGearExactOffhandEquipConservesMainhandAndDisplacedItem(t *testing.T) {
	for _, c := range []struct {
		name      string
		main, off bool
	}{{"empty hands", false, false}, {"enchanted mace and old shield", true, true}, {"one-hand sword and free offhand", true, false}} {
		t.Run(c.name, func(t *testing.T) {
			s, f, r := fixtureShield(t, c.main, c.off)
			if c.main && !c.off {
				const sword = 13035 // Serpent Slicer, usable in either hand.
				f.w.SelfEquipment[mainhandSlot].Entry = sword
				s.metadata[sword] = itemMetadata{known: true, entry: sword, class: 2, subclass: 7, inventoryType: 13}
				s.queried[sword] = true
			}
			beforeMain := proto.Clone(f.w.SelfEquipment[mainhandSlot])
			old := f.w.SelfEquipment[offhandSlot]
			st := stateInventory(t, s)
			r.ExpectedRevision = st.Revision
			var destinations []*inventoryv1.GearDestination
			for _, item := range st.Items {
				if item.Item.ItemGuid == r.Source.ItemGuid {
					destinations = item.GearFacts.Destinations
				}
			}
			if len(destinations) != 1 || !destinations[0].Eligible || destinations[0].EquipmentSlot != offhandSlot || destinations[0].ExpectedEquippedItemGuid != old.ItemGuid {
				t.Fatalf("shield discovery: %v", destinations)
			}
			f.afterSend = func(p *modv1.SendPacketRequest) {
				if p.Opcode != opcode.CMSGAutoequipItemSlot {
					return
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				f.w.SelfEquipment[offhandSlot] = &worldv1.EquipmentSlotState{Slot: offhandSlot, SlotKnown: true, ItemGuid: r.Source.ItemGuid, Entry: crestEntry, StackCount: 1, MetadataKnown: true}
				f.w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: old.ItemGuid, Entry: old.Entry, StackCount: old.StackCount, MetadataKnown: old.MetadataKnown}
				f.w.InventoryRevision++
				f.w.SelfEquipmentRevision++
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			st = waitInventory(t, s)
			op := st.Operation
			if op.Status != "CONFIRMED" || !op.InventoryConfirmed || !op.EquipmentConfirmed || !op.LeaseReleased ||
				st.Equipment[offhandSlot].ItemGuid != r.Source.ItemGuid || !proto.Equal(st.Equipment[mainhandSlot], beforeMain) || st.InventoryBags[0].Slots[2].ItemGuid != old.ItemGuid {
				t.Fatal("unconfirmed shield or hand/bag change", st)
			}
			if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAutoequipItemSlot || len(f.packets[0].Payload) != 9 ||
				binary.LittleEndian.Uint64(f.packets[0].Payload) != r.Source.ItemGuid || f.packets[0].Payload[8] != offhandSlot {
				t.Fatal("unexpected outbound equip", f.packets)
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil || len(f.packets) != 1 {
				t.Fatal("duplicate shield request", err, f.packets)
			}
		})
	}
}

func TestShieldGearUnexpectedMainhandOrLostDisplacedItemRemainsUnconfirmed(t *testing.T) {
	for _, lost := range []string{"mainhand", "displaced shield"} {
		t.Run(lost, func(t *testing.T) {
			s, f, r := fixtureShield(t, true, true)
			gearProof(s, f)
			applySwap := f.afterSend
			f.afterSend = func(p *modv1.SendPacketRequest) {
				applySwap(p)
				f.mu.Lock()
				if lost == "mainhand" {
					f.w.SelfEquipment[mainhandSlot] = &worldv1.EquipmentSlotState{Slot: mainhandSlot, SlotKnown: true}
				} else {
					f.w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2}
				}
				f.mu.Unlock()
				s.mu.Lock()
				s.last.cancel()
				s.mu.Unlock()
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if st := waitInventory(t, s); st.Operation.Status != "OUTCOME_UNCONFIRMED" || !st.Operation.TransportAttempted {
				t.Fatal(st.Operation)
			}
			r.RequestId = "another-shield-request"
			r.ExpectedRevision = stateInventory(t, s).Revision
			if _, err := s.EquipGear(context.Background(), r); err == nil || len(f.packets) != 1 {
				t.Fatal("fresh attempt after partial evidence", err, f.packets)
			}
		})
	}
}

func TestShieldGearBlockedHandsSkillsAndRevisionBindings(t *testing.T) {
	for _, name := range []string{"two-handed", "unknown weapon template", "unknown main item", "offhand-only weapon", "held item", "wrong skill", "incomplete skills",
		"mismatched shield subclass", "mismatched shield inventory type", "wrong slot", "wrong owner", "foreign source", "stale equipment", "stale inventory",
		"wrong displaced item", "two-handed after renewal", "skill lost after renewal"} {
		t.Run(name, func(t *testing.T) {
			s, f, r := fixtureShield(t, true, true)
			m, shield := s.metadata[maceEntry], s.metadata[crestEntry]
			switch name {
			case "two-handed":
				m.subclass, m.inventoryType = 5, 17
			case "unknown weapon template":
				m.known = false
			case "unknown main item":
				f.w.SelfEquipment[mainhandSlot].MetadataKnown = false
			case "offhand-only weapon":
				m.inventoryType = 22
			case "held item":
				m.class, m.inventoryType = 4, 23
			case "wrong skill":
				f.w.SelfSkills[0].SkillId = 293
			case "incomplete skills":
				f.w.SelfSkills = f.w.SelfSkills[:127]
			case "mismatched shield subclass":
				shield.subclass = 0
			case "mismatched shield inventory type":
				shield.inventoryType = 23
			case "wrong slot":
				r.EquipmentSlot, r.ExpectedEquippedItemGuid = mainhandSlot, otherHandGUID
			case "wrong owner":
				f.w.Self.Guid++
			case "foreign source":
				r.Source.ItemGuid = otherHandGUID
			case "stale equipment":
				r.ExpectedEquipmentRevision++
			case "stale inventory":
				r.ExpectedInventoryRevision++
			case "wrong displaced item":
				r.ExpectedEquippedItemGuid = 0
			case "two-handed after renewal", "skill lost after renewal":
				s.metadata[35] = itemMetadata{known: true, entry: 35, class: 2, subclass: 10, inventoryType: 17}
				s.queried[35] = true
				f.afterRenew = func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					if name == "two-handed after renewal" {
						f.w.SelfEquipment[mainhandSlot].Entry = 35
					} else {
						f.w.SelfSkills[0].SkillId = 293
						f.w.SelfSkillsRevision++
					}
				}
			}
			s.metadata[maceEntry], s.metadata[crestEntry] = m, shield
			r.ExpectedRevision = stateInventory(t, s).Revision
			if _, err := s.EquipGear(context.Background(), r); err == nil {
				if st := waitInventory(t, s); st.Operation.Status != "BLOCKED" {
					t.Fatal(st.Operation)
				}
			}
			if len(f.packets) != 0 {
				t.Fatal("ineligible shield was sent", f.packets)
			}
		})
	}
}
