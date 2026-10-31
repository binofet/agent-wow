package buddyloot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func roadLootRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold170CapacityRig(t, 1718)
	s.session = f // Exercise the actual V3 fake's token renewal and RPC trace.
	s.selfGUID, f.world.Self.Guid = 8, 8
	s.state.SessionId = "road-session-fixture"
	f.world.ObservedPackets = 100
	p := f.world.Self
	p.Level, p.Health, p.MaxHealth = 5, 113, 113
	p.X, p.Y, p.Z = -6450, 563, 385.94476 // Separate retained raw baseline.
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = -6248.84, 135.042, 431.5892
	f.actions.Position.Revision, f.actions.Position.Estimated = 636, true
	f.world.SelfQuestLog[0].QuestId = 218
	c := f.world.LootableCorpses[0]
	c.Level, c.MaxHealth, c.FactionTemplate, c.PositionRevision = 3, 71, 59, 9
	c.X, c.Y, c.Z = f.actions.Position.X+3, f.actions.Position.Y, f.actions.Position.Z
	f.actions.RoadClear = &actionsv1.RoadClearState{
		RequestId: "road-one", Goal: roadClearGoal, TargetGuid: c.Guid, SourceGuid: c.Guid,
		SelfGuid: s.selfGUID, SessionId: s.state.SessionId, PositionRevision: p.PositionRevision,
		Origin: proto.Clone(f.actions.Position).(*actionsv1.ClientPosition), RemainingMs: 170000,
		MaxPulls: 1, PullReserved: true, ObservedLevel: 5,
		SelectedEntry: 1718, SelectedLevel: 3, SelectedMaxHealth: 71, SelectedFactionTemplate: 59,
		SourceDeathObserved: true, SourceDeathPositionRevision: c.PositionRevision,
	}
	return s, f, advance
}

func roadNearbyOwner(f *fakeSession) {
	p := f.actions.Position
	f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, AliveKnown: true, Alive: true, MapKnown: true, PositionKnown: true, UnitFlagsKnown: true, X: p.X, Y: p.Y, Z: p.Z}
}

func roadCompleteRelease(t *testing.T, s *Service, f *fakeSession) {
	t.Helper()
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(f.actions.RoadClear.SourceGuid), 1))
	s.Tick(context.Background())
}

func TestRoadClearLootExactDeathSourceNeedsIntentAndActualReceipts(t *testing.T) {
	for _, level := range []uint32{3, 4} {
		for _, coinsOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("level%d/coinsOnly%v", level, coinsOnly), func(t *testing.T) {
				s, f, _ := roadLootRig(t)
				c, r := f.world.LootableCorpses[0], f.actions.RoadClear
				c.Level, r.SelectedLevel = level, level
				if level == 4 {
					c.MaxHealth, r.SelectedMaxHealth = 86, 86
				}
				// The normal operation does not require or invent a quest mapping.
				f.world.SelfQuestLog[0].QuestId = 0
				s.Tick(context.Background())
				if len(f.packets) != 0 {
					t.Fatal("death handoff enabled loot implicitly")
				}
				enable(t, s)
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 || s.coldridgeGoal.roadRequest != r.RequestId || s.coldridgeGoal.corpseGUID != c.Guid || f.approachCalls != 0 {
					t.Fatal("road source was not bound without approach", current(t, s), f.calls)
				}
				cold170AssertFinalWriteTail(t, f, true)
				if coinsOnly {
					emit(s, opcode.SMSGLootResponse, windowPacket(c.Guid, 7))
				} else {
					emit(s, opcode.SMSGLootResponse, windowPacket(c.Guid, 7, lootItem{slot: 2, id: 117, count: 1}))
				}
				s.Tick(context.Background())
				cold170AssertFinalWriteTail(t, f, false)
				if current(t, s).MoneyAwardedNotified != 0 || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("transport fabricated loot receipts")
				}
				emit(s, opcode.SMSGLootClearMoney, nil)
				emit(s, opcode.SMSGLootMoneyNotify, []byte{7, 0, 0, 0, 1})
				s.Tick(context.Background())
				s.Tick(context.Background())
				if !coinsOnly {
					cold170AssertFinalWriteTail(t, f, false)
					emit(s, opcode.SMSGLootRemoved, []byte{2})
					emit(s, opcode.SMSGItemPushResult, pushPacket(3, 117, 1))
					s.Tick(context.Background())
					if current(t, s).ItemsStoredConfirmed != 0 {
						t.Fatal("foreign push became own item receipt")
					}
					// An actual pending window can survive discovery eviction.
					f.world.LootableCorpses = nil
					emit(s, opcode.SMSGItemPushResult, pushPacket(s.selfGUID, 117, 1))
					s.Tick(context.Background())
					s.Tick(context.Background())
				}
				if cold170PacketCount(f, opcode.CMSGLootRelease) != 1 || current(t, s).MoneyAwardedNotified != 7 || !coinsOnly && current(t, s).ItemsStoredConfirmed != 1 {
					t.Fatal("matched receipts did not reach one release", current(t, s))
				}
				roadCompleteRelease(t, s, f)
				if f.leaseOwner != "" || current(t, s).PendingCorpseGuid != 0 || f.world.Self.Level != 5 || f.world.SelfQuestLog[0].ObjectiveCounts[0] != 0 {
					t.Fatal("loot changed progression or retained its lease", current(t, s))
				}
			})
		}
	}
}

func TestRoadClearLootRejectsMalformedForeignAndBorrowedAuthority(t *testing.T) {
	t.Run("coherent_protected_guid7", func(t *testing.T) {
		s, f, _ := roadLootRig(t)
		s.selfGUID, f.world.Self.Guid, f.actions.RoadClear.SelfGuid = 7, 7, 7
		roadNearbyOwner(f)
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 0 || f.approachCalls != 0 || f.leaseOwner != "" {
			t.Fatal("a consistent other body acquired road authority", current(t, s), f.calls)
		}
	})
	changes := map[string]func(*fakeSession){
		"missing_receipt":        func(f *fakeSession) { f.actions.RoadClear = nil },
		"empty_id":               func(f *fakeSession) { f.actions.RoadClear.RequestId = "" },
		"oversize_id":            func(f *fakeSession) { f.actions.RoadClear.RequestId = strings.Repeat("x", 65) },
		"invalid_id":             func(f *fakeSession) { f.actions.RoadClear.RequestId = "not/a/request" },
		"wrong_goal":             func(f *fakeSession) { f.actions.RoadClear.Goal = coldridgeLevelingGoal },
		"wrong_session":          func(f *fakeSession) { f.actions.RoadClear.SessionId = "old-process" },
		"wrong_self":             func(f *fakeSession) { f.actions.RoadClear.SelfGuid = 3 },
		"wrong_target":           func(f *fakeSession) { f.actions.RoadClear.TargetGuid++ },
		"wrong_source":           func(f *fakeSession) { f.actions.RoadClear.SourceGuid++ },
		"unreserved":             func(f *fakeSession) { f.actions.RoadClear.PullReserved = false },
		"no_death":               func(f *fakeSession) { f.actions.RoadClear.SourceDeathObserved = false },
		"unknown_death_revision": func(f *fakeSession) { f.actions.RoadClear.SourceDeathPositionRevision = 0 },
		"changed_death_revision": func(f *fakeSession) { f.world.LootableCorpses[0].PositionRevision++ },
		"expired":                func(f *fakeSession) { f.actions.RoadClear.RemainingMs = 0 },
		"extended_deadline":      func(f *fakeSession) { f.actions.RoadClear.RemainingMs = 180001 },
		"multiple_pulls":         func(f *fakeSession) { f.actions.RoadClear.MaxPulls = 2 },
		"wrong_level_receipt":    func(f *fakeSession) { f.actions.RoadClear.ObservedLevel = 4 },
		"own_level4":             func(f *fakeSession) { f.world.Self.Level = 4 },
		"own_level6":             func(f *fakeSession) { f.world.Self.Level = 6 },
		"unknown_level":          func(f *fakeSession) { f.world.Self.LevelKnown = false },
		"different_baseline":     func(f *fakeSession) { f.world.Self.PositionRevision++ },
		"no_origin":              func(f *fakeSession) { f.actions.RoadClear.Origin = nil },
		"unknown_origin":         func(f *fakeSession) { f.actions.RoadClear.Origin.Known = false },
		"moved_from_origin":      func(f *fakeSession) { f.actions.Position.X += .1 },
		"regressed_estimate":     func(f *fakeSession) { f.actions.Position.Revision = 0 },
		"origin_nan":             func(f *fakeSession) { f.actions.RoadClear.Origin.X = float32(math.NaN()) },
		"origin_outside":         func(f *fakeSession) { f.actions.RoadClear.Origin.X = -6260.01 },
		"wrong_faction":          func(f *fakeSession) { f.world.LootableCorpses[0].FactionTemplate = 190 },
		"mixed_profile":          func(f *fakeSession) { f.world.LootableCorpses[0].MaxHealth = 86 },
		"wrong_selected_profile": func(f *fakeSession) { f.actions.RoadClear.SelectedMaxHealth = 86 },
		"body_outside":           func(f *fakeSession) { f.world.LootableCorpses[0].Y = 175.01 },
		"body_destination_outside": func(f *fakeSession) {
			f.world.LootableCorpses[0].Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: -6200, DestinationY: 176, DestinationZ: 431}
		},
		"body_alive":               func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"body_not_lootable":        func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"body_too_far_no_approach": func(f *fakeSession) { f.world.LootableCorpses[0].X = f.actions.Position.X + 6 },
		"unknown_log":              func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"partial_log":              func(f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_slot":           func(f *fakeSession) { f.world.SelfQuestLog[24].Slot = 0 },
		"duplicate_quest":          func(f *fakeSession) { f.world.SelfQuestLog[24].QuestId = 218 },
		"partial_objectives":       func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{0} },
		"overflow_counter":         func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[0] = 65536 },
		"moving":                   func(f *fakeSession) { f.actions.Moving = true },
		"pending_cast":             func(f *fakeSession) { f.actions.CastPending = true },
		"raw_casting":              func(f *fakeSession) { f.world.Self.UnitFlags |= 0x2000 },
		"raw_channel_spell":        func(f *fakeSession) { f.world.Self.ChannelSpellKnown, f.world.Self.ChannelSpellId = true, 7620 },
		"raw_channel_object":       func(f *fakeSession) { f.world.Self.ChannelObjectKnown, f.world.Self.ChannelObjectGuid = true, 7 },
		"raw_nan":                  func(f *fakeSession) { f.world.Self.X = float32(math.NaN()) },
		"combat":                   func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"ambiguous_two_modes":      func(f *fakeSession) { f.actions.ColdridgeLeveling = &actionsv1.ColdridgeLevelingState{} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := roadLootRig(t)
			// Invalid explicit road intent must not be rescued by a real nearby
			// owner or an inserted supported quest with its own corpse.
			if name != "missing_receipt" {
				roadNearbyOwner(f)
			}
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.approachCalls != 0 || f.leaseOwner != "" {
				t.Fatal("invalid road handoff gained packet or movement authority", current(t, s), f.calls)
			}
		})
	}
	for _, quest := range []uint32{179, 182} {
		t.Run(fmt.Sprintf("quest%d_cannot_rescue", quest), func(t *testing.T) {
			s, f, _ := roadLootRig(t)
			roadNearbyOwner(f)
			f.world.SelfQuestLog[0].QuestId = quest
			f.world.LootableCorpses[0].Entry, f.world.LootableCorpses[0].Guid = 705, cold170LootGUID(705)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 {
				t.Fatal("quest or nearby owner replaced the selected road source")
			}
		})
	}
}

type roadStatusFailure struct{ *fakeSession }

func (f *roadStatusFailure) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-actions" && r.Method == "status" {
		return nil, errors.New("unknown road intent")
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

func TestRoadClearIntentPreflightNeverBorrowsGatherButAbsentRecordPreservesIt(t *testing.T) {
	for _, mode := range []string{"no_road_control", "published_road", "published_road_unknown_level", "published_road_level4", "published_road_unknown_class", "unknown_status"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			f.world.Self.ClassKnown, f.world.Self.ClassId, f.world.Self.LevelKnown, f.world.Self.Level = true, 9, true, 5
			if strings.HasPrefix(mode, "published_road") {
				f.actions.RoadClear = &actionsv1.RoadClearState{Goal: roadClearGoal}
			}
			switch mode {
			case "published_road_unknown_level":
				f.world.Self.LevelKnown = false
			case "published_road_level4":
				f.world.Self.Level = 4
			case "published_road_unknown_class":
				f.world.Self.ClassKnown = false
			}
			if mode == "unknown_status" {
				s.session = &roadStatusFailure{f}
			}
			enable(t, s)
			s.Tick(context.Background())
			if mode == "no_road_control" {
				if cold170PacketCount(f, opcode.CMSGCastSpell) != 1 {
					t.Fatal("absent road record changed ordinary quest gather", current(t, s), f.calls)
				}
			} else if len(f.packets) != 0 || f.approachCalls != 0 || f.leaseOwner != "" {
				t.Fatal("published or unknown road intent borrowed gather", current(t, s), f.calls)
			}
		})
	}
}
