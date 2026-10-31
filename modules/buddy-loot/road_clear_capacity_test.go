package buddyloot

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestRoadClearLootUsesSameFinalWorldForCapacityAndBoundSource(t *testing.T) {
	for _, mode := range []string{"unchanged", "full", "unknown_bag", "source_revision", "profile", "raw_revision", "raw_nan", "casting", "channel", "combat", "duplicate_body", "body_evicted_before_open"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := roadLootRig(t)
			reads := 0
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if !cold170FinalProofRead(s, f) {
					return
				}
				reads++
				w.InventoryRevision = 91
				switch mode {
				case "full":
					cold170FillLastGeneralSlot(w)
				case "unknown_bag":
					w.InventoryBags[1].ContentsKnown = false
				case "source_revision":
					w.LootableCorpses[0].PositionRevision++
				case "profile":
					w.LootableCorpses[0].FactionTemplate = 190
				case "raw_revision":
					w.Self.PositionRevision++
				case "raw_nan":
					w.Self.X = float32(math.NaN())
				case "casting":
					w.Self.UnitFlags |= 0x2000
				case "channel":
					w.Self.ChannelSpellKnown, w.Self.ChannelSpellId = true, 7620
				case "combat":
					w.Self.UnitFlags |= 0x80000
				case "duplicate_body":
					w.LootableCorpses = append(w.LootableCorpses, proto.Clone(w.LootableCorpses[0]).(*worldv1.CreatureState))
				case "body_evicted_before_open":
					w.LootableCorpses = nil
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if reads != 1 {
				t.Fatal("final coherent proof was not exercised", reads, f.calls)
			}
			if mode == "unchanged" {
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 || s.inventoryWriteRevision != 91 {
					t.Fatal("opening did not use the last complete carried layout", current(t, s))
				}
				cold170AssertFinalWriteTail(t, f, true)
			} else if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("stale source or capacity allowed opening", current(t, s), f.calls)
			}
		})
	}
	for _, change := range []string{"same_owner_replaced", "receipt_session", "receipt_request", "receipt_origin"} {
		t.Run("during_status/"+change, func(t *testing.T) {
			s, f, _ := roadLootRig(t)
			f.beforeStatus = func() {
				if s.state.PendingCorpseGuid == 0 {
					return
				}
				switch change {
				case "same_owner_replaced":
					f.leaseToken = "replacement-loot-token"
				case "receipt_session":
					f.actions.RoadClear.SessionId = "new-process"
				case "receipt_request":
					f.actions.RoadClear.RequestId = "replacement-road"
				case "receipt_origin":
					f.actions.RoadClear.Origin.Revision++
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 {
				t.Fatal("last status replaced bound receipt or exact lease", current(t, s))
			}
		})
	}
}

func TestRoadClearLootTTLIncludesDependencyLatencyAtEveryMutation(t *testing.T) {
	for _, op := range []uint32{opcode.CMSGLoot, opcode.CMSGLootMoney, opcode.CMSGAutostoreLootItem} {
		t.Run(fmt.Sprint(op), func(t *testing.T) {
			s, f, advance := roadLootRig(t)
			enable(t, s)
			if op != opcode.CMSGLoot {
				s.Tick(context.Background())
				if op == opcode.CMSGLootMoney {
					emit(s, opcode.SMSGLootResponse, windowPacket(f.actions.RoadClear.SourceGuid, 3))
				} else {
					emit(s, opcode.SMSGLootResponse, windowPacket(f.actions.RoadClear.SourceGuid, 0, lootItem{slot: 2, id: 117, count: 1}))
				}
			}
			before := cold170PacketCount(f, op)
			f.afterRenew = func() { f.actions.RoadClear.RemainingMs = 100 }
			f.onSnapshot = func(_ int, _ *worldv1.SnapshotResponse) {
				if cold170FinalProofRead(s, f) {
					advance(200 * time.Millisecond)
				}
			}
			s.Tick(context.Background())
			if cold170PacketCount(f, op) != before {
				t.Fatal("network latency extended road authority", current(t, s), f.calls)
			}
			// Expiration forbids another mutation, but an owned window may still
			// release exactly once through the existing exact-token cleanup.
			s.Tick(context.Background())
			if op != opcode.CMSGLoot && cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
				t.Fatal("expired mutation prevented bounded owned release", current(t, s))
			}
		})
	}
	t.Run("initial_status_latency", func(t *testing.T) {
		s, f, advance := roadLootRig(t)
		f.actions.RoadClear.RemainingMs = 100
		f.beforeStatus = func() { advance(200 * time.Millisecond) }
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 0 {
			t.Fatal("initial response arrival reset the road deadline")
		}
	})
}

func TestRoadClearLootConsumesRequestAndSourceAcrossReleaseEnableNewLifeAndAmbiguity(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			s, f, advance := roadLootRig(t)
			if ambiguous {
				f.failOpcode = opcode.CMSGLoot
			}
			enable(t, s)
			s.Tick(context.Background())
			guid := f.actions.RoadClear.SourceGuid
			if !s.roadRequests["road-one"].consumed {
				t.Fatal("OPEN intent was not consumed before transport")
			}
			if !ambiguous {
				emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0))
				s.Tick(context.Background())
				roadCompleteRelease(t, s, f)
			}
			f.failOpcode = 0
			f.world.LootableCorpses = nil
			advance(time.Second)
			s.Tick(context.Background())
			c := &worldv1.CreatureState{Guid: guid, Entry: 1718, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 9, X: f.actions.Position.X + 3, Y: f.actions.Position.Y, Z: f.actions.Position.Z, AliveKnown: true, HealthKnown: true, LevelKnown: true, Level: 3, MaxHealth: 71, FactionTemplate: 59, DynamicFlags: 1}
			f.world.LootableCorpses = []*worldv1.CreatureState{c}
			for _, id := range []string{"road-one", "different-id-same-body"} {
				f.actions.RoadClear.RequestId = id
				enable(t, s)
				advance(time.Second)
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
					t.Fatal("Enable/cache churn or new ID replayed the consumed source")
				}
			}
			// A different, explicitly reserved source has its own bounded ID.
			c.Guid++
			f.actions.RoadClear.SourceGuid, f.actions.RoadClear.TargetGuid = c.Guid, c.Guid
			f.actions.RoadClear.RequestId = "new-request-new-body"
			advance(time.Second)
			s.Tick(context.Background())
			if cold170PacketCount(f, opcode.CMSGLoot) != 2 {
				t.Fatal("fresh explicit source was blocked by an unrelated consumed request", current(t, s))
			}
		})
	}
}

func TestRoadClearLootActiveWindowRejectsReplacementAndInventoryFullCannotReplay(t *testing.T) {
	for _, mode := range []string{"token_replaced", "receipt_replaced", "combat_token_replaced", "full", "error50"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := roadLootRig(t)
			enable(t, s)
			s.Tick(context.Background())
			guid := f.actions.RoadClear.SourceGuid
			emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0, lootItem{slot: 2, id: 117, count: 1}))
			switch mode {
			case "token_replaced", "combat_token_replaced":
				f.leaseToken = "same-owner-new-token"
				if mode == "combat_token_replaced" {
					f.world.Self.UnitFlags |= 0x80000
				}
			case "receipt_replaced":
				f.actions.RoadClear.RequestId = "new-road"
			case "full":
				f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
					if cold170FinalProofRead(s, f) {
						cold170FillLastGeneralSlot(w)
						w.InventoryRevision = 90
					}
				}
			}
			s.Tick(context.Background())
			if mode == "error50" {
				if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 {
					t.Fatal("STORE control did not run")
				}
				failure := make([]byte, 18)
				failure[0] = 50
				emit(s, opcode.SMSGInventoryChangeFailure, failure)
				s.Tick(context.Background())
			} else if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 {
				t.Fatal("changed final authority or capacity allowed STORE", current(t, s))
			}
			if mode == "token_replaced" || mode == "combat_token_replaced" {
				if cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
					t.Fatal("old token released a replacement's window")
				}
				return
			}
			if cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
				t.Fatal("owned aborted window was not released once", current(t, s))
			}
			roadCompleteRelease(t, s, f)
			f.onSnapshot = nil
			if mode == "error50" {
				if !current(t, s).InventoryPaused || s.rejectedInventoryRevision != 31 {
					t.Fatal("error50 lost its exact rejected inventory revision", current(t, s))
				}
				advance(time.Second)
				s.Tick(context.Background())
				if !current(t, s).InventoryPaused {
					t.Fatal("same inventory revision cleared error50")
				}
				f.world.InventoryRevision = 32
				f.world.InventoryBags[1].ContentsKnown = false
				advance(time.Second)
				s.Tick(context.Background())
				if !current(t, s).InventoryPaused {
					t.Fatal("unknown newer bag cleared error50")
				}
				f.world.InventoryBags[1].ContentsKnown = true
				advance(time.Second)
				s.Tick(context.Background())
				if current(t, s).InventoryPaused {
					t.Fatal("complete newer free inventory did not clear capacity pause")
				}
			}
			for _, rev := range []uint64{31, 32, 91} {
				f.world.InventoryRevision = rev
				advance(time.Second)
				s.Tick(context.Background())
			}
			if cold170PacketCount(f, opcode.CMSGLoot) != 1 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) > 1 {
				t.Fatal("new free capacity replayed a consumed road window", current(t, s))
			}
		})
	}
}

func TestRoadClearLootRequestLifetimeBoundIsSeparateFrom706(t *testing.T) {
	s, f, _ := roadLootRig(t)
	for i := 0; i < 64; i++ {
		g := coldridgeLootGoal{roadRequest: fmt.Sprint(i), corpseGUID: f.actions.RoadClear.SourceGuid + uint64(i), roadObservedPackets: 100}
		if !s.bindRoadLootDeadline(&g, s.now(), 1000) {
			t.Fatal("valid lifetime slot rejected", i)
		}
	}
	g := coldridgeLootGoal{roadRequest: "overflow", corpseGUID: f.actions.RoadClear.SourceGuid + 65, roadObservedPackets: 100}
	if s.bindRoadLootDeadline(&g, s.now(), 1000) || len(s.roadRequests) != 64 || len(s.levelingRequests) != 0 {
		t.Fatal("road lifetime cap altered706 request authority")
	}
	// Existing706 L5 handoff continues through the old branch unchanged.
	s, f, _ = coldLevelingRig(t)
	f.world.Self.Level, f.actions.ColdridgeLeveling.ObservedLevel, f.actions.ColdridgeLeveling.LevelFiveObserved = 5, 5, true
	enable(t, s)
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLoot) != 1 || s.coldridgeGoal.roadRequest != "" || s.coldridgeGoal.levelingRequest == "" {
		t.Fatal("new road mode changed706's exact handoff", current(t, s))
	}
}

func TestRoadClearLootCausalWorldEpochGuardsEveryFinalMutation(t *testing.T) {
	for _, op := range []uint32{opcode.CMSGLoot, opcode.CMSGLootMoney, opcode.CMSGAutostoreLootItem} {
		for _, mode := range []string{"unchanged", "growing", "parse_poison", "zero_counter", "positive_counter_regression"} {
			t.Run(fmt.Sprintf("%d/%s", op, mode), func(t *testing.T) {
				s, f, _ := roadLootRig(t)
				enable(t, s)
				if op != opcode.CMSGLoot {
					s.Tick(context.Background())
					if op == opcode.CMSGLootMoney {
						emit(s, opcode.SMSGLootResponse, windowPacket(f.actions.RoadClear.SourceGuid, 3))
					} else {
						emit(s, opcode.SMSGLootResponse, windowPacket(f.actions.RoadClear.SourceGuid, 0, lootItem{slot: 2, id: 117, count: 1}))
					}
				}
				before := cold170PacketCount(f, op)
				finalReads := 0
				f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
					if s.state.PendingCorpseGuid == 0 {
						return
					}
					// This baseline is accepted after selection100. A later119
					// must fail even though it exceeds the original selection.
					if mode != "unchanged" {
						w.ObservedPackets = 120
					}
					if !cold170FinalProofRead(s, f) {
						return
					}
					finalReads++
					switch mode {
					case "growing":
						w.ObservedPackets = 121
					case "parse_poison":
						w.MalformedPackets = 1
					case "zero_counter":
						w.ObservedPackets = 0
					case "positive_counter_regression":
						w.ObservedPackets = 119
					}
				}
				s.Tick(context.Background())
				if finalReads != 1 {
					t.Fatal("did not reach the shared final-world epoch proof", finalReads, f.calls)
				}
				if mode == "unchanged" || mode == "growing" {
					if cold170PacketCount(f, op) != before+1 {
						t.Fatal("stable or monotonic epoch lost normal authority", current(t, s))
					}
					cold170AssertFinalWriteTail(t, f, op == opcode.CMSGLoot)
					want := uint64(100)
					if mode == "growing" {
						want = 121
					}
					if s.coldridgeGoal.roadObservedPackets != want {
						t.Fatal("final accepted counter was not retained", s.coldridgeGoal.roadObservedPackets)
					}
				} else if cold170PacketCount(f, op) != before {
					t.Fatal("parse poison or epoch regression allowed mutation", current(t, s), f.calls)
				} else if op != opcode.CMSGLoot && cold170PacketCount(f, opcode.CMSGLootRelease) != 1 {
					t.Fatal("invalid new authority blocked exact owned cleanup", current(t, s))
				}
			})
		}
	}
	for _, mode := range []string{"initial_parse_poison", "initial_zero_counter"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := roadLootRig(t)
			if mode == "initial_parse_poison" {
				f.world.MalformedPackets = 1
			} else {
				f.world.ObservedPackets = 0
			}
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("invalid initial epoch became the new accepted baseline", current(t, s))
			}
			r := s.roadRequests["road-one"]
			if r == nil || !r.causalBroken {
				t.Fatal("published request did not retain its initial causal break", r)
			}
			f.world.MalformedPackets, f.world.ObservedPackets = 0, 200
			enable(t, s)
			advance(time.Second)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" || !r.causalBroken {
				t.Fatal("polling repaired the initially invalid published request", current(t, s))
			}
		})
	}
}

func TestRoadClearLootUnopenedCausalBreakCannotHealOnEnableOrLaterCounter(t *testing.T) {
	for _, mode := range []string{"parse_poison", "zero_counter", "positive_counter_regression", "parse_poison_unknown_capacity", "zero_counter_full_capacity", "positive_counter_regression_full_capacity"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := roadLootRig(t)
			f.onSnapshot = func(_ int, w *worldv1.SnapshotResponse) {
				if s.state.PendingCorpseGuid == 0 {
					return
				}
				w.ObservedPackets = 120
				if !cold170FinalProofRead(s, f) {
					return
				}
				switch mode {
				case "parse_poison", "parse_poison_unknown_capacity":
					w.MalformedPackets = 1
				case "zero_counter", "zero_counter_full_capacity":
					w.ObservedPackets = 0
				case "positive_counter_regression", "positive_counter_regression_full_capacity":
					w.ObservedPackets = 119
				}
				if mode == "parse_poison_unknown_capacity" {
					w.InventoryBags[1].ContentsKnown = false
				} else if mode == "zero_counter_full_capacity" || mode == "positive_counter_regression_full_capacity" {
					cold170FillLastGeneralSlot(w)
				}
			}
			enable(t, s)
			s.Tick(context.Background())
			r := s.roadRequests["road-one"]
			if r == nil || !r.causalBroken || r.consumed || r.observedPackets != 120 || cold170PacketCount(f, opcode.CMSGLoot) != 0 || s.token != "" {
				t.Fatal("unopened finish lost or consumed the wrong causal record", current(t, s), r)
			}
			until := r.until
			f.onSnapshot = nil
			for _, count := range []uint64{119, 121, 200} {
				f.world.ObservedPackets = count
				enable(t, s)
				advance(time.Second)
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 0 || f.leaseOwner != "" || !r.causalBroken || !r.until.Equal(until) {
					t.Fatal("polling, Enable or a growing count healed the causal break", current(t, s), count)
				}
			}
			f.actions.RoadClear.RequestId = "replacement-id-same-broken-source"
			advance(time.Second)
			s.Tick(context.Background())
			if len(f.packets) != 0 {
				t.Fatal("a different request ID healed the same broken source")
			}
		})
	}
}
