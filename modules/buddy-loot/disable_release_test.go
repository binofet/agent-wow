package buddyloot

import (
	"context"
	"testing"
	"time"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func storedLootWaitingRelease(t *testing.T, gather bool) (*Service, *fakeSession, func(time.Duration), uint64, uint32) {
	t.Helper()
	s, f, advance := rig(t)
	guid, item := corpseGUID, uint32(750)
	if gather {
		s, f, advance = harvestRig(t)
		guid, item = harvestGUID, 11119
	}
	enable(t, s)
	s.Tick(context.Background())
	window := windowPacket(guid, 0, lootItem{slot: 2, id: item, count: 1})
	if gather {
		window = harvestWindow(guid, lootItem{slot: 2, id: item, count: 1})
	}
	emit(s, opcode.SMSGLootResponse, window)
	s.Tick(context.Background()) // store exactly one server-offered item
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, item, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	s.Tick(context.Background()) // consume the two item receipts
	s.Tick(context.Background()) // send the single loot release
	if got := current(t, s); got.Phase != "waiting_release" || got.ItemsStoredConfirmed != 1 || got.LootSlotsRemoved != 1 || len(got.RecentItems) != 1 || len(f.packets) != 3 {
		t.Fatal("fixture did not reach actual stored item + pending release", got, f.packets)
	}
	return s, f, advance, guid, item
}

func disableLoot(t *testing.T, s *Service) *lootv1.State {
	t.Helper()
	got, err := s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDisableAfterReleaseReceiptFinishesOnceAndPreservesStoredItemProof(t *testing.T) {
	for _, gather := range []bool{false, true} {
		s, f, advance, guid, item := storedLootWaitingRelease(t, gather)
		if gather {
			f.world.SelfQuestLog[0].Complete = true
			f.world.SelfQuestLog[0].State = 1
			f.world.SelfQuestLogRevision++
		}
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
		if current(t, s).Phase != "release_observed" {
			t.Fatal(current(t, s))
		}
		got := disableLoot(t, s)
		if got.Enabled || got.Phase != "disabled" || s.token != "" || got.PendingCorpseGuid != 0 || got.PendingGameObjectGuid != 0 || got.ReleasesObserved != 1 || f.releases != 1 || len(f.packets) != 3 {
			t.Fatal("disable resent/forgot confirmed release", got, f.packets, f.releases)
		}
		if got.ItemsStoredConfirmed != 1 || got.LootSlotsRemoved != 1 || len(got.RecentItems) != 1 || got.RecentItems[0].ItemId != item || got.RecentItems[0].Count != 1 {
			t.Fatal("stored inventory proof lost", got)
		}
		if gather && (got.LastGather.Status != lootv1.GatherStatus_GATHER_CONFIRMED || !got.LastGather.ItemPushObserved || !got.LastGather.SlotRemoved || !got.LastGather.ReleaseObserved || !got.LastGather.OwnQuestCompleteObserved) {
			t.Fatal("gather receipts not finalized", got.LastGather)
		}
		if !gather && got.LastCorpseGuid != guid {
			t.Fatal(got)
		}
		advance(3 * time.Second)
		s.Tick(context.Background())
		disableLoot(t, s)
		if got := current(t, s); len(f.packets) != 3 || f.releases != 1 || got.Reason != "disabled_by_request" || got.ReleasesObserved != 1 {
			t.Fatal("finished disable invented another write/timeout", got, f.packets)
		}
	}
}

func TestDisableDuringOutstandingReleasePreservesDeadlineAndReceiptBinding(t *testing.T) {
	for _, gather := range []bool{false, true} {
		s, f, advance, guid, _ := storedLootWaitingRelease(t, gather)
		deadline, token := s.deadline, s.token
		got := disableLoot(t, s)
		if got.Enabled || got.Phase != "waiting_release" || s.token != token || !s.deadline.Equal(deadline) || len(f.packets) != 3 || f.releases != 0 {
			t.Fatal("disable overwrote in-flight release", got)
		}
		advance(time.Second)
		disableLoot(t, s)
		s.Tick(context.Background())
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid+1), 1))
		if got := current(t, s); got.Phase != "waiting_release" || got.ReleasesObserved != 0 || !s.deadline.Equal(deadline) || len(f.packets) != 3 {
			t.Fatal(got)
		}
		emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
		s.Tick(context.Background())
		got = current(t, s)
		if got.Enabled || got.Phase != "disabled" || got.ReleasesObserved != 1 || got.ItemsStoredConfirmed != 1 || len(f.packets) != 3 || f.releases != 1 {
			t.Fatal(got, f.packets)
		}
		if gather && got.LastGather.Status != lootv1.GatherStatus_GATHER_CONFIRMED {
			t.Fatal(got.LastGather)
		}
	}
}

func TestDisabledOutstandingReleaseStillTimesOutWithoutResend(t *testing.T) {
	s, f, advance, _, _ := storedLootWaitingRelease(t, false)
	deadline := s.deadline
	disableLoot(t, s)
	advance(1500 * time.Millisecond)
	disableLoot(t, s)
	if !s.deadline.Equal(deadline) {
		t.Fatal("disable extended original receipt deadline")
	}
	advance(600 * time.Millisecond)
	s.Tick(context.Background())
	got := current(t, s)
	if got.Enabled || got.Phase != "disabled" || got.Reason != "loot_response_timeout_no_retry" || got.ReleasesObserved != 0 || got.ItemsStoredConfirmed != 1 || got.LootSlotsRemoved != 1 || len(f.packets) != 3 || f.releases != 1 {
		t.Fatal("timeout retried or invented release proof", got, f.packets)
	}
}

func TestDisableBeforeReleaseKeepsExistingAbortAndFailureGuards(t *testing.T) {
	s, f, _ := rig(t)
	enable(t, s)
	s.Tick(context.Background())
	got := disableLoot(t, s)
	if got.Phase != "abort" || len(f.packets) != 1 {
		t.Fatal(got, f.packets)
	}
	s.Tick(context.Background())
	if got := current(t, s); got.Phase != "waiting_release" || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease {
		t.Fatal(got, f.packets)
	}
	// A negative server receipt remains unconfirmed, even if disabled exactly
	// between its callback and the transaction's normal finalization.
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(corpseGUID), 0))
	got = disableLoot(t, s)
	if got.Enabled || got.Phase != "disabled" || got.ReleasesObserved != 0 || got.ItemsStoredConfirmed != 0 || len(f.packets) != 2 || f.releases != 1 {
		t.Fatal(got, f.packets)
	}
}
