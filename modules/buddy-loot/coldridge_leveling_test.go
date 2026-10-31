package buddyloot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func coldLevelingRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold182ServiceRig(t)
	f.world.SelfQuestLog[0].QuestId = 218
	f.actions.ColdridgeLeveling = &actionsv1.ColdridgeLevelingState{
		RequestId: "one-leveling-pull", Goal: coldridgeLevelingGoal, TargetLevel: 5, MaxPulls: 1,
		SourceGuid: f.world.LootableCorpses[0].Guid, PositionRevision: f.world.Self.PositionRevision,
		PullReserved: true, RemainingMs: 179000, ObservedLevel: 4, SelfGuid: s.selfGUID,
	}
	return s, f, advance
}

func coldLevelingNearbyOwner(f *fakeSession) {
	p := f.actions.Position
	f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, AliveKnown: true, Alive: true,
		UnitFlagsKnown: true, MapKnown: true, MapId: p.MapId, PositionKnown: true, X: p.X, Y: p.Y, Z: p.Z}
}

func TestColdridgeLevelingLootExplicitIntentExactSourceAndRealReceipts(t *testing.T) {
	for _, ownLevel := range []uint32{4, 5} {
		for _, bodyLevel := range []uint32{3, 4} {
			t.Run(fmt.Sprintf("own%d/body%d", ownLevel, bodyLevel), func(t *testing.T) {
				s, f, _ := coldLevelingRig(t)
				f.world.Self.Level = ownLevel
				f.actions.ColdridgeLeveling.ObservedLevel = ownLevel
				f.actions.ColdridgeLeveling.LevelFiveObserved = ownLevel == 5
				c := f.world.LootableCorpses[0]
				c.Level = bodyLevel
				if bodyLevel == 4 {
					c.MaxHealth = 86
				}
				s.Tick(context.Background())
				if len(f.packets) != 0 || current(t, s).Enabled {
					t.Fatal("leveling handoff implicitly enabled loot")
				}
				enable(t, s)
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 || !s.questColdridge || s.coldridgeGoal.questID != 0 || s.coldridgeGoal.levelingRequest != "one-leveling-pull" || s.coldridgeGoal.corpseGUID != c.Guid || s.coldridgeGoal.levelingLevel != bodyLevel {
					t.Fatal("leveling did not bind its exact source independently of quest182", current(t, s), f.calls)
				}
				cold170AssertFinalWriteTail(t, f, true)
				emit(s, opcode.SMSGLootResponse, windowPacket(c.Guid, 0, lootItem{slot: 2, id: 750, count: 1}))
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("write fabricated receipt")
				}
				cold170AssertFinalWriteTail(t, f, false)
				// Discovery eviction does not erase an already owned loot window.
				f.world.LootableCorpses = nil
				emit(s, opcode.SMSGLootRemoved, []byte{2})
				emit(s, opcode.SMSGItemPushResult, pushPacket(3, 750, 1))
				s.Tick(context.Background())
				if current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("foreign item push became own receipt")
				}
				emit(s, opcode.SMSGItemPushResult, pushPacket(s.selfGUID, 750, 1))
				s.Tick(context.Background())
				s.Tick(context.Background())
				if current(t, s).ItemsStoredConfirmed != 1 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
					t.Fatal("own item+slot receipts did not reach bounded release", current(t, s), f.packets)
				}
				for _, count := range f.world.SelfQuestLog[0].ObjectiveCounts {
					if count != 0 {
						t.Fatal("ordinary loot changed quest218 item objective into kill credit")
					}
				}
			})
		}
	}
}

func TestColdridgeLevelingLootRejectsMissingMalformedOrForeignHandoffs(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"no_handoff":             func(f *fakeSession) { f.actions.ColdridgeLeveling = nil },
		"empty_request":          func(f *fakeSession) { f.actions.ColdridgeLeveling.RequestId = "" },
		"oversized_request":      func(f *fakeSession) { f.actions.ColdridgeLeveling.RequestId = strings.Repeat("a", 65) },
		"wrong_goal":             func(f *fakeSession) { f.actions.ColdridgeLeveling.Goal = "quest_182" },
		"other_target_level":     func(f *fakeSession) { f.actions.ColdridgeLeveling.TargetLevel = 6 },
		"multiple_pulls":         func(f *fakeSession) { f.actions.ColdridgeLeveling.MaxPulls = 2 },
		"unreserved":             func(f *fakeSession) { f.actions.ColdridgeLeveling.PullReserved = false },
		"expired":                func(f *fakeSession) { f.actions.ColdridgeLeveling.RemainingMs = 0 },
		"renewed_beyond_cap":     func(f *fakeSession) { f.actions.ColdridgeLeveling.RemainingMs = 180001 },
		"foreign_self":           func(f *fakeSession) { f.actions.ColdridgeLeveling.SelfGuid = 3 },
		"different_source":       func(f *fakeSession) { f.actions.ColdridgeLeveling.SourceGuid++ },
		"no_source":              func(f *fakeSession) { f.actions.ColdridgeLeveling.SourceGuid = 0 },
		"unknown_baseline":       func(f *fakeSession) { f.actions.ColdridgeLeveling.PositionRevision = 0 },
		"different_baseline":     func(f *fakeSession) { f.actions.ColdridgeLeveling.PositionRevision++ },
		"stale_level":            func(f *fakeSession) { f.actions.ColdridgeLeveling.ObservedLevel = 5 },
		"unproven_level_five":    func(f *fakeSession) { f.actions.ColdridgeLeveling.LevelFiveObserved = true },
		"own_level3":             func(f *fakeSession) { f.world.Self.Level = 3; f.actions.ColdridgeLeveling.ObservedLevel = 3 },
		"own_level6":             func(f *fakeSession) { f.world.Self.Level = 6; f.actions.ColdridgeLeveling.ObservedLevel = 6 },
		"foreign_world_self":     func(f *fakeSession) { f.world.Self.Guid = 3 },
		"other_class":            func(f *fakeSession) { f.world.Self.ClassId = 8 },
		"no_218":                 func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 0 },
		"failed_218":             func(f *fakeSession) { f.world.SelfQuestLog[0].Failed = true },
		"failed_state":           func(f *fakeSession) { f.world.SelfQuestLog[0].State = 2 },
		"complete_218":           func(f *fakeSession) { f.world.SelfQuestLog[0].Complete = true },
		"complete_state":         func(f *fakeSession) { f.world.SelfQuestLog[0].State = 1 },
		"fake_kill_credit":       func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[0] = 1 },
		"partial_counters":       func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{0} },
		"partial_log":            func(f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_slot":         func(f *fakeSession) { f.world.SelfQuestLog[24].Slot = 0 },
		"duplicate_quest":        func(f *fakeSession) { f.world.SelfQuestLog[24].QuestId = 218 },
		"unrelated_active_quest": func(f *fakeSession) { f.world.SelfQuestLog[24].QuestId = 7 },
		"raw_outside_both":       func(f *fakeSession) { f.world.Self.X = -8900 },
		"estimate_only_legacy":   func(f *fakeSession) { f.actions.Position.X, f.actions.Position.Y = f.world.Self.X, f.world.Self.Y },
		"body_mixed_profile":     func(f *fakeSession) { f.world.LootableCorpses[0].MaxHealth = 86 },
		"body_raw_outside":       func(f *fakeSession) { f.world.LootableCorpses[0].Y = 570.001 },
		"body_destination_outside": func(f *fakeSession) {
			f.world.LootableCorpses[0].Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: -6360, DestinationY: 571, DestinationZ: 386}
		},
		"not_lootable": func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"still_alive":  func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := coldLevelingRig(t)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("invalid handoff gained ordinary loot authority", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridgeLevelingLootRechecksReplacementAndExpiredHandoff(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"request_replaced": func(f *fakeSession) { f.actions.ColdridgeLeveling.RequestId = "replacement" },
		"source_replaced":  func(f *fakeSession) { f.actions.ColdridgeLeveling.SourceGuid++ },
		"handoff_expired":  func(f *fakeSession) { f.actions.ColdridgeLeveling.RemainingMs = 0 },
		"baseline_changed": func(f *fakeSession) { f.world.Self.PositionRevision++ },
		"profile_changed":  func(f *fakeSession) { c := f.world.LootableCorpses[0]; c.Level, c.MaxHealth = 4, 86 },
		"quest_removed":    func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 0 },
	}
	for _, phase := range []string{"after_acquire", "final_renewal", "active_window"} {
		for name, change := range changes {
			t.Run(phase+"/"+name, func(t *testing.T) {
				s, f, _ := coldLevelingRig(t)
				if phase == "after_acquire" {
					f.afterAcquire = func() { change(f) }
				}
				if phase == "final_renewal" {
					f.afterRenew = func() { change(f) }
				}
				enable(t, s)
				s.Tick(context.Background())
				if phase == "active_window" {
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 750, count: 1}))
					change(f)
					// A nearby owner must not rescue the invalid bound handoff.
					coldLevelingNearbyOwner(f)
					s.Tick(context.Background())
					if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
						t.Fatal("invalid bound handoff reached STORE or leaked owned window", current(t, s), f.packets)
					}
				} else if len(f.packets) != 0 || s.token != "" {
					t.Fatal("changed handoff reached OPEN", current(t, s), f.calls)
				}
			})
		}
	}
}

func TestColdridgeLevelingPublishedHandoffCannotUseNearbyOwnerFallback(t *testing.T) {
	for _, mode := range []string{"exact_source", "foreign_corpse", "expired", "empty_request", "request_replaced_after_acquire", "legacy_quest_cannot_rescue", "receipt_absent_owner_legacy"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldLevelingRig(t)
			coldLevelingNearbyOwner(f)
			if !activeOwnerReady(f.world) || f.world.Owner.Guid != 3 || f.world.Owner.X != f.actions.Position.X || f.world.Owner.Y != f.actions.Position.Y {
				t.Fatal("fixture did not establish a valid physically nearby owner")
			}
			switch mode {
			case "foreign_corpse":
				f.world.LootableCorpses[0].Guid++
			case "expired":
				f.actions.ColdridgeLeveling.RemainingMs = 0
			case "empty_request":
				f.actions.ColdridgeLeveling.RequestId = ""
			case "request_replaced_after_acquire":
				f.afterAcquire = func() { f.actions.ColdridgeLeveling.RequestId = "replacement-request" }
			case "legacy_quest_cannot_rescue":
				f.world.SelfQuestLog[0].QuestId = 182
			case "receipt_absent_owner_legacy":
				f.actions.ColdridgeLeveling = nil
				f.world.LootableCorpses[0].Guid++
			}
			enable(t, s)
			s.Tick(context.Background())
			want := 0
			if mode == "exact_source" || mode == "receipt_absent_owner_legacy" {
				want = 1
			}
			if cold170PacketCount(f, opcode.CMSGLoot) != want || want == 0 && (len(f.packets) != 0 || f.leaseOwner != "") {
				t.Fatal("nearby owner rescued a wrong, expired or replaced bound leveling source", current(t, s), f.calls, f.packets)
			}
			if mode == "receipt_absent_owner_legacy" && s.questColdridge {
				t.Fatal("absent record changed ordinary owner admission")
			}
		})
	}
}

func TestColdridgeLevelingLootFinalWorldCapacityAndTokenFences(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"unchanged":                          func(w *worldv1.SnapshotResponse) {},
		"inventory_unknown":                  func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"inventory_revision_unknown":         func(w *worldv1.SnapshotResponse) { w.InventoryRevision = 0 },
		"inventory_full":                     cold170FillLastGeneralSlot,
		"quest_removed":                      func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].QuestId = 0 },
		"quest_fake_credit":                  func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].ObjectiveCounts[0] = 1 },
		"self_revision_changed":              func(w *worldv1.SnapshotResponse) { w.Self.PositionRevision++ },
		"world_level_changed_before_receipt": func(w *worldv1.SnapshotResponse) { w.Self.Level = 5 },
		"body_scope_changed":                 func(w *worldv1.SnapshotResponse) { w.LootableCorpses[0].Y = 570.001 },
	}
	for _, phase := range []string{"opening", "active_store"} {
		for name, change := range changes {
			t.Run(phase+"/"+name, func(t *testing.T) {
				s, f, _ := coldLevelingRig(t)
				enable(t, s)
				if phase == "active_store" {
					s.Tick(context.Background())
					emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 750, count: 1}))
				}
				lastReads := 0
				f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
					if cold170FinalProofRead(s, f) {
						lastReads++
						w.InventoryRevision = 97
						change(w)
					}
				}
				s.Tick(context.Background())
				writeOp := uint32(opcode.CMSGLoot)
				if phase == "active_store" {
					writeOp = opcode.CMSGAutostoreLootItem
				}
				want := 0
				if name == "unchanged" {
					want = 1
				}
				if lastReads != 1 || cold170PacketCount(f, writeOp) != want {
					t.Fatal("last world did not jointly govern bound source and capacity", current(t, s), f.calls, f.packets)
				}
				if want == 1 {
					if s.inventoryWriteRevision != 97 {
						t.Fatal("write used earlier carried revision")
					}
					cold170AssertFinalWriteTail(t, f, phase == "opening")
				}
			})
		}
	}
	for _, phase := range []string{"opening", "active_store"} {
		t.Run(phase+"/same_owner_token_replaced", func(t *testing.T) {
			s, f, _ := coldLevelingRig(t)
			enable(t, s)
			if phase == "active_store" {
				s.Tick(context.Background())
				emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 750, count: 1}))
			}
			f.beforeStatus = func() {
				if s.state.PendingCorpseGuid != 0 {
					f.leaseToken = "replacement-token"
				}
			}
			s.Tick(context.Background())
			if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 0 || phase == "opening" && cold170PacketCount(f, opcode.CMSGLoot) != 0 || f.leaseOwner != "loot" || f.leaseToken != "replacement-token" || f.releases != 0 {
				t.Fatal("old leveling operation wrote into or released replacement ownership", current(t, s), f.calls, f.packets)
			}
		})
	}
}

func TestColdridgeLevelingLootPendingReceiptsAndNewerFreeRevision(t *testing.T) {
	t.Run("pending_write_receipt", func(t *testing.T) {
		s, f, _ := coldLevelingRig(t)
		enable(t, s)
		s.Tick(context.Background())
		emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 750, count: 1}))
		s.Tick(context.Background())
		cold170FillLastGeneralSlot(f.world)
		emit(s, opcode.SMSGLootRemoved, []byte{2})
		s.Tick(context.Background())
		if current(t, s).Phase != "waiting_item" || current(t, s).ItemsStoredConfirmed != 0 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 {
			t.Fatal("capacity change discarded or repeated pending write", current(t, s))
		}
		emit(s, opcode.SMSGItemPushResult, pushPacket(s.selfGUID, 750, 1))
		s.Tick(context.Background())
		s.Tick(context.Background())
		if current(t, s).ItemsStoredConfirmed != 1 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
			t.Fatal("pending actual receipt failed to release", current(t, s))
		}
	})
	t.Run("error50_strictly_newer_complete_free", func(t *testing.T) {
		s, f, advance := coldLevelingRig(t)
		enable(t, s)
		s.Tick(context.Background())
		emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), 0, lootItem{slot: 2, id: 750, count: 1}))
		s.Tick(context.Background())
		written := s.inventoryWriteRevision
		failure := make([]byte, 18)
		failure[0] = 50
		emit(s, opcode.SMSGInventoryChangeFailure, failure)
		if !current(t, s).InventoryPaused || s.rejectedInventoryRevision != written {
			t.Fatal("error50 lost actual write revision", current(t, s))
		}
		// Complete the owned release; a free slot at the old revision is not new evidence.
		s.Tick(context.Background())
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(cold170LootGUID(706)), 1))
		s.Tick(context.Background())
		for _, mode := range []string{"same_revision", "newer_unknown", "newer_full", "newer_free"} {
			f.world.InventoryRevision = written
			f.world.InventoryKnown = true
			f.world.InventoryBags[1].Slots[19].ItemGuid = 0
			if mode != "same_revision" {
				f.world.InventoryRevision++
			}
			if mode == "newer_unknown" {
				f.world.InventoryKnown = false
			}
			if mode == "newer_full" {
				cold170FillLastGeneralSlot(f.world)
			}
			advance(2 * time.Second)
			s.Tick(context.Background())
			if mode != "newer_free" && (!current(t, s).InventoryPaused || cold170PacketCount(f, opcode.CMSGLoot) != 1) {
				t.Fatal("old/incomplete/full capacity resumed", mode, current(t, s), f.packets)
			}
			if mode == "newer_free" && (current(t, s).InventoryPaused || cold170PacketCount(f, opcode.CMSGLoot) != 1) {
				t.Fatal("capacity recovery replayed an already consumed leveling handoff", current(t, s), f.calls)
			}
		}
		f.actions.ColdridgeLeveling.RequestId = "fresh-pull-after-capacity-recovery"
		f.world.LootableCorpses[0].Guid++
		f.actions.ColdridgeLeveling.SourceGuid = f.world.LootableCorpses[0].Guid
		advance(2 * time.Second)
		s.Tick(context.Background())
		if cold170PacketCount(f, opcode.CMSGLoot) != 2 {
			t.Fatal("fresh explicit source could not use independently recovered capacity", current(t, s), f.calls)
		}
	})
}

func TestColdridgeLevelingLocalDeadlineIncludesRPCElapsedTime(t *testing.T) {
	for _, phase := range []string{"opening", "opening_initial_status_latency", "opening_renewal_shorter", "money", "store", "money_renewal_shorter", "store_renewal_shorter"} {
		t.Run(phase, func(t *testing.T) {
			s, f, advance := coldLevelingRig(t)
			shorterRenewal := strings.HasSuffix(phase, "renewal_shorter")
			f.actions.ColdridgeLeveling.RemainingMs = 100
			if shorterRenewal {
				f.actions.ColdridgeLeveling.RemainingMs = 179000
			}
			if phase == "opening_initial_status_latency" {
				f.beforeStatus = func() {
					if s.state.PendingCorpseGuid == 0 {
						advance(60 * time.Millisecond)
					}
				}
			}
			enable(t, s)
			if strings.HasPrefix(phase, "money") || strings.HasPrefix(phase, "store") {
				s.Tick(context.Background())
				money := uint32(0)
				if strings.HasPrefix(phase, "money") {
					money = 7
				}
				emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(706), money, lootItem{slot: 2, id: 750, count: 1}))
				// A later response cannot replenish the original local allowance.
				f.actions.ColdridgeLeveling.RemainingMs = 180000
			}
			if shorterRenewal {
				f.afterRenew = func() { f.actions.ColdridgeLeveling.RemainingMs = 100 }
			}
			lastReads := 0
			f.onSnapshot = func(_ int, _ *worldv1.SnapshotResponse) {
				if cold170FinalProofRead(s, f) {
					lastReads++
					elapsed := 200 * time.Millisecond
					if phase == "opening_initial_status_latency" {
						elapsed = 60 * time.Millisecond
					}
					advance(elapsed)
				}
			}
			s.Tick(context.Background())
			if lastReads != 1 || cold170PacketCount(f, opcode.CMSGLootMoney) != 0 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 {
				t.Fatal("expired allowance reached a mutation", current(t, s), f.calls, f.packets)
			}
			if strings.HasPrefix(phase, "opening") {
				if len(f.packets) != 0 || f.leaseOwner != "" {
					t.Fatal("last-world latency crossed the handoff deadline before OPEN", current(t, s), f.packets)
				}
				f.onSnapshot, f.beforeStatus, f.afterRenew = nil, nil, nil
				f.actions.ColdridgeLeveling.RemainingMs = 180000
				enable(t, s)
				advance(2 * time.Second)
				s.Tick(context.Background())
				if len(f.packets) != 0 {
					t.Fatal("later response rearmed the immutable expired request deadline", f.packets)
				}
			} else {
				f.onSnapshot = nil
				s.Tick(context.Background())
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
					t.Fatal("expired handoff prevented or repeated owned cleanup release", current(t, s), f.packets)
				}
			}
		})
	}
}

func TestColdridgeLevelingHandoffIsOneUseAcrossReleaseChurnAndEnable(t *testing.T) {
	for _, mode := range []string{"ordinary_release", "discovery_eviction", "living_then_dead", "enable_reset", "ambiguous_open"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := coldLevelingRig(t)
			guid := f.world.LootableCorpses[0].Guid
			if mode == "ambiguous_open" {
				f.failOpcode = opcode.CMSGLoot
			}
			enable(t, s)
			s.Tick(context.Background())
			if cold170PacketCount(f, opcode.CMSGLoot) != 1 || !s.levelingRequests["one-leveling-pull"].consumed {
				t.Fatal("OPEN was not reserved before potential transport", current(t, s), f.packets)
			}
			if mode != "ambiguous_open" {
				emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0))
				s.Tick(context.Background())
				emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
				s.Tick(context.Background())
			}
			f.failOpcode = 0
			body := f.world.LootableCorpses[0]
			if mode == "discovery_eviction" || mode == "ordinary_release" {
				f.world.LootableCorpses = nil
				advance(2 * time.Second)
				s.Tick(context.Background())
				f.world.LootableCorpses = []*worldv1.CreatureState{body}
			}
			if mode == "living_then_dead" {
				// Exercise the ordinary cache's actual-life reset path too.
				s.retired = map[uint64]bool{guid: true}
				body.Alive, body.Health = true, body.MaxHealth
				f.world.RequestedCreature = body
				advance(2 * time.Second)
				s.Tick(context.Background())
				if s.retired[guid] || s.seen[guid] {
					t.Fatal("fixture failed to reset generic new-life caches")
				}
				body.Alive, body.Health = false, 0
			}
			if mode == "enable_reset" || mode == "ambiguous_open" {
				_, _ = s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false})
				enable(t, s)
			}
			advance(2 * time.Second)
			s.Tick(context.Background())
			if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
				t.Fatal("same leveling request reopened after generic lifecycle reset", mode, current(t, s), f.calls, f.packets)
			}
			// A distinct explicit action pull can authorize its own fresh source.
			body.Guid++
			f.world.RequestedCreature = nil
			f.actions.ColdridgeLeveling.RequestId = "new-explicit-pull"
			f.actions.ColdridgeLeveling.SourceGuid = body.Guid
			advance(2 * time.Second)
			s.Tick(context.Background())
			if cold170PacketCount(f, opcode.CMSGLoot) != 2 {
				t.Fatal("fresh explicit request/source was confused with prior consumption", current(t, s), f.calls)
			}
		})
	}
	t.Run("bounded_request_history", func(t *testing.T) {
		s, _, _ := coldLevelingRig(t)
		for i := range maxColdridgeLevelingLootRequests {
			g := coldridgeLootGoal{levelingRequest: fmt.Sprintf("request-%d", i), corpseGUID: cold170LootGUID(706)}
			if !s.bindLevelingLootDeadline(&g, s.now(), 1000) {
				t.Fatal("request history filled early", i)
			}
		}
		g := coldridgeLootGoal{levelingRequest: "overflow", corpseGUID: cold170LootGUID(706)}
		if s.bindLevelingLootDeadline(&g, s.now(), 1000) || len(s.levelingRequests) != maxColdridgeLevelingLootRequests {
			t.Fatal("request history exceeded finite bound")
		}
	})
}
