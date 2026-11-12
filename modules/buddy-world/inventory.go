package buddyworld

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

// Primary 3.3.5a UpdateFields.h and Player.h: four equipped bag slots19..22,
// ITEM_END64, and 36 public container slot GUIDs beginning at field66.
const (
	playerBagsFirst = 0x16a
	containerSlots  = 64
	containerFirst  = 66
	maxBagSlots     = 36
)

type inventoryItemView struct {
	guid                            uint64
	entry, count                    uint32
	metadata                        bool
	durabilityKnown                 bool
	durability, maximum             uint32
	enhancementsKnown, enhancements bool
}
type inventoryBagView struct {
	guid                          uint64
	entry, count                  uint32
	slotKnown, metadata, contents bool
	slots                         [maxBagSlots]inventoryItemView
}
type inventoryView struct {
	known, baseline bool
	bags            [5]inventoryBagView
}

func clearInventoryContext(p *observed) {
	if !p.isSelf {
		return
	}
	p.bagSlotsKnown = false
	p.bags = [4]uint64{}
	p.equipmentKnown = false
	p.equipment = [19]uint64{}
	p.inventoryEpoch++
}

func (s *observations) ownedItem(guid uint64) *observed {
	p := s.items[guid]
	if p == nil || !p.present || !p.inventoryItemKnown || !itemGUID(guid) || !p.known[itemOwner] || !p.known[itemOwner+1] || uint64(p.fields[itemOwner])|uint64(p.fields[itemOwner+1])<<32 != s.players[0].guid {
		return nil
	}
	return p
}

func (s *observations) carriedItem(guid uint64) inventoryItemView {
	r := inventoryItemView{guid: guid}
	p := s.ownedItem(guid)
	if p != nil && p.inventoryEpoch == s.players[0].inventoryEpoch && p.known[objectEntry] && p.known[itemStack] && p.fields[objectEntry] != 0 && p.fields[itemStack] != 0 {
		r.entry, r.count, r.metadata = p.fields[objectEntry], p.fields[itemStack], true
		r.durability, r.maximum = p.fields[0x3c], p.fields[0x3d]
		r.durabilityKnown = p.known[0x3c] && p.known[0x3d] && r.durability <= r.maximum
		r.enhancementsKnown, r.enhancements = itemEnhancements(p)
	}
	return r
}

// This comparable, age-free view lets a whole valid packet advance inventory
// revision once, without allocating protobuf messages for unrelated updates.
func (s *observations) inventoryView() inventoryView {
	p := s.players[0]
	r := inventoryView{}
	if !p.isSelf || !p.present || !p.bagSlotsKnown || !p.backpackKnown {
		return r
	}
	r.baseline, r.known = true, true
	r.bags[0].slotKnown, r.bags[0].contents, r.bags[0].count = true, true, 16
	for slot, guid := range p.backpack {
		r.bags[0].slots[slot] = s.carriedItem(guid)
	}
	for n, guid := range p.bags {
		b := &r.bags[n+1]
		b.guid, b.slotKnown = guid, guid == 0 || itemGUID(guid)
		if guid == 0 {
			b.contents = true
			continue
		}
		item := s.ownedItem(guid)
		if !b.slotKnown || item == nil || item.inventoryEpoch != p.inventoryEpoch || !item.isContainer || !item.known[objectEntry] || item.fields[objectEntry] == 0 {
			r.known = false
			continue
		}
		b.metadata, b.entry = true, item.fields[objectEntry]
		count := item.fields[containerSlots]
		if !item.containerKnown || !item.known[containerSlots] || count == 0 || count > maxBagSlots {
			r.known = false
			continue
		}
		b.count, b.contents = count, true
		for slot := uint32(0); slot < count; slot++ {
			f := containerFirst + int(slot)*2
			if !item.known[f] || !item.known[f+1] {
				b.contents, r.known = false, false
				break
			}
			g := uint64(item.fields[f]) | uint64(item.fields[f+1])<<32
			b.slots[slot] = s.carriedItem(g)
		}
	}
	// A transient duplicate or invalid GUID is not an actionable inventory
	// location. Preserve its observations but qualify the whole layout unknown.
	seen := make(map[uint64]bool, 164)
	for _, b := range r.bags {
		if b.guid != 0 {
			if seen[b.guid] || !itemGUID(b.guid) {
				r.known = false
			}
			seen[b.guid] = true
		}
	}
	for bi := range r.bags {
		b := &r.bags[bi]
		if !b.contents {
			continue
		}
		for slot := uint32(0); slot < b.count; slot++ {
			guid := b.slots[slot].guid
			if guid == 0 {
				continue
			}
			if !itemGUID(guid) || seen[guid] {
				r.known, b.contents = false, false
			}
			seen[guid] = true
		}
	}
	return r
}

func (s *observations) appendInventory(r *worldv1.SnapshotResponse) {
	v := s.inventoryView()
	r.InventoryKnown, r.InventoryRevision = v.known, s.inventoryRevision
	if !v.baseline {
		return
	}
	for bag, b := range v.bags {
		p := &worldv1.InventoryBag{Bag: uint32(bag), SlotKnown: b.slotKnown, ContainerGuid: b.guid, MetadataKnown: b.metadata, Entry: b.entry, ContentsKnown: b.contents, SlotCount: b.count}
		if b.contents {
			for slot := uint32(0); slot < b.count; slot++ {
				item := b.slots[slot]
				p.Slots = append(p.Slots, &worldv1.BackpackSlot{Bag: uint32(bag), Slot: slot, ItemGuid: item.guid, Entry: item.entry, StackCount: item.count, MetadataKnown: item.metadata, DurabilityKnown: item.durabilityKnown, Durability: item.durability, MaxDurability: item.maximum, EnhancementsKnown: item.enhancementsKnown, HasEnhancements: item.enhancements})
			}
		}
		r.InventoryBags = append(r.InventoryBags, p)
	}
}

// Referenced carried containers and their current contents are retained before
// unrelated owned items. Banks and unequipped bags are never projected here.
func (s *observations) protectedItem(guid uint64) bool {
	p := s.players[0]
	if guid == p.mainhand || guid == p.offhand {
		return true
	}
	if p.equipmentKnown {
		for _, g := range p.equipment {
			if g == guid {
				return true
			}
		}
	}
	for _, g := range p.backpack {
		if g == guid {
			return true
		}
	}
	if !p.bagSlotsKnown {
		return false
	}
	for _, g := range p.bags {
		if g == guid {
			return true
		}
		bag := s.ownedItem(g)
		if bag == nil || bag.inventoryEpoch != p.inventoryEpoch || !bag.isContainer || !bag.containerKnown || !bag.known[containerSlots] || bag.fields[containerSlots] > maxBagSlots {
			continue
		}
		for slot := uint32(0); slot < bag.fields[containerSlots]; slot++ {
			f := containerFirst + int(slot)*2
			if bag.known[f] && bag.known[f+1] && uint64(bag.fields[f])|uint64(bag.fields[f+1])<<32 == guid {
				return true
			}
		}
	}
	return false
}
