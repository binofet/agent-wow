package buddytrade

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeSession struct {
	mu               sync.Mutex
	world            *worldv1.SnapshotResponse
	actions          *actionsv1.State
	packets          []*modv1.SendPacketRequest
	fail             uint32
	leases, releases int
	leaseChange      func()
}

func fixture() (*Service, *fakeSession) {
	f := &fakeSession{world: &worldv1.SnapshotResponse{
		Self:          &worldv1.PlayerState{Guid: 1, Present: true, AliveKnown: true, Alive: true, MapKnown: true},
		Owner:         &worldv1.PlayerState{Guid: 3, Present: true, AliveKnown: true, Alive: true, MapKnown: true, PositionKnown: true, X: 1},
		BackpackKnown: true, BackpackRevision: 4,
	}, actions: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true}}}
	for i := range 16 {
		f.world.Backpack = append(f.world.Backpack, &worldv1.BackpackSlot{Slot: uint32(i)})
	}
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: 0x4000000000000007, Entry: 159, StackCount: 5, MetadataKnown: true}
	return NewService(f, "session-A", 1), f
}
func (f *fakeSession) SendPacket(_ context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	if r.Opcode == f.fail {
		return nil, errors.New("ambiguous write")
	}
	return &emptypb.Empty{}, nil
}
func (f *fakeSession) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		result = f.world
	case "buddy-actions.status":
		result = f.actions
	case "buddy-actions.acquire_interaction":
		f.leases++
		if f.leaseChange != nil {
			f.leaseChange()
		}
		result = &actionsv1.InteractionLease{Token: "lease-token", State: f.actions}
	case "buddy-actions.release_interaction":
		f.releases++
		result = f.actions
	default:
		return nil, errors.New("unexpected dependency call")
	}
	value, err := anypb.New(result)
	return &modv1.InvokeModuleResponse{Result: value}, err
}
func (f *fakeSession) count(op uint32) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.packets {
		if p.Opcode == op {
			n++
		}
	}
	return n
}
func packet(s *Service, op uint32, data []byte) {
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: data})
}
func statusPacket(s *Service, code uint32) {
	data := u32(code)
	if code == 2 {
		data = append(data, u32(0)...)
	}
	packet(s, opcode.SMSGTradeStatus, data)
}
func openIncoming(s *Service) {
	packet(s, opcode.SMSGTradeStatus, append(u32(1), u64(3)...))
	s.Tick(context.Background())
	statusPacket(s, 2)
}
func extended(partner bool, entry, count, slot, spell uint32) []byte {
	data := make([]byte, 532)
	if partner {
		data[0] = 1
	}
	binary.LittleEndian.PutUint32(data[5:], 7)
	binary.LittleEndian.PutUint32(data[9:], 7)
	binary.LittleEndian.PutUint32(data[17:], spell)
	for i := range 7 {
		at := 21 + i*73
		data[at] = byte(i)
		if uint32(i) == slot {
			binary.LittleEndian.PutUint32(data[at+1:], entry)
			binary.LittleEndian.PutUint32(data[at+9:], count)
		}
	}
	return data
}
func TestReceiveGiftWaitsForJoelAndServerCompletion(t *testing.T) {
	s, f := fixture()
	openIncoming(s)
	if f.count(opcode.CMSGBeginTrade) != 1 || s.snapshot().Phase != "OPEN" {
		t.Fatal(s.snapshot())
	}
	packet(s, opcode.SMSGTradeStatusExtended, extended(true, 117, 2, 0, 0))
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 0 {
		t.Fatal("accepted without Joel")
	}
	statusPacket(s, 4)
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 1 || s.snapshot().CompletedTrades != 0 {
		t.Fatal(s.snapshot())
	}
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 1 {
		t.Fatal("repeated ambiguous consent")
	}
	statusPacket(s, 8)
	s.Tick(context.Background())
	if s.snapshot().CompletedTrades != 1 || s.snapshot().Phase != "COMPLETE" || f.releases != 1 {
		t.Fatal(s.snapshot(), f.releases)
	}
}
func TestUnknownPartnerIsCanceledWithoutOpening(t *testing.T) {
	s, f := fixture()
	packet(s, opcode.SMSGTradeStatus, append(u32(1), u64(2)...))
	s.Tick(context.Background())
	if f.count(opcode.CMSGBeginTrade) != 0 || f.count(opcode.CMSGCancelTrade) != 1 {
		t.Fatal(f.packets)
	}
}

func TestServerWindowErrorsCancelAllocatedTradeOnce(t *testing.T) {
	for _, code := range []uint32{3, 10, 12, 23} {
		t.Run(string(rune('A'+code)), func(t *testing.T) {
			s, f := fixture()
			openIncoming(s)
			data := u32(code)
			if code == 12 {
				data = append(data, make([]byte, 9)...)
			}
			if code == 23 {
				data = append(data, 0)
			}
			packet(s, opcode.SMSGTradeStatus, data)
			s.Tick(context.Background())
			s.Tick(context.Background())
			if f.count(opcode.CMSGCancelTrade) != 1 || f.releases != 1 {
				t.Fatal("server trade allocation was not canceled exactly once", s.snapshot())
			}
		})
	}
}
func TestWholeStackOfferBindsSessionInventoryAndSlot(t *testing.T) {
	s, f := fixture()
	openIncoming(s)
	current := s.snapshot()
	request := &tradev1.ItemRequest{SessionId: current.SessionId, Revision: current.Revision, BackpackRevision: 4, BackpackSlot: 2, ExpectedItemGuid: f.world.Backpack[2].ItemGuid, TradeSlot: 0}
	stale := proto.Clone(request).(*tradev1.ItemRequest)
	stale.BackpackRevision = 3
	if _, err := s.OfferItem(context.Background(), stale); err == nil {
		t.Fatal("accepted stale inventory")
	}
	stale = proto.Clone(request).(*tradev1.ItemRequest)
	stale.ExpectedItemGuid++
	if _, err := s.OfferItem(context.Background(), stale); err == nil {
		t.Fatal("accepted wrong GUID")
	}
	offered, err := s.OfferItem(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(offered.OwnRequestedItems) != 1 || offered.OwnRequestedItems[0].Count != 5 {
		t.Fatal(offered)
	}
	found := false
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGSetTradeItem {
			found = true
			if string(p.Payload) != string([]byte{0, 255, 25}) {
				t.Fatal(p)
			}
		}
	}
	if !found {
		t.Fatal("no offer")
	}
	if _, err := s.OfferItem(context.Background(), request); err == nil {
		t.Fatal("reused revision")
	}
	statusPacket(s, 4)
	f.world.Backpack[2].StackCount = 4
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 0 {
		t.Fatal("accepted changed stack")
	}
	s.Tick(context.Background())
	if f.count(opcode.CMSGCancelTrade) != 1 {
		t.Fatal("changed trade not canceled")
	}
}
func TestPartnerChangesTermsInvalidatePriorConsent(t *testing.T) {
	s, f := fixture()
	openIncoming(s)
	statusPacket(s, 4)
	packet(s, opcode.SMSGTradeStatusExtended, extended(true, 117, 1, 0, 0))
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 0 {
		t.Fatal("accepted changed terms")
	}
	statusPacket(s, 4)
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 1 {
		t.Fatal("new consent not accepted")
	}
}
func TestEnchantSlotNeverAutoAccepted(t *testing.T) {
	s, f := fixture()
	openIncoming(s)
	packet(s, opcode.SMSGTradeStatusExtended, extended(true, 117, 1, 6, 0))
	statusPacket(s, 4)
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 0 {
		t.Fatal("accepted nontraded slot")
	}
}
func TestUncertainAcceptNotRetried(t *testing.T) {
	s, f := fixture()
	openIncoming(s)
	f.fail = opcode.CMSGAcceptTrade
	statusPacket(s, 4)
	s.Tick(context.Background())
	s.Tick(context.Background())
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 1 || s.snapshot().CompletedTrades != 0 {
		t.Fatal(s.snapshot())
	}
}
func TestTradeTimeoutReleasesPause(t *testing.T) {
	s, f := fixture()
	openIncoming(s)
	future := time.Now().Add(6 * time.Minute)
	s.now = func() time.Time { return future }
	s.Tick(context.Background())
	if f.count(opcode.CMSGCancelTrade) != 1 || f.releases != 1 {
		t.Fatal(s.snapshot())
	}
}

func TestTradeYieldsToCombatAndUnknownOwnOffers(t *testing.T) {
	s, f := fixture()
	openIncoming(s)
	f.world.Owner.UnitFlags = 0x80000
	s.Tick(context.Background())
	if f.count(opcode.CMSGCancelTrade) != 1 || f.releases != 1 {
		t.Fatal("trade did not yield to combat", s.snapshot())
	}
	s, f = fixture()
	openIncoming(s)
	packet(s, opcode.SMSGTradeStatusExtended, extended(false, 159, 5, 0, 0))
	statusPacket(s, 4)
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 0 || f.count(opcode.CMSGCancelTrade) != 1 {
		t.Fatal("accepted an unrequested own offer", s.snapshot())
	}
}
func TestGoldAndOutgoingTradeAreExplicitAndRevisionBound(t *testing.T) {
	s, f := fixture()
	if _, err := s.Open(context.Background(), &tradev1.BoundRequest{SessionId: "stale"}); err == nil {
		t.Fatal("wrong session")
	}
	st, err := s.Open(context.Background(), &tradev1.BoundRequest{SessionId: "session-A"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != "REQUESTED" || f.count(opcode.CMSGInitiateTrade) != 1 {
		t.Fatal(st)
	}
	statusPacket(s, 2)
	st = s.snapshot()
	st, err = s.OfferGold(context.Background(), &tradev1.GoldRequest{SessionId: st.SessionId, Revision: st.Revision, Copper: 12345})
	if err != nil || st.OwnRequestedCopper != 12345 {
		t.Fatal(st, err)
	}
	if f.count(opcode.CMSGSetTradeGold) != 1 {
		t.Fatal("gold not offered")
	}
}
func TestConcurrentPacketStatusAndTick(t *testing.T) {
	s, _ := fixture()
	openIncoming(s)
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			for range 100 {
				switch i {
				case 0:
					packet(s, opcode.SMSGTradeStatusExtended, extended(true, 117, 1, 0, 0))
				case 1:
					s.Tick(context.Background())
				case 2:
					_, _ = s.GetState(context.Background(), &emptypb.Empty{})
				}
			}
		})
	}
	wg.Wait()
}

func TestStrictProtocolAndItemName(t *testing.T) {
	data := extended(true, 159, 5, 0, 0)
	partner, _, _, items, err := parseExtended(data)
	if err != nil || !partner || len(items) != 1 || items[0].Count != 5 {
		t.Fatal(items, err)
	}
	for i := range len(data) {
		if _, _, _, _, err := parseExtended(data[:i]); err == nil {
			t.Fatalf("accepted truncation %d", i)
		}
	}
	data[21+73] = 0
	if _, _, _, _, err := parseExtended(data); err == nil {
		t.Fatal("duplicate slot")
	}
	name := append(make([]byte, 16), []byte("Refreshing Spring Water\x00\x00\x00\x00")...)
	name = append(name, make([]byte, 100)...)
	binary.LittleEndian.PutUint32(name, 159)
	entry, value, err := parseItemName(name)
	if err != nil || entry != 159 || value != "Refreshing Spring Water" {
		t.Fatal(entry, value, err)
	}
	name[17] = '|'
	if _, _, err := parseItemName(name); err == nil {
		t.Fatal("markup name")
	}
}
func FuzzTradePackets(f *testing.F) {
	f.Add(extended(true, 159, 5, 0, 0))
	f.Add(u32(4))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = parseStatus(data)
		_, _, _, _, _ = parseExtended(data)
		_, _, _ = parseItemName(data)
	})
}
