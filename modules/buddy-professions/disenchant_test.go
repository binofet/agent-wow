package buddyprofessions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"

	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const disenchantBehaviorTarget uint64 = 0x4000000000000099

func disenchantBehaviorCast(goEvent bool, caster, target uint64, count byte) []byte {
	p := protocolPackedFixture(caster)
	p = append(p, protocolPackedFixture(caster)...)
	p = append(p, count)
	p = binary.LittleEndian.AppendUint32(p, 13262)
	p = binary.LittleEndian.AppendUint32(p, 0x100)
	p = binary.LittleEndian.AppendUint32(p, 3000)
	if goEvent {
		p = append(p, 0, 0)
	}
	p = binary.LittleEndian.AppendUint32(p, 0x10)
	return append(p, protocolPackedFixture(target)...)
}

func disenchantBehaviorLoot(n int) []byte {
	p := binary.LittleEndian.AppendUint64(nil, disenchantBehaviorTarget)
	p = append(p, 4)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = append(p, byte(n))
	for i := 0; i < n; i++ {
		row := make([]byte, 22)
		row[0] = byte(i)
		binary.LittleEndian.PutUint32(row[1:], 10940+uint32(i))
		binary.LittleEndian.PutUint32(row[5:], 2)
		p = append(p, row...)
	}
	return p
}

func disenchantBehaviorFixture(t *testing.T) (*Service, *professionSessionFixture) {
	t.Helper()
	s, f := newProfessionServiceFixture(t)
	f.mu.Lock()
	w := f.world
	w.Self.PositionRevision = 11
	w.Self.ChannelObjectKnown, w.Self.ChannelSpellKnown = true, true
	w.SelfSkills[1] = &worldv1.SkillState{Slot: 1, SkillId: 333, Value: 75, MaxValue: 75, Step: 1}
	b := w.InventoryBags[1]
	b.ContainerGuid, b.Entry, b.MetadataKnown, b.SlotCount = 0x4000000000000066, 41599, true, 20
	for slot := range 20 {
		b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: 1, Slot: uint32(slot)})
	}
	b.Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0, ItemGuid: disenchantBehaviorTarget, Entry: 1001, StackCount: 1, MetadataKnown: true}
	f.mu.Unlock()
	professionPacket(t, s, opcode.SMSGInitialSpells, professionInitialFixture(3275, 2538, 2540, 7620, 8613, 2366, 13262))
	s.mu.Lock()
	s.selectedDisenchantGUID = disenchantBehaviorTarget
	s.deRequested[1001] = true
	s.mu.Unlock()
	p, _, _ := disenchantMetadataFixture(1001, 2, 2, 75)
	professionPacket(t, s, opcode.SMSGItemQuerySingleResponse, p)
	if err := s.observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, f
}

func disenchantBehaviorDirect(t *testing.T, s *Service, f *professionSessionFixture) *operation {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.mu.Lock()
	spec, _ := recipeForSpell(13262)
	op := &operation{request: &professionsv1.AttemptRequest{SessionId: "body", RequestId: "de-direct", SkillId: 333, SpellId: 13262, TargetGuid: disenchantBehaviorTarget}, spec: spec, baseline: s.view, token: "de-owned-token", ctx: ctx, cancel: cancel, done: make(chan struct{}), stored: map[byte]bool{}, removed: map[byte]bool{}, pushed: map[byte]bool{}, pushCounts: map[uint32]uint32{}, receipt: &professionsv1.Operation{TargetGuid: disenchantBehaviorTarget, TargetEntry: 1001, SourceBag: 1, SourceSlot: 0, CastCount: 9, Status: professionsv1.AttemptStatus_RESERVED, BeforeRank: 75, BaselineSkillsRevision: s.view.skillsRevision, BaselineInventoryRevision: s.view.inventoryRevision}}
	s.active, s.last = op, op
	s.mu.Unlock()
	f.mu.Lock()
	f.token = op.token
	f.actions.InteractionOwner, f.actions.InteractionLeaseRemainingMs = "professions", 30000
	f.mu.Unlock()
	return op
}

func disenchantBehaviorRequest(s *Service, id string) *professionsv1.AttemptRequest {
	r := professionRequest(s, id, 13262)
	r.TargetGuid = disenchantBehaviorTarget
	return r
}

func disenchantBehaviorOwnWindow(t *testing.T, s *Service, op *operation, n int) {
	t.Helper()
	op.castReserved = true
	professionPacket(t, s, opcode.SMSGSpellGo, disenchantBehaviorCast(true, 7, disenchantBehaviorTarget, byte(op.receipt.CastCount)))
	professionPacket(t, s, opcode.SMSGLootResponse, disenchantBehaviorLoot(n))
}

func disenchantBehaviorOutput(f *professionSessionFixture, consume bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.world.InventoryBags[0].Slots[3] = &worldv1.BackpackSlot{Slot: 3, ItemGuid: 0x4000000000000555, Entry: 10940, StackCount: 2, MetadataKnown: true}
	if consume {
		f.world.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0}
	}
	f.world.InventoryRevision++
	f.world.BackpackRevision++
}

func disenchantBehaviorMaterialReceipts(t *testing.T, s *Service, release bool) {
	t.Helper()
	professionPacket(t, s, opcode.SMSGLootRemoved, []byte{0})
	p := professionPushReceipt(7, false, false, 10940, 2, 2)
	binary.LittleEndian.PutUint32(p[21:], 26)
	professionPacket(t, s, opcode.SMSGItemPushResult, p)
	if release {
		professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(disenchantBehaviorTarget), 1))
	}
}

func TestDisenchantFinalWorldAndExactLeaseBindSelectedSource(t *testing.T) {
	for _, mode := range []string{"success", "dead", "combat", "self_pose", "map", "unknown_inventory", "inventory_revision", "skill_revision", "pure_skill", "channel", "gone", "moved", "entry", "stack", "capacity", "replacement_token", "expired_lease", "follow_toggle"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			f.mu.Lock()
			if mode == "replacement_token" {
				f.token = "replacement"
			}
			if mode == "expired_lease" {
				f.actions.InteractionLeaseRemainingMs = 0
			}
			if mode == "follow_toggle" {
				f.actions.Following = true
			}
			f.worldHook = func(r *worldv1.SnapshotRequest) {
				if r.CreatureGuid != 0 || r.GameObjectEntry != 0 {
					t.Error("selected item became public target lookup", r)
				}
				w := f.world
				switch mode {
				case "dead":
					w.Self.Alive = false
				case "combat":
					w.Self.UnitFlags |= 0x80000
				case "self_pose":
					w.Self.PositionRevision++
				case "map":
					w.Self.MapId++
				case "unknown_inventory":
					w.InventoryKnown = false
				case "inventory_revision":
					w.InventoryRevision++
				case "skill_revision":
					w.SelfSkillsRevision++
				case "pure_skill":
					w.SelfSkills[1].Value = 74
				case "channel":
					w.Self.ChannelSpellId = 13262
				case "gone":
					w.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0}
				case "moved":
					row := w.InventoryBags[1].Slots[0]
					w.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0}
					row.Slot = 1
					w.InventoryBags[1].Slots[1] = row
				case "entry":
					w.InventoryBags[1].Slots[0].Entry++
				case "stack":
					w.InventoryBags[1].Slots[0].StackCount = 2
				case "capacity":
					for _, bag := range w.InventoryBags {
						for i, row := range bag.Slots {
							if row.ItemGuid == 0 {
								bag.Slots[i] = &worldv1.BackpackSlot{Bag: bag.Bag, Slot: row.Slot, ItemGuid: 0x4000000000001000 + uint64(bag.Bag)*100 + uint64(i), Entry: 7070, StackCount: 1, MetadataKnown: true}
							}
						}
					}
				}
			}
			f.mu.Unlock()
			attempted, err := s.disenchantWrite(op.ctx, op, opcode.CMSGCastSpell, disenchantCastPacket(9, disenchantBehaviorTarget))
			if mode == "success" {
				if !attempted || err != nil {
					t.Fatal(attempted, err)
				}
				f.mu.Lock()
				events := append([]string(nil), f.rpcEvents...)
				f.mu.Unlock()
				want := []string{"buddy-actions/acquire_interaction", "buddy-world/snapshot", fmt.Sprintf("send:%x", opcode.CMSGCastSpell)}
				if !reflect.DeepEqual(events[len(events)-3:], want) {
					t.Fatal("RPC after LAST world", events)
				}
			} else if attempted || err == nil || len(f.packets) != 0 {
				t.Fatal("changed final proof wrote", attempted, err, f.packets)
			}
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func TestDisenchantOneFiniteCycleConsumesExactBagItemAndNeverReleases(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		s.mu.Lock()
		count := byte(s.active.receipt.CastCount)
		s.mu.Unlock()
		switch p.Opcode {
		case opcode.CMSGCastSpell:
			professionPacket(t, s, opcode.SMSGSpellStart, disenchantBehaviorCast(false, 7, disenchantBehaviorTarget, count))
			professionPacket(t, s, opcode.SMSGSpellGo, disenchantBehaviorCast(true, 7, disenchantBehaviorTarget, count))
			professionPacket(t, s, opcode.SMSGLootResponse, disenchantBehaviorLoot(1))
		case opcode.CMSGAutostoreLootItem:
			disenchantBehaviorOutput(f, true)
			disenchantBehaviorMaterialReceipts(t, s, true)
		default:
			t.Errorf("forbidden extra packet%x", p.Opcode)
		}
	}
	r := disenchantBehaviorRequest(s, "de-full-cycle")
	if _, err := s.Attempt(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	op := s.last
	s.mu.Unlock()
	professionWaitDone(t, op)
	got := professionReceipt(s, op)
	if got.Status != professionsv1.AttemptStatus_COMPLETED || !got.CastTransportWritten || !got.OwnGoObserved || !got.LootWindowObserved || !got.InputConsumedObserved || !got.OutputInventoryObserved || !got.LootReleaseObserved || got.LootSlotsRemoved != 1 || got.OwnLootedItemsObserved != 2 || got.SourceBag != 1 || got.SourceSlot != 0 || got.TargetEntry != 1001 || !got.LeaseReleased || got.CleanupUnconfirmed || got.SkillIncreased || s.pendingDisenchant != nil {
		t.Fatal(got)
	}
	if len(f.packets) != 2 || f.packets[0].Opcode != opcode.CMSGCastSpell || f.packets[1].Opcode != opcode.CMSGAutostoreLootItem {
		t.Fatal(f.packets)
	}
	if _, err := s.Attempt(context.Background(), r); err != nil || len(f.packets) != 2 {
		t.Fatal("same ID replayed", err, f.packets)
	}
}

func TestDisenchantAmbiguousStoreRetainsPendingAndLateProofNeverWrites(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial%t", partial), func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			f.sendErr[opcode.CMSGAutostoreLootItem] = errors.New("ambiguous write")
			f.beforeSend = func(p *modv1.SendPacketRequest) {
				s.mu.Lock()
				count := byte(s.active.receipt.CastCount)
				s.mu.Unlock()
				if p.Opcode == opcode.CMSGCastSpell {
					professionPacket(t, s, opcode.SMSGSpellGo, disenchantBehaviorCast(true, 7, disenchantBehaviorTarget, count))
					n := 1
					if partial {
						n = 2
					}
					professionPacket(t, s, opcode.SMSGLootResponse, disenchantBehaviorLoot(n))
				} else if p.Opcode == opcode.CMSGAutostoreLootItem {
					disenchantBehaviorOutput(f, !partial)
					disenchantBehaviorMaterialReceipts(t, s, !partial)
				} else {
					t.Error("cleanup sent item-window packet", p.Opcode)
				}
			}
			r := disenchantBehaviorRequest(s, "de-ambiguous-store")
			if _, err := s.Attempt(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			op := s.last
			s.mu.Unlock()
			professionWaitDone(t, op)
			if s.pendingDisenchant != op || !op.receipt.CleanupUnconfirmed || !op.receipt.LeaseReleased || op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || len(f.packets) != 2 {
				t.Fatal("worker discarded unresolved receipt", op.receipt, f.packets)
			}
			state, err := s.GetState(context.Background(), &emptypb.Empty{})
			if err != nil || len(f.packets) != 2 {
				t.Fatal("read-only reconciliation wrote", err, f.packets)
			}
			if partial {
				if s.pendingDisenchant != op || !state.Operation.CleanupUnconfirmed || state.Operation.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED {
					t.Fatal(state)
				}
				if _, err := s.Attempt(context.Background(), professionRequest(s, "blocked-other-skill", 3275)); err == nil || len(f.packets) != 2 {
					t.Fatal("pending materials permitted another attempt", err)
				}
			} else if s.pendingDisenchant != nil || state.Operation.Status != professionsv1.AttemptStatus_COMPLETED || state.Operation.CleanupUnconfirmed || !state.Operation.InputConsumedObserved || !state.Operation.OutputInventoryObserved {
				t.Fatal("late exact proof did not reconcile", state)
			}
			if _, err := s.Attempt(context.Background(), r); err != nil || len(f.packets) != 2 {
				t.Fatal("old request replayed", err)
			}
		})
	}
}

func TestDisenchantProofRequiresPositiveMaterialsExactUnionAndAbsentGUID(t *testing.T) {
	for _, mode := range []string{"success", "empty", "source_present", "unrelated_loss", "unrelated_gain", "same_entry_material", "unchanged_revision", "missing_push", "missing_release", "prior_reject", "lease_unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			n := 1
			if mode == "empty" {
				n = 0
			}
			disenchantBehaviorOwnWindow(t, s, op, n)
			if mode == "prior_reject" {
				professionPacket(t, s, opcode.SMSGCastFailed, []byte{9, 0xce, 0x33, 0, 0, 121})
			}
			s.mu.Lock()
			op.stored[0], op.removed[0], op.pushed[0] = true, true, mode != "missing_push"
			op.receipt.LootReleaseObserved = mode != "missing_release"
			op.receipt.SkillIncreased = true // Skill increase never substitutes for materials.
			w := proto.Clone(op.baseline.raw).(*worldv1.SnapshotResponse)
			w.InventoryRevision++
			w.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0}
			if mode != "empty" {
				w.InventoryBags[0].Slots[3] = &worldv1.BackpackSlot{Slot: 3, ItemGuid: 0x4000000000000888, Entry: 10940, StackCount: 2, MetadataKnown: true}
			}
			switch mode {
			case "source_present":
				w.InventoryBags[1].Slots[0] = proto.Clone(op.baseline.raw.InventoryBags[1].Slots[0]).(*worldv1.BackpackSlot)
			case "unrelated_loss":
				w.InventoryBags[0].Slots[0].StackCount--
			case "unrelated_gain":
				w.InventoryBags[0].Slots[0].StackCount++
			case "same_entry_material":
				op.window.slots[0].entry = 1001
				w.InventoryBags[0].Slots[3].Entry = 1001
			case "unchanged_revision":
				w.InventoryRevision = op.baseline.inventoryRevision
			}
			v, err := validateWorld(w, 7)
			if err != nil {
				t.Fatal(err)
			}
			s.view = v
			if mode == "lease_unknown" {
				s.pendingDisenchant = op
			}
			s.reconcileDisenchantLocked(op)
			wantComplete := mode == "success" || mode == "same_entry_material"
			wantOutput := wantComplete || mode == "prior_reject" || mode == "lease_unknown"
			if (op.receipt.Status == professionsv1.AttemptStatus_COMPLETED) != wantComplete || op.receipt.OutputInventoryObserved != wantOutput {
				t.Fatal(mode, op.receipt)
			}
			if mode == "prior_reject" && (op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || op.receipt.FailureCode != 121 || op.receipt.Reason != "actual_selected_disenchant_server_cast_failure_121") {
				t.Fatal("late facts erased definitive rejection", op.receipt)
			}
			if mode == "lease_unknown" && (!op.receipt.CleanupUnconfirmed || s.pendingDisenchant != op || op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED) {
				t.Fatal("material proof erased unknown lease", op.receipt)
			}
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func TestDisenchantStoreRechecksLastWorldAndCurrentWindow(t *testing.T) {
	for _, mode := range []string{"success", "combat", "source_moved", "source_gone", "pure_skill", "channel", "foreign_window"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			disenchantBehaviorOwnWindow(t, s, op, 1)
			f.mu.Lock()
			f.worldHook = func(_ *worldv1.SnapshotRequest) {
				w := f.world
				switch mode {
				case "combat":
					w.Self.UnitFlags |= 0x80000
				case "source_moved":
					row := w.InventoryBags[1].Slots[0]
					w.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0}
					row.Slot = 1
					w.InventoryBags[1].Slots[1] = row
				case "source_gone":
					w.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0}
				case "pure_skill":
					w.SelfSkills[1].Value = 74
				case "channel":
					w.Self.ChannelSpellId = 13262
				case "foreign_window":
					p := disenchantBehaviorLoot(1)
					binary.LittleEndian.PutUint64(p, disenchantBehaviorTarget+1)
					professionPacket(t, s, opcode.SMSGLootResponse, p)
				}
			}
			f.mu.Unlock()
			done := s.stepDisenchant(op)
			if mode == "success" {
				if done || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAutostoreLootItem {
					t.Fatal(done, f.packets)
				}
				want := []string{"buddy-actions/acquire_interaction", "buddy-world/snapshot", fmt.Sprintf("send:%x", opcode.CMSGAutostoreLootItem)}
				if !reflect.DeepEqual(f.rpcEvents[len(f.rpcEvents)-3:], want) {
					t.Fatal(f.rpcEvents)
				}
			} else if !done || len(f.packets) != 0 {
				t.Fatal("last-read store change wrote", done, f.packets)
			}
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func TestDisenchantForeignWindowPermanentlyInvalidatesSlotReceipts(t *testing.T) {
	for _, mode := range []string{"foreign", "wrong_type", "malformed", "duplicate", "foreign_release"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			disenchantBehaviorOwnWindow(t, s, op, 1)
			p := disenchantBehaviorLoot(1)
			switch mode {
			case "foreign":
				binary.LittleEndian.PutUint64(p, disenchantBehaviorTarget+1)
			case "wrong_type":
				p[8] = 6
			case "malformed":
				p = p[:13]
			}
			if mode == "foreign_release" {
				professionPacket(t, s, opcode.SMSGLootReleaseResponse, append(fullGUIDPacket(disenchantBehaviorTarget+1), 1))
			} else {
				professionPacket(t, s, opcode.SMSGLootResponse, p)
			}
			op.stored[0] = true
			professionPacket(t, s, opcode.SMSGLootResponse, disenchantBehaviorLoot(1))
			disenchantBehaviorMaterialReceipts(t, s, true)
			if !s.stepDisenchant(op) || !op.windowInvalid || !op.receipt.CleanupUnconfirmed || op.receipt.LootSlotsRemoved != 0 || op.receipt.OwnLootedItemsObserved != 0 || op.receipt.LootReleaseObserved || len(f.packets) != 0 {
				t.Fatal("foreign window retained authority", op.receipt, f.packets)
			}
			if attempted, err := s.disenchantWrite(context.Background(), op, opcode.CMSGLootRelease, releaseLootPacket(disenchantBehaviorTarget)); attempted || err == nil || len(f.packets) != 0 {
				t.Fatal("DE release is reachable", attempted, err)
			}
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func TestDisenchantLatePacketsAfterWorkerDoneRemainReadOnlyPending(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	f.sendErr[opcode.CMSGCastSpell] = errors.New("cast transport ambiguous")
	r := disenchantBehaviorRequest(s, "de-late-window")
	if _, err := s.Attempt(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	op := s.last
	s.mu.Unlock()
	professionWaitDone(t, op)
	if s.active != nil || s.pendingDisenchant != op || len(f.packets) != 1 {
		t.Fatal(op.receipt)
	}
	professionPacket(t, s, opcode.SMSGSpellGo, disenchantBehaviorCast(true, 7, disenchantBehaviorTarget, byte(op.receipt.CastCount)))
	professionPacket(t, s, opcode.SMSGLootResponse, disenchantBehaviorLoot(1))
	if !op.receipt.OwnGoObserved || !op.receipt.LootWindowObserved || !op.receipt.CleanupUnconfirmed || s.pendingDisenchant != op || s.active != nil {
		t.Fatal("late packet discarded or restarted worker", op.receipt)
	}
	if _, err := s.GetState(context.Background(), &emptypb.Empty{}); err != nil || len(f.packets) != 1 || s.pendingDisenchant != op {
		t.Fatal("late window performed recovery writes", err, f.packets)
	}
	if _, err := s.Attempt(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("late request replayed cast", err)
	}
}

func TestDisenchantPostAcquireSourceCannotRebindAttempt(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	op := disenchantBehaviorDirect(t, s, f)
	s.mu.Lock()
	op.token = ""
	s.mu.Unlock()
	f.mu.Lock()
	f.token, f.actions.InteractionOwner = "", ""
	f.worldHook = func(_ *worldv1.SnapshotRequest) {
		row := f.world.InventoryBags[1].Slots[0]
		if row.ItemGuid == disenchantBehaviorTarget {
			f.world.InventoryBags[1].Slots[0] = &worldv1.BackpackSlot{Bag: 1, Slot: 0}
			row.Slot = 1
			f.world.InventoryBags[1].Slots[1] = row
		}
	}
	f.mu.Unlock()
	go s.runDisenchant(op)
	professionWaitDone(t, op)
	if len(f.packets) != 0 || op.receipt.SourceBag != 1 || op.receipt.SourceSlot != 0 || op.receipt.TargetEntry != 1001 || op.receipt.Status != professionsv1.AttemptStatus_BLOCKED || op.castReserved || s.pendingDisenchant != nil {
		t.Fatal("source silently rebound after reservation", op.receipt, f.packets)
	}
}

func TestDisenchantCasterReceiptsRequireActualOwnActorAndItem(t *testing.T) {
	for _, mode := range []string{"zero_start", "zero_go", "foreign_go", "foreign_target", "wrong_count", "zero_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			op.castReserved = true
			caster, target, count, code := uint64(7), disenchantBehaviorTarget, byte(9), uint32(opcode.SMSGSpellGo)
			switch mode {
			case "zero_start":
				caster, code = 0, opcode.SMSGSpellStart
			case "zero_go":
				caster = 0
			case "foreign_go":
				caster = 8
			case "foreign_target":
				target++
			case "wrong_count":
				count++
			case "zero_failure":
				caster, code = 0, opcode.SMSGSpellFailure
			}
			p := disenchantBehaviorCast(code == opcode.SMSGSpellGo, caster, target, count)
			if code == opcode.SMSGSpellFailure {
				p = append(protocolPackedFixture(caster), 9, 0xce, 0x33, 0, 0, 121)
			}
			professionPacket(t, s, code, p)
			if op.receipt.OwnStartObserved || op.receipt.OwnGoObserved || op.receipt.FailureCode != 0 || op.receipt.Status != professionsv1.AttemptStatus_RESERVED {
				t.Fatal("foreign cast became own", op.receipt)
			}
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func TestDisenchantTargetGatesExplicitSourceTemplateAndControllers(t *testing.T) {
	for _, mode := range []string{"ready", "armor", "stack", "equipped", "template_missing", "quality", "class", "container", "minus_one", "pure_below", "attempted", "follow", "heal_lease", "assist", "trade_foreign", "trade_partner", "trade_accept", "junk_unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := disenchantBehaviorFixture(t)
			s.mu.Lock()
			m := s.deTemplates[1001]
			switch mode {
			case "armor":
				m.class = 4
			case "stack":
				s.view.raw.InventoryBags[1].Slots[0].StackCount = 2
			case "equipped":
				s.view.self.Offhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: disenchantBehaviorTarget}
			case "template_missing":
				m.known = false
			case "quality":
				m.quality = 1
			case "class":
				m.class = 15
			case "container":
				m.containerSlots = 1
			case "minus_one":
				m.requiredSkill = ^uint32(0)
			case "pure_below":
				m.requiredSkill = 76
			case "attempted":
				s.deAttempted[disenchantBehaviorTarget] = true
			case "follow":
				s.actions.Following = true
			case "heal_lease":
				s.actions.HealingLeaseRemainingMs = 1
			case "assist":
				s.actions.Assisting = true
			case "trade_foreign":
				s.deTrade.SessionId = "foreign"
			case "trade_partner":
				s.deTrade.PartnerGuid = 8
			case "trade_accept":
				s.deTrade.AcceptTransportWritten = true
			case "junk_unknown":
				s.deTrade.LastJunkDisposal = &tradev1.JunkDisposal{Status: "UNCONFIRMED_NO_RETRY"}
			}
			s.deTemplates[1001] = m
			_, reason := s.disenchantTargetLocked(disenchantBehaviorTarget, nil)
			item := s.disenchantItemLocked()
			want := mode == "ready" || mode == "armor"
			if (reason == "") != want || item.Ready != want {
				t.Fatal(mode, reason, item)
			}
			s.mu.Unlock()
		})
	}
}
