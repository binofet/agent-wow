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
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
)

func coldCatchupRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold182ServiceRig(t)
	s.selfGUID, f.world.Self.Guid, s.state.SessionId = 8, 8, "own-catchup-session"
	f.world.Self.Level = 5
	f.world.Self.ChannelSpellKnown, f.world.Self.ChannelObjectKnown = true, true
	f.world.SelfQuestLog[0].QuestId = 218
	f.world.SelfQuestLog[1].QuestId = 2160
	f.world.SelfQuestLog[1].State, f.world.SelfQuestLog[1].Complete = 1, true
	f.world.SelfQuestLog[1].ObjectiveCounts = []uint32{0, 0, 0, 0}
	for _, b := range f.world.InventoryBags {
		for _, item := range b.Slots {
			if item.ItemGuid != 0 {
				item.Entry, item.StackCount, item.MetadataKnown = 117, 1, true
			}
		}
	}
	crate := f.world.InventoryBags[0].Slots[0]
	crate.ItemGuid, crate.Entry, crate.StackCount, crate.MetadataKnown = coldridgeCatchupCrateGUID, 7646, 1, true
	f.actions.ColdridgeCatchup = &actionsv1.ColdridgeCatchupState{
		RequestId: "catchup-5-to-6-one-body", Goal: coldridgeCatchupGoal, StartLevel: 5, TargetLevel: 6, MaxPulls: 1,
		SourceGuid: f.world.LootableCorpses[0].Guid, TargetGuid: f.world.LootableCorpses[0].Guid,
		PositionRevision: f.world.Self.PositionRevision, PullReserved: true, RemainingMs: 179000,
		ObservedLevel: 5, SelfGuid: 8, SessionId: s.state.SessionId,
	}
	if !coldridgeCatchupContext(f.world, f.actions, s.selfGUID, s.state.SessionId) {
		t.Fatal("invalid catchup fixture")
	}
	return s, f, advance
}

func TestColdridgeCatchupExactSourceAtStartOrNextLevelWithRealReceipts(t *testing.T) {
	for start := uint32(5); start <= 7; start++ {
		for _, capped := range []bool{false, true} {
			t.Run(fmt.Sprintf("start%d/capped%t", start, capped), func(t *testing.T) {
				s, f, _ := coldCatchupRig(t)
				r := f.actions.ColdridgeCatchup
				r.StartLevel, r.TargetLevel = start, start+1
				r.ObservedLevel, f.world.Self.Level = start, start
				if capped {
					r.ObservedLevel, f.world.Self.Level, r.TargetLevelObserved = start+1, start+1, true
				}
				guid := r.SourceGuid
				s.Tick(context.Background())
				if len(f.packets) != 0 {
					t.Fatal("handoff enabled loot implicitly")
				}
				enable(t, s)
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 || s.coldridgeGoal.catchupRequest != r.RequestId || s.coldridgeGoal.levelingRequest != "" || s.coldridgeGoal.questID != 0 || !s.catchupRequests[r.RequestId].consumed {
					t.Fatal("missing independent one-source binding", current(t, s), f.calls)
				}
				cold170AssertFinalWriteTail(t, f, true)
				emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0, lootItem{slot: 2, id: 750, count: 1}))
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("write was treated as receipt")
				}
				cold170AssertFinalWriteTail(t, f, false)
				// The receiving-client item ACK and slot removal are both required.
				f.world.LootableCorpses = nil
				emit(s, opcode.SMSGLootRemoved, []byte{2})
				emit(s, opcode.SMSGItemPushResult, pushPacket(3, 750, 1))
				s.Tick(context.Background())
				if current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("foreign push accepted")
				}
				emit(s, opcode.SMSGItemPushResult, pushPacket(8, 750, 1))
				s.Tick(context.Background())
				s.Tick(context.Background())
				if current(t, s).ItemsStoredConfirmed != 1 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
					t.Fatal("actual receipt cleanup failed", current(t, s), f.packets)
				}
				emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
				s.Tick(context.Background())
				if current(t, s).Enabled || f.leaseOwner != "" || current(t, s).Phase != "disabled" {
					t.Fatal("one-shot catchup left ambient loot enabled", current(t, s))
				}
				for _, q := range f.world.SelfQuestLog[:2] {
					for _, n := range q.ObjectiveCounts {
						if n != 0 {
							t.Fatal("loot invented item quest credit")
						}
					}
				}
			})
		}
	}
}

func TestColdridgeCatchupInvalidIntentCannotBorrowOwnerOrQuest(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"empty_request":       func(f *fakeSession) { f.actions.ColdridgeCatchup.RequestId = "" },
		"long_request":        func(f *fakeSession) { f.actions.ColdridgeCatchup.RequestId = strings.Repeat("x", 65) },
		"invalid_request":     func(f *fakeSession) { f.actions.ColdridgeCatchup.RequestId = "wrong/request" },
		"wrong_goal":          func(f *fakeSession) { f.actions.ColdridgeCatchup.Goal = coldridgeLevelingGoal },
		"old_start":           func(f *fakeSession) { f.actions.ColdridgeCatchup.StartLevel = 4 },
		"start_at_cap":        func(f *fakeSession) { f.actions.ColdridgeCatchup.StartLevel = 8 },
		"skip_level":          func(f *fakeSession) { f.actions.ColdridgeCatchup.TargetLevel = 7 },
		"multiple_pulls":      func(f *fakeSession) { f.actions.ColdridgeCatchup.MaxPulls = 2 },
		"source_not_reserved": func(f *fakeSession) { f.actions.ColdridgeCatchup.PullReserved = false },
		"no_source":           func(f *fakeSession) { f.actions.ColdridgeCatchup.SourceGuid = 0 },
		"source_not_target":   func(f *fakeSession) { f.actions.ColdridgeCatchup.TargetGuid++ },
		"expired":             func(f *fakeSession) { f.actions.ColdridgeCatchup.RemainingMs = 0 },
		"renewed_past_cap":    func(f *fakeSession) { f.actions.ColdridgeCatchup.RemainingMs = 180001 },
		"foreign_self":        func(f *fakeSession) { f.actions.ColdridgeCatchup.SelfGuid = 3 },
		"foreign_session":     func(f *fakeSession) { f.actions.ColdridgeCatchup.SessionId = "other" },
		"empty_session":       func(f *fakeSession) { f.actions.ColdridgeCatchup.SessionId = "" },
		"baseline_changed":    func(f *fakeSession) { f.actions.ColdridgeCatchup.PositionRevision++ },
		"no_baseline":         func(f *fakeSession) { f.actions.ColdridgeCatchup.PositionRevision = 0 },
		"stale_level":         func(f *fakeSession) { f.actions.ColdridgeCatchup.ObservedLevel = 6 },
		"unproven_cap":        func(f *fakeSession) { f.actions.ColdridgeCatchup.TargetLevelObserved = true },
		"regressed_level":     func(f *fakeSession) { f.world.Self.Level = 4; f.actions.ColdridgeCatchup.ObservedLevel = 4 },
		"past_next_cap": func(f *fakeSession) {
			f.world.Self.Level = 7
			f.actions.ColdridgeCatchup.ObservedLevel = 7
			f.actions.ColdridgeCatchup.TargetLevelObserved = true
		},
		"foreign_world":       func(f *fakeSession) { f.world.Self.Guid = 3 },
		"other_class":         func(f *fakeSession) { f.world.Self.ClassId = 8 },
		"unknown_level":       func(f *fakeSession) { f.world.Self.LevelKnown = false },
		"wrong_quest":         func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 182 },
		"missing_delivery":    func(f *fakeSession) { f.world.SelfQuestLog[1].QuestId = 0 },
		"delivery_incomplete": func(f *fakeSession) { f.world.SelfQuestLog[1].Complete = false },
		"delivery_state":      func(f *fakeSession) { f.world.SelfQuestLog[1].State = 0 },
		"218_complete":        func(f *fakeSession) { f.world.SelfQuestLog[0].Complete = true },
		"fake_credit":         func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[0] = 1 },
		"partial_counts":      func(f *fakeSession) { f.world.SelfQuestLog[1].ObjectiveCounts = []uint32{0} },
		"partial_log":         func(f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_slot":      func(f *fakeSession) { f.world.SelfQuestLog[24].Slot = 0 },
		"duplicate_quest":     func(f *fakeSession) { f.world.SelfQuestLog[24].QuestId = 218 },
		"extra_quest":         func(f *fakeSession) { f.world.SelfQuestLog[24].QuestId = 7 },
		"empty_slot_credit":   func(f *fakeSession) { f.world.SelfQuestLog[24].ObjectiveCounts = []uint32{1, 0, 0, 0} },
		"empty_slot_state":    func(f *fakeSession) { f.world.SelfQuestLog[24].Complete = true },
		"unknown_inventory":   func(f *fakeSession) { f.world.InventoryKnown = false },
		"partial_bags":        func(f *fakeSession) { f.world.InventoryBags = f.world.InventoryBags[:4] },
		"crate_missing":       func(f *fakeSession) { f.world.InventoryBags[0].Slots[0].Entry = 117 },
		"crate_replaced":      func(f *fakeSession) { f.world.InventoryBags[0].Slots[0].ItemGuid++ },
		"crate_stack":         func(f *fakeSession) { f.world.InventoryBags[0].Slots[0].StackCount = 2 },
		"unknown_item":        func(f *fakeSession) { f.world.InventoryBags[1].Slots[0].MetadataKnown = false },
		"partial_bag":         func(f *fakeSession) { f.world.InventoryBags[1].ContentsKnown = false },
		"raw_outside":         func(f *fakeSession) { f.world.Self.X = -8900 },
		"estimate_outside":    func(f *fakeSession) { f.actions.Position.X = -8900 },
		"larger_706":          func(f *fakeSession) { c := f.world.LootableCorpses[0]; c.Level, c.MaxHealth = 4, 86 },
		"mixed_profile":       func(f *fakeSession) { f.world.LootableCorpses[0].MaxHealth = 86 },
		"wrong_faction":       func(f *fakeSession) { f.world.LootableCorpses[0].FactionTemplate = 59 },
		"other_source":        func(f *fakeSession) { f.world.LootableCorpses[0].Guid++ },
		"living":              func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"not_lootable":        func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"cast_pending":        func(f *fakeSession) { f.actions.CastPending = true },
		"autoattack":          func(f *fakeSession) { f.actions.AutoAttackRequested = true },
		"uncertain_movement":  func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
		"own_combat":          func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"channel":             func(f *fakeSession) { f.world.Self.ChannelSpellId = 689 },
		"channel_unknown":     func(f *fakeSession) { f.world.Self.ChannelSpellKnown = false },
		"mixed_old_goal":      func(f *fakeSession) { f.actions.ColdridgeLeveling = &actionsv1.ColdridgeLevelingState{} },
		"mixed_road_goal":     func(f *fakeSession) { f.actions.RoadClear = &actionsv1.RoadClearState{} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := coldCatchupRig(t)
			coldLevelingNearbyOwner(f)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("invalid catchup borrowed ambient authority", current(t, s), f.calls, f.packets)
			}
		})
	}
}

func TestColdridgeCatchupRechecksBoundContextAndFinalCapacity(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"request":    func(f *fakeSession) { f.actions.ColdridgeCatchup.RequestId = "new-request" },
		"session":    func(f *fakeSession) { f.actions.ColdridgeCatchup.SessionId = "new-session" },
		"deadline":   func(f *fakeSession) { f.actions.ColdridgeCatchup.RemainingMs = 0 },
		"source":     func(f *fakeSession) { f.actions.ColdridgeCatchup.SourceGuid++ },
		"level_pair": func(f *fakeSession) { r := f.actions.ColdridgeCatchup; r.StartLevel, r.TargetLevel = 6, 7 },
		"crate":      func(f *fakeSession) { f.world.InventoryBags[0].Slots[0].StackCount = 2 },
	}
	for _, phase := range []string{"after_acquire", "final_renewal", "active_window"} {
		for name, change := range changes {
			t.Run(phase+"/"+name, func(t *testing.T) {
				s, f, _ := coldCatchupRig(t)
				if phase == "after_acquire" {
					f.afterAcquire = func() { change(f) }
				}
				if phase == "final_renewal" {
					f.afterRenew = func() { change(f) }
				}
				enable(t, s)
				s.Tick(context.Background())
				if phase == "active_window" {
					emit(s, opcode.SMSGLootResponse, windowPacket(f.actions.ColdridgeCatchup.SourceGuid, 0, lootItem{slot: 2, id: 750, count: 1}))
					change(f)
					s.Tick(context.Background())
					if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
						t.Fatal("changed handoff wrote or failed owned cleanup", current(t, s), f.packets)
					}
				} else if len(f.packets) != 0 || f.leaseOwner != "" || current(t, s).Enabled {
					t.Fatal("prepublication failure wrote or remained enabled", current(t, s), f.calls)
				}
			})
		}
	}
	for _, phase := range []string{"open", "store", "money"} {
		for _, change := range []string{"crate", "capacity", "level_regression", "source_profile", "token"} {
			t.Run(phase+"/final_"+change, func(t *testing.T) {
				s, f, _ := coldCatchupRig(t)
				enable(t, s)
				if phase != "open" {
					s.Tick(context.Background())
					money := uint32(0)
					if phase == "money" {
						money = 7
					}
					emit(s, opcode.SMSGLootResponse, windowPacket(f.actions.ColdridgeCatchup.SourceGuid, money, lootItem{slot: 2, id: 750, count: 1}))
				}
				if change == "token" {
					f.beforeStatus = func() {
						if s.state.PendingCorpseGuid != 0 {
							f.leaseToken = "replacement"
						}
					}
				} else {
					f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
						if !cold170FinalProofRead(s, f) {
							return
						}
						switch change {
						case "crate":
							w.InventoryBags[0].Slots[0].Entry = 117
						case "capacity":
							cold170FillLastGeneralSlot(w)
						case "level_regression":
							w.Self.Level = 4
						case "source_profile":
							w.LootableCorpses[0].FactionTemplate = 59
						}
					}
				}
				s.Tick(context.Background())
				op := uint32(opcode.CMSGLoot)
				if phase == "store" {
					op = opcode.CMSGAutostoreLootItem
				}
				if phase == "money" {
					op = opcode.CMSGLootMoney
				}
				if cold170PacketCount(f, op) != 0 {
					t.Fatal("final guard was superseded", current(t, s), f.calls, f.packets)
				}
			})
		}
	}
}

func TestColdridgeCatchupDeadlineIncludesLatencyAndNeverRenews(t *testing.T) {
	for _, phase := range []string{"open", "initial_status_latency", "renewal_shrink", "store", "money"} {
		t.Run(phase, func(t *testing.T) {
			s, f, advance := coldCatchupRig(t)
			r := f.actions.ColdridgeCatchup
			r.RemainingMs = 100
			if phase == "renewal_shrink" {
				r.RemainingMs = 179000
				f.afterRenew = func() { r.RemainingMs = 100 }
			}
			if phase == "initial_status_latency" {
				f.beforeStatus = func() {
					if s.state.PendingCorpseGuid == 0 {
						advance(60 * time.Millisecond)
					}
				}
			}
			enable(t, s)
			if phase == "store" || phase == "money" {
				s.Tick(context.Background())
				money := uint32(0)
				if phase == "money" {
					money = 7
				}
				emit(s, opcode.SMSGLootResponse, windowPacket(r.SourceGuid, money, lootItem{slot: 2, id: 750, count: 1}))
				r.RemainingMs = 180000
			}
			f.onSnapshot = func(_ int, _ *worldv1.SnapshotResponse) {
				if cold170FinalProofRead(s, f) {
					elapsed := 200 * time.Millisecond
					if phase == "initial_status_latency" {
						elapsed = 60 * time.Millisecond
					}
					advance(elapsed)
				}
			}
			s.Tick(context.Background())
			if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootMoney) != 0 {
				t.Fatal("expired handoff wrote", current(t, s), f.packets)
			}
			if phase == "store" || phase == "money" {
				f.onSnapshot = nil
				s.Tick(context.Background())
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
					t.Fatal("expiry blocked cleanup", current(t, s), f.packets)
				}
			} else {
				if len(f.packets) != 0 || f.leaseOwner != "" {
					t.Fatal("expiry reached OPEN", current(t, s))
				}
				f.onSnapshot, f.beforeStatus, f.afterRenew = nil, nil, nil
				r.RemainingMs = 180000
				enable(t, s)
				advance(2 * time.Second)
				s.Tick(context.Background())
				if len(f.packets) != 0 {
					t.Fatal("poll replenished deadline")
				}
			}
		})
	}
}

func TestColdridgeCatchupOneUseSurvivesEnableEvictionAndAmbiguousOpen(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprintf("ambiguous%t", ambiguous), func(t *testing.T) {
			s, f, advance := coldCatchupRig(t)
			r := f.actions.ColdridgeCatchup
			body := f.world.LootableCorpses[0]
			if ambiguous {
				f.failOpcode = opcode.CMSGLoot
			}
			enable(t, s)
			s.Tick(context.Background())
			if !s.catchupRequests[r.RequestId].consumed || cold170PacketCount(f, opcode.CMSGLoot) != 1 {
				t.Fatal("OPEN not reserved before callback")
			}
			if !ambiguous {
				emit(s, opcode.SMSGLootResponse, windowPacket(body.Guid, 0))
				s.Tick(context.Background())
				emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(body.Guid), 1))
				s.Tick(context.Background())
			}
			f.failOpcode = 0
			if current(t, s).Enabled {
				t.Fatal("completed/ambiguous handoff left loot enabled")
			}
			for _, mode := range []string{"same_request", "replacement_request", "different_request_same_body_after_eviction"} {
				if mode != "same_request" {
					r.RequestId = mode
				}
				f.world.LootableCorpses = nil
				enable(t, s)
				advance(2 * time.Second)
				s.Tick(context.Background())
				f.world.LootableCorpses = []*worldv1.CreatureState{body}
				enable(t, s)
				advance(2 * time.Second)
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
					t.Fatal("reopened consumed body", mode, current(t, s))
				}
			}
			// Only another explicit body plus request and another Enable can proceed.
			body.Guid++
			r.SourceGuid, r.TargetGuid, r.RequestId = body.Guid, body.Guid, "new-body-request"
			enable(t, s)
			advance(2 * time.Second)
			s.Tick(context.Background())
			if cold170PacketCount(f, opcode.CMSGLoot) != 2 {
				t.Fatal("fresh explicit body blocked", current(t, s), f.calls)
			}
		})
	}
}

func TestColdridgeCatchupFullLastSlotCleanupAndMonotonicLevel(t *testing.T) {
	s, f, _ := coldCatchupRig(t)
	enable(t, s)
	s.Tick(context.Background())
	r := f.actions.ColdridgeCatchup
	emit(s, opcode.SMSGLootResponse, windowPacket(r.SourceGuid, 0, lootItem{slot: 2, id: 750, count: 1}))
	s.Tick(context.Background())
	last := f.world.InventoryBags[1].Slots[19]
	last.ItemGuid, last.Entry, last.StackCount, last.MetadataKnown = 0x4000000000009000, 750, 1, true
	r.ObservedLevel, r.TargetLevelObserved, f.world.Self.Level = 6, true, 6
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(8, 750, 1))
	s.Tick(context.Background())
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 1 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
		t.Fatal("full carried layout discarded actual pending receipt", current(t, s), f.packets)
	}
	// The pure bound context may never regress an observed cap.
	g, ok := selectColdridgeCatchupLootGoal(f.world, f.actions, 8, s.state.SessionId, f.world.LootableCorpses[0])
	if !ok {
		t.Fatal("cap handoff invalid")
	}
	r.ObservedLevel, r.TargetLevelObserved, f.world.Self.Level = 5, false, 5
	if g.catchupContext(f.world, f.actions, 8, r.SourceGuid) {
		t.Fatal("level regressed inside bound operation")
	}
}

func TestColdridgeCatchupBoundedHistoryAndReleaseException(t *testing.T) {
	s, _, _ := coldCatchupRig(t)
	for i := range maxColdridgeCatchupLootRequests {
		g := coldridgeLootGoal{catchupRequest: fmt.Sprintf("r%d", i), catchupSession: s.state.SessionId, catchupStart: 5, catchupTarget: 6, corpseGUID: cold170LootGUID(706) + uint64(i)}
		if !s.bindCatchupLootDeadline(&g, s.now(), 1000) {
			t.Fatal("history filled early", i)
		}
	}
	g := coldridgeLootGoal{catchupRequest: "overflow", catchupSession: s.state.SessionId, catchupStart: 5, catchupTarget: 6, corpseGUID: cold170LootGUID(706) + 100}
	if s.bindCatchupLootDeadline(&g, s.now(), 1000) || len(s.catchupRequests) != 64 {
		t.Fatal("unbounded request history")
	}
	s.questColdridge = true
	s.coldridgeGoal = g
	if s.catchupPacketAllowed(opcode.CMSGAutostoreLootItem) || s.catchupPacketAllowed(opcode.CMSGLootMoney) || !s.catchupPacketAllowed(opcode.CMSGLootRelease) {
		t.Fatal("unowned window or cleanup exception invalid")
	}
	_, _ = s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false})
	if len(s.catchupRequests) != 64 {
		t.Fatal("enable discarded history")
	}
}

// Only the first actual acquire response is shortened; later STATUS and renew
// receipts advertise more time. This proves its smaller proof is not lost while
// the service is installing the selected goal after the acquire returns.
type catchupAcquireReceiptSession struct {
	sessionAPI
	change func(*actionsv1.InteractionLease)
}

func (s *catchupAcquireReceiptSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	result, err := s.sessionAPI.InvokeModule(ctx, r, opts...)
	if err != nil || r.Module != "buddy-actions" || r.Method != "acquire_interaction" {
		return result, err
	}
	req := &actionsv1.InteractionRequest{}
	if err := r.Request.UnmarshalTo(req); err != nil {
		return nil, err
	}
	if req.Token != "" {
		return result, err
	}
	lease := &actionsv1.InteractionLease{}
	if err := result.Result.UnmarshalTo(lease); err != nil {
		return nil, err
	}
	s.change(lease)
	result.Result, err = anypb.New(lease)
	return result, err
}

func TestColdridgeCatchupInitialAcquireCannotRenewShorterProof(t *testing.T) {
	s, f, advance := coldCatchupRig(t)
	s.session = &catchupAcquireReceiptSession{sessionAPI: s.session, change: func(lease *actionsv1.InteractionLease) { lease.State.ColdridgeCatchup.RemainingMs = 100 }}
	f.onSnapshot = func(_ int, _ *worldv1.SnapshotResponse) {
		if cold170FinalProofRead(s, f) {
			advance(200 * time.Millisecond)
		}
	}
	enable(t, s)
	s.Tick(context.Background())
	if len(f.packets) != 0 || f.leaseOwner != "" || current(t, s).Enabled || !s.catchupRequests[f.actions.ColdridgeCatchup.RequestId].finished {
		t.Fatal("acquire's smaller remaining allowance was replenished", current(t, s), f.calls)
	}
}

func TestColdridgeCatchupPrepublicationFailureCannotBeRearmed(t *testing.T) {
	for _, phase := range []string{"acquire_busy", "after_acquire", "after_renew"} {
		t.Run(phase, func(t *testing.T) {
			s, f, advance := coldCatchupRig(t)
			id := f.actions.ColdridgeCatchup.RequestId
			bad := func() { f.world.InventoryBags[0].Slots[0].Entry = 117 }
			switch phase {
			case "acquire_busy":
				f.leaseOwner = "other"
			case "after_acquire":
				f.afterAcquire = bad
			case "after_renew":
				f.afterRenew = bad
			}
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || current(t, s).Enabled || !s.catchupRequests[id].finished {
				t.Fatal("failed publication not retired", current(t, s), f.calls)
			}
			f.leaseOwner, f.leaseToken = "", ""
			f.afterAcquire, f.afterRenew = nil, nil
			f.world.InventoryBags[0].Slots[0].Entry = 7646
			for _, newID := range []string{id, "replacement-after-failed-stop"} {
				f.actions.ColdridgeCatchup.RequestId = newID
				enable(t, s)
				advance(2 * time.Second)
				s.Tick(context.Background())
				if len(f.packets) != 0 || f.leaseOwner != "" {
					t.Fatal("Enable/request replacement retried failed one-body attempt", current(t, s), f.calls)
				}
			}
		})
	}
}

func TestColdridgeCatchupCurrencyRequiresActualServerNotification(t *testing.T) {
	s, f, _ := coldCatchupRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(f.actions.ColdridgeCatchup.SourceGuid, 7))
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLootMoney) != 1 || current(t, s).MoneyAwardedNotified != 0 {
		t.Fatal("ordinary money write was blocked or became optimistic credit", current(t, s))
	}
	cold170AssertFinalWriteTail(t, f, false)
	emit(s, opcode.SMSGLootClearMoney, nil)
	emit(s, opcode.SMSGLootMoneyNotify, []byte{7, 0, 0, 0, 1})
	s.Tick(context.Background())
	s.Tick(context.Background())
	if current(t, s).MoneyAwardedNotified != 7 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
		t.Fatal("actual currency notification/release missing", current(t, s))
	}
}

func TestColdridgeCatchupPublishedIntentPreventsGatherFallback(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprint(published), func(t *testing.T) {
			s, f, _ := harvestRig(t)
			s.selfGUID, f.world.Self.Guid = 8, 8
			f.world.Self.ClassKnown, f.world.Self.ClassId, f.world.Self.LevelKnown, f.world.Self.Level = true, 9, true, 6
			if published {
				f.actions.ColdridgeCatchup = &actionsv1.ColdridgeCatchupState{}
			}
			enable(t, s)
			s.Tick(context.Background())
			want := 1
			if published {
				want = 0
			}
			if cold170PacketCount(f, opcode.CMSGCastSpell) != want || published && f.leaseOwner != "" {
				t.Fatal("published catchup borrowed gathering authority", current(t, s), f.calls)
			}
		})
	}
}
