package buddyinventory

import (
	"context"
	"encoding/binary"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"testing"
)

const ammoGUID = uint64(0x4000000000000031)
const bowGUID = uint64(0x4000000000000033)

func ammoFixture(t *testing.T) (*Service, *fakeInventory) {
	s, f := fixtureGear(t, false)
	f.w.Self.Level = 50
	f.w.SelfAmmoKnown, f.w.SelfAmmoRevision = true, 1
	f.w.SelfSkills[1] = &worldv1.SkillState{Slot: 1, SkillId: 45, Value: 1, MaxValue: 300}
	f.w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: ammoGUID, Entry: 11285, StackCount: 200, MetadataKnown: true}
	f.w.SelfEquipment[17] = &worldv1.EquipmentSlotState{Slot: 17, SlotKnown: true, ItemGuid: bowGUID, Entry: 9400, StackCount: 1, MetadataKnown: true}
	s.metadata[11285] = itemMetadata{known: true, entry: 11285, class: 6, subclass: 2, inventoryType: 24, stackable: 1000, requiredLevel: 45, allowableClass: ^uint32(0), allowableRace: ^uint32(0)}
	s.metadata[9400] = itemMetadata{known: true, entry: 9400, class: 2, subclass: 2, inventoryType: 15, ammoType: 2, stackable: 1, allowableClass: ^uint32(0), allowableRace: ^uint32(0)}
	s.queried[11285], s.queried[9400] = true, true
	return s, f
}
func ammoRequest(t *testing.T, s *Service) *inventoryv1.SelectAmmoRequest {
	st := stateGear(t, s)
	return &inventoryv1.SelectAmmoRequest{SessionId: st.SessionId, RequestId: "select-arrows", ExpectedRevision: st.Revision, ExpectedInventoryRevision: st.InventoryRevision,
		Source: &inventoryv1.ItemRef{Slot: 2, ItemGuid: ammoGUID, Entry: 11285, StackCount: 200}, ExpectedAmmoRevision: st.AmmoRevision, ExpectedAmmoEntry: st.AmmoEntry,
		ExpectedEquipmentRevision: st.EquipmentRevision, ExpectedRangedItemGuid: bowGUID}
}
func ammoProof(f *fakeInventory) {
	f.afterSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode == opcode.CMSGSetAmmo {
			f.mu.Lock()
			f.w = proto.Clone(f.w).(*worldv1.SnapshotResponse)
			f.w.SelfAmmoEntry = binary.LittleEndian.Uint32(p.Payload)
			f.w.SelfAmmoRevision++
			f.mu.Unlock()
		}
	}
}
func TestAmmoSelectionNormalPacketIndependentPrivateReceiptAndNoReplay(t *testing.T) {
	s, f := ammoFixture(t)
	r := ammoRequest(t, s)
	ammoProof(f)
	f.sendError = true
	if _, err := s.SelectAmmo(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	st := waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || !st.Operation.AmmoConfirmed || !st.Operation.InventoryConfirmed || !st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased || st.Operation.TransportWritten {
		t.Fatal(st)
	}
	if st.AmmoEntry != 11285 || !st.AmmoKnown || st.InventoryRevision != r.ExpectedInventoryRevision || st.Equipment[17].ItemGuid != bowGUID || st.InventoryBags[0].Slots[2].StackCount != 200 {
		t.Fatal(st)
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGSetAmmo || len(f.packets[0].Payload) != 4 || binary.LittleEndian.Uint32(f.packets[0].Payload) != 11285 {
		t.Fatal(f.packets)
	}
	if _, err := s.SelectAmmo(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("same-ID replay", err, f.packets)
	}
	changed := proto.Clone(r).(*inventoryv1.SelectAmmoRequest)
	changed.Source.StackCount--
	if _, err := s.SelectAmmo(context.Background(), changed); err == nil {
		t.Fatal("changed same-ID request accepted")
	}
}
func TestAmmoSelectionFreshGatesAndFinalRacesNeverWrite(t *testing.T) {
	cases := map[string]func(*Service, *fakeInventory, *inventoryv1.SelectAmmoRequest){
		"stack":           func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { r.Source.StackCount-- },
		"item":            func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { r.Source.ItemGuid++ },
		"ammo unknown":    func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { f.w.SelfAmmoKnown = false },
		"ammo revision":   func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { r.ExpectedAmmoRevision++ },
		"weapon revision": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { r.ExpectedEquipmentRevision++ },
		"weapon guid":     func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { r.ExpectedRangedItemGuid++ },
		"no skill": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			f.w.SelfSkills[1].SkillId = 0
			f.w.SelfSkills[1].Value = 0
			f.w.SelfSkills[1].MaxValue = 0
		},
		"level": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { f.w.Self.Level = 44 },
		"bullets": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			m := s.metadata[11285]
			m.subclass = 3
			s.metadata[11285] = m
		},
		"thrown": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			m := s.metadata[9400]
			m.subclass = 16
			s.metadata[9400] = m
		},
		"wand": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			m := s.metadata[9400]
			m.subclass = 19
			s.metadata[9400] = m
		},
		"cast unknown": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			f.a.CastPending = false
			f.a.LastSpellOperation = &actionsv1.SpellOperation{Phase: "outcome_unconfirmed_no_retry"}
		},
		"cancel unknown": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			f.a.LastSpellOperation = &actionsv1.SpellOperation{CancelOutcomeUnknown: true}
		},
		"buff unknown": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			f.a.LastBuff = &actionsv1.BuffOperation{Phase: "outcome_unconfirmed"}
		},
		"cast":           func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { f.a.CastPending = true },
		"unknown motion": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) { f.a.MotionUnconfirmed = true },
		"late ammo": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			f.afterRenew = func() { f.mu.Lock(); f.w.SelfAmmoRevision++; f.mu.Unlock() }
		},
		"late stack": func(s *Service, f *fakeInventory, r *inventoryv1.SelectAmmoRequest) {
			f.afterRenew = func() {
				f.mu.Lock()
				f.w.InventoryRevision++
				f.w.InventoryBags[0].Slots[2].StackCount--
				f.mu.Unlock()
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, f := ammoFixture(t)
			r := ammoRequest(t, s)
			mutate(s, f, r)
			r.ExpectedRevision = stateInventory(t, s).Revision
			_, err := s.SelectAmmo(context.Background(), r)
			if err == nil {
				st := waitInventory(t, s)
				if st.Operation.Status != "BLOCKED" {
					t.Fatal(st)
				}
			}
			if len(f.packets) != 0 {
				t.Fatal(f.packets)
			}
		})
	}
}
func TestAmmoSelectionWriteWithoutNewPrivateEvidenceRemainsUnknown(t *testing.T) {
	s, f := ammoFixture(t)
	r := ammoRequest(t, s)
	f.afterSend = func(_ *modv1.SendPacketRequest) { s.mu.Lock(); s.active.cancel(); s.mu.Unlock() }
	if _, err := s.SelectAmmo(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	st := waitInventory(t, s)
	if st.Operation.Status != "OUTCOME_UNCONFIRMED" || st.Operation.AmmoConfirmed || len(f.packets) != 1 {
		t.Fatal(st)
	}
	newer := proto.Clone(r).(*inventoryv1.SelectAmmoRequest)
	newer.RequestId = "different-id"
	newer.ExpectedRevision = st.Revision
	if _, err := s.SelectAmmo(context.Background(), newer); err == nil {
		t.Fatal("unknown operation allowed new retry")
	}
	if len(f.packets) != 1 {
		t.Fatal(f.packets)
	}
}

func TestAmmoSelectionFromKnownOwnedQuiverDoesNotMoveItems(t *testing.T) {
	s, f := ammoFixture(t)
	source := f.w.InventoryBags[0].Slots[2]
	f.w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2}
	bag := f.w.InventoryBags[1]
	bag.ContainerGuid = 0x4000000000000050
	bag.Entry = 2101
	bag.MetadataKnown = true
	bag.SlotCount = 6
	for n := uint32(0); n < 6; n++ {
		bag.Slots = append(bag.Slots, &worldv1.BackpackSlot{Bag: 1, Slot: n})
	}
	source.Bag = 1
	bag.Slots[2] = source
	s.metadata[2101] = itemMetadata{known: true, entry: 2101, class: 1, subclass: 2, inventoryType: 18, containerSlots: 6, stackable: 1, bagFamily: 1}
	s.queried[2101] = true
	r := ammoRequest(t, s)
	r.Source.Bag = 1
	ammoProof(f)
	if _, err := s.SelectAmmo(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	st := waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || len(f.packets) != 1 || st.InventoryBags[1].Slots[2].ItemGuid != ammoGUID || st.InventoryBags[1].Slots[2].StackCount != 200 {
		t.Fatal(st, f.packets)
	}
}

func TestAmmoWeaponTypeIsReadFromCompleteItemQuery(t *testing.T) {
	p := metadataFixture(9400, 2, 2, 15, 0)
	prefix := 16 + len("Fixture") + 4
	ammoOffset := prefix + 88 + 8 + 24 + 28 + 4
	binary.LittleEndian.PutUint32(p[ammoOffset:], 2)
	m, err := parseItemMetadata(p)
	if err != nil || !m.known || m.ammoType != 2 {
		t.Fatal(m, err)
	}
}
