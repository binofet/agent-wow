package buddytrade

import (
	"context"
	"encoding/binary"
	"math"
	"sync"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Independent source-order fixture for ItemHandler's normal item template.
// No fixture value represents a live item or an equipment permission.
func lookupTemplate(id uint32) []byte {
	b := []byte{}
	word := func(v uint32) { b = append(b, u32(v)...) }
	for _, v := range []uint32{id, 2, 10, 0} {
		word(v)
	}
	b = append(b, []byte("Fixture Staff\x00\x00\x00\x00")...)
	for _, v := range []uint32{987, 2, 16, 32, 12345, 2345, 17, 0xffffffff, 0x7ff, 12, 7, 136, 1, 227, 3, 4, 72, 4, 0, 1, 0, 2} {
		word(v)
	}
	word(5)
	word(7)
	word(7)
	word(0xfffffffd) // Intellect+7, Stamina-3.
	word(0)
	word(0) // Scaling record.
	for _, v := range []uint32{math.Float32bits(3.5), math.Float32bits(6.25), 0, math.Float32bits(1), math.Float32bits(2), 2} {
		word(v)
	}
	for _, v := range []uint32{12, 1, 2, 3, 4, 5, 6, 2800, 0, 0} {
		word(v)
	}
	for i := range 5 {
		for j := range 6 {
			v := uint32(0)
			if i == 0 && j == 0 {
				v = 133
			}
			word(v)
		}
	}
	word(1)
	b = append(b, []byte("Fixture description\x00")...)
	for range 29 {
		word(0)
	}
	return b
}

func TestItemLookupDecodesCompleteServerStatsAndRestrictions(t *testing.T) {
	d, err := parseLookupItem(lookupTemplate(900001))
	if err != nil || !d.Available || d.Pending || d.ItemId != 900001 || d.Name != "Fixture Staff" || d.ItemClass != 2 || d.Subclass != 10 || d.InventoryType != 17 || d.Quality != 2 || d.ItemLevel != 12 || d.RequiredLevel != 7 || d.BuyPrice != 12345 || d.SellPrice != 2345 || d.AllowableClass != 0xffffffff || d.AllowableRace != 0x7ff || d.RequiredSkill != 136 || d.RequiredSkillRank != 1 || d.RequiredSpell != 227 || d.RequiredReputationFaction != 72 || d.RequiredReputationRank != 4 || d.Armor != 12 || d.DelayMs != 2800 || d.Flags != 16 || d.Flags2 != 32 || !d.HasSpell {
		t.Fatal(d, err)
	}
	if len(d.Stats) != 2 || d.Stats[0].Type != 5 || d.Stats[0].Value != 7 || d.Stats[1].Type != 7 || d.Stats[1].Value != -3 || len(d.Damages) != 2 || d.Damages[0].Minimum != 3.5 || d.Damages[0].Maximum != 6.25 || d.Damages[1].School != 2 {
		t.Fatal(d)
	}
	if d.RequiredHonorRank != 3 || d.RequiredCityRank != 4 {
		t.Fatal("lost rank restrictions", d)
	}
	for i := 0; i < len(lookupTemplate(900001)); i++ {
		if _, err := parseLookupItem(lookupTemplate(900001)[:i]); err == nil {
			t.Fatal("accepted truncated template", i)
		}
	}
	for _, mode := range []string{"tail", "name_markup", "nan_damage", "negative_damage", "reversed_damage", "school"} {
		b := lookupTemplate(900001)
		damageAt := 16 + len("Fixture Staff") + 4 + 22*4 + 16 + 8
		switch mode {
		case "tail":
			b = append(b, 0)
		case "name_markup":
			b[16] = '|'
		case "nan_damage":
			binary.LittleEndian.PutUint32(b[damageAt:], math.Float32bits(float32(math.NaN())))
		case "negative_damage":
			binary.LittleEndian.PutUint32(b[damageAt:], math.Float32bits(-1))
		case "reversed_damage":
			binary.LittleEndian.PutUint32(b[damageAt:], math.Float32bits(8))
		case "school":
			binary.LittleEndian.PutUint32(b[damageAt+8:], 7)
		}
		if _, err := parseLookupItem(b); err == nil {
			t.Fatal("accepted malformed template", mode)
		}
	}
}

func TestItemLookupQueriesNormalWireThenReturnsOnlyObservedFacts(t *testing.T) {
	s, f := fixture()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	r := &tradev1.LookupItemsRequest{ItemIds: []uint32{1111, 2222, 3333, 4444, 5555, 6666}}
	got, err := s.LookupItems(context.Background(), r)
	if err != nil || got.SessionId != "session-A" || len(got.Items) != 6 || f.count(opcode.CMSGItemQuerySingle) != 6 {
		t.Fatal(got, err)
	}
	for i, item := range got.Items {
		if item.Available || !item.Pending || item.Name != "" || len(item.Stats) != 0 || binary.LittleEndian.Uint32(f.packets[i].Payload) != r.ItemIds[i] || len(f.packets[i].Payload) != 4 {
			t.Fatal(item, f.packets[i])
		}
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, lookupTemplate(9999)) // Unsolicited.
	if s.lookup.items[9999] != nil {
		t.Fatal("cached unsolicited facts")
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, lookupTemplate(1111))
	packet(s, opcode.SMSGItemQuerySingleResponse, u32(2222|0x80000000))
	packet(s, opcode.SMSGItemQuerySingleResponse, lookupTemplate(3333)[:100])
	got, err = s.LookupItems(context.Background(), r)
	if err != nil || !got.Items[0].Available || got.Items[0].Name != "Fixture Staff" || got.Items[1].Available || got.Items[1].Pending || got.Items[1].Reason != "server_item_unavailable" || got.Items[2].Available || !got.Items[2].Pending || f.count(opcode.CMSGItemQuerySingle) != 6 {
		t.Fatal(got, err)
	}
	got.Items[0].Name = "changed"
	got.Items[0].Stats[0].Value = 999
	again, _ := s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{1111}})
	if again.Items[0].Name != "Fixture Staff" || again.Items[0].Stats[0].Value != 7 || s.names[1111] != "Fixture Staff" || !s.metadata[1111].known {
		t.Fatal("cache alias or existing metadata broken", again)
	}
	limited, _ := s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{7777}})
	if !limited.Items[0].Pending || limited.Items[0].Reason != "item_query_rate_limited" || f.count(opcode.CMSGItemQuerySingle) != 6 {
		t.Fatal(limited)
	}
	now = now.Add(time.Second)
	s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{7777}})
	if f.count(opcode.CMSGItemQuerySingle) != 7 {
		t.Fatal("rate limit never recovered")
	}
	now = now.Add(6 * time.Second)
	expired, _ := s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{3333}})
	if expired.Items[0].Available || expired.Items[0].Pending || expired.Items[0].Reason != "server_item_query_unconfirmed" || f.count(opcode.CMSGItemQuerySingle) != 7 {
		t.Fatal(expired)
	}
}

func TestItemLookupBoundsInvalidRequestsCapacityAndTransportFailure(t *testing.T) {
	s, f := fixture()
	for _, ids := range [][]uint32{nil, {0}, {1, 1}, {0x80000000}, {1, 2, 3, 4, 5, 6, 7}} {
		if _, err := s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: ids}); err == nil {
			t.Fatal("accepted IDs", ids)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.LookupItems(ctx, &tradev1.LookupItemsRequest{ItemIds: []uint32{1}}); err == nil || len(f.packets) != 0 {
		t.Fatal("canceled query sent")
	}
	for i := uint32(1); i <= 128; i++ {
		s.queried[i] = true
	}
	got, _ := s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{129}})
	if got.Items[0].Available || got.Items[0].Pending || got.Items[0].Reason != "item_query_body_capacity_reached" || len(f.packets) != 0 {
		t.Fatal(got)
	}
	s, f = fixture()
	f.fail = opcode.CMSGItemQuerySingle
	got, _ = s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{1111}})
	if got.Items[0].Available || got.Items[0].Pending || got.Items[0].Reason != "item_query_transport_unconfirmed" {
		t.Fatal(got)
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, lookupTemplate(1111))
	got, _ = s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{1111}})
	if !got.Items[0].Available || f.count(opcode.CMSGItemQuerySingle) != 1 {
		t.Fatal("late proof not retained", got)
	}
}

type itemLookupInlineSession struct {
	*fakeSession
	observe func(*modv1.SendPacketRequest)
}

func (f *itemLookupInlineSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, options ...grpc.CallOption) (*emptypb.Empty, error) {
	out, err := f.fakeSession.SendPacket(ctx, r, options...)
	if f.observe != nil {
		f.observe(r)
	}
	return out, err
}
func TestItemLookupSynchronousResponseAndConcurrentReaders(t *testing.T) {
	s, f := fixture()
	f.fail = opcode.CMSGItemQuerySingle
	s.session = &itemLookupInlineSession{fakeSession: f, observe: func(r *modv1.SendPacketRequest) {
		packet(s, opcode.SMSGItemQuerySingleResponse, lookupTemplate(binary.LittleEndian.Uint32(r.Payload)))
	}}
	got, _ := s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{1111}})
	if !got.Items[0].Available {
		t.Fatal("transport error erased actual synchronous response", got)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.LookupItems(context.Background(), &tradev1.LookupItemsRequest{ItemIds: []uint32{1111}})
			if e != nil || !r.Items[0].Available {
				t.Error(r, e)
			}
			r.Items[0].Stats[0].Value = 999
		}()
	}
	wg.Wait()
	if f.count(opcode.CMSGItemQuerySingle) != 1 || !proto.Equal(s.lookup.items[1111], got.Items[0]) {
		t.Fatal("query replay or mutable cache")
	}
}

func FuzzItemLookupTemplate(f *testing.F) {
	f.Add(lookupTemplate(1111))
	f.Add(u32(1111 | 0x80000000))
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := parseLookupItem(b)
		if err == nil && d.Available && (d.ItemId == 0 || d.Name == "" || len(d.Stats) > 10 || len(d.Damages) != 2) {
			t.Fatal("invalid accepted item", d)
		}
	})
}
