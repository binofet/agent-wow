package buddyloot

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func cold183LootRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold170CapacityRig(t, 708)
	f.world.Self.Level = 3
	f.world.SelfQuestLog[0].QuestId = 183
	c := f.world.LootableCorpses[0]
	c.Level, c.MaxHealth, c.FactionTemplate = 3, 71, 189
	return s, f, advance
}

func TestColdridge183LootNeedsExplicitEnableAndSameFinalWorldProof(t *testing.T) {
	for _, mode := range []string{"incomplete", "reordered", "complete"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold183LootRig(t)
			s.Tick(context.Background())
			if current(t, s).Enabled || len(f.packets) != 0 {
				t.Fatal("183 quest implicitly enabled loot")
			}
			finalReads := 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if !cold170FinalProofRead(s, f) {
					return
				}
				finalReads++
				w.InventoryRevision = 91
				q := w.SelfQuestLog[0]
				if mode == "reordered" {
					cold170MoveQuest(w, q, 17)
				}
				if mode == "complete" {
					q.Complete, q.State, q.ObjectiveCounts = true, 1, []uint32{12, 0, 0, 0}
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			got := current(t, s)
			if finalReads != 1 || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || binary.LittleEndian.Uint64(f.packets[0].Payload) != cold170LootGUID(708) || s.coldridgeGoal.questID != 183 || s.inventoryWriteRevision != 91 || got.LootWindowsObserved != 0 || got.ItemsStoredConfirmed != 0 {
				t.Fatal("183 did not use one actual final capacity/scope proof", got, f.calls)
			}
			cold170AssertFinalWriteTail(t, f, true)
		})
	}
}

func TestColdridge183LootRejectsOtherQuestsEntriesAndUnsafeBodies(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"quest179": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 179 },
		"quest170": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 170 },
		"quest182": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 182 },
		"entry706": func(f *fakeSession) {
			f.world.LootableCorpses[0].Entry, f.world.LootableCorpses[0].Guid = 706, cold170LootGUID(706)
		},
		"entry707": func(f *fakeSession) {
			f.world.LootableCorpses[0].Entry, f.world.LootableCorpses[0].Guid = 707, cold170LootGUID(707)
		},
		"self_level2":     func(f *fakeSession) { f.world.Self.Level = 2 },
		"foreign_self":    func(f *fakeSession) { f.world.Self.Guid = 7 },
		"living_body":     func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"health_nonzero":  func(f *fakeSession) { f.world.LootableCorpses[0].Health = 1 },
		"health_unknown":  func(f *fakeSession) { f.world.LootableCorpses[0].HealthKnown = false },
		"permission_lost": func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"absent":          func(f *fakeSession) { f.world.LootableCorpses[0].Present = false },
		"outside_y700": func(f *fakeSession) {
			f.world.Self.Y, f.actions.Position.Y = 698, 698
			f.world.LootableCorpses[0].Y = 701.332 // In opening range but outside the sealed region.
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := cold183LootRig(t)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("unsupported or unsafe183 scope acquired loot authority", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridge183LootRetainsItsBoundQuestAfterAcquireOpenAndWindow(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"removed179": func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].QuestId = 179 },
		"removed170": func(w *worldv1.SnapshotResponse) {
			w.SelfQuestLog[0].QuestId, w.SelfQuestLog[0].ObjectiveCounts = 170, []uint32{0, 0, 0, 0}
		},
		"failed_with_other_quests": func(w *worldv1.SnapshotResponse) {
			w.SelfQuestLog[0].Failed = true
			w.SelfQuestLog[1].QuestId, w.SelfQuestLog[1].ObjectiveCounts = 170, []uint32{0, 0, 0, 0}
			w.SelfQuestLog[2].QuestId = 179
		},
		"regressed":       func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].ObjectiveCounts[0] = 2 },
		"extra_counter":   func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].ObjectiveCounts[1] = 1 },
		"actual_position": func(w *worldv1.SnapshotResponse) { w.Self.PositionRevision++ },
	}
	for _, phase := range []string{"after_acquire", "last_world", "active_window"} {
		for name, change := range changes {
			t.Run(phase+"/"+name, func(t *testing.T) {
				s, f, _ := cold183LootRig(t)
				f.world.SelfQuestLog[0].ObjectiveCounts[0] = 3
				if phase == "after_acquire" {
					f.afterAcquire = func() { change(f.world) }
				} else if phase == "last_world" {
					f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
						if cold170FinalProofRead(s, f) {
							change(w)
						}
					}
				}
				enable(t, s)
				s.Tick(context.Background())
				if phase == "active_window" {
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(708), 0, lootItem{slot: 2, id: 123, count: 1}))
					f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
						if cold170FinalProofRead(s, f) {
							change(w)
						}
					}
					s.Tick(context.Background())
					if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 {
						t.Fatal("active window escaped exact183 quest or position proof", current(t, s), f.packets)
					}
				} else if len(f.packets) != 0 || f.releases != 1 || s.token != "" {
					t.Fatal("disproved bound183 scope opened using another quest", current(t, s), f.packets)
				}
			})
		}
	}
}

func TestColdridge183LootReceiptsStaySeparateFromOwnQuestKillCounters(t *testing.T) {
	s, f, _ := cold183LootRig(t)
	guid := cold170LootGUID(708)
	enable(t, s)
	s.Tick(context.Background())
	// Local public-wire fixtures offer a synthetic ordinary item and coins;
	// neither a drop table nor a kill receipt is inferred from this corpse.
	emit(s, opcode.SMSGLootResponse, windowPacket(guid, 17, lootItem{slot: 2, id: 123, count: 1}))
	f.world.LootableCorpses = nil
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
	if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("autostore transport became own item proof", current(t, s))
	}
	cold170AssertFinalWriteTail(t, f, false)
	emit(s, opcode.SMSGItemPushResult, cold170ItemPush(3, 123, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{3})
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
		t.Fatal("foreign item or slot proof claimed183 loot")
	}
	emit(s, opcode.SMSGItemPushResult, cold170ItemPush(1, 123, 1))
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
		t.Fatal("own push alone skipped required slot removal")
	}
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	s.Tick(context.Background())
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid+1), 1))
	if current(t, s).ReleasesObserved != 0 {
		t.Fatal("foreign release confirmed own183 corpse")
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
	s.Tick(context.Background())
	got := current(t, s)
	if len(f.packets) != 4 || got.ItemsStoredConfirmed != 1 || got.MoneyAwardedNotified != 17 || got.LootSlotsRemoved != 1 || got.ReleasesObserved != 1 || got.LastCorpseGuid != guid || f.leaseOwner != "" || s.coldridgeGoal.questID != 0 || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{0, 0, 0, 0}) || f.world.SelfQuestLog[0].Complete {
		t.Fatal("actual inventory receipts altered own kill counters or lost cleanup", got, f.packets)
	}
}

func TestColdridge183LootReplacementTokenCannotOpenStoreOrCleanAnotherWindow(t *testing.T) {
	for _, mode := range []string{"opening", "store", "unsafe_cleanup"} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, f, _ := cold183LootRig(t)
			enable(t, s)
			wantWrites := 0
			if mode != "opening" {
				s.Tick(context.Background())
				emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(708), 0, lootItem{slot: 2, id: 123, count: 1}))
				wantWrites = 1
				if mode == "unsafe_cleanup" {
					f.world.Self.UnitFlags |= 0x80000
					f.leaseToken = "replacement-token"
				}
			}
			f.beforeStatus = func() {
				if s.state.PendingCorpseGuid != 0 {
					f.leaseToken = "replacement-token"
				}
			}
			s.Tick(context.Background())
			if len(f.packets) != wantWrites || f.leaseOwner != "loot" || f.leaseToken != "replacement-token" || f.releases != 0 || s.token != "" {
				t.Fatal("old183 operation wrote under or cleared a replacement lease", current(t, s), f.packets, f.calls)
			}
		})
	}
}
