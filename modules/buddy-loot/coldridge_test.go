package buddyloot

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const coldWolfGUID uint64 = 0xf1300002c1000012

func coldLootRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := wolfRig(t)
	p := f.world.Self
	p.ClassKnown, p.ClassId, p.LevelKnown, p.Level = true, 9, true, 1
	p.X, p.Y, p.Z = -6240, 331, 383
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = p.X, p.Y, p.Z
	f.world.SelfQuestLog[0].QuestId = 179
	c := f.world.LootableCorpses[0]
	c.Guid, c.Entry, c.X, c.Y, c.Z = coldWolfGUID, 705, p.X+2, p.Y, p.Z
	s.session = &coldridgeLeaseSession{fakeSession: f}
	return s, f, advance
}

func TestColdridgeOwnQuestLootRequiresExplicitIntentAndActualScope(t *testing.T) {
	s, f, _ := coldLootRig(t)
	s.Tick(context.Background())
	if len(f.packets) != 0 || current(t, s).Enabled {
		t.Fatal("quest enabled looting without intent")
	}
	for _, complete := range []bool{false, true} {
		s, f, _ := coldLootRig(t)
		f.world.SelfQuestLog[0].Complete = complete
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || !s.questColdridge || current(t, s).PendingCorpseGuid != coldWolfGUID {
			t.Fatal("actual own quest corpse blocked with owner offline", current(t, s))
		}
	}
}

func TestColdridgeLootRejectsOtherQuestsCreaturesClassesAndUnsafeState(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"foreign_self":     func(f *fakeSession) { f.world.Self.Guid = 3 },
		"wrong_class":      func(f *fakeSession) { f.world.Self.ClassId = 8 },
		"unknown_class":    func(f *fakeSession) { f.world.Self.ClassKnown = false },
		"unknown_level":    func(f *fakeSession) { f.world.Self.LevelKnown = false },
		"level_cap":        func(f *fakeSession) { f.world.Self.Level = 6 },
		"quest":            func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 33 },
		"failed":           func(f *fakeSession) { f.world.SelfQuestLog[0].Failed = true },
		"unknown_log":      func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"partial_log":      func(f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_slot":   func(f *fakeSession) { f.world.SelfQuestLog[24].Slot = 0 },
		"creature_entry":   func(f *fakeSession) { f.world.LootableCorpses[0].Entry = 299 },
		"encoded_entry":    func(f *fakeSession) { f.world.LootableCorpses[0].Guid = corpseGUID },
		"living":           func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"not_lootable":     func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"combat":           func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"unknown_position": func(f *fakeSession) { f.actions.Position.Known = false },
		"uncertain_motion": func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
		"region":           func(f *fakeSession) { f.world.Self.X = -8900 },
		"estimate_region":  func(f *fakeSession) { f.actions.Position.X = -8900 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := coldLootRig(t)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("unsafe independent loot", current(t, s))
			}
		})
	}
}

func TestColdridgeLootRechecksStoppedScopeAndPreemptsUnsafeWindow(t *testing.T) {
	for _, phase := range []string{"after_lease", "open_window"} {
		for _, mode := range []string{"quest", "combat", "position", "corpse"} {
			t.Run(phase+"_"+mode, func(t *testing.T) {
				s, f, _ := coldLootRig(t)
				change := func() {
					switch mode {
					case "quest":
						f.world.SelfQuestLog[0].QuestId = 0
					case "combat":
						f.world.Self.UnitFlags |= 0x80000
					case "position":
						f.actions.Position.Known = false
					case "corpse":
						f.world.LootableCorpses[0].Entry = 299
					}
				}
				if phase == "after_lease" {
					f.afterAcquire = change
				}
				enable(t, s)
				s.Tick(context.Background())
				if phase == "after_lease" {
					if len(f.packets) != 0 || f.leaseOwner != "" {
						t.Fatal(current(t, s))
					}
					return
				}
				// A corpse may leave the nearby cache after opening; the bound
				// transaction is still legitimate. Test only own safety here.
				if mode == "corpse" {
					return
				}
				emit(s, opcode.SMSGLootResponse, windowPacket(coldWolfGUID, 0, lootItem{slot: 2, id: 750, count: 1}))
				change()
				s.Tick(context.Background())
				if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal(current(t, s))
				}
			})
		}
	}
}

func TestColdridgeMeatNeedsOwnItemPushSlotRemovalAndRelease(t *testing.T) {
	s, f, _ := coldLootRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(coldWolfGUID, 0, lootItem{slot: 2, id: 750, count: 1}))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("write claimed receipt")
	}
	f.world.LootableCorpses = nil
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(3, 750, 1))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("foreign item receipt claimed own meat")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 750, 1))
	s.Tick(context.Background())
	s.Tick(context.Background())
	st := current(t, s)
	if st.ItemsStoredConfirmed != 1 || len(st.RecentItems) != 1 || st.RecentItems[0].ItemId != 750 || st.RecentItems[0].Count != 1 || st.ReleasesObserved != 0 || f.world.SelfQuestLog[0].Complete {
		t.Fatal(st)
	}
	release := releasePacket()
	// Ordinary release helper uses the other fixture's GUID; bind this receipt.
	for i := range 8 {
		release[i] = byte(coldWolfGUID >> (8 * i))
	}
	emit(s, opcode.SMSGLootReleaseResponse, release)
	s.Tick(context.Background())
	if f.leaseOwner != "" || current(t, s).ReleasesObserved != 1 {
		t.Fatal(current(t, s))
	}
}

func cold170LootGUID(entry uint32) uint64 {
	return uint64(0xf130000000000000) | uint64(entry)<<24 | 0x31
}

// Count exact-token renewals while delegating all behavior and call recording
// to the complete carried-inventory fake, including its afterRenew callback.
type coldridgeLeaseSession struct {
	*fakeSession
	renewAttempts int
}

func (f *coldridgeLeaseSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-actions" && r.Method == "acquire_interaction" {
		request := &actionsv1.InteractionRequest{}
		if err := r.Request.UnmarshalTo(request); err != nil {
			return nil, err
		}
		if request.Token != "" {
			f.renewAttempts++
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

func cold170LootRig(t *testing.T, entry uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := coldLootRig(t)
	f.world.Self.Level = 2
	f.world.Self.Health, f.world.Self.MaxHealth = 68, 68
	f.world.Self.PositionRevision = 11
	f.world.SelfQuestLogRevision = 7
	q := f.world.SelfQuestLog[0]
	q.QuestId, q.ObjectiveCounts = 170, []uint32{0, 0, 0, 0}
	c := f.world.LootableCorpses[0]
	c.Guid, c.Entry = cold170LootGUID(entry), entry
	c.LevelKnown, c.Level, c.MaxHealth = true, 2, 55
	c.FactionTemplate, c.UnitFlags_2 = 36, 2048
	return s, f, advance
}

func cold170ItemPush(player uint64, entry, count uint32) []byte {
	p := pushPacket(player, entry, count)
	p[20] = 255 // Raw INVENTORY_SLOT_BAG_0; world inventory slots use another representation.
	binary.LittleEndian.PutUint32(p[21:], 23)
	return p
}

func cold170PacketCount(f *fakeSession, op uint32) int {
	n := 0
	for _, p := range f.packets {
		if p.Opcode == op {
			n++
		}
	}
	return n
}

func cold170MoveQuest(w *worldv1.SnapshotResponse, q *worldv1.QuestLogEntry, slot uint32) {
	for _, other := range w.SelfQuestLog {
		if other != q && other.Slot == slot {
			other.Slot = q.Slot
			break
		}
	}
	q.Slot = slot
	slices.Reverse(w.SelfQuestLog)
}

func TestColdridge170LootRequiresExplicitIntentAndActualMatchingCorpse(t *testing.T) {
	for _, entry := range []uint32{707, 724} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			s, f, _ := cold170LootRig(t, entry)
			s.Tick(context.Background())
			if len(f.packets) != 0 || current(t, s).Enabled {
				t.Fatal("accepted quest implicitly enabled looting")
			}
			enable(t, s)
			s.Tick(context.Background())
			got := current(t, s)
			if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || len(f.packets[0].Payload) != 8 || binary.LittleEndian.Uint64(f.packets[0].Payload) != cold170LootGUID(entry) || got.PendingCorpseGuid != cold170LootGUID(entry) || got.Phase != "waiting_open" || s.coldridgeGoal.questID != 170 {
				t.Fatal("own observed threat corpse did not bind and open", got, f.packets)
			}
			if got.LootWindowsObserved != 0 || got.ItemsStoredConfirmed != 0 || got.MoneyAwardedNotified != 0 || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{0, 0, 0, 0}) {
				t.Fatal("loot transport fabricated server or quest progress", got)
			}
		})
	}
}

func TestColdridge170LootAllowsLastCorpseAfterObjectiveCompletion(t *testing.T) {
	for _, entry := range []uint32{707, 724} {
		for _, mode := range []string{"complete_flag", "complete_state", "both_counts_six"} {
			t.Run(fmt.Sprintf("%d/%s", entry, mode), func(t *testing.T) {
				s, f, _ := cold170LootRig(t, entry)
				q := f.world.SelfQuestLog[0]
				q.ObjectiveCounts = []uint32{6, 6, 0, 0}
				q.Complete, q.State = mode == "complete_flag", 0
				if mode == "complete_state" {
					q.State = 1
				}
				enable(t, s)
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 || current(t, s).PendingCorpseGuid != cold170LootGUID(entry) {
					t.Fatal("objective completion blocked final publicly lootable corpse", current(t, s))
				}
			})
		}
	}
}

func TestColdridge170LootRejectsQuestCorpseCrossProductsAndUnsupportedProfiles(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"179_does_not_authorize707": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 179 },
		"182_does_not_authorize707": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 182 },
		"183_does_not_authorize707": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 183 },
		"170_does_not_authorize705": func(f *fakeSession) { c := f.world.LootableCorpses[0]; c.Entry, c.Guid = 705, cold170LootGUID(705) },
		"170_does_not_authorize706": func(f *fakeSession) { c := f.world.LootableCorpses[0]; c.Entry, c.Guid = 706, cold170LootGUID(706) },
		"170_does_not_authorize299": func(f *fakeSession) { c := f.world.LootableCorpses[0]; c.Entry, c.Guid = 299, cold170LootGUID(299) },
		"no_quest":                  func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 0 },
		"other_actor":               func(f *fakeSession) { f.world.Self.Guid = 3 },
		"other_class":               func(f *fakeSession) { f.world.Self.ClassId = 8 },
		"level_one_actor":           func(f *fakeSession) { f.world.Self.Level = 1 },
		"actor_level_cap":           func(f *fakeSession) { f.world.Self.Level = 6 },
		"unknown_actor_class":       func(f *fakeSession) { f.world.Self.ClassKnown = false },
		"unknown_actor_level":       func(f *fakeSession) { f.world.Self.LevelKnown = false },
		"unknown_corpse_level":      func(f *fakeSession) { f.world.LootableCorpses[0].LevelKnown = false },
		"level_one_corpse":          func(f *fakeSession) { f.world.LootableCorpses[0].Level = 1 },
		"level_three_corpse":        func(f *fakeSession) { f.world.LootableCorpses[0].Level = 3 },
		"wrong_max_health":          func(f *fakeSession) { f.world.LootableCorpses[0].MaxHealth = 56 },
		"wrong_faction":             func(f *fakeSession) { f.world.LootableCorpses[0].FactionTemplate = 35 },
		"alive":                     func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"nonzero_health":            func(f *fakeSession) { f.world.LootableCorpses[0].Health = 1 },
		"unknown_health":            func(f *fakeSession) { f.world.LootableCorpses[0].HealthKnown = false },
		"unknown_death":             func(f *fakeSession) { f.world.LootableCorpses[0].AliveKnown = false },
		"not_present":               func(f *fakeSession) { f.world.LootableCorpses[0].Present = false },
		"no_public_loot_permission": func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"encoded_entry_mismatch":    func(f *fakeSession) { f.world.LootableCorpses[0].Guid = cold170LootGUID(724) },
		"zero_guid_low":             func(f *fakeSession) { f.world.LootableCorpses[0].Guid &^= 0xffffff },
		"unsupported_guid_kind": func(f *fakeSession) {
			f.world.LootableCorpses[0].Guid = uint64(0xf150000000000000) | uint64(707)<<24 | 0x31
		},
		"wrong_corpse_map":    func(f *fakeSession) { f.world.LootableCorpses[0].MapId = 1 },
		"unknown_corpse_map":  func(f *fakeSession) { f.world.LootableCorpses[0].MapKnown = false },
		"wrong_own_map":       func(f *fakeSession) { f.world.Self.MapId = 1 },
		"unknown_position":    func(f *fakeSession) { f.actions.Position.Known = false },
		"nonfinite_position":  func(f *fakeSession) { f.actions.Position.X = float32(math.NaN()) },
		"uncertain_motion":    func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
		"combat":              func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"outside_region":      func(f *fakeSession) { f.world.Self.X = -8900 },
		"beyond_nearby_range": func(f *fakeSession) { f.world.LootableCorpses[0].X = f.actions.Position.X + 12.1 },
		"vertical":            func(f *fakeSession) { f.world.LootableCorpses[0].Z = f.actions.Position.Z + 1.6 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := cold170LootRig(t, 707)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("unsupported independent loot acquired authority", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridge170LootRejectsMalformedOwnLogAndObjectiveCounters(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"unknown_log":    func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"partial_log":    func(f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"nil_row":        func(f *fakeSession) { f.world.SelfQuestLog[24] = nil },
		"duplicate_slot": func(f *fakeSession) { f.world.SelfQuestLog[24].Slot = 0 },
		"duplicate_quest": func(f *fakeSession) {
			f.world.SelfQuestLog[24].QuestId, f.world.SelfQuestLog[24].ObjectiveCounts = 170, []uint32{0, 0, 0, 0}
		},
		"absent_counts":  func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts = nil },
		"partial_counts": func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{0, 0, 0} },
		"extra_counts":   func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{0, 0, 0, 0, 0} },
		"707_over_six":   func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[0] = 7 },
		"724_over_six":   func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[1] = 7 },
		"third_count":    func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[2] = 1 },
		"fourth_count":   func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[3] = 1 },
		"overflow_count": func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[0] = 0x10000 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := cold170LootRig(t, 724)
			change(f)
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" {
				t.Fatal("malformed own quest observation authorized loot", current(t, s))
			}
		})
	}
}

func TestColdridge170LootRetainsQuestBindingAcrossSlotReorder(t *testing.T) {
	s, f, _ := cold170LootRig(t, 724)
	q := f.world.SelfQuestLog[0]
	f.afterAcquire = func() { cold170MoveQuest(f.world, q, 17) }
	enable(t, s)
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLoot) != 1 {
		t.Fatal("slot reorder during stop lost actual own quest", current(t, s))
	}
	emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(724), 0, lootItem{slot: 2, id: 123, count: 1}))
	cold170MoveQuest(f.world, q, 9)
	q.ObjectiveCounts[1] = 1
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || s.coldridgeGoal.questID != 170 || s.coldridgeGoal.counts[1] != 1 {
		t.Fatal("quest identity depended on array or log slot", current(t, s))
	}
}

func TestColdridge170LootRevalidatesExactCorpseAfterAcquire(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"missing":        func(f *fakeSession) { f.world.LootableCorpses = nil },
		"other_guid":     func(f *fakeSession) { f.world.LootableCorpses[0].Guid++ },
		"other_entry":    func(f *fakeSession) { f.world.LootableCorpses[0].Entry = 724 },
		"other_profile":  func(f *fakeSession) { f.world.LootableCorpses[0].MaxHealth = 56 },
		"other_map":      func(f *fakeSession) { f.world.LootableCorpses[0].MapId = 1 },
		"not_present":    func(f *fakeSession) { f.world.LootableCorpses[0].Present = false },
		"alive":          func(f *fakeSession) { f.world.LootableCorpses[0].Alive = true },
		"no_permission":  func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 },
		"out_of_range":   func(f *fakeSession) { f.world.LootableCorpses[0].X = f.actions.Position.X + 12.1 },
		"unknown_health": func(f *fakeSession) { f.world.LootableCorpses[0].HealthKnown = false },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := cold170LootRig(t, 707)
			f.afterAcquire = func() { change(f) }
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" || f.releases != 1 || current(t, s).PendingCorpseGuid != 0 {
				t.Fatal("pre-stop corpse proof survived a contradictory current observation", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridge170LootCannotReplaceBoundQuestOrRegressAfterStopOrWindow(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"removed_then179": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 179 },
		"failed_then179": func(f *fakeSession) {
			f.world.SelfQuestLog[0].Failed = true
			f.world.SelfQuestLog[1].QuestId = 179
		},
		"failed_state_then179": func(f *fakeSession) {
			f.world.SelfQuestLog[0].State = 2
			f.world.SelfQuestLog[1].QuestId = 179
		},
		"regressed707_then179": func(f *fakeSession) {
			f.world.SelfQuestLog[0].ObjectiveCounts[0] = 2
			f.world.SelfQuestLog[1].QuestId = 179
		},
		"regressed724":     func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[1] = 1 },
		"malformed_counts": func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts = nil },
		"unknown_log":      func(f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"combat":           func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"unknown_position": func(f *fakeSession) { f.actions.Position.Known = false },
		"changed_map":      func(f *fakeSession) { f.world.Self.MapId = 1 },
		"uncertain_motion": func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
	}
	for _, phase := range []string{"after_acquire", "open_window"} {
		for name, change := range changes {
			t.Run(phase+"/"+name, func(t *testing.T) {
				s, f, _ := cold170LootRig(t, 707)
				f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{3, 2, 0, 0}
				if phase == "after_acquire" {
					f.afterAcquire = func() { change(f) }
				}
				enable(t, s)
				s.Tick(context.Background())
				if phase == "after_acquire" {
					if len(f.packets) != 0 || f.leaseOwner != "" || f.releases != 1 {
						t.Fatal("stop refreshed a different or invalid bound quest", current(t, s), f.packets)
					}
					return
				}
				emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(707), 0, lootItem{slot: 2, id: 123, count: 1}))
				change(f)
				s.Tick(context.Background())
				if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("open window escaped bound quest or own-safety preemption", current(t, s), f.packets)
				}
			})
		}
	}
}

func TestColdridge170LootActiveWindowRetainsNewCounterHighWater(t *testing.T) {
	s, f, _ := cold170LootRig(t, 707)
	q := f.world.SelfQuestLog[0]
	q.ObjectiveCounts = []uint32{1, 2, 0, 0}
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(707), 0, lootItem{slot: 2, id: 123, count: 1}))
	q.ObjectiveCounts[0] = 3
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || s.coldridgeGoal.counts[0] != 3 {
		t.Fatal("active observation did not retain new objective high water", current(t, s))
	}
	q.ObjectiveCounts[0] = 2 // Still above the original baseline, below the observed3.
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLootRelease) != 1 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 || current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("newer observation regressed behind retained progress", current(t, s), f.packets)
	}
}

func TestColdridge170LootNeedsOwnItemAndSlotReceiptsThenExactRelease(t *testing.T) {
	for _, entry := range []uint32{707, 724} {
		for _, pushFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/push_first_%t", entry, pushFirst), func(t *testing.T) {
				s, f, advance := cold170LootRig(t, entry)
				guid := cold170LootGUID(entry)
				enable(t, s)
				s.Tick(context.Background())
				// These are local server-frame fixtures, not a claim about a creature's drop table.
				emit(s, opcode.SMSGLootResponse, windowPacket(guid+1, 0, lootItem{slot: 2, id: 123, count: 2}))
				if current(t, s).LootWindowsObserved != 0 {
					t.Fatal("foreign window advanced the bound corpse")
				}
				emit(s, opcode.SMSGLootResponse, windowPacket(guid, 0, lootItem{slot: 2, id: 123, count: 2}))
				s.Tick(context.Background())
				if current(t, s).ItemsStoredConfirmed != 0 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 1 {
					t.Fatal("item write fabricated inventory receipt", current(t, s))
				}
				f.world.LootableCorpses = nil // An opened, bound window outlives nearby-cache presence.
				emit(s, opcode.SMSGLootRemoved, []byte{3})
				emit(s, opcode.SMSGItemPushResult, cold170ItemPush(3, 123, 2))
				emit(s, opcode.SMSGItemPushResult, cold170ItemPush(1, 124, 2))
				emit(s, opcode.SMSGItemPushResult, cold170ItemPush(1, 123, 1))
				crafted := cold170ItemPush(1, 123, 2)
				binary.LittleEndian.PutUint32(crafted[12:], 1)
				emit(s, opcode.SMSGItemPushResult, crafted)
				s.Tick(context.Background())
				if current(t, s).ItemsStoredConfirmed != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
					t.Fatal("unrelated item or slot receipts completed loot", current(t, s))
				}
				if pushFirst {
					emit(s, opcode.SMSGItemPushResult, cold170ItemPush(1, 123, 2))
				} else {
					emit(s, opcode.SMSGLootRemoved, []byte{2})
				}
				s.Tick(context.Background())
				if cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
					t.Fatal("one receipt bypassed the other", current(t, s))
				}
				if pushFirst {
					emit(s, opcode.SMSGLootRemoved, []byte{2})
				} else {
					emit(s, opcode.SMSGItemPushResult, cold170ItemPush(1, 123, 2))
				}
				s.Tick(context.Background())
				s.Tick(context.Background())
				got := current(t, s)
				if got.ItemsStoredConfirmed != 2 || got.LootSlotsRemoved != 1 || len(got.RecentItems) != 1 || got.RecentItems[0].ItemId != 123 || got.RecentItems[0].Count != 2 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 || got.ReleasesObserved != 0 {
					t.Fatal("actual item/slot proof did not produce one pending release", got)
				}
				emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid+1), 1))
				if current(t, s).ReleasesObserved != 0 {
					t.Fatal("foreign release confirmed own corpse")
				}
				emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
				s.Tick(context.Background())
				got = current(t, s)
				if got.ReleasesObserved != 1 || got.LastCorpseGuid != guid || f.leaseOwner != "" || f.releases != 1 || s.coldridgeGoal.questID != 0 || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{0, 0, 0, 0}) || f.world.SelfQuestLog[0].Complete {
					t.Fatal("release or loot fabricated quest progress or retained transaction", got)
				}
				advance(3 * time.Second)
				s.Tick(context.Background())
				if len(f.packets) != 3 {
					t.Fatal("finished corpse transaction retried", f.packets)
				}
			})
		}
	}
}

func TestColdridge170LootCoinsOnlyWindowUsesActualMoneyReceipts(t *testing.T) {
	s, f, _ := cold170LootRig(t, 724)
	guid := cold170LootGUID(724)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(guid, 17))
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLootMoney) != 1 || len(f.packets[1].Payload) != 0 || current(t, s).MoneyAwardedNotified != 0 {
		t.Fatal("coins-only window guessed a monetary award", current(t, s), f.packets)
	}
	notify := make([]byte, 5)
	binary.LittleEndian.PutUint32(notify, 17)
	notify[4] = 1
	emit(s, opcode.SMSGLootMoneyNotify, notify)
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
		t.Fatal("money notification replaced clear-money receipt")
	}
	emit(s, opcode.SMSGLootClearMoney, nil)
	s.Tick(context.Background())
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
	s.Tick(context.Background())
	got := current(t, s)
	if got.MoneyAwardedNotified != 17 || got.ItemsStoredConfirmed != 0 || len(got.RecentItems) != 0 || got.ReleasesObserved != 1 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || len(f.packets) != 3 || f.leaseOwner != "" || !slices.Equal(f.world.SelfQuestLog[0].ObjectiveCounts, []uint32{0, 0, 0, 0}) {
		t.Fatal("coins-only loot invented items, objective progress or release", got)
	}
}

func TestColdridge170LootLeaseLossAndAmbiguousOpenNeverWriteAgain(t *testing.T) {
	for _, mode := range []string{"busy_lease", "lost_open_lease", "ambiguous_open", "missing_open_receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := cold170LootRig(t, 707)
			if mode == "busy_lease" {
				f.leaseOwner = "trade"
			}
			if mode == "ambiguous_open" {
				f.failOpcode = opcode.CMSGLoot
			}
			enable(t, s)
			s.Tick(context.Background())
			if mode == "busy_lease" {
				if len(f.packets) != 0 || f.leaseOwner != "trade" {
					t.Fatal("loot bypassed another interaction", current(t, s))
				}
				return
			}
			if mode == "lost_open_lease" {
				emit(s, opcode.SMSGLootResponse, windowPacket(cold170LootGUID(707), 0, lootItem{slot: 2, id: 123, count: 1}))
				f.leaseOwner = "trade"
				s.Tick(context.Background())
			}
			advance(3 * time.Second)
			s.Tick(context.Background())
			advance(3 * time.Second)
			s.Tick(context.Background())
			advance(time.Minute)
			for range 5 {
				s.Tick(context.Background())
			}
			wantWrites := 1
			wantReleases := 1
			if mode == "missing_open_receipt" {
				wantWrites = 2 // One open and one bounded release, neither is retried.
			}
			if mode == "lost_open_lease" {
				wantReleases = 0 // The old token cannot release a different interaction.
			}
			got := current(t, s)
			if len(f.packets) != wantWrites || cold170PacketCount(f, opcode.CMSGLoot) != 1 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || got.Enabled != (mode == "missing_open_receipt") || got.ItemsStoredConfirmed != 0 || got.ReleasesObserved != 0 || f.releases != wantReleases {
				t.Fatal("uncertain or unowned transaction retried or fabricated receipts", got, f.packets)
			}
		})
	}
}

// The callback runs only once the selected operation has a pending corpse,
// after the lease-stop recheck and before the final opening observation returns.
type cold170OpeningSession struct {
	*coldridgeLeaseSession
	service          *Service
	beforeWorld      func(*fakeSession)
	beforeStatus     func(*coldridgeLeaseSession)
	changeStatus     func(*actionsv1.State)
	finalReadSeen    bool
	statusSeen       bool
	worldAfterStatus bool
}

func (f *cold170OpeningSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	selectedStatus := r.Module == "buddy-actions" && r.Method == "status" && f.service.state.PendingCorpseGuid != 0
	if selectedStatus && !f.statusSeen {
		f.statusSeen = true
		if f.beforeStatus != nil {
			f.beforeStatus(f.coldridgeLeaseSession)
		}
	}
	if r.Module == "buddy-world" && r.Method == "snapshot" && f.service.state.PendingCorpseGuid != 0 && f.statusSeen && !f.finalReadSeen {
		f.finalReadSeen = true
		if f.beforeWorld != nil {
			f.beforeWorld(f.fakeSession)
		}
	}
	if r.Module == "buddy-world" && r.Method == "snapshot" && f.statusSeen {
		f.worldAfterStatus = true
	}
	result, err := f.coldridgeLeaseSession.InvokeModule(ctx, r, opts...)
	if err == nil && selectedStatus && f.changeStatus != nil {
		a := &actionsv1.State{}
		if err = result.Result.UnmarshalTo(a); err != nil {
			return nil, err
		}
		f.changeStatus(a)
		f.actions = proto.Clone(a).(*actionsv1.State)
		result.Result, err = anypb.New(a)
	}
	return result, err
}

func TestColdridge170LootSameOwnerReplacementRejectsOldTokenBeforeWrites(t *testing.T) {
	for _, phase := range []string{"opening", "money", "store"} {
		t.Run(phase, func(t *testing.T) {
			s, f, _ := cold170LootRig(t, 707)
			lease := s.session.(*coldridgeLeaseSession)
			enable(t, s)
			wantWrites := 0
			if phase != "opening" {
				s.Tick(context.Background())
				window := windowPacket(cold170LootGUID(707), 0, lootItem{slot: 2, id: 123, count: 1})
				if phase == "money" {
					window = windowPacket(cold170LootGUID(707), 17)
				}
				emit(s, opcode.SMSGLootResponse, window)
				wantWrites = 1
			}
			beforeRenewals := lease.renewAttempts
			gate := &cold170OpeningSession{
				coldridgeLeaseSession: lease, service: s,
				beforeStatus: func(l *coldridgeLeaseSession) { l.leaseToken = "replacement-loot-token" },
			}
			s.session = gate
			s.Tick(context.Background())
			if !gate.statusSeen || lease.renewAttempts != beforeRenewals+1 || f.leaseOwner != "loot" || lease.leaseToken != "replacement-loot-token" || f.releases != 0 {
				t.Fatal("replacement fixture did not exercise same-label exact-token rejection", current(t, s))
			}
			if len(f.packets) != wantWrites || cold170PacketCount(f, opcode.CMSGLootMoney) != 0 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || cold170PacketCount(f, opcode.CMSGLootRelease) != 0 || s.token != "" || current(t, s).ItemsStoredConfirmed != 0 {
				t.Fatal("stale token wrote or released a replacement operation", current(t, s), f.packets)
			}
		})
	}
}

func TestColdridge170LootStatusWorldRefreshCannotUseEarlierProof(t *testing.T) {
	changes := map[string]func(*fakeSession){
		"unchanged_control":         func(*fakeSession) {},
		"quest_removed179_inserted": func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 179 },
		"quest_failed":              func(f *fakeSession) { f.world.SelfQuestLog[0].Failed = true },
		"self_combat":               func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"same_map_position_correction": func(f *fakeSession) {
			f.world.Self.PositionRevision++
			f.world.Self.X += 20 // Still in-region; cached actions position remains near the corpse.
		},
	}
	for _, phase := range []string{"opening", "money", "store"} {
		for name, change := range changes {
			t.Run(phase+"/"+name, func(t *testing.T) {
				s, f, _ := cold170LootRig(t, 724)
				enable(t, s)
				if phase != "opening" {
					s.Tick(context.Background())
					window := windowPacket(cold170LootGUID(724), 0, lootItem{slot: 2, id: 123, count: 1})
					if phase == "money" {
						window = windowPacket(cold170LootGUID(724), 17)
					}
					emit(s, opcode.SMSGLootResponse, window)
				}
				positionBefore := proto.Clone(f.actions.Position).(*actionsv1.ClientPosition)
				gate := &cold170OpeningSession{
					coldridgeLeaseSession: s.session.(*coldridgeLeaseSession), service: s,
					beforeStatus: func(l *coldridgeLeaseSession) { change(l.fakeSession) },
				}
				s.session = gate
				s.Tick(context.Background())
				if !gate.statusSeen || !proto.Equal(positionBefore, f.actions.Position) {
					t.Fatal("status fixture did not retain the same valid returned position")
				}
				if name == "unchanged_control" {
					wantOpcode := uint32(opcode.CMSGLoot)
					if phase == "money" {
						wantOpcode = opcode.CMSGLootMoney
					} else if phase == "store" {
						wantOpcode = opcode.CMSGAutostoreLootItem
					}
					if !gate.worldAfterStatus || cold170PacketCount(f, wantOpcode) != 1 || cold170PacketCount(f, opcode.CMSGLootRelease) != 0 {
						t.Fatal("unchanged control did not verify last world before its write", current(t, s), f.packets)
					}
					return
				}
				if phase == "opening" {
					if len(f.packets) != 0 || f.releases != 1 {
						t.Fatal("status-time disproof still opened loot", current(t, s), f.packets)
					}
				} else if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || cold170PacketCount(f, opcode.CMSGLootMoney) != 0 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 {
					t.Fatal("active write used proof from before actions refreshed world", current(t, s), f.packets)
				}
			})
		}
	}
}

func TestColdridge170LootRenewalDoesNotExtendTransactionOrReceiptDeadline(t *testing.T) {
	s, f, advance := cold170LootRig(t, 707)
	lease := s.session.(*coldridgeLeaseSession)
	enable(t, s)
	s.Tick(context.Background())
	transaction, response := s.transactionUntil, s.deadline
	beforeRenewals := lease.renewAttempts
	for range 2 {
		advance(500 * time.Millisecond)
		s.Tick(context.Background())
		if !s.transactionUntil.Equal(transaction) || !s.deadline.Equal(response) {
			t.Fatal("token renewal extended the operation or receipt deadline")
		}
	}
	if lease.renewAttempts != beforeRenewals+2 {
		t.Fatal("fixture never renewed the exact token during the finite wait")
	}
	advance(1100 * time.Millisecond)
	s.Tick(context.Background())
	if cold170PacketCount(f, opcode.CMSGLoot) != 1 || cold170PacketCount(f, opcode.CMSGLootRelease) != 1 || !current(t, s).Enabled || current(t, s).Phase != "waiting_release" {
		t.Fatal("renewal hid a missing opening receipt", current(t, s), f.packets)
	}
	advance(3 * time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 2 || f.releases != 1 {
		t.Fatal("finite release was retried after renewal", current(t, s), f.packets)
	}
}

func TestColdridge170LootCleanupNeedsExactTokenEvenWhenStateBecameUnsafe(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		for _, mode := range []string{"quest_removed", "self_combat", "timeout", "server_abort", "disabled", "logout"} {
			t.Run(fmt.Sprintf("%s/replaced_%t", mode, replaced), func(t *testing.T) {
				s, f, advance := cold170LootRig(t, 707)
				lease := s.session.(*coldridgeLeaseSession)
				enable(t, s)
				s.Tick(context.Background())
				if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot {
					t.Fatal("cleanup fixture did not open its actual bound corpse")
				}
				if replaced {
					lease.leaseToken = "replacement-loot-token"
				}
				switch mode {
				case "quest_removed":
					f.world.SelfQuestLog[0].QuestId = 179
				case "self_combat":
					f.world.Self.UnitFlags |= 0x80000
				case "timeout":
					advance(13 * time.Second)
				case "server_abort":
					emit(s, opcode.SMSGLootResponse, append(guidPacket(cold170LootGUID(707)), 0, 4))
				case "disabled":
					disableLoot(t, s)
				case "logout":
					if _, err := s.BeforeLogout(context.Background(), nil); err != nil {
						t.Fatal(err)
					}
				}
				if mode != "logout" {
					s.Tick(context.Background())
				}
				if cold170PacketCount(f, opcode.CMSGLootMoney) != 0 || cold170PacketCount(f, opcode.CMSGAutostoreLootItem) != 0 || current(t, s).ItemsStoredConfirmed != 0 {
					t.Fatal("cleanup sent a new collection write or invented inventory", current(t, s), f.packets)
				}
				if replaced {
					if len(f.packets) != 1 || f.leaseOwner != "loot" || lease.leaseToken != "replacement-loot-token" || f.releases != 0 || s.token != "" {
						t.Fatal("unsafe old operation released another lease or server window", current(t, s), f.packets)
					}
				} else if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || binary.LittleEndian.Uint64(f.packets[1].Payload) != cold170LootGUID(707) {
					t.Fatal("valid exact owner did not release its unsafe bound window once", current(t, s), f.packets)
				}
			})
		}
	}
}

func TestColdridge170LootFinalOpeningReadRechecksBoundProof(t *testing.T) {
	tests := []struct {
		name   string
		world  func(*fakeSession)
		status func(*actionsv1.State)
	}{
		{name: "unchanged_control"},
		{name: "quest_removed179_inserted", world: func(f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 179 }},
		{name: "quest_failed", world: func(f *fakeSession) { f.world.SelfQuestLog[0].Failed = true }},
		{name: "quest_counts_regressed", world: func(f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[0] = 2 }},
		{name: "quest_unknown", world: func(f *fakeSession) { f.world.SelfQuestLogKnown = false }},
		{name: "self_combat", world: func(f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 }},
		{name: "self_map_changed", world: func(f *fakeSession) { f.world.Self.MapId = 1 }},
		{name: "self_dead", world: func(f *fakeSession) { f.world.Self.Alive = false }},
		{name: "self_outside_region", world: func(f *fakeSession) { f.world.Self.X = -8900 }},
		{name: "corpse_missing", world: func(f *fakeSession) { f.world.LootableCorpses = nil }},
		{name: "corpse_guid_changed", world: func(f *fakeSession) { f.world.LootableCorpses[0].Guid++ }},
		{name: "corpse_entry_changed", world: func(f *fakeSession) { f.world.LootableCorpses[0].Entry = 724 }},
		{name: "corpse_profile_changed", world: func(f *fakeSession) { f.world.LootableCorpses[0].MaxHealth = 56 }},
		{name: "corpse_permission_lost", world: func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 }},
		{name: "corpse_map_changed", world: func(f *fakeSession) { f.world.LootableCorpses[0].MapId = 1 }},
		{name: "corpse_beyond_open_range", world: func(f *fakeSession) { f.world.LootableCorpses[0].X = f.actions.Position.X + 4.6 }},
		{name: "lease_owner_changed", status: func(a *actionsv1.State) { a.InteractionOwner = "trade" }},
		{name: "lease_too_short", status: func(a *actionsv1.State) { a.InteractionLeaseRemainingMs = 999 }},
		{name: "still_moving", status: func(a *actionsv1.State) { a.Moving = true }},
		{name: "uncertain_motion", status: func(a *actionsv1.State) { a.MotionUnconfirmed = true }},
		{name: "position_unknown", status: func(a *actionsv1.State) { a.Position.Known = false }},
		{name: "position_wrong_map", status: func(a *actionsv1.State) { a.Position.MapId = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, f, _ := cold170LootRig(t, 707)
			f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{3, 2, 0, 0}
			gate := &cold170OpeningSession{coldridgeLeaseSession: s.session.(*coldridgeLeaseSession), service: s, beforeWorld: test.world, changeStatus: test.status}
			s.session = gate
			enable(t, s)
			s.Tick(context.Background())
			if !gate.finalReadSeen && test.status == nil {
				t.Fatal("opening never refreshed the selected transaction")
			}
			if test.name == "unchanged_control" {
				if cold170PacketCount(f, opcode.CMSGLoot) != 1 || len(f.packets) != 1 || current(t, s).PendingCorpseGuid != cold170LootGUID(707) {
					t.Fatal("fresh unchanged proof did not open", current(t, s), f.packets)
				}
				return
			}
			if len(f.packets) != 0 || f.releases != 1 || s.token != "" || current(t, s).PendingCorpseGuid != 0 {
				t.Fatal("final opening read disproved authority but loot still wrote", current(t, s), f.packets)
			}
		})
	}
}
