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

func TestColdridge183CapacityRetainsBaselineProgressBeforeLastWorld(t *testing.T) {
	for _, latest := range []uint32{2, 4} {
		t.Run(fmt.Sprint(latest), func(t *testing.T) {
			s, f, _ := cold183LootRig(t)
			f.world.SelfQuestLog[0].ObjectiveCounts[0] = 1
			baselineReads, lastReads := 0, 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if s.state.PendingCorpseGuid == 0 {
					return
				}
				if cold170FinalProofRead(s, f) {
					lastReads++
					if s.coldridgeGoal.counts != [2]uint32{3, 0} {
						t.Error("183 capacity baseline was not retained before final proof")
					}
					w.SelfQuestLog[0].ObjectiveCounts[0] = latest
				} else {
					baselineReads++
					w.SelfQuestLog[0].ObjectiveCounts[0] = 3
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if baselineReads != 1 || lastReads != 1 {
				t.Fatal("fixture missed baseline or post-renewal final observation", f.calls)
			}
			if latest == 2 {
				if len(f.packets) != 0 || f.releases != 1 {
					t.Fatal("final2 overrode baseline3 because selection had only1", current(t, s), f.packets)
				}
			} else {
				if len(f.packets) != 1 || s.coldridgeGoal.counts[0] != 4 {
					t.Fatal("advancing183 counter control could not open", current(t, s), f.packets)
				}
				cold170AssertFinalWriteTail(t, f, true)
			}
		})
	}
}

func TestColdridge183CapacityAndCorpseMustBothRemainValidInLastWorld(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"unknown_capacity":  func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"full_capacity":     cold170FillLastGeneralSlot,
		"other_bag_unknown": func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].ContentsKnown = false },
		"changed_profile":   func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].MaxHealth = 55 },
		"changed_guid":      func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].Guid++ },
		"lost_permission":   func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].DynamicFlags = 0 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := cold183LootRig(t)
			lastReads := 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if cold170FinalProofRead(s, f) {
					lastReads++
					change(w)
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if lastReads != 1 || len(f.packets) != 0 || f.leaseOwner != "" || f.releases != 1 {
				t.Fatal("old capacity or corpse proof escaped final disproof", current(t, s), f.calls)
			}
		})
	}
}

func TestColdridge183CapacityActiveStoreUsesLatestAllBagObservation(t *testing.T) {
	for _, mode := range []string{"unchanged", "unknown", "full"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold183LootRig(t)
			enable(t, s)
			s.Tick(context.Background())
			emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(708), 0, lootItem{slot: 2, id: 123, count: 1}))
			lastReads := 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if !cold170FinalProofRead(s, f) {
					return
				}
				lastReads++
				w.InventoryRevision = 92
				if mode == "unknown" {
					w.InventoryKnown = false
				} else if mode == "full" {
					cold170FillLastGeneralSlot(w)
				}
			}
			s.Tick(context.Background())
			if lastReads != 1 {
				t.Fatal("active183 STORE lacked one final post-renewal observation", f.calls)
			}
			if mode == "unchanged" {
				if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || s.inventoryWriteRevision != 92 || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("183 STORE used a different carried revision or invented receipt", current(t, s), f.calls)
				}
				cold170AssertFinalWriteTail(t, f, false)
			} else if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 || !current(t, s).InventoryPaused {
				t.Fatal("183 STORE ignored final full or unknown inventory", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridge183CapacityWindowProgressKeepsNewHighWater(t *testing.T) {
	s, f, _ := cold183LootRig(t)
	q := f.world.SelfQuestLog[0]
	q.ObjectiveCounts[0] = 1
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(708), 0, lootItem{slot: 2, id: 123, count: 1}))
	q.ObjectiveCounts[0] = 3
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || s.coldridgeGoal.counts != [2]uint32{3, 0} {
		t.Fatal("active183 observation did not retain actual new counter", current(t, s))
	}
	q.ObjectiveCounts[0] = 2
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLootRelease) != 1 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("183 active window regressed below its retained progress", current(t, s), f.packets)
	}
}

func TestColdridge183CapacityInventoryFullNeedsNewCompleteFreeRevision(t *testing.T) {
	s, f, advance := cold183LootRig(t)
	guid := cold170LootGUID(708)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0, lootItem{slot: 2, id: 123, count: 1}))
	s.Tick(context.Background())
	written := s.inventoryWriteRevision
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
	if len(f.packets) != 3 || !current(t, s).InventoryPaused || f.world.InventoryRevision != written {
		t.Fatal("cached free layout retried rejected183 inventory write", current(t, s), f.packets)
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
		t.Fatal("new unknown or full view resumed183 collection", current(t, s), f.packets)
	}
	f.world.InventoryBags[1].Slots[19] = &worldv1.BackpackSlot{Bag: 1, Slot: 19}
	f.world.InventoryRevision++
	advance(time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 4 || f.packets[3].Opcode != opcode.CMSGLoot || current(t, s).InventoryPaused || s.coldridgeGoal.questID != 183 || s.inventoryWriteRevision != f.world.InventoryRevision || current(t, s).ItemsStoredConfirmed != 0 || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{0, 0, 0, 0}) {
		t.Fatal("new complete free view did not resume bound183 corpse, or invented kill/item proof", current(t, s), f.packets)
	}
	cold170AssertFinalWriteTail(t, f, true)
}
