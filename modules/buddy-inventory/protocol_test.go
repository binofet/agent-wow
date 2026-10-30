package buddyinventory

import (
	"context"
	"encoding/binary"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"testing"
)

func metadataFixture(entry, class, quality, inv, containers uint32) []byte {
	p := binary.LittleEndian.AppendUint32(nil, entry)
	p = binary.LittleEndian.AppendUint32(p, class)
	p = append(p, make([]byte, 8)...)
	p = append(p, []byte("Fixture\x00\x00\x00\x00")...)
	prefix := make([]byte, 88)
	binary.LittleEndian.PutUint32(prefix[4:], quality)
	binary.LittleEndian.PutUint32(prefix[20:], 5)
	binary.LittleEndian.PutUint32(prefix[24:], inv)
	binary.LittleEndian.PutUint32(prefix[76:], 20)
	binary.LittleEndian.PutUint32(prefix[80:], containers)
	p = append(p, prefix...)
	p = append(p, make([]byte, 8+24+28+12+5*24+4)...)
	p = append(p, 0)
	return append(p, make([]byte, 29*4)...)
}
func TestInventoryMetadataCompleteFrameAndJunkExclusions(t *testing.T) {
	p := metadataFixture(4865, 15, 0, 0, 0)
	m, e := parseItemMetadata(p)
	if e != nil || !m.disposable() || m.sellPrice != 5 {
		t.Fatal(m, e)
	}
	for n := range len(p) {
		partial, e := parseItemMetadata(p[:n])
		if e == nil && partial.disposable() {
			t.Fatalf("truncation authorized at%d", n)
		}
	}
	if _, e := parseItemMetadata(append(p, 0)); e == nil {
		t.Fatal("trailing bytes")
	}
	for _, change := range []func(*itemMetadata){func(m *itemMetadata) { m.flags = 1 }, func(m *itemMetadata) { m.flags2 = 1 }, func(m *itemMetadata) { m.quality = 1 }, func(m *itemMetadata) { m.class = 7 }, func(m *itemMetadata) { m.subclass = 1 }, func(m *itemMetadata) { m.inventoryType = 1 }, func(m *itemMetadata) { m.containerSlots = 1 }, func(m *itemMetadata) { m.bonding = 4 }, func(m *itemMetadata) { m.startQuest = 1 }, func(m *itemMetadata) { m.hasSpell = true }, func(m *itemMetadata) { m.pageText = 1 }, func(m *itemMetadata) { m.lockID = 1 }, func(m *itemMetadata) { m.bagFamily = 1 }, func(m *itemMetadata) { m.duration = 1 }, func(m *itemMetadata) { m.maxDurability = 1 }} {
		q := m
		change(&q)
		if q.disposable() {
			t.Fatal("functional item accepted", q)
		}
	}
	bag, e := parseItemMetadata(metadataFixture(41599, 1, 1, 18, 20))
	if e != nil || !normalBag(bag) || bag.disposable() {
		t.Fatal(bag, e)
	}
	bag.containerSlots = 19
	if normalBag(bag) {
		t.Fatal("wrong capacity")
	}
}
func TestInventoryWireLogicalSlotsAndStrictFailureLayouts(t *testing.T) {
	for _, tc := range []struct {
		b, s   uint32
		wb, ws byte
	}{{0, 0, 255, 23}, {0, 15, 255, 38}, {1, 0, 19, 0}, {4, 35, 22, 35}} {
		b, s, ok := wireSlot(tc.b, tc.s)
		if !ok || b != tc.wb || s != tc.ws {
			t.Fatal(tc, b, s)
		}
	}
	if movePacket(0, 0, 5, 0) != nil || movePacket(0, 0, 0, 0) != nil || equipPacket(bagGUID, 18) != nil || sellPacket(vendorGUID, junkGUID, 0) != nil {
		t.Fatal("invalid packet emitted")
	}
	if p := movePacket(0, 0, 1, 19); len(p) != 4 || p[0] != 19 || p[1] != 19 || p[2] != 255 || p[3] != 23 {
		t.Fatal(p)
	}
	p := binary.LittleEndian.AppendUint64(nil, vendorGUID)
	p = binary.LittleEndian.AppendUint64(p, junkGUID)
	p = append(p, 1)
	if _, _, _, e := sellFailure(p); e != nil {
		t.Fatal(e)
	}
	for n := range len(p) {
		if _, _, _, e := sellFailure(p[:n]); e == nil {
			t.Fatal("truncated sale error")
		}
	}
	if _, _, _, e := sellFailure(append(p, 0)); e == nil {
		t.Fatal("trailing sale error")
	}
	for _, p := range [][]byte{{}, {0, 0}, {1}} {
		if _, e := parseInventoryChangeFailure(p); e == nil {
			t.Fatal("malformed inventory failure accepted")
		}
	}
}
func TestInventoryQueriesOnlyOwnedIDsAndDoesNotCacheUnsolicitedMetadata(t *testing.T) {
	s, f := fixtureInventory()
	s.metadata = map[uint32]itemMetadata{}
	s.queried = map[uint32]bool{}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemQuerySingleResponse, Payload: metadataFixture(9999, 15, 0, 0, 0)})
	if len(s.metadata) != 0 {
		t.Fatal("unsolicited metadata cached")
	}
	stateInventory(t, s)
	if len(f.packets) != 2 {
		t.Fatal(f.packets)
	}
	for _, p := range f.packets {
		if p.Opcode != opcode.CMSGItemQuerySingle || len(p.Payload) != 4 {
			t.Fatal(p)
		}
		id := binary.LittleEndian.Uint32(p.Payload)
		if id != 4865 && id != 41599 {
			t.Fatal(id)
		}
	}
	stateInventory(t, s)
	if len(f.packets) != 2 {
		t.Fatal("query replay")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemQuerySingleResponse, Payload: metadataFixture(4865, 15, 0, 0, 0)})
	st := stateInventory(t, s)
	if !st.Items[0].SafeJunkSaleEligible {
		t.Fatal(st)
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemQuerySingleResponse, Payload: metadataFixture(4865, 15, 0, 0, 0)[:50]})
	if s.snapshot().MalformedPackets != 1 {
		t.Fatal("malformed metadata uncounted")
	}
}
func TestInventoryVendorContradictoryViewsAndHistoryLimit(t *testing.T) {
	s, f := fixtureInventory()
	bad := proto.Clone(f.w.NearbyCreatures[0]).(*worldv1.CreatureState)
	bad.Alive = false
	f.w.QuestGivers = []*worldv1.CreatureState{bad}
	r := sellRequest(t, s)
	st, e := s.Sell(context.Background(), r)
	if e != nil || st.Operation.Status != "BLOCKED" || len(f.packets) != 0 {
		t.Fatal(st, e)
	}
	s, f = fixtureInventory()
	r = sellRequest(t, s)
	for n := 0; n < 256; n++ {
		s.requests[string(rune(n+128))] = &operation{receipt: &inventoryv1.Operation{Status: "BLOCKED", LeaseReleased: true}}
	}
	if _, e := s.Sell(context.Background(), r); e == nil || len(f.packets) != 0 {
		t.Fatal("history bound failed")
	}
}
func FuzzInventoryMetadataAndErrors(f *testing.F) {
	f.Add(metadataFixture(4865, 15, 0, 0, 0))
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, p []byte) {
		if m, e := parseItemMetadata(p); e == nil && m.disposable() && (!m.known || m.class != 15 || m.quality != 0 || m.entry == 0) {
			t.Fatal(m)
		}
		_, _ = parseInventoryChangeFailure(p)
		_, _, _, _ = sellFailure(p)
	})
}
