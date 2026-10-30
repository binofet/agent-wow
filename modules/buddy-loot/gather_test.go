package buddyloot

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"
	"time"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

const harvestGUID uint64 = 0xf110000000000000 | uint64(161557)<<24 | 42

func harvestRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := wolfRig(t)
	f.world.SelfQuestLog[0].QuestId = 3904
	f.world.SelfQuestLogRevision = 7
	f.world.LootableCorpses = nil
	f.world.QuestObjects = []*worldv1.GameObjectState{{Guid: harvestGUID, Entry: 161557, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: 2, TypeKnown: true, GameObjectType: 3, FlagsKnown: true, DynamicFlagsKnown: true, DynamicFlags: 9}}
	return s, f, advance
}
func harvestWindow(guid uint64, items ...lootItem) []byte {
	p := windowPacket(guid, 0, items...)
	p[8] = 6
	return p
}
func openHarvest(t *testing.T, s *Service) {
	t.Helper()
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, harvestWindow(harvestGUID, lootItem{slot: 2, id: 11119, count: 1, slotType: 4}))
	s.Tick(context.Background())
}
func TestHarvestOpeningGoldenAndLootType(t *testing.T) {
	if got := hex.EncodeToString(gatherOpenPacket(7, harvestGUID)); got != "074e1900000000080000f92a15770210f1" {
		t.Fatal(got)
	}
	p := harvestWindow(harvestGUID, lootItem{slot: 2, id: 11119, count: 1, slotType: 4})
	w, e := decodeGatherWindow(p)
	if e != nil || w.guid != harvestGUID || len(w.items) != 1 || w.items[0].id != 11119 {
		t.Fatal(w, e)
	}
	for i := 0; i < len(p); i++ {
		if _, e := decodeGatherWindow(p[:i]); e == nil {
			t.Fatal("accepted prefix", i)
		}
	}
	if _, e := decodeWindow(p); e == nil {
		t.Fatal("corpse decoder accepted chest opening")
	}
	p[8] = 1
	if _, e := decodeGatherWindow(p); e == nil {
		t.Fatal("gather decoder accepted corpse loot type")
	}
}
func TestHarvestNeedsEnabledOwnIncompleteQuestAndObservedCrate(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Service, *fakeSession)
	}{
		{"disabled", func(s *Service, _ *fakeSession) { s.state.Enabled = false }},
		{"no_quest", func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 0 }},
		{"other_quest", func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 3903 }},
		{"complete", func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[0].Complete = true }},
		{"complete_bit", func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[0].State = 1 }},
		{"failed", func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[0].Failed = true }},
		{"unknown_log", func(_ *Service, f *fakeSession) { f.world.SelfQuestLogKnown = false }},
		{"partial_log", func(_ *Service, f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] }},
		{"duplicate_slot", func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[1].Slot = 0 }},
		{"foreign_self", func(_ *Service, f *fakeSession) { f.world.Self.Guid = 3 }},
		{"combat", func(_ *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 }},
		{"dead", func(_ *Service, f *fakeSession) { f.world.Self.Alive = false }},
		{"no_health", func(_ *Service, f *fakeSession) { f.world.Self.HealthKnown = false }},
		{"swim", func(_ *Service, f *fakeSession) { f.world.Self.MovementFlags = 0x200000 }},
		{"unsafe_motion", func(_ *Service, f *fakeSession) { f.actions.MotionUnconfirmed = true }},
		{"unknown_position", func(_ *Service, f *fakeSession) { f.actions.Position.Known = false }},
		{"casting", func(_ *Service, f *fakeSession) { f.actions.CastPending = true }},
		{"far", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].X = 5 }},
		{"height", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].Z = 2 }},
		{"nan", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].X = float32(math.NaN()) }},
		{"absent", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].Present = false }},
		{"wrong_entry", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].Entry = 161558 }},
		{"wrong_guid", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].Guid = corpseGUID }},
		{"wrong_type", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].GameObjectType = 19 }},
		{"unknown_flags", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].FlagsKnown = false }},
		{"busy", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].Flags = 1 }},
		{"not_selectable", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].Flags = 16 }},
		{"other_map", func(_ *Service, f *fakeSession) { f.world.QuestObjects[0].MapId = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			enable(t, s)
			tc.change(s, f)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" || current(t, s).LastGather != nil {
				t.Fatal("unsafe gather", current(t, s), f.packets)
			}
		})
	}
}
func TestHarvestRevalidatesAfterStopAndBeforeItemStore(t *testing.T) {
	for _, mode := range []string{"quest_complete", "object_changed", "body_changed", "moving"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			f.afterAcquire = func() {
				switch mode {
				case "quest_complete":
					f.world.SelfQuestLog[0].Complete = true
				case "object_changed":
					f.world.QuestObjects[0].PositionRevision++
				case "body_changed":
					f.world.Self.Guid = 4
				case "moving":
					f.actions.Moving = true
				}
			}
			// fake lease snapshot is captured before callback; use current world to test the first three changes.
			if mode == "moving" {
				f.actions.Moving = true
				f.afterAcquire = nil
			}
			enable(t, s)
			s.Tick(context.Background())
			if len(f.packets) != 0 || f.leaseOwner != "" || f.releases != 1 {
				t.Fatal(current(t, s), f.packets, f.releases)
			}
		})
	}
	s, f, _ := harvestRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, harvestWindow(harvestGUID, lootItem{slot: 2, id: 11119, count: 1}))
	f.world.QuestObjects = nil
	s.Tick(context.Background())
	if f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease || current(t, s).LastGather.ItemPushObserved {
		t.Fatal("stored vanished crate item", current(t, s))
	}
}
func TestHarvestRequiresOwnInventorySlotAndReleaseProof(t *testing.T) {
	s, f, advance := harvestRig(t)
	openHarvest(t, s)
	out := current(t, s)
	if out.PendingCorpseGuid != 0 || out.PendingGameObjectGuid != harvestGUID || out.LastGather.Status != lootv1.GatherStatus_GATHER_REQUESTED || out.ItemsStoredConfirmed != 0 {
		t.Fatal(out)
	}
	if len(f.packets) != 2 || f.packets[0].Opcode != opcode.CMSGCastSpell || f.packets[1].Opcode != opcode.CMSGAutostoreLootItem {
		t.Fatal(f.packets)
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(3, 11119, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	s.Tick(context.Background())
	if current(t, s).LastGather.Status != lootv1.GatherStatus_GATHER_REQUESTED {
		t.Fatal("foreign inventory receipt accepted")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 11119, 1))
	s.Tick(context.Background())
	if current(t, s).LastGather.Status != lootv1.GatherStatus_GATHER_ITEM_STORED || current(t, s).LastGather.OwnQuestCompleteObserved {
		t.Fatal(current(t, s))
	}
	// Only a normal own quest-log update establishes completion; never infer8 from this one item.
	f.world.SelfQuestLog[0].Complete = true
	f.world.SelfQuestLogRevision++
	s.Tick(context.Background())
	if f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease {
		t.Fatal(f.packets)
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
	s.Tick(context.Background())
	out = current(t, s)
	r := out.LastGather
	if r.Status != lootv1.GatherStatus_GATHER_CONFIRMED || r.Count != 1 || !r.ItemPushObserved || !r.SlotRemoved || !r.ReleaseObserved || !r.OwnQuestCompleteObserved || r.ObservedQuestLogRevision != 8 || out.PendingGameObjectGuid != 0 || out.LastCorpseGuid != 0 || f.releases != 1 || f.leaseOwner != "" {
		t.Fatal(out)
	}
	f.world.SelfQuestLog[0].Complete = false
	advance(time.Second)
	before := len(f.packets)
	s.Tick(context.Background())
	if len(f.packets) != before {
		t.Fatal("same observed crate replayed")
	}
}
func TestHarvestOnlyStoresRequiredServerOfferedItemAndNeverMoney(t *testing.T) {
	s, f, _ := harvestRig(t)
	enable(t, s)
	s.Tick(context.Background())
	p := harvestWindow(harvestGUID, lootItem{slot: 1, id: 123, count: 1}, lootItem{slot: 2, id: 11119, count: 1})
	binary.LittleEndian.PutUint32(p[9:], 500)
	emit(s, opcode.SMSGLootResponse, p)
	s.Tick(context.Background())
	if f.packets[len(f.packets)-1].Opcode != opcode.CMSGAutostoreLootItem || f.packets[len(f.packets)-1].Payload[0] != 2 {
		t.Fatal(f.packets)
	}
	for _, items := range [][]lootItem{{{slot: 1, id: 123, count: 1}}, {{slot: 1, id: 11119, count: 9}}, {{slot: 1, id: 11119, count: 1}, {slot: 2, id: 11119, count: 1}}, {{slot: 1, id: 11119, count: 1, slotType: 2}}} {
		s, f, _ := harvestRig(t)
		enable(t, s)
		s.Tick(context.Background())
		emit(s, opcode.SMSGLootResponse, harvestWindow(harvestGUID, items...))
		s.Tick(context.Background())
		if f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease {
			t.Fatal("unsupported window used", current(t, s), f.packets)
		}
	}
}
func TestHarvestTimeoutFailureAndDisableNeverRetryOrLeakLease(t *testing.T) {
	for _, mode := range []string{"timeout", "cast_reject", "write_failure", "disable", "logout"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := harvestRig(t)
			if mode == "write_failure" {
				f.failOpcode = opcode.CMSGCastSpell
			}
			enable(t, s)
			s.Tick(context.Background())
			switch mode {
			case "timeout":
				advance(4 * time.Second)
				s.Tick(context.Background())
			case "cast_reject":
				p := []byte{s.gatherCastCount}
				p = binary.LittleEndian.AppendUint32(p, 6478)
				p = append(p, 49)
				emit(s, opcode.SMSGCastFailed, p)
				s.Tick(context.Background())
			case "disable":
				s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false})
				s.Tick(context.Background())
			case "logout":
				s.BeforeLogout(context.Background(), &emptypb.Empty{})
			}
			if f.leaseOwner != "" {
				emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
				s.Tick(context.Background())
			}
			r := current(t, s).LastGather
			if r.Status == lootv1.GatherStatus_GATHER_CONFIRMED || r.ItemPushObserved || f.leaseOwner != "" {
				t.Fatal(current(t, s))
			}
			before := len(f.packets)
			advance(3 * time.Second)
			f.world.QuestObjects = nil
			s.Tick(context.Background())
			f.world.QuestObjects = []*worldv1.GameObjectState{{Guid: harvestGUID, Entry: 161557, Present: true, MapKnown: true, PositionKnown: true, X: 2, TypeKnown: true, GameObjectType: 3, FlagsKnown: true}}
			advance(time.Second)
			s.Tick(context.Background())
			if len(f.packets) != before {
				t.Fatal("ambiguous crate replayed", mode)
			}
		})
	}
}

func TestHarvestMalformedForeignAndInventoryFailureCannotClaimSuccess(t *testing.T) {
	s, f, _ := harvestRig(t)
	enable(t, s)
	s.Tick(context.Background())
	good := harvestWindow(harvestGUID, lootItem{slot: 2, id: 11119, count: 1})
	for i := 0; i < len(good); i++ {
		emit(s, opcode.SMSGLootResponse, good[:i])
	}
	if current(t, s).LastGather.LootWindowObserved || current(t, s).Phase != "waiting_open" {
		t.Fatal("partial packet changed gather", current(t, s))
	}
	emit(s, opcode.SMSGLootResponse, harvestWindow(harvestGUID+1, lootItem{slot: 2, id: 11119, count: 1}))
	if current(t, s).LastGather.LootWindowObserved {
		t.Fatal("foreign object supplied loot")
	}
	emit(s, opcode.SMSGLootResponse, good)
	s.Tick(context.Background())
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 11119, 2))
	emit(s, opcode.SMSGLootRemoved, []byte{3})
	s.Tick(context.Background())
	if current(t, s).LastGather.ItemPushObserved || current(t, s).LastGather.SlotRemoved {
		t.Fatal("wrong item count or slot accepted")
	}
	failure := make([]byte, 18)
	failure[0] = 4
	emit(s, opcode.SMSGInventoryChangeFailure, failure)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
	s.Tick(context.Background())
	if r := current(t, s); r.Enabled || r.LastGather.Status != lootv1.GatherStatus_GATHER_SERVER_REJECTED || r.LastGather.ItemPushObserved || f.leaseOwner != "" {
		t.Fatal(r)
	}
}

func TestInventoryFullPausesOnlyUntilKnownFreeSlotAndPreservesExplicitDisable(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		s, f, advance := harvestRig(t)
		openHarvest(t, s)
		f.world.BackpackKnown = true
		for i := range 16 {
			f.world.Backpack = append(f.world.Backpack, &worldv1.BackpackSlot{Slot: uint32(i), ItemGuid: uint64(i + 1)})
		}
		p := make([]byte, 18)
		p[0] = 50
		emit(s, opcode.SMSGInventoryChangeFailure, p)
		s.Tick(context.Background())
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
		s.Tick(context.Background())
		if out := current(t, s); !out.Enabled || !out.InventoryPaused {
			t.Fatal(out)
		}
		before := len(f.packets)
		advance(time.Second)
		s.Tick(context.Background())
		if len(f.packets) != before {
			t.Fatal("retried while full")
		}
		f.world.Backpack = f.world.Backpack[:15]
		advance(time.Second)
		s.Tick(context.Background())
		if len(f.packets) != before {
			t.Fatal("partial backpack resumed")
		}
		f.world.Backpack = append(f.world.Backpack, &worldv1.BackpackSlot{Slot: 15})
		if disabled {
			s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false})
		}
		advance(time.Second)
		s.Tick(context.Background())
		out := current(t, s)
		if disabled {
			if out.Enabled || len(f.packets) != before {
				t.Fatal("explicit disable overridden", out)
			}
		} else if out.InventoryPaused || len(f.packets) != before+1 || f.packets[len(f.packets)-1].Opcode != opcode.CMSGCastSpell {
			t.Fatal("known free slot did not resume", out, f.packets)
		}
	}
}
