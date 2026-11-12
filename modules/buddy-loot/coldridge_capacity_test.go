package buddyloot

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Capacity depends on the carried layout, not the backpack alone: all16
// backpack slots and19 ordinary-bag slots are occupied, leaving bag1/slot19.
func cold170CapacityRig(t *testing.T, entry uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold170LootRig(t, entry)
	fillBackpack(f.world)
	extraCapacity(f.world, 41599)
	for slot := range 19 {
		f.world.InventoryBags[1].Slots[slot].ItemGuid = 0x4000000000001000 + uint64(slot)
	}
	f.world.InventoryRevision = 31
	if !ownKnownFreeSlot(f.world, 1) {
		t.Fatal("complete carried fixture lost its one general-bag free slot")
	}
	return s, f, advance
}

func cold170FillLastGeneralSlot(w *worldv1.SnapshotResponse) {
	w.InventoryBags[1].Slots[19].ItemGuid = 0x4000000000002000
}

// This identifies the response after STATUS and exact-token RENEW without
// depending on how many preflight observations precede the selected operation.
func cold170FinalProofRead(s *Service, f *fakeSession) bool {
	return s.state.PendingCorpseGuid != 0 && len(f.calls) >= 3 && slices.Equal(f.calls[len(f.calls)-3:], []string{
		"buddy-actions.status", "buddy-actions.acquire_interaction", "buddy-world.snapshot",
	})
}

func cold170AssertFinalWriteTail(t *testing.T, f *fakeSession, opening bool) {
	t.Helper()
	want := []string{"buddy-actions.status", "buddy-actions.acquire_interaction", "buddy-world.snapshot", "packet"}
	if opening {
		want = append([]string{"buddy-world.snapshot"}, want...)
	}
	if len(f.calls) < len(want) || !slices.Equal(f.calls[len(f.calls)-len(want):], want) {
		t.Fatal("a dependency RPC superseded the last capacity and scope proof", f.calls)
	}
}

func TestColdridge170CapacityAndScopeUseOneFinalWorldBeforeOpening(t *testing.T) {
	for _, entry := range []uint32{707, 724} {
		for _, mode := range []string{"unchanged", "quest_slot_reordered", "complete_last_corpse"} {
			t.Run(fmt.Sprintf("%d/%s", entry, mode), func(t *testing.T) {
				s, f, _ := cold170CapacityRig(t, entry)
				finalReads := 0
				f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
					if !cold170FinalProofRead(s, f) {
						return
					}
					finalReads++
					w.InventoryRevision = 61 // Distinct from every earlier carried observation.
					q := w.SelfQuestLog[0]
					if mode == "quest_slot_reordered" {
						cold170MoveQuest(w, q, 17)
					}
					if mode == "complete_last_corpse" {
						q.Complete, q.ObjectiveCounts = true, []uint32{6, 6, 0, 0}
					}
				}
				enable(t, s)
				s.Tick(context.Background())
				if finalReads != 1 || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || s.inventoryWriteRevision != 61 || s.coldridgeGoal.questID != 170 || current(t, s).InventoryPaused || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("all-bag capacity and quest scope did not use the same final response", current(t, s), f.calls)
				}
				cold170AssertFinalWriteTail(t, f, true)
			})
		}
	}
}

func TestColdridge170CapacityFinalWorldRejectsChangedCapacityOrBoundScope(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"capacity_unknown":           func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"capacity_revision_unknown":  func(w *worldv1.SnapshotResponse) { w.InventoryRevision = 0 },
		"capacity_all_bags_full":     cold170FillLastGeneralSlot,
		"capacity_specialty_only":    func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Entry = 2101 },
		"capacity_other_bag_unknown": func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].ContentsKnown = false },
		"quest_removed179_inserted":  func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].QuestId = 179 },
		"quest_failed":               func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].Failed = true },
		"quest_counter_regressed":    func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].ObjectiveCounts[0] = 2 },
		"corpse_profile_changed":     func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].MaxHealth = 56 },
		"corpse_guid_changed":        func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].Guid++ },
		"corpse_permission_lost":     func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].DynamicFlags = 0 },
		"same_map_position_changed":  func(w *worldv1.SnapshotResponse) { w.Self.PositionRevision++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := cold170CapacityRig(t, 707)
			f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{3, 2, 0, 0}
			finalReads := 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if cold170FinalProofRead(s, f) {
					finalReads++
					change(w) // Earlier capacity and quest responses remain fully valid.
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if finalReads != 1 || len(f.packets) != 0 || s.token != "" || f.leaseOwner != "" || f.releases != 1 {
				t.Fatal("an earlier good view overrode the final disproof", current(t, s), f.calls, f.packets)
			}
		})
	}
}

func TestColdridge170CapacityBaselineProgressIsRetainedBeforeFinalRead(t *testing.T) {
	for _, latest := range []uint32{2, 4} {
		t.Run(fmt.Sprint(latest), func(t *testing.T) {
			s, f, _ := cold170CapacityRig(t, 724)
			f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{1, 1, 0, 0}
			baselineReads, finalReads := 0, 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if s.state.PendingCorpseGuid == 0 {
					return
				}
				if cold170FinalProofRead(s, f) {
					finalReads++
					if s.coldridgeGoal.counts[0] != 3 {
						t.Error("capacity baseline did not advance bound quest high water before renewal")
					}
					w.SelfQuestLog[0].ObjectiveCounts[0] = latest
				} else {
					baselineReads++
					w.SelfQuestLog[0].ObjectiveCounts[0] = 3
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if baselineReads != 1 || finalReads != 1 {
				t.Fatal("fixture missed capacity baseline or final proof", f.calls)
			}
			if latest == 2 {
				if len(f.packets) != 0 || f.releases != 1 {
					t.Fatal("count2 was accepted after observed3 because selection had only1", current(t, s), f.packets)
				}
			} else if len(f.packets) != 1 || s.coldridgeGoal.counts[0] != 4 {
				t.Fatal("valid advancing counter control did not open", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridge170CapacityUsesRealRenewalAndStoppedStateChecks(t *testing.T) {
	for _, mode := range []string{"unchanged", "same_owner_replacement", "changed_return_token", "renewed_cast_pending"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold170CapacityRig(t, 707)
			renewed := 0
			f.beforeStatus = func() {
				if s.state.PendingCorpseGuid != 0 && mode == "same_owner_replacement" {
					f.leaseToken = "replacement-token"
				}
			}
			f.afterRenew = func() {
				renewed++
				if mode == "changed_return_token" {
					f.leaseToken = "replacement-token"
				}
				if mode == "renewed_cast_pending" {
					f.actions.CastPending = true
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if mode == "unchanged" {
				if renewed != 1 || len(f.packets) != 1 {
					t.Fatal("wrapper bypassed real renewal behavior", current(t, s), f.calls)
				}
				cold170AssertFinalWriteTail(t, f, true)
				return
			}
			if len(f.packets) != 0 || s.token != "" || mode != "same_owner_replacement" && renewed != 1 {
				t.Fatal("real rejected or unsafe renewal reached a loot write", current(t, s), f.calls)
			}
			if mode == "same_owner_replacement" || mode == "changed_return_token" {
				if f.leaseOwner != "loot" || f.leaseToken != "replacement-token" || f.releases != 0 {
					t.Fatal("old operation cleared the base fake's replacement lease")
				}
			}
		})
	}
}

func TestColdridge170CapacityActiveStoreUsesSameLatestWorld(t *testing.T) {
	for _, mode := range []string{"unchanged", "unknown", "full"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold170CapacityRig(t, 724)
			enable(t, s)
			s.Tick(context.Background())
			emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(724), 0, lootItem{slot: 2, id: 123, count: 1}))
			finalReads := 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if !cold170FinalProofRead(s, f) {
					return
				}
				finalReads++
				w.InventoryRevision = 62
				if mode == "unknown" {
					w.InventoryKnown = false
				} else if mode == "full" {
					cold170FillLastGeneralSlot(w)
				}
			}
			s.Tick(context.Background())
			if finalReads != 1 {
				t.Fatal("active write did not read exactly one post-renewal world", f.calls)
			}
			if mode == "unchanged" {
				if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || s.inventoryWriteRevision != 62 || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("autostore did not bind its actual latest carried revision", current(t, s), f.calls)
				}
				cold170AssertFinalWriteTail(t, f, false)
			} else if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 || !current(t, s).InventoryPaused {
				t.Fatal("active autostore used capacity from before status and renewal", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridge170CapacityChangeCannotResetPendingItemReceipts(t *testing.T) {
	s, f, _ := cold170CapacityRig(t, 707)
	guid := cold170LootGUID(707)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0, lootItem{slot: 2, id: 123, count: 1}))
	s.Tick(context.Background())
	f.world.InventoryKnown = false
	s.Tick(context.Background())
	if len(f.packets) != 2 || current(t, s).Phase != "waiting_item" || current(t, s).InventoryPaused {
		t.Fatal("new capacity uncertainty reset an already reserved item write", current(t, s), f.packets)
	}
	emit(s, opcode.SMSGItemPushResult, cold170ItemPush(1, 123, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	s.Tick(context.Background())
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
	s.Tick(context.Background())
	if len(f.packets) != 3 || current(t, s).ItemsStoredConfirmed != 1 || current(t, s).LootSlotsRemoved != 1 || current(t, s).ReleasesObserved != 1 || f.leaseOwner != "" {
		t.Fatal("capacity uncertainty discarded actual item or cleanup receipts", current(t, s), f.packets)
	}
}

func TestColdridge170CapacityInventoryFullWaitsForNewCompleteFreeRevision(t *testing.T) {
	s, f, advance := cold170CapacityRig(t, 724)
	guid := cold170LootGUID(724)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0, lootItem{slot: 2, id: 123, count: 1}))
	s.Tick(context.Background())
	writtenRevision := s.inventoryWriteRevision
	failure := make([]byte, 18)
	failure[0] = 50
	emit(s, opcode.SMSGInventoryChangeFailure, failure)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
	s.Tick(context.Background())
	for range 2 {
		advance(time.Second)
		s.Tick(context.Background())
	}
	if len(f.packets) != 3 || !current(t, s).InventoryPaused || f.world.InventoryRevision != writtenRevision {
		t.Fatal("cached free layout retried a rejected inventory write", current(t, s), f.packets)
	}
	f.world.InventoryRevision++
	f.world.InventoryKnown = false
	advance(time.Second)
	s.Tick(context.Background())
	f.world.InventoryKnown = true
	cold170FillLastGeneralSlot(f.world)
	advance(time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 3 || !current(t, s).InventoryPaused {
		t.Fatal("newer unknown or full layout resumed collection", current(t, s), f.packets)
	}
	f.world.InventoryBags[1].Slots[19] = &worldv1.BackpackSlot{Bag: 1, Slot: 19}
	f.world.InventoryRevision++
	advance(time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 4 || f.packets[3].Opcode != opcode.CMSGLoot || current(t, s).InventoryPaused || s.inventoryWriteRevision != f.world.InventoryRevision || current(t, s).ItemsStoredConfirmed != 0 || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{0, 0, 0, 0}) {
		t.Fatal("new complete free carried view failed to resume exact quest corpse, or invented progress", current(t, s), f.packets)
	}
	cold170AssertFinalWriteTail(t, f, true)
}
