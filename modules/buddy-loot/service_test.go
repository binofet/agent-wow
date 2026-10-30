package buddyloot

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const corpseGUID = uint64(0xf130000001000001)

type fakeSession struct {
	mu         sync.Mutex
	world      *worldv1.SnapshotResponse
	actions    *actionsv1.State
	packets    []*modv1.SendPacketRequest
	leaseOwner string
	releases   int
	failOpcode uint32
}

func (f *fakeSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	if r.Opcode == f.failOpcode {
		return nil, errors.New("ambiguous write")
	}
	return &emptypb.Empty{}, ctx.Err()
}
func (f *fakeSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var value proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		value = f.world
	case "buddy-actions.status":
		a := proto.Clone(f.actions).(*actionsv1.State)
		a.InteractionOwner = f.leaseOwner
		a.InteractionLeaseRemainingMs = 15000
		value = a
	case "buddy-actions.acquire_interaction":
		request := &actionsv1.InteractionRequest{}
		if err := r.Request.UnmarshalTo(request); err != nil {
			return nil, err
		}
		if request.Owner != "loot" || request.LeaseMs != 15000 || f.leaseOwner != "" {
			return nil, errors.New("lease unavailable")
		}
		f.leaseOwner = "loot"
		a := proto.Clone(f.actions).(*actionsv1.State)
		a.InteractionOwner = "loot"
		a.InteractionLeaseRemainingMs = 15000
		value = &actionsv1.InteractionLease{Token: "owned-loot-token", State: a}
	case "buddy-actions.release_interaction":
		request := &actionsv1.InteractionRelease{}
		if err := r.Request.UnmarshalTo(request); err != nil {
			return nil, err
		}
		if request.Owner != "loot" || request.Token != "owned-loot-token" {
			return nil, errors.New("wrong lease")
		}
		f.leaseOwner = ""
		f.releases++
		value = &actionsv1.State{}
	default:
		return nil, errors.New("unexpected module invocation")
	}
	packed, err := anypb.New(value)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}
func rig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	f := &fakeSession{world: &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 1, Present: true, AliveKnown: true, Alive: true, UnitFlagsKnown: true, UnitFlags: 8}, Owner: &worldv1.PlayerState{Guid: 3, Present: true, AliveKnown: true, Alive: true, UnitFlagsKnown: true, UnitFlags: 8}, LootableCorpses: []*worldv1.CreatureState{{Guid: corpseGUID, Present: true, MapKnown: true, PositionKnown: true, X: 2, HealthKnown: true, MaxHealth: 100, AliveKnown: true, DynamicFlags: 1}}}, actions: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true}}}
	s := NewService(f, 1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	return s, f, func(d time.Duration) { now = now.Add(d) }
}
func current(t *testing.T, s *Service) *lootv1.State {
	t.Helper()
	r, err := s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func enable(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerContextPausesLootAndEnableLastsFourHours(t *testing.T) {
	for _, mode := range []string{"missing", "afk", "dead"} {
		s, f, _ := rig(t)
		enable(t, s)
		if current(t, s).EnabledRemainingMs != uint64(4*time.Hour/time.Millisecond) {
			t.Fatal("intent duration")
		}
		switch mode {
		case "missing":
			f.world.Owner.Present = false
		case "afk":
			f.world.Owner.PlayerFlags = 2
		case "dead":
			f.world.Owner.Alive = false
		}
		s.Tick(context.Background())
		if len(f.packets) != 0 || !current(t, s).Enabled {
			t.Fatalf("context %s: %+v", mode, current(t, s))
		}
	}
}
func emit(s *Service, op uint32, p []byte) {
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p})
}
func windowPacket(guid uint64, gold uint32, items ...lootItem) []byte {
	p := append(guidPacket(guid), 1)
	header := make([]byte, 5)
	binary.LittleEndian.PutUint32(header, gold)
	header[4] = byte(len(items))
	p = append(p, header...)
	for _, item := range items {
		row := make([]byte, 22)
		row[0] = item.slot
		binary.LittleEndian.PutUint32(row[1:], item.id)
		binary.LittleEndian.PutUint32(row[5:], item.count)
		row[21] = item.slotType
		p = append(p, row...)
	}
	return p
}
func pushPacket(player uint64, id, count uint32) []byte {
	p := make([]byte, 45)
	binary.LittleEndian.PutUint64(p, player)
	binary.LittleEndian.PutUint32(p[25:], id)
	binary.LittleEndian.PutUint32(p[37:], count)
	binary.LittleEndian.PutUint32(p[41:], count)
	return p
}
func releasePacket() []byte { return append(guidPacket(corpseGUID), 1) }
func openItem(t *testing.T, s *Service) {
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 123, count: 3}))
	s.Tick(context.Background())
}

func TestLootRequiresEnableAndNearbyActualCorpse(t *testing.T) {
	s, f, _ := rig(t)
	s.Tick(context.Background())
	if len(f.packets) != 0 {
		t.Fatal("disabled loot sent packet")
	}
	for _, change := range []func(*fakeSession){func(f *fakeSession) { f.world.LootableCorpses[0].X = 5 }, func(f *fakeSession) { f.world.LootableCorpses[0].Health = 1 }, func(f *fakeSession) { f.world.LootableCorpses[0].DynamicFlags = 0 }, func(f *fakeSession) { f.world.LootableCorpses[0].Guid = 3 }, func(f *fakeSession) { f.world.LootableCorpses[0].MapId = 1 }, func(f *fakeSession) { f.actions.Position.Known = false }, func(f *fakeSession) { f.world.Owner.UnitFlags |= 0x80000 }} {
		s, f, _ := rig(t)
		change(f)
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 0 {
			t.Fatalf("ineligible corpse looted %+v", current(t, s))
		}
	}
}

func TestOrdinaryItemNeedsOwnReceiptAndSlotRemovalThenReleases(t *testing.T) {
	s, f, advance := rig(t)
	openItem(t, s)
	if len(f.packets) != 2 || f.packets[0].Opcode != opcode.CMSGLoot || f.packets[1].Opcode != opcode.CMSGAutostoreLootItem || len(f.packets[1].Payload) != 1 || f.packets[1].Payload[0] != 2 {
		t.Fatalf("unexpected requests %+v", f.packets)
	}
	if current(t, s).ItemsStoredConfirmed != 0 {
		t.Fatal("transport claimed inventory success")
	}
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	emit(s, opcode.SMSGItemPushResult, pushPacket(3, 123, 3))
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 0 || len(f.packets) != 2 {
		t.Fatal("party member receipt claimed as ours")
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 123, 3))
	s.Tick(context.Background())
	s.Tick(context.Background())
	if current(t, s).ItemsStoredConfirmed != 3 || len(current(t, s).RecentItems) != 1 || f.packets[2].Opcode != opcode.CMSGLootRelease {
		t.Fatalf("receipt %+v", current(t, s))
	}
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	if f.releases != 1 || f.leaseOwner != "" || current(t, s).PendingCorpseGuid != 0 {
		t.Fatal("lease not released after server response")
	}
	advance(2 * time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 3 {
		t.Fatal("same corpse retried endlessly")
	}
}

func TestRestrictedRollAndMasterSlotsNeverAutostore(t *testing.T) {
	s, f, _ := rig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 0, id: 1, count: 1, slotType: 1}, lootItem{slot: 1, id: 2, count: 1, slotType: 2}, lootItem{slot: 2, id: 3, count: 1, slotType: 3}))
	s.Tick(context.Background())
	if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease {
		t.Fatalf("restricted items requested %+v", f.packets)
	}
}

func TestMoneyRequestsAreEmptyAndCountOnlyServerNotifications(t *testing.T) {
	s, f, _ := rig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 17))
	s.Tick(context.Background())
	if f.packets[1].Opcode != opcode.CMSGLootMoney || len(f.packets[1].Payload) != 0 || current(t, s).MoneyAwardedNotified != 0 {
		t.Fatal("money optimism or bad packet")
	}
	emit(s, opcode.SMSGLootClearMoney, nil)
	emit(s, opcode.SMSGLootMoneyNotify, []byte{17, 0, 0, 0, 1})
	s.Tick(context.Background())
	s.Tick(context.Background())
	if current(t, s).MoneyAwardedNotified != 17 || f.packets[2].Opcode != opcode.CMSGLootRelease {
		t.Fatalf("money %+v", current(t, s))
	}
}

func TestInventoryFailureDisablesAndDoesNotRetry(t *testing.T) {
	s, f, advance := rig(t)
	openItem(t, s)
	failure := make([]byte, 18)
	failure[0] = 50
	emit(s, opcode.SMSGInventoryChangeFailure, failure)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
	s.Tick(context.Background())
	if got := current(t, s); got.Enabled || got.LastInventoryError != 50 || got.ItemsStoredConfirmed != 0 {
		t.Fatalf("inventory error %+v", got)
	}
	count := len(f.packets)
	advance(time.Minute)
	for i := 0; i < 10; i++ {
		s.Tick(context.Background())
	}
	if len(f.packets) != count {
		t.Fatal("full inventory retried")
	}
}

func TestTimeoutIsFiniteEvenAfterOverallTransactionDeadline(t *testing.T) {
	s, f, advance := rig(t)
	enable(t, s)
	s.Tick(context.Background())
	advance(13 * time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease {
		t.Fatal("timeout did not release")
	}
	advance(3 * time.Second)
	for i := 0; i < 20; i++ {
		s.Tick(context.Background())
	}
	if len(f.packets) != 2 || f.releases != 1 || current(t, s).Enabled {
		t.Fatalf("timeout loop %+v packets%d", current(t, s), len(f.packets))
	}
}

func TestBusyOrLostLeasePreventsLootWrites(t *testing.T) {
	s, f, _ := rig(t)
	f.leaseOwner = "trade"
	enable(t, s)
	s.Tick(context.Background())
	if len(f.packets) != 0 {
		t.Fatal("loot bypassed trade lease")
	}
	s, f, _ = rig(t)
	openItem(t, s)
	f.leaseOwner = "trade"
	s.Tick(context.Background())
	if current(t, s).Enabled || len(f.packets) != 2 {
		t.Fatal("loot continued after lease loss")
	}
}

func TestMalformedPacketsAndForeignWindowsCannotAdvance(t *testing.T) {
	s, _, _ := rig(t)
	enable(t, s)
	s.Tick(context.Background())
	for _, op := range []uint32{opcode.SMSGLootResponse, opcode.SMSGLootReleaseResponse, opcode.SMSGLootRemoved, opcode.SMSGItemPushResult, opcode.SMSGLootMoneyNotify, opcode.SMSGInventoryChangeFailure} {
		emit(s, op, []byte{0xff})
	}
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID+1, 0, lootItem{slot: 0, id: 1, count: 1}))
	if got := current(t, s); got.MalformedPackets != 5 || got.Phase != "waiting_open" || got.ItemsStoredConfirmed != 0 {
		t.Fatalf("malformed %+v", got)
	}
}

func TestTransportFailureDoesNotRetryAndLogoutCloses(t *testing.T) {
	s, f, advance := rig(t)
	f.failOpcode = opcode.CMSGLoot
	enable(t, s)
	s.Tick(context.Background())
	advance(time.Second)
	s.Tick(context.Background())
	if len(f.packets) != 1 || current(t, s).Enabled || f.releases != 1 {
		t.Fatal("ambiguous open retried")
	}
	s, f, _ = rig(t)
	openItem(t, s)
	_, _ = s.BeforeLogout(context.Background(), &emptypb.Empty{})
	s.Tick(context.Background())
	if f.releases != 1 || f.packets[len(f.packets)-1].Opcode != opcode.CMSGLootRelease {
		t.Fatal("logout did not release owned loot")
	}
	if _, err := s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: true}); err == nil {
		t.Fatal("loot enabled during logout")
	}
}

func TestConcurrentStateAndPackets(t *testing.T) {
	s, _, _ := rig(t)
	s.now = time.Now
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				_, _ = s.GetState(context.Background(), &emptypb.Empty{})
				emit(s, opcode.SMSGLootResponse, []byte{1})
			}
		}()
	}
	wg.Wait()
}
