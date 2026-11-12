package buddytrade

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func itemQuery(entry, quality, class, subclass uint32) []byte {
	b := append(u32(entry), u32(class)...)
	b = append(b, u32(subclass)...)
	b = append(b, u32(0)...)
	b = append(b, []byte("Ruined Pelt\x00\x00\x00\x00")...)
	prefix := make([]byte, 22*4)
	binary.LittleEndian.PutUint32(prefix[4:], quality)
	binary.LittleEndian.PutUint32(prefix[19*4:], 20)
	b = append(b, prefix...)
	b = append(b, make([]byte, 8+24+28+12+5*24+4)...)
	b = append(b, 0) // description
	return append(b, make([]byte, 29*4)...)
}

func junkFixture() (*Service, *fakeSession) {
	s, f := fixture()
	f.world.Self.UnitFlagsKnown = true
	for i := range 16 {
		f.world.Backpack[i] = &worldv1.BackpackSlot{Bag: 0, Slot: uint32(i), ItemGuid: 0x4000000000000100 + uint64(i), Entry: 117, StackCount: 1, MetadataKnown: true}
	}
	f.world.Backpack[2].Entry = 4865
	f.world.Backpack[2].StackCount = 5
	_, _ = s.GetState(context.Background(), nil)
	packet(s, opcode.SMSGItemQuerySingleResponse, itemQuery(4865, 0, 15, 0))
	packet(s, opcode.SMSGItemQuerySingleResponse, itemQuery(117, 1, 0, 0))
	return s, f
}

func requestJunk(s *Service, f *fakeSession) *tradev1.JunkRequest {
	state := s.snapshot()
	return &tradev1.JunkRequest{SessionId: state.SessionId, Revision: state.Revision, BackpackRevision: f.world.BackpackRevision, BackpackSlot: 2, ExpectedItemGuid: f.world.Backpack[2].ItemGuid}
}

func TestJunkWholeStackWaitsForServerInventoryReceipt(t *testing.T) {
	s, f := junkFixture()
	got, err := s.DestroyJunk(context.Background(), requestJunk(s, f))
	if err != nil {
		t.Fatal(err)
	}
	if got.JunkStacksDestroyed != 0 || got.LastJunkDisposal.Status != "AWAITING_SERVER_SLOT_UPDATE" {
		t.Fatal(got)
	}
	f.mu.Lock()
	var body []byte
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGDestroyitem {
			body = p.Payload
		}
	}
	f.mu.Unlock()
	if string(body) != string([]byte{255, 25, 0, 0, 0, 0}) {
		t.Fatalf("destroy body %x", body)
	}
	s.Tick(context.Background())
	if s.snapshot().JunkStacksDestroyed != 0 {
		t.Fatal("transport treated as success")
	}
	// An unrelated newer slot update is not the reserved item's removal.
	f.world.BackpackRevision++
	s.Tick(context.Background())
	if s.snapshot().JunkStacksDestroyed != 0 {
		t.Fatal("unrelated update treated as success")
	}
	// Moving the item to another backpack slot does not prove destruction.
	old := f.world.Backpack[2].ItemGuid
	f.world.Backpack[2].ItemGuid = 0
	f.world.Backpack[3].ItemGuid = old
	f.world.BackpackRevision++
	s.Tick(context.Background())
	if s.snapshot().JunkStacksDestroyed != 0 {
		t.Fatal("moved item treated as destroyed")
	}
	f.world.Backpack[3].ItemGuid = 0
	f.world.BackpackRevision++
	s.Tick(context.Background())
	if s.snapshot().JunkStacksDestroyed != 1 || s.snapshot().LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" || f.releases != 1 {
		t.Fatal(s.snapshot(), f.releases)
	}
	s.Tick(context.Background())
	if s.snapshot().JunkStacksDestroyed != 1 || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal(s.snapshot())
	}
}

func TestJunkAmbiguousWriteNeverRetriesOrDisposesSecondStack(t *testing.T) {
	s, f := junkFixture()
	f.fail = opcode.CMSGDestroyitem
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	_, _ = s.JunkDisposal(context.Background(), &tradev1.JunkToggle{Enabled: true})
	s.Tick(context.Background())
	if f.count(opcode.CMSGDestroyitem) != 1 || s.snapshot().LastJunkDisposal.Status != "TRANSPORT_UNCONFIRMED_NO_RETRY" {
		t.Fatal(s.snapshot())
	}
	now = now.Add(6 * time.Second)
	for range 3 {
		s.Tick(context.Background())
	}
	if f.count(opcode.CMSGDestroyitem) != 1 || s.snapshot().JunkStacksDestroyed != 0 || f.releases != 1 {
		t.Fatal(s.snapshot(), f.releases)
	}
	_, _ = s.JunkDisposal(context.Background(), &tradev1.JunkToggle{Enabled: false})
	_, _ = s.JunkDisposal(context.Background(), &tradev1.JunkToggle{Enabled: true})
	if _, err := s.DestroyJunk(context.Background(), requestJunk(s, f)); err == nil {
		t.Fatal("ambiguous reservation cleared by toggle")
	}
	if _, err := s.Open(context.Background(), &tradev1.BoundRequest{SessionId: s.snapshot().SessionId, Revision: s.snapshot().Revision}); err == nil {
		t.Fatal("trade started with ambiguous inventory mutation")
	}
	// A delayed real receipt still resolves the reservation exactly once.
	f.world.Backpack[2].ItemGuid = 0
	f.world.BackpackRevision++
	s.Tick(context.Background())
	if s.snapshot().JunkStacksDestroyed != 1 {
		t.Fatal(s.snapshot())
	}
}

func TestJunkRequiresOptInAndFullUniqueKnownBackpack(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(*Service, *fakeSession)
	}{
		{"space", func(_ *Service, f *fakeSession) { f.world.Backpack[0].ItemGuid = 0 }},
		{"unknown", func(_ *Service, f *fakeSession) { f.world.BackpackKnown = false }},
		{"missing_slot", func(_ *Service, f *fakeSession) { f.world.Backpack = f.world.Backpack[:15] }},
		{"duplicate_slot", func(_ *Service, f *fakeSession) { f.world.Backpack[0].Slot = 1 }},
		{"duplicate_guid", func(_ *Service, f *fakeSession) { f.world.Backpack[0].ItemGuid = f.world.Backpack[1].ItemGuid }},
		{"unknown_item", func(_ *Service, f *fakeSession) { f.world.Backpack[0].MetadataKnown = false }},
		{"combat", func(_ *Service, f *fakeSession) { f.world.Self.UnitFlags = 0x80000 }},
		{"dead", func(_ *Service, f *fakeSession) { f.world.Self.Alive = false }},
		{"unknown_flags", func(_ *Service, f *fakeSession) { f.world.Self.UnitFlagsKnown = false }},
		{"casting", func(_ *Service, f *fakeSession) { f.actions.CastPending = true }},
		{"motion_unconfirmed", func(_ *Service, f *fakeSession) { f.actions.MotionUnconfirmed = true }},
		{"autoattack_unconfirmed", func(_ *Service, f *fakeSession) { f.actions.AutoAttackUnconfirmed = true }},
		{"other_interaction", func(_ *Service, f *fakeSession) { f.actions.InteractionOwner = "loot" }},
		{"trade", func(s *Service, _ *fakeSession) { s.state.Phase = "OPEN" }},
		{"trade_cancel_unconfirmed", func(s *Service, _ *fakeSession) { s.state.Phase = "CANCEL_SENT" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, f := junkFixture()
			change.edit(s, f)
			if _, err := s.DestroyJunk(context.Background(), requestJunk(s, f)); err == nil {
				t.Fatal("unsafe junk disposal allowed")
			}
			if f.count(opcode.CMSGDestroyitem) != 0 {
				t.Fatal("destroy written")
			}
		})
	}
	s, f := junkFixture()
	s.Tick(context.Background())
	if f.count(opcode.CMSGDestroyitem) != 0 {
		t.Fatal("automatic disposal enabled by default")
	}
}

func TestJunkMetadataExactClassQualityAndFunctionalExclusions(t *testing.T) {
	good := itemQuery(4865, 0, 15, 0)
	m, err := parseItemMetadata(good)
	if err != nil || !m.disposable() {
		t.Fatal(m, err)
	}
	for i := range len(good) {
		if partial, e := parseItemMetadata(good[:i]); e == nil && partial.disposable() {
			t.Fatalf("truncation authorized at %d", i)
		}
	}
	if _, e := parseItemMetadata(append(good, 0)); e == nil {
		t.Fatal("trailing byte")
	}
	for _, b := range [][]byte{itemQuery(1, 1, 15, 0), itemQuery(1, 0, 12, 0), itemQuery(1, 0, 4, 0), itemQuery(1, 0, 15, 1), itemQuery(1, 0, 0, 0), itemQuery(1, 0, 9, 0), itemQuery(1, 0, 1, 0)} {
		v, e := parseItemMetadata(b)
		if e != nil || v.disposable() {
			t.Fatal(v, e)
		}
	}
	for _, edit := range []func(*itemMetadata){func(m *itemMetadata) { m.flags = 0x20 }, func(m *itemMetadata) { m.inventoryType = 1 }, func(m *itemMetadata) { m.containerSlots = 1 }, func(m *itemMetadata) { m.hasSpell = true }, func(m *itemMetadata) { m.startQuest = 1 }, func(m *itemMetadata) { m.lockID = 1 }, func(m *itemMetadata) { m.pageText = 1 }, func(m *itemMetadata) { m.bagFamily = 1 }, func(m *itemMetadata) { m.bonding = 4 }, func(m *itemMetadata) { m.maxDurability = 1 }, func(m *itemMetadata) { m.duration = 1 }} {
		v := m
		edit(&v)
		if v.disposable() {
			t.Fatal(v)
		}
	}
}

func TestJunkBindingAndIncomingTradeCannotRaceMutation(t *testing.T) {
	s, f := junkFixture()
	request := requestJunk(s, f)
	request.BackpackRevision--
	if _, e := s.DestroyJunk(context.Background(), request); e == nil {
		t.Fatal("stale backpack")
	}
	request = requestJunk(s, f)
	request.ExpectedItemGuid++
	if _, e := s.DestroyJunk(context.Background(), request); e == nil {
		t.Fatal("wrong GUID")
	}
	request = requestJunk(s, f)
	request.Revision++
	if _, e := s.DestroyJunk(context.Background(), request); e == nil {
		t.Fatal("stale state")
	}
	_, e := s.DestroyJunk(context.Background(), requestJunk(s, f))
	if e != nil {
		t.Fatal(e)
	}
	packet(s, opcode.SMSGTradeStatus, append(u32(1), u64(3)...))
	s.Tick(context.Background())
	if f.count(opcode.CMSGBeginTrade) != 0 || f.count(opcode.CMSGCancelTrade) != 1 {
		t.Fatal("trade opened during disposal")
	}
}

func TestJunkRevalidatesInventoryAndSafetyAfterLease(t *testing.T) {
	for _, change := range []func(*fakeSession){
		func(f *fakeSession) { f.world.Backpack[2].ItemGuid++ },
		func(f *fakeSession) { f.world.Backpack[2].StackCount--; f.world.BackpackRevision++ },
		func(f *fakeSession) { f.world.Self.UnitFlags = 0x80000 },
		func(f *fakeSession) { f.actions.CastPending = true },
	} {
		s, f := junkFixture()
		f.leaseChange = func() { change(f) }
		if _, e := s.DestroyJunk(context.Background(), requestJunk(s, f)); e == nil {
			t.Fatal("postlease change ignored")
		}
		if f.count(opcode.CMSGDestroyitem) != 0 || f.releases != 1 {
			t.Fatal(s.snapshot(), f.releases)
		}
	}
}

func FuzzItemMetadata(f *testing.F) {
	f.Add(itemQuery(4865, 0, 15, 0))
	f.Add(u32(0x80000001))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, e := parseItemMetadata(b)
		if e != nil && m.known {
			t.Fatal("malformed query became known")
		}
	})
}

func TestJunkConcurrentStatePacketAndTicks(t *testing.T) {
	s, _ := junkFixture()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				s.Tick(context.Background())
				packet(s, opcode.SMSGItemQuerySingleResponse, itemQuery(4865, 0, 15, 0))
				_, _ = s.GetState(context.Background(), nil)
			}
		}()
	}
	wg.Wait()
}
