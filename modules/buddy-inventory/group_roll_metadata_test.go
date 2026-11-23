package buddyinventory

import (
	"context"
	"encoding/binary"
	"math"
	"strconv"
	"testing"
	"time"

	buddytrade "github.com/agent-wow/agent-wow/modules/buddy-trade"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Synthetic complete normal wire frames, not saved realm item payloads.
func rollMetadataWire(entry uint32) []byte {
	p := weaponMetadataFrame(entry, 10, 17, 8, 16, 2800)
	at := 16 + len("Fixture") + 4
	for index, value := range map[int]uint32{1: 2, 4: 12345, 5: 2345, 9: 42, 10: 7, 11: 136, 12: 1, 13: 227, 14: 2, 15: 3, 16: 72, 17: 4, 21: 2} {
		binary.LittleEndian.PutUint32(p[at+index*4:], value)
	}
	stats := binary.LittleEndian.AppendUint32(nil, 5)
	stats = binary.LittleEndian.AppendUint32(stats, math.MaxUint32-2) // signed -3
	stats = binary.LittleEndian.AppendUint32(stats, 7)
	stats = binary.LittleEndian.AppendUint32(stats, 9)
	out := append([]byte(nil), p[:at+88]...)
	out = append(out, stats...)
	out = append(out, p[at+88:]...)
	spell := at + 88 + len(stats) + 8 + 24 + 28 + 12
	binary.LittleEndian.PutUint32(out[spell:], 868)
	binary.LittleEndian.PutUint32(out[spell+4:], 2) // observed on-hit spell, not a Go choice
	return out
}

func rollMetadataQueries(f *fakeInventory) []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []uint32
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGItemQuerySingle && len(packet.Payload) == 4 {
			ids = append(ids, binary.LittleEndian.Uint32(packet.Payload))
		}
	}
	return ids
}

func rollMetadataRow(t *testing.T, rows []*tradev1.ItemDetails, id uint32) *tradev1.ItemDetails {
	t.Helper()
	for _, row := range rows {
		if row.GetItemId() == id {
			return row
		}
	}
	t.Fatal("offered template row absent", id, rows)
	return nil
}

func TestRollMetadataOwnQueryAndFullReplyReflectExactSharedParser(t *testing.T) {
	const entry = uint32(99001)
	s, f, _ := newRollTest(t)
	wire := rollMetadataWire(entry)
	reserved := false
	f.afterSend = func(packet *modv1.SendPacketRequest) {
		if packet.Opcode != opcode.CMSGItemQuerySingle {
			return
		}
		if len(packet.Payload) != 4 || binary.LittleEndian.Uint32(packet.Payload) != entry {
			t.Fatal("unexpected own template query", packet)
		}
		s.mu.Lock()
		reserved = s.queried[entry]
		s.mu.Unlock()
		rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, wire)
	}
	rollTestPacket(t, s, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, entry, 0, 0, 60000, 15))
	state := readRollTest(t, s)
	got := rollMetadataRow(t, state.Metadata, entry)
	want, err := buddytrade.DecodeItemTemplateQuery(wire)
	if err != nil || !reserved || !proto.Equal(got, want) || !got.Available || !got.HasSpell ||
		!got.ArmorComparisonKnown || !got.WeaponComparisonKnown || got.ItemLevel != 42 || got.BuyPrice != 12345 || got.SellPrice != 2345 ||
		got.AllowableClass != math.MaxUint32 || got.RequiredSkill != 136 || got.RequiredSpell != 227 ||
		len(got.Stats) != 2 || got.Stats[0].Value != -3 || got.RawTemplateFacts == nil || got.RawTemplateFacts.MaxStackCount == nil || *got.RawTemplateFacts.MaxStackCount != 1 {
		t.Fatal("full owned query response was not reflected truthfully", got, want, err)
	}
	if ids := rollMetadataQueries(f); len(ids) != 1 || ids[0] != entry || len(rollTestWrites(f)) != 0 {
		t.Fatal("query sent a vote or was duplicated", ids, rollTestWrites(f))
	}
	for _, call := range f.calls {
		if call == "buddy-trade.lookup_items" {
			t.Fatal("roll facts used the pinned Trade peer")
		}
	}
	got.Stats[0].Value = 999
	*got.RawTemplateFacts.MaxStackCount = 999
	again := rollMetadataRow(t, readRollTest(t, s).Metadata, entry)
	if !proto.Equal(again, want) || len(rollMetadataQueries(f)) != 1 {
		t.Fatal("returned facts aliased the cache or caused a replay", again)
	}
	// A queried but unowned roll template must not become carried gear facts.
	owned := stateInventory(t, s)
	if owned.GearContext.Templates[strconv.FormatUint(uint64(entry), 10)] != nil {
		t.Fatal("unowned offered template leaked into carried gear context")
	}
	for _, row := range owned.Items {
		if row.Item.GetEntry() == entry {
			t.Fatal("unowned offered template became an owned item")
		}
	}
}

func TestRollMetadataUnknownFramesNeverBecomeComparisonCoverage(t *testing.T) {
	const entry = uint32(99002)
	s, f, _ := newRollTest(t)
	wire := rollMetadataWire(entry)
	rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, wire) // unsolicited
	rollTestPacket(t, s, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, entry, 0, 0, 60000, 15))
	row := rollMetadataRow(t, readRollTest(t, s).Metadata, entry)
	if row.Available || row.ArmorComparisonKnown || row.WeaponComparisonKnown || row.RawTemplateFacts != nil || !row.Pending {
		t.Fatal("unsolicited response became observed roll facts", row)
	}
	for n := 0; n < len(wire); n++ {
		rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, wire[:n])
	}
	rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, append(append([]byte(nil), wire...), 0))
	rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, binary.LittleEndian.AppendUint32(nil, entry|0x80000000))
	row = rollMetadataRow(t, readRollTest(t, s).Metadata, entry)
	if row.Available || row.ArmorComparisonKnown || row.WeaponComparisonKnown || row.RawTemplateFacts != nil || len(rollMetadataQueries(f)) != 1 {
		t.Fatal("partial/malformed/negative response invented data or repeated a query", row)
	}
	// Real nonempty damage alone cannot establish the hidden-property coverage.
	at := 16 + len("Fixture") + 4
	ammo := at + 88 + 16 + 8 + 24 + 28 + 4
	binary.LittleEndian.PutUint32(wire[ammo:], 1)
	rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, wire)
	row = rollMetadataRow(t, readRollTest(t, s).Metadata, entry)
	if !row.Available || row.ArmorComparisonKnown || row.WeaponComparisonKnown || row.Damages[0].Maximum != 16 || !row.HasSpell {
		t.Fatal("coverage was inferred from visible damage instead of the full frame", row)
	}
}

func TestRollMetadataPreservesEveryRawStackWordAndPresence(t *testing.T) {
	for _, word := range []uint32{0, 1, 20, 200, math.MaxInt32, 0x80000000, math.MaxUint32} {
		t.Run(strconv.FormatUint(uint64(word), 10), func(t *testing.T) {
			const entry = uint32(99003)
			s, f, _ := newRollTest(t)
			wire := rollMetadataWire(entry)
			at := 16 + len("Fixture") + 4
			binary.LittleEndian.PutUint32(wire[at+19*4:], word)
			f.afterSend = func(packet *modv1.SendPacketRequest) {
				if packet.Opcode == opcode.CMSGItemQuerySingle {
					rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, wire)
				}
			}
			rollTestPacket(t, s, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, entry, 0, 0, 60000, 15))
			row := rollMetadataRow(t, readRollTest(t, s).Metadata, entry)
			if !row.Available || row.RawTemplateFacts == nil || row.RawTemplateFacts.MaxStackCount == nil || *row.RawTemplateFacts.MaxStackCount != word || row.WeaponComparisonKnown != (word == 1) {
				t.Fatal("raw unsigned Stackable word or known-zero presence changed", row)
			}
		})
	}
}

func TestRollMetadataSharesExistingEightPerSecondAndBodyPool(t *testing.T) {
	s, f, advance := newRollTest(t)
	for index := uint32(0); index < 16; index++ {
		rollTestPacket(t, s, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID+uint64(index), 99100+index, 0, 0, 60000, 15))
	}
	readRollTest(t, s)
	if len(rollMetadataQueries(f)) != 8 || len(s.queried) > 256 {
		t.Fatal("first shared query batch exceeded bounds", rollMetadataQueries(f))
	}
	readRollTest(t, s)
	if len(rollMetadataQueries(f)) != 8 {
		t.Fatal("same clock bypassed shared queryAfter")
	}
	advance(time.Second)
	readRollTest(t, s)
	if len(rollMetadataQueries(f)) != 16 {
		t.Fatal("next bounded batch was not queried", rollMetadataQueries(f))
	}
	advance(time.Second)
	readRollTest(t, s)
	if len(rollMetadataQueries(f)) != 16 {
		t.Fatal("unconfirmed query was blindly replayed")
	}
	bounded, peer, _ := newRollTest(t)
	for id := uint32(1000); len(bounded.queried) < 256; id++ {
		bounded.queried[id] = true
	}
	rollTestPacket(t, bounded, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, 99999, 0, 0, 60000, 15))
	row := rollMetadataRow(t, readRollTest(t, bounded).Metadata, 99999)
	if row.Available || row.Pending || row.Reason != "item_query_body_capacity_reached" || len(rollMetadataQueries(peer)) != 0 || len(bounded.queried) != 256 {
		t.Fatal("roll query bypassed or evicted the existing body pool", row)
	}
}

func TestRollMetadataAmbiguousQueryHasNoRetryButLateFullFactsCanArrive(t *testing.T) {
	const entry = uint32(99004)
	s, f, advance := newRollTest(t)
	f.sendError = true
	rollTestPacket(t, s, opcode.SMSGLootStartRoll, rollTestOffer(rollTestGUID, entry, 0, 0, 60000, 15))
	row := rollMetadataRow(t, readRollTest(t, s).Metadata, entry)
	if row.Available || !row.Pending || len(rollMetadataQueries(f)) != 1 {
		t.Fatal(row)
	}
	f.sendError = false
	advance(2 * time.Second)
	readRollTest(t, s)
	if len(rollMetadataQueries(f)) != 1 {
		t.Fatal("ambiguous query resent")
	}
	rollTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, rollMetadataWire(entry))
	if row := rollMetadataRow(t, readRollTest(t, s).Metadata, entry); !row.Available || len(rollMetadataQueries(f)) != 1 || len(rollTestWrites(f)) != 0 {
		t.Fatal("late actual data was lost or mistaken for a vote", row)
	}
}

func TestRollMetadataClosedOrCanceledReadsDoNotQuery(t *testing.T) {
	for _, closed := range []bool{false, true} {
		s, f, _ := newRollTest(t)
		ctx, cancel := context.WithCancel(context.Background())
		if closed {
			if _, err := s.BeforeLogout(ctx, &emptypb.Empty{}); err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
		rows := s.rollItemMetadata(ctx, []uint32{99005})
		cancel()
		if len(rows) != 1 || rows[0].Available || len(rollMetadataQueries(f)) != 0 || s.queried[99005] {
			t.Fatal("closed/canceled read reserved or sent an item query", rows)
		}
	}
}
