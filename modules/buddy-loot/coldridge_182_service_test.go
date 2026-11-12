package buddyloot

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
	"time"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func cold182ServiceRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold170CapacityRig(t, 706)
	// The profile comes from saved public observations. Geometry and low GUID
	// bits are synthetic: this is no claim that a706 was observed beside Talin.
	p := f.world.Self
	p.Level, p.X, p.Y, p.Z = 4, -6222.4404, 686.27026, 384.9266
	// The unchanged authoritative Talin baseline is valid old-region evidence;
	// the client estimate and corpse remain inside the new combat corridor.
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = -6361.6, 568.8, 385.89725
	f.world.SelfQuestLog[0].QuestId = 182
	c := f.world.LootableCorpses[0]
	c.Level, c.MaxHealth, c.FactionTemplate = 3, 71, 190
	c.X, c.Y, c.Z = f.actions.Position.X+3, f.actions.Position.Y, f.actions.Position.Z
	return s, f, advance
}

func cold182OtherAcceptedQuests(w *worldv1.SnapshotResponse) {
	for i, quest := range []uint32{179, 170, 183} {
		w.SelfQuestLog[i+1].QuestId = quest
		w.SelfQuestLog[i+1].ObjectiveCounts = []uint32{0, 0, 0, 0}
	}
}

func TestColdridge182ServicePinsBothProfilesAndCompletedActiveLastCorpse(t *testing.T) {
	for _, level := range []uint32{3, 4} {
		for _, mode := range []string{"incomplete", "reordered", "complete10", "complete_flag", "complete_state"} {
			t.Run(fmt.Sprintf("profile%d/%s", level, mode), func(t *testing.T) {
				s, f, _ := cold182ServiceRig(t)
				c := f.world.LootableCorpses[0]
				c.Level = level
				if level == 4 {
					c.MaxHealth = 86
					f.world.Self.Level = 6
				}
				s.Tick(context.Background())
				if current(t, s).Enabled || len(f.packets) != 0 {
					t.Fatal("182 enabled looting without explicit intent")
				}
				lastReads := 0
				f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
					if !cold170FinalProofRead(s, f) {
						return
					}
					lastReads++
					w.InventoryRevision = 91
					q := w.SelfQuestLog[0]
					switch mode {
					case "reordered":
						cold170MoveQuest(w, q, 17)
					case "complete10":
						q.ObjectiveCounts = []uint32{10, 0, 0, 0}
					case "complete_flag":
						q.Complete = true
					case "complete_state":
						q.State = 1
					}
				}
				enable(t, s)
				s.Tick(context.Background())
				got := current(t, s)
				if lastReads != 1 || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || binary.LittleEndian.Uint64(f.packets[0].Payload) != cold170LootGUID(706) || s.coldridgeGoal.questID != 182 || s.coldridgeGoal.corpseGUID != c.Guid || s.coldridgeGoal.positionRevision != f.world.Self.PositionRevision || s.inventoryWriteRevision != 91 || f.leaseOwner != "loot" || got.LootWindowsObserved != 0 || got.ItemsStoredConfirmed != 0 {
					t.Fatal("182 did not bind one exact corpse to the final scope/capacity proof", got, f.calls)
				}
				cold170AssertFinalWriteTail(t, f, true)
			})
		}
	}
}

func TestColdridge182ServiceRejectsChangedBoundScopeAtAcquireFinalWorldAndWindow(t *testing.T) {
	type mutation struct {
		change       func(*worldv1.SnapshotResponse)
		activeWindow bool
	}
	changes := map[string]mutation{
		"removed_with_older_quests": {func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].QuestId = 0 }, true},
		"failed":                    {func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].Failed = true }, true},
		"failed_state":              {func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].State = 2 }, true},
		"regressed":                 {func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].ObjectiveCounts[0] = 2 }, true},
		"extra_counter":             {func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].ObjectiveCounts[1] = 1 }, true},
		"above10":                   {func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].ObjectiveCounts[0] = 11 }, true},
		"unknown_log":               {func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false }, true},
		"same_map_actual_revision":  {func(w *worldv1.SnapshotResponse) { w.Self.PositionRevision++ }, true},
		"self_outside_scope":        {func(w *worldv1.SnapshotResponse) { w.Self.X = -8900 }, true},
		"self_combat":               {func(w *worldv1.SnapshotResponse) { w.Self.UnitFlags |= 0x80000 }, true},
		"own_level3":                {func(w *worldv1.SnapshotResponse) { w.Self.Level = 3 }, true},
		"own_level7":                {func(w *worldv1.SnapshotResponse) { w.Self.Level = 7 }, true},
		"mixed_profile":             {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].MaxHealth = 86 }, true},
		"other_faction":             {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].FactionTemplate = 189 }, true},
		"other_entry_same_guid":     {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].Entry = 708 }, true},
		"body_other_map":            {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].MapId = 1 }, true},
		"body_raw_unknown":          {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].PositionKnown = false }, true},
		"body_raw_outside":          {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].X = -8900 }, true},
		"outside_raw_inside_elapsed_endpoint": {func(w *worldv1.SnapshotResponse) {
			c := w.LootableCorpses[0]
			c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, DestinationX: c.X, DestinationY: c.Y, DestinationZ: c.Z, DurationMs: 1000, AgeMs: 1000}
			c.X = -8900
		}, true},
		"known_destination_outside": {func(w *worldv1.SnapshotResponse) {
			w.LootableCorpses[0].Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: -8900}
		}, true},
		"active_trajectory": {func(w *worldv1.SnapshotResponse) {
			c := w.LootableCorpses[0]
			c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, DestinationX: c.X, DestinationY: c.Y, DestinationZ: c.Z, DurationMs: 1000, AgeMs: 999}
		}, true},
		"duplicate_pending_guid": {func(w *worldv1.SnapshotResponse) {
			w.LootableCorpses = append(w.LootableCorpses, w.LootableCorpses[0])
		}, true},
		"oversized_bodies": {func(w *worldv1.SnapshotResponse) {
			for i := range 64 {
				w.LootableCorpses = append(w.LootableCorpses, &worldv1.CreatureState{Guid: uint64(i + 1)})
			}
		}, true},
		"different_guid": {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].Guid++ }, false},
		"living":         {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].Alive, w.LootableCorpses[0].Health = true, 71 }, false},
		"not_lootable":   {func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].DynamicFlags = 0 }, false},
	}
	for _, phase := range []string{"after_acquire", "last_world", "active_window"} {
		for name, mutate := range changes {
			if phase == "active_window" && !mutate.activeWindow {
				continue // An opened window permits cache eviction; it retains its own wire authority.
			}
			t.Run(phase+"/"+name, func(t *testing.T) {
				s, f, _ := cold182ServiceRig(t)
				f.world.SelfQuestLog[0].ObjectiveCounts[0] = 3
				cold182OtherAcceptedQuests(f.world)
				changed := 0
				if phase == "after_acquire" {
					f.afterAcquire = func() { changed++; mutate.change(f.world) }
				} else if phase == "last_world" {
					f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
						if cold170FinalProofRead(s, f) {
							changed++
							mutate.change(w)
						}
					}
				}
				enable(t, s)
				s.Tick(context.Background())
				if phase == "active_window" {
					if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot {
						t.Fatal("valid initial182 control failed before window mutation", current(t, s), f.calls)
					}
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 123, count: 1}))
					f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
						if cold170FinalProofRead(s, f) {
							changed++
							mutate.change(w)
						}
					}
					s.Tick(context.Background())
					if changed != 1 || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || s.coldridgeGoal.counts != [2]uint32{3, 0} {
						t.Fatal("changed current182 evidence permitted STORE or advanced highwater", current(t, s), f.packets, f.calls)
					}
				} else if changed != 1 || len(f.packets) != 0 || f.releases != 1 || s.token != "" {
					t.Fatal("another accepted quest rescued disproved bound182 before OPEN", current(t, s), f.packets, f.calls)
				}
			})
		}
	}
	for _, phase := range []string{"opening", "active_window"} {
		for _, mode := range []string{"unknown", "other_map", "outside", "talin_outside_combat_corridor"} {
			t.Run(phase+"/client_estimate_"+mode, func(t *testing.T) {
				s, f, _ := cold182ServiceRig(t)
				cold182OtherAcceptedQuests(f.world)
				enable(t, s)
				if phase == "active_window" {
					s.Tick(context.Background())
					if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
						t.Fatal("valid182 control never opened", current(t, s))
					}
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 123, count: 1}))
				}
				f.beforeStatus = func() {
					if s.state.PendingCorpseGuid == 0 {
						return
					}
					switch mode {
					case "unknown":
						f.actions.Position.Known = false
					case "other_map":
						f.actions.Position.MapId = 1
					case "outside":
						f.actions.Position.X = -8900
					case "talin_outside_combat_corridor":
						f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = f.world.Self.X, f.world.Self.Y, f.world.Self.Z
					}
				}
				s.Tick(context.Background())
				want := 0
				if phase == "active_window" {
					want = 2 // Already-owned OPEN, followed by exact-token cleanup RELEASE.
				}
				if len(f.packets) != want || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 {
					t.Fatal("stale raw position or another quest rescued disproved client estimate", current(t, s), f.calls)
				}
			})
		}
	}
}

func TestColdridge182ServiceSameFinalWorldRetainsHighwaterAndAllBagCapacity(t *testing.T) {
	for _, phase := range []string{"opening", "active_store"} {
		modes := []string{"advance4", "regress2", "unknown_capacity", "full_capacity", "other_bag_unknown"}
		if phase == "active_store" {
			modes = append(modes, "evicted_body", "nonpresent_body", "window_retains_receipt")
		}
		for _, mode := range modes {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				s, f, _ := cold182ServiceRig(t)
				q := f.world.SelfQuestLog[0]
				q.ObjectiveCounts[0] = 1
				enable(t, s)
				if phase == "active_store" {
					s.Tick(context.Background())
					if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
						t.Fatal("valid182 control never opened", current(t, s))
					}
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 123, count: 1}))
					q.ObjectiveCounts[0] = 3
				}
				baselineReads, lastReads := 0, 0
				f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
					if s.state.PendingCorpseGuid == 0 {
						return
					}
					if !cold170FinalProofRead(s, f) {
						baselineReads++
						w.SelfQuestLog[0].ObjectiveCounts[0] = 3
						return
					}
					lastReads++
					if s.coldridgeGoal.counts != [2]uint32{3, 0} {
						t.Error("capacity baseline counter was not retained before STATUS/RENEW/LASTworld")
					}
					w.InventoryRevision = 92
					w.SelfQuestLog[0].ObjectiveCounts[0] = 4
					switch mode {
					case "regress2":
						w.SelfQuestLog[0].ObjectiveCounts[0] = 2
					case "unknown_capacity":
						w.InventoryKnown = false
					case "full_capacity":
						cold170FillLastGeneralSlot(w)
					case "other_bag_unknown":
						w.InventoryBags[4].ContentsKnown = false
					case "evicted_body":
						w.LootableCorpses = nil
					case "nonpresent_body":
						w.LootableCorpses[0].Present = false
					case "window_retains_receipt":
						// An already-owned window, not a second death/permission
						// inference from discovery, retains transaction authority.
						c := w.LootableCorpses[0]
						c.Alive, c.Health, c.DynamicFlags = true, 71, 0
					}
				}
				s.Tick(context.Background())
				if baselineReads != 1 || lastReads != 1 {
					t.Fatal("fixture missed exact baseline or final post-renewal world", f.calls)
				}
				if mode == "advance4" || mode == "evicted_body" || mode == "nonpresent_body" || mode == "window_retains_receipt" {
					wanted := uint32(opcode.CMSGLoot)
					if phase == "active_store" {
						wanted = opcode.CMSGAutostoreLootItem
					}
					if cold170PacketCount(f, wanted) != 1 || f.packets[len(f.packets)-1].Opcode != wanted || s.coldridgeGoal.counts != [2]uint32{4, 0} || s.inventoryWriteRevision != 92 || current(t, s).ItemsStoredConfirmed != 0 {
						t.Fatal("valid same-world scope/capacity control did not dispatch exactly once", current(t, s), f.calls)
					}
					cold170AssertFinalWriteTail(t, f, phase == "opening")
				} else if phase == "opening" {
					if len(f.packets) != 0 || f.releases != 1 {
						t.Fatal("invalid final counter/capacity proof permitted OPEN", current(t, s), f.calls)
					}
				} else if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
					t.Fatal("STORE used earlier capacity or counter evidence", current(t, s), f.calls)
				}
			})
		}
	}
}

func TestColdridge182ServiceReplacementTokenCannotOpenStoreMoneyOrCleanAnotherWindow(t *testing.T) {
	for _, mode := range []string{"opening", "store", "money", "unsafe_cleanup", "disable_cleanup"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold182ServiceRig(t)
			enable(t, s)
			wantWrites := 0
			if mode != "opening" {
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
					t.Fatal("valid182 control never opened", current(t, s))
				}
				if mode == "money" {
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 17))
				} else {
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 123, count: 1}))
				}
				wantWrites = 1
			}
			if mode == "unsafe_cleanup" || mode == "disable_cleanup" {
				f.leaseToken = "replacement-token"
				if mode == "unsafe_cleanup" {
					f.world.Self.UnitFlags |= 0x80000
				} else {
					_, _ = s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false})
				}
			} else {
				f.beforeStatus = func() {
					if s.state.PendingCorpseGuid != 0 {
						f.leaseToken = "replacement-token"
					}
				}
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if len(f.packets) != wantWrites || f.leaseOwner != "loot" || f.leaseToken != "replacement-token" || f.releases != 0 || s.token != "" {
				t.Fatal("old182 operation wrote under or released a replacement operation", current(t, s), f.packets, f.calls)
			}
		})
	}
}

func TestColdridge182ServiceReceiptsAndNewerInventoryFenceDoNotInventQuestCredit(t *testing.T) {
	t.Run("separate_wire_receipts_and_fresh_own_log", func(t *testing.T) {
		s, f, _ := cold182ServiceRig(t)
		guid := cold170LootGUID(706)
		enable(t, s)
		s.Tick(context.Background())
		if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
			t.Fatal("valid182 control never opened", current(t, s))
		}
		emit(s, opcode.SMSGLootResponse, windowPacket(guid, 17, lootItem{slot: 2, id: 123, count: 1}))
		f.world.LootableCorpses = nil // Its owned window survives discovery eviction.
		s.Tick(context.Background())
		if cold170PacketCount(f, opcode.CMSGLootMoney) != 1 || current(t, s).MoneyAwardedNotified != 0 {
			t.Fatal("money transport became a server award", current(t, s))
		}
		notify := make([]byte, 5)
		binary.LittleEndian.PutUint32(notify, 17)
		notify[4] = 1
		emit(s, opcode.SMSGLootMoneyNotify, notify)
		emit(s, opcode.SMSGLootClearMoney, nil)
		s.Tick(context.Background())
		s.Tick(context.Background())
		if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || current(t, s).ItemsStoredConfirmed != 0 || s.coldridgeGoal.counts != [2]uint32{} {
			t.Fatal("STORE or money receipts became item/kill proof", current(t, s), s.coldridgeGoal)
		}
		cold170AssertFinalWriteTail(t, f, false)
		emit(s, opcode.SMSGItemPushResult, cold170ItemPush(3, 123, 1))
		emit(s, opcode.SMSGLootRemoved, []byte{3})
		s.Tick(context.Background())
		if current(t, s).ItemsStoredConfirmed != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
			t.Fatal("foreign actor/slot receipt confirmed182 loot")
		}
		emit(s, opcode.SMSGItemPushResult, cold170ItemPush(1, 123, 1))
		s.Tick(context.Background())
		if cold170PacketCount(f, opcode.CMSGLootRelease) != 0 || current(t, s).ItemsStoredConfirmed != 1 || s.coldridgeGoal.counts != [2]uint32{} {
			t.Fatal("own push skipped slot removal or fabricated kill credit")
		}
		f.world.SelfQuestLog[0].ObjectiveCounts[0] = 1
		s.Tick(context.Background())
		if s.coldridgeGoal.counts != [2]uint32{1, 0} || current(t, s).ItemsStoredConfirmed != 1 {
			t.Fatal("fresh own log and actual item receipt were not kept separate", s.coldridgeGoal, current(t, s))
		}
		emit(s, opcode.SMSGLootRemoved, []byte{2})
		s.Tick(context.Background())
		s.Tick(context.Background())
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid+1), 1))
		if current(t, s).ReleasesObserved != 0 {
			t.Fatal("foreign release confirmed the pending706 corpse")
		}
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
		s.Tick(context.Background())
		got := current(t, s)
		if len(f.packets) != 4 || got.ItemsStoredConfirmed != 1 || got.MoneyAwardedNotified != 17 || got.LootSlotsRemoved != 1 || got.ReleasesObserved != 1 || got.LastCorpseGuid != guid || f.leaseOwner != "" || s.coldridgeGoal.questID != 0 || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{1, 0, 0, 0}) || f.world.SelfQuestLog[0].Complete {
			t.Fatal("actual transfer/release receipts lost identity or altered actual kill credit", got, f.packets)
		}
	})
	t.Run("inventory50_requires_new_complete_free_revision", func(t *testing.T) {
		s, f, advance := cold182ServiceRig(t)
		guid := cold170LootGUID(706)
		enable(t, s)
		s.Tick(context.Background())
		if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
			t.Fatal("valid182 control never opened", current(t, s))
		}
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
			t.Fatal("cached free inventory retried a rejected182 STORE", current(t, s), f.packets)
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
			t.Fatal("new unknown/full inventory resumed rejected182 collection", current(t, s), f.packets)
		}
		f.world.InventoryBags[1].Slots[19] = &worldv1.BackpackSlot{Bag: 1, Slot: 19}
		f.world.InventoryRevision++
		advance(time.Second)
		s.Tick(context.Background())
		if len(f.packets) != 4 || f.packets[3].Opcode != opcode.CMSGLoot || current(t, s).InventoryPaused || s.coldridgeGoal.questID != 182 || s.coldridgeGoal.corpseGUID != guid || s.inventoryWriteRevision != f.world.InventoryRevision || current(t, s).ItemsStoredConfirmed != 0 || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{0, 0, 0, 0}) {
			t.Fatal("new complete free revision lost exact182 scope or invented item/kill proof", current(t, s), f.packets)
		}
		cold170AssertFinalWriteTail(t, f, true)
	})
}
