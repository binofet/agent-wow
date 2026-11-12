package buddyloot

import (
	"context"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func healthyLootInventory(f *fakeSession) {
	f.world.Self.HealthKnown, f.world.Self.Health, f.world.Self.MaxHealth = true, 100, 100
}

func uncertainItem(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	healthyLootInventory(f)
	openItem(t, s)
	advance(3 * time.Second)
	s.Tick(context.Background())
	if s.cleanup.source != corpseGUID || !s.itemRequested || s.cleanup.safeToContinue {
		t.Fatal("missing uncertain item", current(t, s))
	}
	return s, f, advance
}

func nextOrdinarySource(t *testing.T, s *Service, f *fakeSession, advance func(time.Duration), id uint32) uint64 {
	t.Helper()
	other := proto.Clone(f.world.LootableCorpses[0]).(*worldv1.CreatureState)
	other.Guid++
	f.world.LootableCorpses = append(f.world.LootableCorpses, other)
	advance(time.Second)
	s.Tick(context.Background())
	if current(t, s).Phase != "waiting_open" || s.pendingGUID() != other.Guid {
		t.Fatal("other source not opened", current(t, s))
	}
	emit(s, opcode.SMSGLootResponse, windowPacket(other.Guid, 0, lootItem{slot: 2, id: id, count: 3}))
	s.Tick(context.Background())
	if current(t, s).Phase != "waiting_item" {
		t.Fatal("new item not requested", current(t, s))
	}
	return other.Guid
}

func TestOrdinarySharedWindowRemovalSkipsOnlyActualRemovedSlots(t *testing.T) {
	s, f, _ := rig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 5,
		lootItem{slot: 0, id: 1195, count: 1}, lootItem{slot: 1, id: 2589, count: 2}, lootItem{slot: 2, id: 772, count: 1}, lootItem{slot: 3, id: 773, count: 1}))
	s.Tick(context.Background()) // money
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(4, 772, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{19}) // not in this window
	emit(s, opcode.SMSGLootClearMoney, nil)
	s.Tick(context.Background())
	s.Tick(context.Background()) // request0
	emit(s, opcode.SMSGLootRemoved, []byte{0})
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 1195, 1))
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootRemoved, []byte{3})
	emit(s, opcode.SMSGItemPushResult, pushPacket(4, 773, 1))
	s.Tick(context.Background()) // request1, not stale slot2/3
	emit(s, opcode.SMSGLootRemoved, []byte{1})
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 2589, 2))
	s.Tick(context.Background())
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	got := current(t, s)
	var slots []byte
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGAutostoreLootItem {
			slots = append(slots, p.Payload[0])
		}
	}
	if len(slots) != 2 || slots[0] != 0 || slots[1] != 1 || got.ItemsStoredConfirmed != 3 || got.LootSlotsRemoved != 2 || len(got.RecentItems) != 2 || got.Phase != "idle" || got.ReleasesObserved != 1 {
		t.Fatal("shared removals became own receipts/stale requests", got, slots)
	}
}

func TestOrdinaryClosedUnknownItemRetiresWithoutInventingReceipt(t *testing.T) {
	for _, mode := range []string{"none", "slot_only", "push_only", "full_bags", "explicit_disable"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			healthyLootInventory(f)
			openItem(t, s)
			if mode == "slot_only" {
				emit(s, opcode.SMSGLootRemoved, []byte{2})
			}
			if mode == "push_only" {
				emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3))
			}
			before := current(t, s)
			advance(3 * time.Second)
			s.Tick(context.Background())
			if mode == "full_bags" {
				for i, slot := range f.world.InventoryBags[0].Slots {
					slot.ItemGuid = uint64(0x4000000000000000) + uint64(i+1)
					slot.MetadataKnown = true
					slot.Entry = 999
					slot.StackCount = 1
				}
				f.world.InventoryRevision++
			}
			if mode == "explicit_disable" {
				disableLoot(t, s)
			}
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			s.Tick(context.Background())
			got := current(t, s)
			if got.Enabled == (mode == "explicit_disable") || got.PendingCorpseGuid != 0 || got.LastCorpseGuid != 0 || got.ItemsStoredConfirmed != before.ItemsStoredConfirmed || got.LootSlotsRemoved != before.LootSlotsRemoved || got.ReleasesObserved != 1 || len(f.packets) != 3 || !s.retired[corpseGUID] || !s.unconfirmedItems[123] || s.cleanup.source != 0 || f.leaseOwner != "" {
				t.Fatal("unknown item not safely retired", got)
			}
			if mode == "none" {
				nextOrdinarySource(t, s, f, advance, 456)
			}
		})
	}
}

func TestOrdinaryUnknownItemRequiresExactReleaseAndCompleteFreshInventory(t *testing.T) {
	cases := map[string]func(*Service, *fakeSession){
		"missing_release": func(s *Service, f *fakeSession) { s.cleanup.releaseObserved = false },
		"wrong_release": func(s *Service, f *fakeSession) {
			s.cleanup.releaseObserved = false
			emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(corpseGUID+1), 1))
		},
		"negative_release": func(s *Service, f *fakeSession) {
			s.cleanup.releaseObserved = false
			emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(corpseGUID), 0))
		},
		"unknown_inventory": func(s *Service, f *fakeSession) { f.world.InventoryKnown = false },
		"missing_bag":       func(s *Service, f *fakeSession) { f.world.InventoryBags = f.world.InventoryBags[:4] },
		"duplicate_bag":     func(s *Service, f *fakeSession) { f.world.InventoryBags[4].Bag = 3 },
		"unknown_contents":  func(s *Service, f *fakeSession) { f.world.InventoryBags[0].ContentsKnown = false },
		"unknown_item":      func(s *Service, f *fakeSession) { f.world.InventoryBags[0].Slots[0].ItemGuid = 0x4000000000000001 },
		"duplicate_item": func(s *Service, f *fakeSession) {
			for _, slot := range f.world.InventoryBags[0].Slots[:2] {
				slot.ItemGuid = 0x4000000000000001
				slot.MetadataKnown = true
				slot.Entry = 123
				slot.StackCount = 1
			}
		},
		"foreign_item": func(s *Service, f *fakeSession) {
			slot := f.world.InventoryBags[0].Slots[0]
			slot.ItemGuid = 3
			slot.MetadataKnown = true
			slot.Entry = 123
			slot.StackCount = 1
		},
		"regressed_revision": func(s *Service, f *fakeSession) { s.inventoryWriteRevision = f.world.InventoryRevision + 1 },
		"foreign_self":       func(s *Service, f *fakeSession) { f.world.Self.Guid = 4 },
		"dead":               func(s *Service, f *fakeSession) { f.world.Self.Alive = false },
		"unknown_health":     func(s *Service, f *fakeSession) { f.world.Self.HealthKnown = false },
		"combat":             func(s *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"nonfinite":          func(s *Service, f *fakeSession) { f.world.Self.X = float32(math.NaN()) },
		"item_bound": func(s *Service, f *fakeSession) {
			s.unconfirmedItems = map[uint32]bool{}
			for id := uint32(1000); id < 1000+maxUnconfirmedLootItems; id++ {
				s.unconfirmedItems[id] = true
			}
		},
		"source_not_retired": func(s *Service, f *fakeSession) { delete(s.retired, corpseGUID) },
		"money_uncertain":    func(s *Service, f *fakeSession) { s.moneySent = true; s.moneyCleared = false },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, advance := uncertainItem(t)
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			change(s, f)
			before := len(f.packets)
			for range 3 {
				advance(time.Second)
				s.Tick(context.Background())
			}
			if s.cleanup.source != corpseGUID || s.state.PendingCorpseGuid != corpseGUID || len(f.packets) != before || s.unconfirmedItems[123] || s.state.ItemsStoredConfirmed != 0 {
				t.Fatal("unsafe barrier retired or wrote", current(t, s))
			}
		})
	}
}

func TestOrdinaryLateUnknownPushCannotBindNewWindow(t *testing.T) {
	s, f, advance := uncertainItem(t)
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3)) // after timeout, before ACK
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3)) // after cleanup
	other := proto.Clone(f.world.LootableCorpses[0]).(*worldv1.CreatureState)
	other.Guid++
	f.world.LootableCorpses = append(f.world.LootableCorpses, other)
	advance(time.Second)
	s.Tick(context.Background())
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3)) // waiting for a different window
	emit(s, opcode.SMSGLootResponse, windowPacket(other.Guid, 0, lootItem{slot: 2, id: 123, count: 3}))
	s.Tick(context.Background())
	before := len(f.packets)
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3)) // same ID/count in new window still has no source
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	s.Tick(context.Background())
	if got := current(t, s); got.Phase != "waiting_item" || got.ItemsStoredConfirmed != 0 || len(got.RecentItems) != 0 || s.pushed || len(f.packets) != before {
		t.Fatal("old push bound new request", got)
	}
	// Current owned delta is independent evidence to advance; it creates no push receipt.
	slot := f.world.InventoryBags[0].Slots[0]
	slot.ItemGuid = 0x4000000000000001
	slot.MetadataKnown = true
	slot.Entry = 123
	slot.StackCount = 3
	s.Tick(context.Background()) // same revision cannot advance
	if s.state.Phase != "waiting_item" {
		t.Fatal("same revision accepted")
	}
	f.world.InventoryRevision++
	s.Tick(context.Background())
	if got := current(t, s); got.Phase != "ready_item" || got.ItemsStoredConfirmed != 0 || len(got.RecentItems) != 0 {
		t.Fatal("actual newer owned delta not isolated", got)
	}
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(other.Guid), 1))
	s.Tick(context.Background())
	if current(t, s).Phase != "idle" || !s.unconfirmedItems[123] || len(f.packets) != before+1 {
		t.Fatal("safe new source did not finish once", current(t, s))
	}
	enable(t, s)
	if !s.unconfirmedItems[123] || !s.retired[corpseGUID] {
		t.Fatal("enable erased unknown source history")
	}
}

func TestOrdinaryUnknownItemDeltaRequiresCurrentRemovalAndExactOwnedIncrease(t *testing.T) {
	for _, mode := range []string{"no_removal", "wrong_id", "too_few", "unknown_layout", "unchanged_revision", "foreign_self"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := uncertainItem(t)
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			s.Tick(context.Background())
			nextOrdinarySource(t, s, f, advance, 123)
			slot := f.world.InventoryBags[0].Slots[0]
			slot.ItemGuid = 0x4000000000000001
			slot.MetadataKnown = true
			slot.Entry = 123
			slot.StackCount = 3
			f.world.InventoryRevision++
			if mode != "no_removal" {
				emit(s, opcode.SMSGLootRemoved, []byte{2})
			}
			switch mode {
			case "wrong_id":
				slot.Entry = 456
			case "too_few":
				slot.StackCount = 2
			case "unknown_layout":
				f.world.InventoryKnown = false
			case "unchanged_revision":
				f.world.InventoryRevision--
			case "foreign_self":
				f.world.Self.Guid = 4
			}
			if s.unconfirmedItemDelta(f.world) {
				t.Fatal("unrelated owned change settled request", mode)
			}
			if current(t, s).ItemsStoredConfirmed != 0 || s.pushed {
				t.Fatal("invented push")
			}
		})
	}
}

func TestOrdinaryEarlyServerCloseCannotBypassUnknownMutationBarrier(t *testing.T) {
	for _, mode := range []string{"item", "unknown_bags", "money", "gather"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			healthyLootInventory(f)
			if mode == "money" {
				enable(t, s)
				s.Tick(context.Background())
				emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 5))
				s.Tick(context.Background())
			} else if mode == "gather" {
				s, f, _ = harvestRig(t)
				enable(t, s)
				s.Tick(context.Background())
				emit(s, opcode.SMSGLootResponse, harvestWindow(harvestGUID, lootItem{slot: 2, id: 11119, count: 1}))
				s.Tick(context.Background())
			} else {
				openItem(t, s)
			}
			if mode == "unknown_bags" {
				f.world.InventoryKnown = false
			}
			guid := s.pendingGUID()
			before := len(f.packets)
			emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
			if len(f.packets) != before || f.releases != 0 {
				t.Fatal("packet callback performed cleanup")
			}
			if mode == "gather" {
				if s.cleanup.source != 0 || s.state.Phase != "release_observed" {
					t.Fatal("ordinary policy altered gather")
				}
				return
			}
			if s.cleanup.source != guid || !s.cleanup.releaseObserved || s.state.ReleasesObserved != 1 {
				t.Fatal("early close bypassed barrier", current(t, s))
			}
			s.Tick(context.Background())
			if mode == "item" {
				if s.cleanup.source != 0 || s.state.PendingCorpseGuid != 0 || !s.unconfirmedItems[123] {
					t.Fatal("early close not retired", current(t, s))
				}
			} else if s.cleanup.source != guid || s.state.Phase != "loot_mutation_unconfirmed" {
				t.Fatal("unsafe early close retired", current(t, s))
			}
			if len(f.packets) != before || s.state.ItemsStoredConfirmed != 0 || s.state.LastCorpseGuid != 0 {
				t.Fatal("early close invented success/write", current(t, s))
			}
		})
	}
}

func TestOrdinaryAmbiguousStoreAbortAndDisabledReleaseKeepCorrelationBarrier(t *testing.T) {
	for _, mode := range []string{"transport_ack", "transport_timeout", "disabled_ack", "disabled_timeout", "money_transport_ack"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			healthyLootInventory(f)
			enable(t, s)
			s.Tick(context.Background())
			if mode == "money_transport_ack" {
				emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 5))
				f.failOpcode = opcode.CMSGLootMoney
			} else {
				emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 123, count: 3}))
				if mode == "transport_ack" || mode == "transport_timeout" {
					f.failOpcode = opcode.CMSGAutostoreLootItem
				}
			}
			s.Tick(context.Background())
			if mode == "disabled_ack" || mode == "disabled_timeout" {
				disableLoot(t, s)
			}
			f.failOpcode = 0
			s.Tick(context.Background()) // abort cleanup, one release
			if s.state.Phase != "waiting_release" || s.state.Enabled {
				t.Fatal("fixture not disabled/releasing", current(t, s))
			}
			before := len(f.packets)
			if mode == "transport_timeout" || mode == "disabled_timeout" {
				advance(3 * time.Second)
				s.Tick(context.Background())
				s.Tick(context.Background())
				if s.cleanup.source != corpseGUID || s.cleanup.releaseObserved {
					t.Fatal("unacknowledged close escaped", current(t, s))
				}
			}
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			s.Tick(context.Background())
			if mode == "money_transport_ack" {
				if s.cleanup.source != corpseGUID || s.state.Phase != "loot_mutation_unconfirmed" {
					t.Fatal("uncertain money escaped", current(t, s))
				}
				return
			}
			if s.cleanup.source != 0 || !s.unconfirmedItems[123] || s.state.Enabled || s.state.ItemsStoredConfirmed != 0 || s.state.LastCorpseGuid != 0 || len(f.packets) != before {
				t.Fatal("ambiguous abort escaped or rewrote", current(t, s))
			}
			enable(t, s)
			nextOrdinarySource(t, s, f, advance, 123)
			emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3))
			emit(s, opcode.SMSGLootRemoved, []byte{2})
			s.Tick(context.Background())
			if s.pushed || s.state.ItemsStoredConfirmed != 0 || s.state.Phase != "waiting_item" {
				t.Fatal("abort push crossed source", current(t, s))
			}
		})
	}
}

func TestOrdinaryUnsettledNegativeReleaseNeverAuthorizesAnotherSource(t *testing.T) {
	s, f, advance := rig(t)
	healthyLootInventory(f)
	openItem(t, s)
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(corpseGUID), 0))
	s.Tick(context.Background())
	enable(t, s)
	before := len(f.packets)
	for range 3 {
		advance(time.Second)
		s.Tick(context.Background())
	}
	if s.cleanup.source != corpseGUID || !s.cleanup.releaseRejected || s.cleanup.releaseObserved || s.state.ReleasesObserved != 0 || s.state.PendingCorpseGuid != corpseGUID || len(f.packets) != before || s.unconfirmedItems[123] {
		t.Fatal("negative close authorized continuation", current(t, s))
	}
}

func TestOrdinaryGenericFinishCannotDiscardUnsettledStore(t *testing.T) {
	for _, mode := range []string{"release_write_failure", "lease_replaced"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			healthyLootInventory(f)
			openItem(t, s)
			if mode == "release_write_failure" {
				f.world.Self.UnitFlags |= 0x80000
				f.failOpcode = opcode.CMSGLootRelease
			} else {
				f.leaseOwner = "trade"
				f.leaseToken = "replacement-token"
			}
			s.Tick(context.Background())
			if s.cleanup.source != corpseGUID || !s.itemRequested || s.state.LastCorpseGuid != 0 || s.state.PendingCorpseGuid != corpseGUID || s.cleanup.releaseObserved || s.state.ItemsStoredConfirmed != 0 {
				t.Fatal("generic finish discarded ambiguous mutation", current(t, s), s.cleanup)
			}
			before := len(f.packets)
			enable(t, s)
			emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3))
			advance(3 * time.Second)
			s.Tick(context.Background())
			if s.cleanup.source != corpseGUID || len(f.packets) != before || s.state.ItemsStoredConfirmed != 0 {
				t.Fatal("enable/retry escaped unknown cleanup", current(t, s))
			}
			if mode == "lease_replaced" {
				if f.leaseOwner != "trade" || f.leaseToken != "replacement-token" || s.cleanup.releaseRequested || f.releases != 0 {
					t.Fatal("replacement lease touched")
				}
				emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
				s.Tick(context.Background())
				if s.cleanup.releaseObserved || s.cleanup.source == 0 {
					t.Fatal("unrequested close accepted")
				}
				return
			}
			// The failed release write was ambiguous, so its real exact ACK can still settle closure.
			f.world.Self.UnitFlags &= ^uint32(0x80000)
			f.failOpcode = 0
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			s.Tick(context.Background())
			if s.cleanup.source != 0 || !s.unconfirmedItems[123] || s.state.LastCorpseGuid != 0 || len(f.packets) != before {
				t.Fatal("actual late close not isolated", current(t, s))
			}
			nextOrdinarySource(t, s, f, advance, 123)
			emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3))
			emit(s, opcode.SMSGLootRemoved, []byte{2})
			s.Tick(context.Background())
			if s.pushed || s.state.ItemsStoredConfirmed != 0 || s.state.Phase != "waiting_item" {
				t.Fatal("failed-release late push rebound", current(t, s))
			}
		})
	}
}
