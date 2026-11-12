package buddyloot

import (
	"context"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func openingTimedOut(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	enable(t, s)
	s.Tick(context.Background())
	advance(3 * time.Second)
	s.Tick(context.Background())
	if got := current(t, s); !got.Enabled || got.Phase != "waiting_release" || s.cleanup.source != corpseGUID || !s.cleanup.safeToContinue || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease {
		t.Fatal("opening timeout did not reserve one source-bound cleanup", got, f.packets)
	}
	return s, f, advance
}

func TestOrdinaryOpeningTimeoutRetainsIntentAndResumesOnlyAfterExactCleanup(t *testing.T) {
	s, f, advance := openingTimedOut(t)
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(corpseGUID+1), 1))
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 750, count: 1}))
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 750, 1))
	s.Tick(context.Background())
	if got := current(t, s); got.Phase != "waiting_release" || got.ReleasesObserved != 0 || got.LootWindowsObserved != 0 || got.ItemsStoredConfirmed != 0 || len(f.packets) != 2 {
		t.Fatal("late/foreign cleanup became a loot operation", got, f.packets)
	}
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	if got := current(t, s); !got.Enabled || got.Phase != "idle" || got.PendingCorpseGuid != 0 || got.LastCorpseGuid != 0 || got.ReleasesObserved != 1 || got.ItemsStoredConfirmed != 0 || len(f.packets) != 2 || f.releases != 1 {
		t.Fatal("cleanup claimed items or disabled intent", got, f.packets)
	}
	// Visibility churn and another enable request must not replay this corpse.
	f.world.LootableCorpses = nil
	advance(time.Second)
	s.Tick(context.Background())
	c := &worldv1.CreatureState{Guid: corpseGUID, Present: true, MapKnown: true, PositionKnown: true, X: 2, HealthKnown: true, MaxHealth: 100, AliveKnown: true, DynamicFlags: 1}
	f.world.LootableCorpses = []*worldv1.CreatureState{c}
	enable(t, s)
	advance(time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 2 {
		t.Fatal("retired corpse replayed after projection churn/enable", f.packets)
	}
	other := proto.Clone(c).(*worldv1.CreatureState)
	other.Guid++
	other.X = 3
	f.world.LootableCorpses = append(f.world.LootableCorpses, other)
	advance(time.Second)
	s.Tick(context.Background())
	if got := current(t, s); got.PendingCorpseGuid != other.Guid || got.Phase != "waiting_open" || len(f.packets) != 3 {
		t.Fatal("confirmed cleanup prevented different source", got, f.packets)
	}
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 750, count: 1}))
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 750, 1))
	s.Tick(context.Background())
	if got := current(t, s); got.Phase != "waiting_open" || got.LootWindowsObserved != 0 || got.ItemsStoredConfirmed != 0 || got.ReleasesObserved != 1 || len(f.packets) != 3 {
		t.Fatal("retired source receipts crossed into new source", got)
	}
}

func TestOrdinaryCleanupWithoutPositiveReceiptNeverResendsOrOpens(t *testing.T) {
	for _, mode := range []string{"missing", "negative", "transport", "replacement_lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			enable(t, s)
			s.Tick(context.Background())
			if mode == "transport" {
				f.failOpcode = opcode.CMSGLootRelease
			}
			if mode == "replacement_lease" {
				f.afterRenew = func() { f.leaseOwner = "trade"; f.leaseToken = "replacement" }
			}
			advance(3 * time.Second)
			s.Tick(context.Background())
			if mode == "negative" {
				emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(corpseGUID), 0))
			}
			advance(3 * time.Second)
			s.Tick(context.Background())
			before := len(f.packets)
			enable(t, s)
			for range 12 {
				advance(time.Second)
				s.Tick(context.Background())
			}
			if got := current(t, s); !got.Enabled || got.Phase != "loot_cleanup_unconfirmed" || got.PendingCorpseGuid != corpseGUID || got.ReleasesObserved != 0 || got.ItemsStoredConfirmed != 0 || len(f.packets) != before {
				t.Fatal("unconfirmed cleanup repeated or discarded intent", got, f.packets)
			}
			if mode == "replacement_lease" {
				if len(f.packets) != 1 || f.leaseOwner != "trade" || f.leaseToken != "replacement" || f.releases != 0 {
					t.Fatal("cleanup used/cleared replacement lease", f.packets)
				}
				// There was no cleanup write; an unsolicited receipt cannot authorize it.
				emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
				s.Tick(context.Background())
				if s.cleanup.source == 0 || current(t, s).ReleasesObserved != 0 {
					t.Fatal("unrequested cleanup accepted")
				}
			}
			if _, err := s.BeforeLogout(context.Background(), &emptypb.Empty{}); err != nil {
				t.Fatal(err)
			}
			if len(f.packets) != before {
				t.Fatal("logout resent an already reserved cleanup", f.packets)
			}
		})
	}
}

func TestOrdinaryItemOrMoneyTimeoutStaysQuarantinedAfterCleanup(t *testing.T) {
	for _, mode := range []string{"item_none", "item_push_only", "item_slot_only", "money_notify_only"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			enable(t, s)
			s.Tick(context.Background())
			if mode == "money_notify_only" {
				emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 5))
				s.Tick(context.Background())
				emit(s, opcode.SMSGLootMoneyNotify, []byte{5, 0, 0, 0, 1})
			} else {
				emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 750, count: 1}))
				s.Tick(context.Background())
				if mode == "item_push_only" {
					emit(s, opcode.SMSGItemPushResult, pushPacket(1, 750, 1))
				}
				if mode == "item_slot_only" {
					emit(s, opcode.SMSGLootRemoved, []byte{2})
				}
			}
			actualPush := current(t, s).ItemsStoredConfirmed
			// An unknown owned layout cannot drain an uncertain item request.
			// Money remains blocked independently of inventory completeness.
			f.world.InventoryKnown = false
			advance(3 * time.Second)
			s.Tick(context.Background())
			if s.cleanup.safeToContinue {
				t.Fatal("unsettled GUID-less mutation treated as safe", mode)
			}
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			s.Tick(context.Background())
			before := len(f.packets)
			// Late receipts and another enable do not rearm an ambiguous item/money.
			emit(s, opcode.SMSGLootRemoved, []byte{2})
			emit(s, opcode.SMSGItemPushResult, pushPacket(1, 750, 1))
			emit(s, opcode.SMSGLootClearMoney, nil)
			enable(t, s)
			for range 5 {
				advance(time.Second)
				s.Tick(context.Background())
			}
			if got := current(t, s); !got.Enabled || got.Phase != "loot_mutation_unconfirmed" || got.ItemsStoredConfirmed != actualPush || got.ReleasesObserved != 1 || got.PendingCorpseGuid != corpseGUID || len(f.packets) != before || f.releases != 1 {
				t.Fatal("ambiguous mutation retried or existing proof lost", got, f.packets)
			}
		})
	}
}

func TestOrdinarySettledMutationAndLateReleaseKeepActualProofAndDisableIntent(t *testing.T) {
	for _, disable := range []bool{false, true} {
		s, f, advance, guid, item := storedLootWaitingRelease(t, false)
		// The release request was already sent. Timeout must not send another one.
		advance(3 * time.Second)
		s.Tick(context.Background())
		if got := current(t, s); !got.Enabled || got.Phase != "loot_cleanup_unconfirmed" || len(f.packets) != 3 || !s.cleanup.safeToContinue {
			t.Fatal(got, f.packets)
		}
		if disable {
			disableLoot(t, s)
		}
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
		s.Tick(context.Background())
		got := current(t, s)
		if got.Enabled == disable || got.ItemsStoredConfirmed != 1 || got.LootSlotsRemoved != 1 || len(got.RecentItems) != 1 || got.RecentItems[0].ItemId != item || got.ReleasesObserved != 1 || got.PendingCorpseGuid != 0 || len(f.packets) != 3 || f.releases != 1 {
			t.Fatal("settled item/release proof or explicit intent lost", got, f.packets)
		}
	}
	// Both receipts arrived before timeout, even if the next Tick has not yet
	// consumed them. This is positive proof, not optimism about the store write.
	s, f, advance := rig(t)
	openItem(t, s)
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3))
	advance(3 * time.Second)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	if !current(t, s).Enabled || s.cleanup.source != 0 || len(f.packets) != 3 {
		t.Fatal(current(t, s), f.packets)
	}
	// A matching clear-money event, not the notification or write, settles money.
	s, f, advance = rig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 5))
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootMoneyNotify, []byte{5, 0, 0, 0, 1})
	emit(s, opcode.SMSGLootClearMoney, nil)
	advance(3 * time.Second)
	s.Tick(context.Background())
	if !s.cleanup.safeToContinue {
		t.Fatal("actual money-clear receipt ignored")
	}
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	if got := current(t, s); !got.Enabled || got.Phase != "idle" || got.MoneyAwardedNotified != 5 || got.ItemsStoredConfirmed != 0 || len(f.packets) != 3 {
		t.Fatal("settled money cleanup lost proof", got)
	}
}

func TestOrdinaryRetirementNeedsActualLivingProofAndFreshLaterCorpse(t *testing.T) {
	for _, mode := range []string{"absent", "dead", "unknown_alive", "unknown_health", "wrong_guid", "foreign_self", "wrong_map", "new_life"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := openingTimedOut(t)
			emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
			s.Tick(context.Background())
			alive := proto.Clone(f.world.LootableCorpses[0]).(*worldv1.CreatureState)
			alive.Alive, alive.AliveKnown, alive.Health, alive.HealthKnown, alive.MaxHealth = true, true, 100, true, 100
			f.world.RequestedCreature = alive
			switch mode {
			case "absent":
				alive.Present = false
			case "dead":
				alive.Alive = false
				alive.Health = 0
			case "unknown_alive":
				alive.AliveKnown = false
			case "unknown_health":
				alive.HealthKnown = false
			case "wrong_guid":
				alive.Guid++
			case "foreign_self":
				f.world.Self.Guid = 3
			case "wrong_map":
				alive.MapId = 1
			}
			before := len(f.packets)
			advance(time.Second)
			s.Tick(context.Background())
			if (s.retired[corpseGUID] == false) != (mode == "new_life") || len(f.packets) != before {
				t.Fatal("unproven life or stale prior corpse reopened", mode, current(t, s), f.packets)
			}
			if mode == "new_life" {
				// A fresh later dead observation after that actual living proof is a new
				// corpse lifetime. The old ambiguous OPEN is never replayed blindly.
				f.world.RequestedCreature.Alive = false
				f.world.RequestedCreature.Health = 0
				advance(time.Second)
				s.Tick(context.Background())
				if got := current(t, s); got.PendingCorpseGuid != corpseGUID || got.Phase != "waiting_open" || len(f.packets) != before+1 {
					t.Fatal("new actual life/death could not loot", got)
				}
			}
		})
	}
}

func TestOrdinaryRetiredSourceCapacityIsBoundedAndDoesNotEraseOldSafety(t *testing.T) {
	s, f, _ := rig(t)
	enable(t, s)
	s.retired = make(map[uint64]bool)
	for n := uint64(0); n < maxRetiredLootSources; n++ {
		s.retired[corpseGUID+n] = true
	}
	s.Tick(context.Background())
	if len(s.retired) != maxRetiredLootSources || len(f.packets) != 0 || f.snapshotReads != 2 || current(t, s).Reason != "loot_retired_source_bound_waiting_for_actual_new_life" {
		t.Fatal("retirement bound dropped records or queried unboundedly", current(t, s), f.snapshotReads)
	}
	// Retirement and an unresolved outcome are body-local, not persisted or
	// cleared by blindly toggling the current body.
	fresh := NewService(f, 1, "new-runtime-body")
	if len(fresh.retired) != 0 || fresh.cleanup.source != 0 {
		t.Fatal("foreign body inherited old transaction")
	}
}

func TestOrdinaryDisableDuringTimeoutCleanupNeverResendsOrRestoresIntent(t *testing.T) {
	s, f, advance := openingTimedOut(t)
	disableLoot(t, s)
	advance(time.Second)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	got := current(t, s)
	if got.Enabled || got.Phase != "disabled" || got.ReleasesObserved != 1 || got.ItemsStoredConfirmed != 0 || len(f.packets) != 2 || f.releases != 1 {
		t.Fatal("cleanup restored explicit disabled intent", got, f.packets)
	}
	// The new timeout policy does not loosen the existing error-on-write gate.
	s, f, advance = rig(t)
	enable(t, s)
	s.Tick(context.Background())
	f.failOpcode = opcode.CMSGLootRelease
	advance(3 * time.Second)
	s.Tick(context.Background())
	if current(t, s).Enabled {
		t.Fatal("transport ambiguity silently kept active execution")
	}
}
