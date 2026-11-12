package buddyinventory

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const otherHandGUID = uint64(0x4000000000000033)

func weaponMetadataFrame(entry, subclass, inv uint32, minimum, maximum float32, delay uint32) []byte {
	p := metadataFixture(entry, 2, 1, inv, 0)
	binary.LittleEndian.PutUint32(p[8:], subclass)
	at := 16 + len("Fixture") + 4
	for _, index := range []int{7, 8} {
		binary.LittleEndian.PutUint32(p[at+4*index:], math.MaxUint32)
	}
	binary.LittleEndian.PutUint32(p[at+4*10:], 1)
	binary.LittleEndian.PutUint32(p[at+4*19:], 1)
	damageAt := at + 88 + 8
	binary.LittleEndian.PutUint32(p[damageAt:], math.Float32bits(minimum))
	binary.LittleEndian.PutUint32(p[damageAt+4:], math.Float32bits(maximum))
	binary.LittleEndian.PutUint32(p[damageAt+24+28:], delay)
	binary.LittleEndian.PutUint32(p[len(p)-29*4+11*4:], 25)
	return p
}

func fixtureWeapon(t *testing.T, staff bool) (*Service, *fakeInventory, *inventoryv1.EquipGearRequest) {
	t.Helper()
	s, f := fixtureGear(t, false)
	entry, old, subclass, inv, skill := uint32(2224), uint32(2092), uint32(15), uint32(13), uint32(173)
	lo, hi, delay, oldLo, oldHi, oldDelay := float32(2), float32(4), uint32(1500), float32(1), float32(2), uint32(1600)
	if staff {
		entry, old, subclass, inv, skill = 1159, 35, 10, 17, 136
		lo, hi, delay, oldLo, oldHi, oldDelay = 6, 9, 2800, 3, 5, 2900
		f.w.Self.ClassId = 8
	} else {
		f.w.SelfEquipment[16] = &worldv1.EquipmentSlotState{Slot: 16, SlotKnown: true, ItemGuid: otherHandGUID, Entry: 50055,
			StackCount: 1, MetadataKnown: true, DurabilityKnown: true, Durability: 7, MaxDurability: 25, EnhancementsKnown: true}
		m, err := parseItemMetadata(weaponMetadataFrame(50055, 15, 22, 1, 2, 1600))
		if err != nil {
			t.Fatal(err)
		}
		s.metadata[50055], s.queried[50055] = m, true
	}
	f.w.SelfSkills[0] = &worldv1.SkillState{Slot: 0, SkillId: skill, Value: 1, MaxValue: 45}
	f.w.InventoryBags[0].Slots[2].Entry = entry
	f.w.InventoryBags[0].Slots[2].Durability, f.w.InventoryBags[0].Slots[2].MaxDurability = 25, 25
	f.w.SelfEquipment[15] = &worldv1.EquipmentSlotState{Slot: 15, SlotKnown: true, ItemGuid: oldPantsGUID, Entry: old,
		StackCount: 1, MetadataKnown: true, DurabilityKnown: true, Durability: 7, MaxDurability: 25, EnhancementsKnown: true}
	for id, frame := range map[uint32][]byte{entry: weaponMetadataFrame(entry, subclass, inv, lo, hi, delay), old: weaponMetadataFrame(old, subclass, inv, oldLo, oldHi, oldDelay)} {
		m, err := parseItemMetadata(frame)
		if err != nil {
			t.Fatal(err)
		}
		s.metadata[id], s.queried[id] = m, true
	}
	st := stateInventory(t, s)
	return s, f, &inventoryv1.EquipGearRequest{SessionId: "body", RequestId: "weapon", ExpectedRevision: st.Revision,
		ExpectedInventoryRevision: st.InventoryRevision, Source: &inventoryv1.ItemRef{Slot: 2, ItemGuid: breechesGUID, Entry: entry, StackCount: 1},
		EquipmentSlot: 15, ExpectedEquipmentRevision: st.EquipmentRevision, ExpectedEquippedItemGuid: oldPantsGUID, RequireConservativeUpgrade: true}
}

func TestWeaponGearActualDaggerAndStaffNormalSwapConservesBothHandsAndBags(t *testing.T) {
	for _, staff := range []bool{false, true} {
		t.Run(map[bool]string{false: "rogue dagger", true: "mage staff"}[staff], func(t *testing.T) {
			s, f, r := fixtureWeapon(t, staff)
			beforeOH := proto.Clone(f.w.SelfEquipment[16]).(*worldv1.EquipmentSlotState)
			gearProof(s, f)
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			st := waitInventory(t, s)
			if st.Operation.Status != "CONFIRMED" || !st.Operation.InventoryConfirmed || !st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased ||
				st.Equipment[15].ItemGuid != breechesGUID || st.InventoryBags[0].Slots[2].ItemGuid != oldPantsGUID || !proto.Equal(st.Equipment[16], beforeOH) {
				t.Fatal("normal weapon/displacement proof incomplete", st)
			}
			if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAutoequipItemSlot || len(f.packets[0].Payload) != 9 ||
				binary.LittleEndian.Uint64(f.packets[0].Payload) != breechesGUID || f.packets[0].Payload[8] != 15 {
				t.Fatal("unexpected weapon packet", f.packets)
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil || len(f.packets) != 1 {
				t.Fatal("weapon request replay wrote", err)
			}
		})
	}
}

func TestWeaponGearActualMetadataAndFullFrameBoundaries(t *testing.T) {
	p := weaponMetadataFrame(2224, 15, 13, 2, 4, 1500)
	m, err := parseItemMetadata(p)
	dps, ok := m.physicalWeaponDPS()
	if err != nil || !m.ordinaryMainhandWeapon() || !ok || dps != 2 || m.delay != 1500 || m.damages[0] != (itemDamage{2, 4, 0}) {
		t.Fatal("actual query damage/delay not retained", m, err)
	}
	for n := range len(p) {
		partial, _ := parseItemMetadata(p[:n])
		if partial.ordinaryMainhandWeapon() {
			t.Fatalf("partial frame authorized weapon at %d", n)
		}
	}
	for name, alter := range map[string]func([]byte){
		"nan": func(p []byte) {
			binary.LittleEndian.PutUint32(p[16+len("Fixture")+4+88+8:], math.Float32bits(float32(math.NaN())))
		},
		"infinity": func(p []byte) {
			binary.LittleEndian.PutUint32(p[16+len("Fixture")+4+88+8+4:], math.Float32bits(float32(math.Inf(1))))
		},
		"zero delay":  func(p []byte) { binary.LittleEndian.PutUint32(p[16+len("Fixture")+4+88+8+24+28:], 0) },
		"nonphysical": func(p []byte) { binary.LittleEndian.PutUint32(p[16+len("Fixture")+4+88+8+8:], 1) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := append([]byte(nil), p...)
			alter(bad)
			m, _ := parseItemMetadata(bad)
			if m.ordinaryMainhandWeapon() {
				t.Fatal("unknown damage authorized weapon", m)
			}
		})
	}
}

func TestWeaponGearRequirementsEnhancementsAndHandSetGates(t *testing.T) {
	for _, mode := range []string{"unknown skills", "wrong proficiency", "class", "race", "level", "required skill", "required spell", "unknown template", "candidate enchanted", "old enchanted", "unknown enhancements", "broken", "offhand dagger", "ranged", "effect", "random stats", "zero damage", "no improvement", "lower armor", "lost stat", "lost resistance", "staff occupied offhand", "staff unknown offhand"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := fixtureWeapon(t, mode == "staff occupied offhand" || mode == "staff unknown offhand")
			m := s.metadata[r.Source.Entry]
			switch mode {
			case "unknown skills":
				f.w.SelfSkillsKnown = false
			case "wrong proficiency":
				f.w.SelfSkills[0].SkillId = 136
			case "class":
				m.allowableClass = 1
			case "race":
				m.allowableRace = 2
			case "level":
				m.requiredLevel = 6
			case "required skill":
				m.requiredSkill, m.requiredSkillRank = 173, 2
			case "required spell":
				m.requiredSpell = 674
			case "unknown template":
				m.known = false
			case "candidate enchanted":
				f.w.InventoryBags[0].Slots[2].HasEnhancements = true
			case "old enchanted":
				f.w.SelfEquipment[15].HasEnhancements = true
			case "unknown enhancements":
				f.w.SelfEquipment[15].EnhancementsKnown = false
			case "broken":
				f.w.InventoryBags[0].Slots[2].Durability = 0
			case "offhand dagger":
				m.inventoryType = 22
			case "ranged":
				m.inventoryType = 26
			case "effect":
				m.hasSpell = true
			case "random stats":
				m.specialArmor = true
			case "zero damage":
				m.damages = [2]itemDamage{}
			case "no improvement":
				m.damages, m.delay = s.metadata[2092].damages, s.metadata[2092].delay
			case "lower armor", "lost stat", "lost resistance":
				old := s.metadata[2092]
				if mode == "lower armor" {
					old.armor = 1
				} else if mode == "lost stat" {
					old.statCount, old.stats[0] = 1, itemStat{3, 1}
				} else {
					old.resistances[0] = 1
				}
				s.metadata[2092] = old
			case "staff occupied offhand":
				f.w.SelfEquipment[16] = &worldv1.EquipmentSlotState{Slot: 16, SlotKnown: true, ItemGuid: otherHandGUID, Entry: 50055, StackCount: 1, MetadataKnown: true}
				s.queried[50055] = true
			case "staff unknown offhand":
				f.w.SelfEquipment[16].SlotKnown = false
			}
			s.metadata[r.Source.Entry] = m
			r.ExpectedRevision = stateInventory(t, s).Revision
			if _, err := s.EquipGear(context.Background(), r); err == nil {
				if st := waitInventory(t, s); st.Operation.Status != "BLOCKED" {
					t.Fatal("unsafe weapon accepted", st)
				}
			}
			if len(f.packets) != 0 {
				t.Fatal("unsafe weapon emitted packet", f.packets)
			}
		})
	}
}

func TestWeaponGearFinalOffhandChangeAndIncompleteDisplacementNeverConfirm(t *testing.T) {
	for _, mode := range []string{"offhand after renew", "missing old weapon", "other hand changed", "inventory only", "equipment only"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := fixtureWeapon(t, true)
			if mode == "offhand after renew" {
				f.afterRenew = func() {
					f.mu.Lock()
					f.w.SelfEquipment[16] = &worldv1.EquipmentSlotState{Slot: 16, SlotKnown: true, ItemGuid: otherHandGUID, Entry: 50055, MetadataKnown: true, StackCount: 1}
					f.mu.Unlock()
				}
			} else {
				f.afterSend = func(p *modv1.SendPacketRequest) {
					if p.Opcode != opcode.CMSGAutoequipItemSlot {
						return
					}
					s.mu.Lock()
					w := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
					s.last.cancel()
					s.mu.Unlock()
					if mode == "missing old weapon" {
						w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2}
					}
					if mode == "other hand changed" {
						w.SelfEquipment[16] = &worldv1.EquipmentSlotState{Slot: 16, SlotKnown: true, ItemGuid: otherHandGUID, Entry: 50055, MetadataKnown: true, StackCount: 1}
					}
					if mode != "equipment only" {
						w.InventoryRevision++
					}
					if mode != "inventory only" {
						w.SelfEquipmentRevision++
					}
					f.mu.Lock()
					f.w = w
					f.mu.Unlock()
				}
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			st := waitInventory(t, s)
			if st.Operation.Status == "CONFIRMED" || mode == "offhand after renew" && len(f.packets) != 0 {
				t.Fatal("incomplete/changed weapon source accepted", st)
			}
		})
	}
}

func TestWeaponGearUpgradeCannotTradeStatsOrBypassUnknownsIntoEmptySlot(t *testing.T) {
	s, f, r := fixtureWeapon(t, false)
	candidate, old := s.metadata[r.Source.Entry], s.metadata[2092]
	carried, equipped := f.w.InventoryBags[0].Slots[2], f.w.SelfEquipment[15]
	if !conservativeWeaponUpgrade(candidate, carried, old, equipped) {
		t.Fatal("strict actual dagger improvement refused")
	}
	for _, mode := range []string{"negative into empty", "unknown stat into empty", "equal dps plus stats", "old elemental damage"} {
		t.Run(mode, func(t *testing.T) {
			c, o := candidate, old
			e := proto.Clone(equipped).(*worldv1.EquipmentSlotState)
			switch mode {
			case "negative into empty":
				e.ItemGuid = 0
				c.statCount, c.stats[0] = 1, itemStat{3, -1}
			case "unknown stat into empty":
				e.ItemGuid = 0
				c.statCount, c.stats[0] = 1, itemStat{42, 1}
			case "equal dps plus stats":
				c.damages, c.delay = o.damages, o.delay
				c.statCount, c.stats[0] = 1, itemStat{3, 1}
			case "old elemental damage":
				o.damages[0].school = 1
			}
			if conservativeWeaponUpgrade(c, carried, o, e) {
				t.Fatal("unsupported comparison authorized", mode)
			}
		})
	}
}
