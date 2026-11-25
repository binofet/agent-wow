package buddyloot

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// These former native-controller sources now use exactly the same accepted
// Lua policy and source-bound RPCs as every other ordinary corpse. The retired
// quest/class/region preferences are not prerequisites for a public loot packet.
var retiredNamedLootSources = []struct {
	quest, entry uint32
}{
	{33, 299}, {179, 705}, {170, 707}, {170, 724},
	{183, 708}, {182, 706}, {47, 40}, {60, 475},
}

func namedLuaLootRig(t *testing.T, quest, entry uint32) (*Service, *fakeSession, uint64) {
	t.Helper()
	s, f, _ := wolfRig(t)
	guid := uint64(0xf130000000000000) | uint64(entry)<<24 | 1
	f.world.Self.ClassKnown, f.world.Self.ClassId = true, 9
	f.world.Self.LevelKnown, f.world.Self.Level = true, 5
	f.world.SelfQuestLog[0].QuestId = quest
	f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{0, 0, 0, 0}
	f.world.LootableCorpses[0].Guid, f.world.LootableCorpses[0].Entry = guid, entry
	return s, f, guid
}

func TestRetiredNamedSelectorsNeedLuaAndNativeTicksNeverChooseWindowWrites(t *testing.T) {
	for _, tc := range retiredNamedLootSources {
		t.Run(fmt.Sprintf("quest%d_entry%d", tc.quest, tc.entry), func(t *testing.T) {
			s, f, guid := namedLuaLootRig(t, tc.quest, tc.entry)
			before := proto.Clone(f.world).(*worldv1.SnapshotResponse)
			for _, enabled := range []bool{true, false, true} {
				if _, err := s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: enabled}); err != nil {
					t.Fatal(err)
				}
				s.Tick(context.Background())
			}
			if len(f.packets) != 0 || f.leaseOwner != "" || f.approachCalls != 0 {
				t.Fatal("native constructor/enable/tick selected a named source", f.packets, f.calls)
			}
			tickCorpseLua(t, s)
			if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || binary.LittleEndian.Uint64(f.packets[0].Payload) != guid {
				t.Fatal("accepted policy did not open its observed source", current(t, s), f.packets)
			}
			emit(s, opcode.SMSGLootResponse, windowPacket(guid, 17, lootItem{slot: 2, id: 50432, count: 1}))
			for range 3 {
				s.Tick(context.Background())
			}
			if len(f.packets) != 1 || current(t, s).ItemsStoredConfirmed != 0 {
				t.Fatal("native timer chose money/slot from the observed window", current(t, s), f.packets)
			}
			tickCorpseLua(t, s)
			if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootMoney || len(f.packets[1].Payload) != 0 {
				t.Fatal("Lua money selection changed the normal packet", f.packets)
			}
			emit(s, opcode.SMSGLootClearMoney, nil)
			money := make([]byte, 5)
			binary.LittleEndian.PutUint32(money, 17)
			money[4] = 1
			emit(s, opcode.SMSGLootMoneyNotify, money)
			s.Tick(context.Background())
			if len(f.packets) != 2 {
				t.Fatal("money receipt triggered a native slot writer", f.packets)
			}
			tickCorpseLua(t, s)
			if len(f.packets) != 3 || f.packets[2].Opcode != opcode.CMSGAutostoreLootItem || len(f.packets[2].Payload) != 1 || f.packets[2].Payload[0] != 2 {
				t.Fatal("Lua slot selection changed the exact slot", f.packets)
			}
			emit(s, opcode.SMSGLootRemoved, []byte{2})
			emit(s, opcode.SMSGItemPushResult, pushPacket(3, 50432, 1))
			s.Tick(context.Background())
			if current(t, s).ItemsStoredConfirmed != 0 || len(f.packets) != 3 {
				t.Fatal("slot removal/foreign push invented own item effect", current(t, s), f.packets)
			}
			emit(s, opcode.SMSGItemPushResult, pushPacket(1, 50432, 1))
			s.Tick(context.Background())
			if current(t, s).ItemsStoredConfirmed != 1 || len(f.packets) != 3 {
				t.Fatal("native settlement chose release or lost matching proof", current(t, s), f.packets)
			}
			tickCorpseLua(t, s)
			if len(f.packets) != 4 || f.packets[3].Opcode != opcode.CMSGLootRelease || binary.LittleEndian.Uint64(f.packets[3].Payload) != guid {
				t.Fatal("Lua did not release its exact observed window", f.packets)
			}
			emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(guid), 1))
			s.Tick(context.Background())
			got := current(t, s)
			if got.ReleasesObserved != 1 || got.LastCorpseGuid != guid || f.leaseOwner != "" || got.MoneyAwardedNotified != 17 {
				t.Fatal("matching server receipts were not retained", got)
			}
			for i, row := range f.world.SelfQuestLog {
				if !proto.Equal(row, before.SelfQuestLog[i]) {
					t.Fatal("loot transport/item receipts invented quest credit", row)
				}
			}
			enable(t, s)
			tickCorpseLua(t, s)
			if len(f.packets) != 4 {
				t.Fatal("enable resurrected an already-consumed named source", f.packets)
			}
		})
	}
}

func TestNamedSourceUnknownOpeningSurvivesEnableAndFreshRequestID(t *testing.T) {
	for _, tc := range retiredNamedLootSources {
		t.Run(fmt.Sprintf("quest%d_entry%d", tc.quest, tc.entry), func(t *testing.T) {
			s, f, guid := namedLuaLootRig(t, tc.quest, tc.entry)
			f.failOpcode = opcode.CMSGLoot
			tickCorpseLua(t, s)
			op := s.corpseReceiptLocked()
			if op == nil || !op.OpeningTransportAttempted || op.OpeningTransportWritten || !op.Terminal || len(f.packets) != 1 {
				t.Fatal("unknown named-source OPEN lost its receipt", op, f.packets)
			}
			enable(t, s)
			s.Tick(context.Background())
			tickCorpseLua(t, s)
			r := proto.Clone(s.corpseRequest.request).(*lootv1.OpenCorpseRequest)
			r.RequestId = "new-call-same-unknown-source"
			if _, err := s.OpenCorpse(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 || !s.retired[guid] {
				t.Fatal("retired controller/enable bypassed unknown custody", err, f.packets)
			}
		})
	}
}

func TestLuaWindowRejectsSameOwnerReplacementTokenForMoneySlotAndCleanup(t *testing.T) {
	for _, mode := range []string{"money", "slot", "unsafe_cleanup"} {
		t.Run(mode, func(t *testing.T) {
			s, f, guid := namedLuaLootRig(t, 183, 708)
			if got := tickCorpseLua(t, s); got != "loot_open" {
				t.Fatal("fixture did not execute the actual Lua opening", got)
			}
			gold := uint32(0)
			if mode == "money" {
				gold = 17
			}
			emit(s, opcode.SMSGLootResponse, windowPacket(guid, gold, lootItem{slot: 2, id: 123, count: 1}))
			f.leaseToken = "replacement-loot-token" // Same owner label, a different operation.
			want := "loot_slot"
			if mode == "money" {
				want = "loot_money"
			} else if mode == "unsafe_cleanup" {
				f.world.Self.UnitFlags |= 0x80000
				want = "loot_close"
			}
			if got := tickCorpseLua(t, s); got != want {
				t.Fatal("test did not reach the intended actual Lua window action", got, want)
			}
			if len(f.packets) != 1 || f.leaseOwner != "loot" || f.leaseToken != "replacement-loot-token" || f.releases != 0 ||
				current(t, s).ItemsStoredConfirmed != 0 || current(t, s).MoneyAwardedNotified != 0 || current(t, s).ReleasesObserved != 0 {
				t.Fatal("stale window token wrote or cleared the replacement operation", current(t, s), f.packets, f.calls)
			}
			if mode != "unsafe_cleanup" {
				f.world.Self.UnitFlags |= 0x80000
				if got := tickCorpseLua(t, s); got != "loot_close" {
					t.Fatal("actual Lua did not request cleanup after defense preemption", got)
				}
			}
			for range 3 {
				s.Tick(context.Background())
			}
			if s.token != "" || len(f.packets) != 1 || f.leaseOwner != "loot" || f.leaseToken != "replacement-loot-token" || f.releases != 0 ||
				s.cleanup.source != guid || s.corpseReceiptLocked().ReleaseObserved {
				t.Fatal("cleanup consumed foreign custody or invented a release", current(t, s), f.packets, f.calls)
			}
		})
	}
}
