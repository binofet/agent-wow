package buddyinventory

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const breechesGUID = uint64(0x4000000000000031)
const oldPantsGUID = uint64(0x4000000000000032)

func gearMetadataFixture(entry, armor uint32) []byte {
	p := metadataFixture(entry, 4, 1, 7, 0)
	binary.LittleEndian.PutUint32(p[8:], 2) // Leather, ordinary legs.
	at := 16 + len("Fixture") + 4
	for _, index := range []int{7, 8} {
		binary.LittleEndian.PutUint32(p[at+4*index:], math.MaxUint32)
	}
	binary.LittleEndian.PutUint32(p[at+4*10:], 1)
	binary.LittleEndian.PutUint32(p[at+4*19:], 1)
	binary.LittleEndian.PutUint32(p[at+88+8+24:], armor)
	binary.LittleEndian.PutUint32(p[len(p)-29*4+11*4:], 20)
	return p
}
func fixtureGear(t *testing.T, occupied bool) (*Service, *fakeInventory) {
	t.Helper()
	s, f := fixtureInventory()
	p := f.w.Self
	p.ClassKnown, p.ClassId, p.RaceKnown, p.RaceId, p.LevelKnown, p.Level = true, 4, true, 1, true, 5
	f.w.SelfSkillsKnown, f.w.SelfSkillsRevision = true, 1
	for n := uint32(0); n < 128; n++ {
		f.w.SelfSkills = append(f.w.SelfSkills, &worldv1.SkillState{Slot: n})
	}
	f.w.SelfSkills[0] = &worldv1.SkillState{Slot: 0, SkillId: 414, Value: 1, MaxValue: 1}
	f.w.SelfEquipmentKnown, f.w.SelfEquipmentRevision = true, 1
	for n := uint32(0); n < 19; n++ {
		f.w.SelfEquipment = append(f.w.SelfEquipment, &worldv1.EquipmentSlotState{Slot: n, SlotKnown: true})
	}
	f.w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: breechesGUID, Entry: 2962, StackCount: 1, MetadataKnown: true, DurabilityKnown: true, Durability: 20, MaxDurability: 20, EnhancementsKnown: true}
	var err error
	s.metadata[2962], err = parseItemMetadata(gearMetadataFixture(2962, 45))
	if err != nil {
		t.Fatal(err)
	}
	s.queried[2962] = true
	if occupied {
		f.w.SelfEquipment[6] = &worldv1.EquipmentSlotState{Slot: 6, SlotKnown: true, ItemGuid: oldPantsGUID, MetadataKnown: true, Entry: 47, StackCount: 1, DurabilityKnown: true, Durability: 18, MaxDurability: 20, EnhancementsKnown: true}
		s.metadata[47], err = parseItemMetadata(gearMetadataFixture(47, 20))
		if err != nil {
			t.Fatal(err)
		}
		s.queried[47] = true
	}
	return s, f
}
func gearRequest(t *testing.T, s *Service) *inventoryv1.EquipGearRequest {
	t.Helper()
	st := stateInventory(t, s)
	return &inventoryv1.EquipGearRequest{SessionId: "body", RequestId: "equip-pants", ExpectedRevision: st.Revision, ExpectedInventoryRevision: st.InventoryRevision, Source: &inventoryv1.ItemRef{Slot: 2, ItemGuid: breechesGUID, Entry: 2962, StackCount: 1}, EquipmentSlot: 6, ExpectedEquipmentRevision: st.EquipmentRevision, ExpectedEquippedItemGuid: st.Equipment[6].ItemGuid, RequireConservativeUpgrade: true}
}
func gearProof(s *Service, f *fakeInventory) {
	f.afterSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode != opcode.CMSGAutoequipItemSlot {
			return
		}
		s.mu.Lock()
		w := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
		s.mu.Unlock()
		w.InventoryRevision++
		w.SelfEquipmentRevision++
		f.mu.Lock()
		f.w = w
		f.mu.Unlock()
	}
}

func TestGearNormalEquipAndReplacementRequireActualFullReceipt(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "replacement"}[occupied], func(t *testing.T) {
			s, f := fixtureGear(t, occupied)
			r := gearRequest(t, s)
			gearProof(s, f)
			// Actual server proof wins even if SendPacket returns an ambiguous error.
			f.sendError = true
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			st := waitInventory(t, s)
			if st.Operation.Status != "CONFIRMED" || !st.Operation.InventoryConfirmed || !st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased || st.Operation.TransportWritten {
				t.Fatal(st)
			}
			if st.Equipment[6].ItemGuid != breechesGUID || len(f.packets) != 1 || f.leases != 1 || f.renewals != 1 || f.releases != 1 {
				t.Fatal(st, f.packets)
			}
			p := f.packets[0]
			if p.Opcode != opcode.CMSGAutoequipItemSlot || len(p.Payload) != 9 || binary.LittleEndian.Uint64(p.Payload) != breechesGUID || p.Payload[8] != 6 {
				t.Fatal(p)
			}
			want := uint64(0)
			if occupied {
				want = oldPantsGUID
			}
			if got := st.InventoryBags[0].Slots[2].ItemGuid; got != want {
				t.Fatal("displaced item lost", got, want)
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil || len(f.packets) != 1 {
				t.Fatal("replay wrote", err)
			}
			changed := proto.Clone(r).(*inventoryv1.EquipGearRequest)
			changed.ExpectedEquippedItemGuid = 99
			if _, err := s.EquipGear(context.Background(), changed); err == nil {
				t.Fatal("changed replay accepted")
			}
		})
	}
}

func TestGearUsabilityAndConflictGatesNeverWrite(t *testing.T) {
	tests := map[string]func(*Service, *fakeInventory, *inventoryv1.EquipGearRequest){
		"foreign source":     func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { r.Source.ItemGuid++ },
		"wrong slot":         func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { r.EquipmentSlot = 7 },
		"stale equipment":    func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { r.ExpectedEquipmentRevision++ },
		"displaced mismatch": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { r.ExpectedEquippedItemGuid++ },
		"level": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			m := s.metadata[2962]
			m.requiredLevel = 6
			s.metadata[2962] = m
		},
		"unknown race": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { f.w.Self.RaceKnown = false },
		"class restriction": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			m := s.metadata[2962]
			m.allowableClass = 1
			s.metadata[2962] = m
		},
		"race restriction": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			m := s.metadata[2962]
			m.allowableRace = 2
			s.metadata[2962] = m
		},
		"unknown skills": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { f.w.SelfSkillsKnown = false },
		"partial skills": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.w.SelfSkills = f.w.SelfSkills[:127]
		},
		"no leather proficiency": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.w.SelfSkills[0] = &worldv1.SkillState{Slot: 0}
		},
		"required skill": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			m := s.metadata[2962]
			m.requiredSkill = 165
			m.requiredSkillRank = 5
			s.metadata[2962] = m
		},
		"spell requirement": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			m := s.metadata[2962]
			m.requiredSpell = 123
			s.metadata[2962] = m
		},
		"broken": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.w.InventoryBags[0].Slots[2].Durability = 0
		},
		"unknown durability": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.w.InventoryBags[0].Slots[2].DurabilityKnown = false
		},
		"unknown equipment": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { f.w.SelfEquipmentKnown = false },
		"partial equipment": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.w.SelfEquipment = f.w.SelfEquipment[:18]
		},
		"duplicate equipment": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.w.SelfEquipment[0] = proto.Clone(f.w.SelfEquipment[6]).(*worldv1.EquipmentSlotState)
			f.w.SelfEquipment[0].Slot = 0
		},
		"combat":    func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { f.w.Self.UnitFlags |= 0x80000 },
		"cast":      func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { f.a.CastPending = true },
		"assisting": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) { f.a.Assisting = true },
		"trade": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.trade = &tradev1.State{SessionId: "body", Phase: "OPEN"}
		},
		"loot": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.loot = &lootv1.State{SessionId: "body", Enabled: true}
		},
		"DE active": func(s *Service, f *fakeInventory, r *inventoryv1.EquipGearRequest) {
			f.prof = &professionsv1.State{SessionId: "body", SelfGuid: 6, Active: true}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			s, f := fixtureGear(t, true)
			r := gearRequest(t, s)
			change(s, f, r)
			r.ExpectedRevision = stateInventory(t, s).Revision
			_, err := s.EquipGear(context.Background(), r)
			if err == nil {
				st := waitInventory(t, s)
				if st.Operation.Status != "BLOCKED" {
					t.Fatal(st)
				}
			}
			if len(f.packets) != 0 {
				t.Fatal("unsafe equip wrote", f.packets)
			}
		})
	}
}

func TestGearProofRejectsPartialLossAndUnrelatedUpdates(t *testing.T) {
	for _, mode := range []string{"inventory-only", "equipment-only", "old-item-lost", "wrong-slot", "unrelated-item-lost", "no-new-equipment-revision"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixtureGear(t, true)
			r := gearRequest(t, s)
			f.afterSend = func(p *modv1.SendPacketRequest) {
				s.mu.Lock()
				w := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
				s.last.cancel()
				s.mu.Unlock()
				w.InventoryRevision++
				w.SelfEquipmentRevision++
				switch mode {
				case "inventory-only":
					w.SelfEquipment = proto.Clone(f.w).(*worldv1.SnapshotResponse).SelfEquipment
				case "equipment-only":
					w.InventoryBags = proto.Clone(f.w).(*worldv1.SnapshotResponse).InventoryBags
				case "old-item-lost":
					w.InventoryBags[0].Slots[2] = &worldv1.BackpackSlot{Slot: 2}
				case "wrong-slot":
					w.SelfEquipment[7] = proto.Clone(w.SelfEquipment[6]).(*worldv1.EquipmentSlotState)
					w.SelfEquipment[7].Slot = 7
					w.SelfEquipment[6] = &worldv1.EquipmentSlotState{Slot: 6, SlotKnown: true}
				case "unrelated-item-lost":
					w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{Slot: 0}
				case "no-new-equipment-revision":
					w.SelfEquipmentRevision--
				}
				f.mu.Lock()
				f.w = w
				f.mu.Unlock()
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			st := waitInventory(t, s)
			if st.Operation.Status != "OUTCOME_UNCONFIRMED" || !st.Operation.LeaseReleased {
				t.Fatal(st)
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil || len(f.packets) != 1 {
				t.Fatal("ambiguous replay", err)
			}
			next := proto.Clone(r).(*inventoryv1.EquipGearRequest)
			next.RequestId = "new-after-uncertainty"
			next.ExpectedRevision = stateInventory(t, s).Revision
			if _, err := s.EquipGear(context.Background(), next); err == nil {
				t.Fatal("new request after ambiguous outcome")
			}
		})
	}
}

func TestGearFinalLeaseRevalidationAndErrorCorrelation(t *testing.T) {
	for _, when := range []string{"acquire", "renew"} {
		t.Run(when, func(t *testing.T) {
			s, f := fixtureGear(t, true)
			r := gearRequest(t, s)
			hook := func() { f.mu.Lock(); f.w.SelfEquipmentRevision++; f.mu.Unlock() }
			if when == "acquire" {
				f.afterLease = hook
			} else {
				f.afterRenew = hook
			}
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			st := waitInventory(t, s)
			if st.Operation.Status != "BLOCKED" || !st.Operation.LeaseReleased || len(f.packets) != 0 {
				t.Fatal(st)
			}
		})
	}
	s, f := fixtureGear(t, true)
	r := gearRequest(t, s)
	f.afterSend = func(p *modv1.SendPacketRequest) {
		// Success-code0 and another item's error must not become equip success/failure.
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInventoryChangeFailure, Payload: []byte{0}})
		bad := append([]byte{2}, binary.LittleEndian.AppendUint64(nil, junkGUID)...)
		bad = append(bad, make([]byte, 9)...)
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInventoryChangeFailure, Payload: bad})
		if s.snapshot().Operation.Status != "REQUESTED" {
			t.Error("unrelated receipt consumed equip")
		}
		bad = append([]byte{2}, binary.LittleEndian.AppendUint64(nil, breechesGUID)...)
		bad = append(bad, make([]byte, 9)...)
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInventoryChangeFailure, Payload: bad})
	}
	s.EquipGear(context.Background(), r)
	st := waitInventory(t, s)
	if st.Operation.Status != "SERVER_REJECTED" || st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased {
		t.Fatal(st)
	}
}

func TestGearUpgradeDominanceAndEnhancedUnknownRefusal(t *testing.T) {
	s, f := fixtureGear(t, true)
	st := stateInventory(t, s)
	var item *inventoryv1.ItemEligibility
	for _, i := range st.Items {
		if i.Item.ItemGuid == breechesGUID {
			item = i
		}
	}
	if item == nil || !item.GearEquipEligible || !item.ConservativeUpgrade || item.GearEquipmentSlot != 6 || item.ExpectedEquippedItemGuid != oldPantsGUID {
		t.Fatal(item)
	}
	for _, mode := range []string{"enhanced-source", "unknown-source", "enhanced-current", "unknown-current", "armor-loss", "stat-loss", "equal"} {
		t.Run(mode, func(t *testing.T) {
			s, f = fixtureGear(t, true)
			source := f.w.InventoryBags[0].Slots[2]
			old := f.w.SelfEquipment[6]
			candidate, current := s.metadata[2962], s.metadata[47]
			switch mode {
			case "enhanced-source":
				source.HasEnhancements = true
			case "unknown-source":
				source.EnhancementsKnown = false
			case "enhanced-current":
				old.HasEnhancements = true
			case "unknown-current":
				old.EnhancementsKnown = false
			case "armor-loss":
				candidate.armor = 10
			case "stat-loss":
				current.statCount = 1
				current.stats[0] = itemStat{7, 1}
			case "equal":
				candidate.armor = current.armor
			}
			if conservativeArmorUpgrade(candidate, source, current, old) {
				t.Fatal("unsupported upgrade", mode)
			}
		})
	}
	s, f = fixtureGear(t, false)
	negative := s.metadata[2962]
	negative.statCount, negative.stats[0] = 1, itemStat{7, -1}
	if conservativeArmorUpgrade(negative, f.w.InventoryBags[0].Slots[2], itemMetadata{}, f.w.SelfEquipment[6]) {
		t.Fatal("negative stat armor labeled conservative upgrade to empty slot")
	}
	for _, kind := range []uint32{2, 8, 9, 10, 11, 40, 41, 42, 49} {
		undefined := s.metadata[2962]
		undefined.statCount, undefined.stats[0] = 1, itemStat{kind, 1}
		if conservativeArmorUpgrade(undefined, f.w.InventoryBags[0].Slots[2], itemMetadata{}, f.w.SelfEquipment[6]) {
			t.Fatal("undefined/deprecated modifier labeled upgrade", kind)
		}
	}
}

func TestGearMetadataFullFramesAndFiniteValues(t *testing.T) {
	p := gearMetadataFixture(2962, 45)
	m, err := parseItemMetadata(p)
	if err != nil || !m.ordinaryArmor() || m.armor != 45 || m.requiredLevel != 1 || m.maxDurability != 20 || m.name != "Fixture" {
		t.Fatal(m, err)
	}
	for n := range len(p) {
		m, err := parseItemMetadata(p[:n])
		if err == nil && m.ordinaryArmor() {
			t.Fatalf("truncated gear authorized at%d", n)
		}
	}
	at := 16 + len("Fixture") + 4
	for _, offset := range []int{at + 88 + 8, at + 88 + 8 + 24 + 28 + 8, len(p) - 29*4 + 25*4} {
		bad := append([]byte(nil), p...)
		binary.LittleEndian.PutUint32(bad[offset:], math.Float32bits(float32(math.NaN())))
		if _, err := parseItemMetadata(bad); err == nil {
			t.Fatal("nonfinite template accepted", offset)
		}
	}
	for _, change := range []func(*itemMetadata){func(m *itemMetadata) { m.class = 2 }, func(m *itemMetadata) { m.inventoryType = 17 }, func(m *itemMetadata) { m.itemLimitCategory = 1 }, func(m *itemMetadata) { m.hasSpell = true }, func(m *itemMetadata) { m.scalingDistribution = 1 }, func(m *itemMetadata) { m.specialArmor = true }} {
		bad := m
		change(&bad)
		if bad.ordinaryArmor() {
			t.Fatal("special gear admitted", bad)
		}
	}
}

func FuzzGearCompleteMetadata(f *testing.F) {
	f.Add(gearMetadataFixture(2962, 45))
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := parseItemMetadata(data)
		if err == nil && m.ordinaryArmor() {
			if len(data) < 4 || binary.LittleEndian.Uint32(data) != m.entry || m.name == "" || m.statCount > 10 {
				t.Fatal("incoherent parsed armor", m)
			}
		}
	})
}

func TestGearComparisonPendingIsExplicitAndQueriesEquippedTemplate(t *testing.T) {
	s, f := fixtureGear(t, true)
	delete(s.metadata, 47)
	delete(s.queried, 47)
	st := stateInventory(t, s)
	var candidate *inventoryv1.ItemEligibility
	for _, row := range st.Items {
		if row.Item.ItemGuid == breechesGUID {
			candidate = row
		}
	}
	if candidate == nil || candidate.GearEquipEligible || candidate.ConservativeUpgrade || candidate.GearBlockedReason != "displaced_item_template_pending" {
		t.Fatal(candidate)
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGItemQuerySingle || binary.LittleEndian.Uint32(f.packets[0].Payload) != 47 {
		t.Fatal("equipped template not queried", f.packets)
	}
	s, f = fixtureGear(t, true)
	f.w.InventoryBags[0].Slots[2].EnhancementsKnown = false
	st = stateInventory(t, s)
	for _, row := range st.Items {
		if row.Item.ItemGuid == breechesGUID && (row.GearBlockedReason != "enhancement_context_pending" || row.ConservativeUpgrade || row.GearEquipEligible) {
			t.Fatal(row)
		}
	}
}
