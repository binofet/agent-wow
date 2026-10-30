package buddytraining

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	trainingv1 "github.com/agent-wow/agent-wow/modules/buddy-training/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeSession struct {
	mu                 sync.Mutex
	world              *worldv1.SnapshotResponse
	actions            *actionsv1.State
	wallet             *tradev1.State
	packets            []*modv1.SendPacketRequest
	acquired, released int
	lease              bool
	onSend             func(context.Context, *modv1.SendPacketRequest) error
	onAcquire          func()
}

func fixture() *fakeSession {
	return &fakeSession{world: &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 1, Present: true, MapKnown: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, UnitFlagsKnown: true, ClassKnown: true, ClassId: 5, LevelKnown: true, Level: 2, MovementFlagsKnown: true}, NearbyCreatures: []*worldv1.CreatureState{{Guid: trainerGUID, Entry: 341, NpcFlags: 0x30, Present: true, MapKnown: true, PositionKnown: true, X: 1, HealthKnown: true, Health: 100, AliveKnown: true, Alive: true}}}, actions: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true}}, wallet: &tradev1.State{SessionId: "body", CopperKnown: true, Copper: 100, CopperRevision: 1}}
}
func (f *fakeSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	fn := f.onSend
	f.mu.Unlock()
	if fn != nil {
		if e := fn(ctx, r); e != nil {
			return nil, e
		}
	}
	return &emptypb.Empty{}, nil
}
func (f *fakeSession) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		out = proto.Clone(f.world)
	case "buddy-actions.status":
		out = proto.Clone(f.actions)
	case "buddy-trade.state":
		out = proto.Clone(f.wallet)
	case "buddy-actions.acquire_interaction":
		in := &actionsv1.InteractionRequest{}
		if e := r.Request.UnmarshalTo(in); e != nil {
			return nil, e
		}
		if in.Owner != "training" || in.LeaseMs != 15000 || f.lease {
			return nil, status.Error(codes.FailedPrecondition, "wrong lease")
		}
		f.lease = true
		f.acquired++
		f.actions.InteractionOwner = "training"
		if f.onAcquire != nil {
			f.onAcquire()
		}
		out = &actionsv1.InteractionLease{Token: "training-token", State: proto.Clone(f.actions).(*actionsv1.State)}
	case "buddy-actions.release_interaction":
		in := &actionsv1.InteractionRelease{}
		if e := r.Request.UnmarshalTo(in); e != nil {
			return nil, e
		}
		if in.Owner != "training" || in.Token != "training-token" || !f.lease {
			return nil, status.Error(codes.FailedPrecondition, "wrong release")
		}
		f.lease = false
		f.released++
		f.actions.InteractionOwner = ""
		out = proto.Clone(f.actions)
	default:
		return nil, errors.New("unexpected module call")
	}
	a, e := anypb.New(out)
	return &modv1.InvokeModuleResponse{Result: a}, e
}
func deliver(s *Service, op uint32, p []byte) {
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p})
}
func setup(t *testing.T) (*Service, *fakeSession, *trainingv1.BuyRequest) {
	t.Helper()
	f := fixture()
	s := NewService(f, "body", 1)
	f.onSend = func(_ context.Context, r *modv1.SendPacketRequest) error {
		if r.Opcode == opcode.CMSGTrainerList {
			deliver(s, opcode.SMSGTrainerList, trainerWire(0))
		}
		return nil
	}
	out, e := s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID})
	if e != nil {
		t.Fatal(e)
	}
	if out.ActionInFlight || f.lease || f.acquired != f.released {
		t.Fatal("list did not clean up")
	}
	return s, f, &trainingv1.BuyRequest{SessionId: "body", NpcGuid: trainerGUID, OfferRevision: out.Offer.Revision, SpellId: 2050, ExpectedCostCopper: 10, MaxSpendCopper: 10}
}
func TestPurchaseNeedsLearningAndServerSuccess(t *testing.T) {
	for _, mode := range []string{"direct", "wrapped", "ack_only", "rejected", "ambiguous", "confirmed_then_transport_error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := setup(t)
			f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
				switch q.Opcode {
				case opcode.CMSGTrainerBuySpell:
					if mode == "ambiguous" {
						return errors.New("write ambiguous")
					}
					if mode == "rejected" {
						deliver(s, opcode.SMSGTrainerBuyFailed, append(buyPacket(trainerGUID, 2050), 1, 0, 0, 0))
						return nil
					}
					if mode == "direct" || mode == "confirmed_then_transport_error" {
						deliver(s, opcode.SMSGLearnedSpell, []byte{2, 8, 0, 0, 0, 0})
					}
					if mode == "wrapped" {
						deliver(s, opcode.SMSGLearnedSpell, []byte{3, 8, 0, 0, 0, 0})
					}
					deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, 2050))
					f.mu.Lock()
					f.wallet.Copper = 90
					f.wallet.CopperRevision = 2
					f.mu.Unlock()
					if mode == "confirmed_then_transport_error" {
						return errors.New("callback succeeded before ambiguous write result")
					}
				case opcode.CMSGTrainerList:
					// Duplicate buy acknowledgement must not complete this distinct phase.
					deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, 2050))
					state := byte(0)
					if mode == "wrapped" || mode == "direct" {
						state = 2
					}
					deliver(s, opcode.SMSGTrainerList, trainerWire(state))
				}
				return nil
			}
			out, e := s.Buy(context.Background(), r)
			if e != nil {
				t.Fatal(e)
			}
			p := out.LastPurchase
			if p == nil || out.ActionInFlight || f.lease || f.acquired != f.released {
				t.Fatalf("bad cleanup %+v", out)
			}
			want := trainingv1.PurchaseStatus_TRAINING_CONFIRMED
			switch mode {
			case "ack_only":
				want = trainingv1.PurchaseStatus_TRAINER_SUCCEEDED
			case "rejected":
				want = trainingv1.PurchaseStatus_SERVER_REJECTED
			case "ambiguous":
				want = trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT
			}
			if p.Status != want {
				t.Fatalf("got%v want%v: %+v", p.Status, want, p)
			}
			if mode == "wrapped" && (!p.RefreshedTrainerKnows || p.LearnedSpellObserved || len(p.LearnedSpellIds) != 1 || p.LearnedSpellIds[0] != 2051) {
				t.Fatal(p)
			}
			if mode == "direct" && (!p.LearnedSpellObserved || !p.CopperDebitObserved || !p.TransportWritten) {
				t.Fatal(p)
			}
			if mode == "ack_only" || mode == "ambiguous" {
				r.OfferRevision = out.Offer.Revision
				if _, e := s.Buy(context.Background(), r); status.Code(e) != codes.FailedPrecondition {
					t.Fatal("ambiguous retry allowed", e)
				}
			}
		})
	}
}
func TestBuyRejectsBeforeAnyPacket(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Service, *fakeSession, *trainingv1.BuyRequest)
	}{
		{"session", func(_ *Service, _ *fakeSession, r *trainingv1.BuyRequest) { r.SessionId = "old" }},
		{"revision", func(_ *Service, _ *fakeSession, r *trainingv1.BuyRequest) { r.OfferRevision++ }},
		{"price", func(_ *Service, _ *fakeSession, r *trainingv1.BuyRequest) { r.ExpectedCostCopper++ }},
		{"budget", func(_ *Service, _ *fakeSession, r *trainingv1.BuyRequest) { r.MaxSpendCopper = 9 }},
		{"large_budget", func(_ *Service, _ *fakeSession, r *trainingv1.BuyRequest) { r.MaxSpendCopper = 10001 }},
		{"unknown_wallet", func(_ *Service, f *fakeSession, _ *trainingv1.BuyRequest) { f.wallet.CopperKnown = false }},
		{"poor", func(_ *Service, f *fakeSession, _ *trainingv1.BuyRequest) {
			f.wallet.Copper = 9
			f.wallet.CopperRevision++
		}},
		{"wallet_session", func(_ *Service, f *fakeSession, _ *trainingv1.BuyRequest) { f.wallet.SessionId = "other" }},
		{"expired", func(s *Service, _ *fakeSession, _ *trainingv1.BuyRequest) { s.offerUntil = s.now().Add(-time.Second) }},
		{"non_class", func(s *Service, _ *fakeSession, _ *trainingv1.BuyRequest) { s.state.Offer.TrainerType = 2 }},
		{"points", func(s *Service, _ *fakeSession, _ *trainingv1.BuyRequest) { s.state.Offer.Spells[0].PointCost[1] = 1 }},
		{"unavailable", func(s *Service, _ *fakeSession, _ *trainingv1.BuyRequest) { s.state.Offer.Spells[0].Usable = 1 }},
		{"moving", func(_ *Service, f *fakeSession, _ *trainingv1.BuyRequest) { f.actions.Moving = true }},
		{"casting", func(_ *Service, f *fakeSession, _ *trainingv1.BuyRequest) { f.actions.CastPending = true }},
		{"far", func(_ *Service, f *fakeSession, _ *trainingv1.BuyRequest) { f.world.NearbyCreatures[0].X = 5 }},
		{"changed_under_lease", func(_ *Service, f *fakeSession, _ *trainingv1.BuyRequest) {
			f.onAcquire = func() { f.world.NearbyCreatures[0].Present = false }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, f, r := setup(t)
			before := len(f.packets)
			c.change(s, f, r)
			if _, e := s.Buy(context.Background(), r); e == nil {
				t.Fatal("unsafe buy allowed")
			}
			if len(f.packets) != before {
				t.Fatal("packet sent")
			}
			if f.lease || f.acquired != f.released {
				t.Fatal("lease leaked")
			}
		})
	}
}
func TestMoneyArrivesAfterListAndFreshOfferBinding(t *testing.T) {
	s, f, r := setup(t)
	s.state.Copper = 0
	f.wallet.Copper = 10000
	f.wallet.CopperRevision++
	f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
		if q.Opcode == opcode.CMSGTrainerBuySpell {
			deliver(s, opcode.SMSGTrainerBuyFailed, append(buyPacket(trainerGUID, 2050), 0, 0, 0, 0))
		}
		return nil
	}
	out, e := s.Buy(context.Background(), r)
	if e != nil || out.LastPurchase.Status != trainingv1.PurchaseStatus_SERVER_REJECTED {
		t.Fatal(out, e)
	}
}
func TestUntrustedPacketsDoNotMutateOrWakeWrongPhase(t *testing.T) {
	s, _, _ := setup(t)
	s.mu.Lock()
	s.state.LastPurchase = &trainingv1.Purchase{NpcGuid: trainerGUID, SpellId: 2050, Status: trainingv1.PurchaseStatus_REQUESTED}
	s.wait = make(chan struct{})
	s.waitKind = "list"
	wait := s.wait
	s.mu.Unlock()
	deliver(s, opcode.SMSGTrainerBuySucceeded, buyPacket(trainerGUID, 2050))
	select {
	case <-wait:
		t.Fatal("buy ACK woke list phase")
	default:
	}
	for _, op := range []uint32{opcode.SMSGTrainerList, opcode.SMSGTrainerBuySucceeded, opcode.SMSGTrainerBuyFailed, opcode.SMSGLearnedSpell, opcode.SMSGSupercededSpell} {
		deliver(s, op, []byte{1})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.MalformedPackets != 5 || s.state.LastPurchase.Status != trainingv1.PurchaseStatus_TRAINER_SUCCEEDED {
		t.Fatal(s.state)
	}
}
func TestConcurrentOperationAndCancellationNoRetry(t *testing.T) {
	s, f, r := setup(t)
	entered := make(chan struct{})
	f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
		if q.Opcode == opcode.CMSGTrainerBuySpell {
			close(entered)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *trainingv1.State, 1)
	go func() { out, _ := s.Buy(ctx, r); done <- out }()
	<-entered
	if _, e := s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID}); status.Code(e) != codes.FailedPrecondition {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				_, _ = s.GetState(context.Background(), &emptypb.Empty{})
				deliver(s, 0xffff, []byte{1})
			}
		}()
	}
	wg.Wait()
	cancel()
	out := <-done
	if out == nil || out.LastPurchase.Status != trainingv1.PurchaseStatus_UNCONFIRMED_TIMEOUT || out.ActionInFlight || f.lease {
		t.Fatal(out)
	}
}
func TestGRPCDiscoveryAndList(t *testing.T) {
	s, _, _ := setup(t)
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	trainingv1.RegisterBuddyTrainingServer(srv, s)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	conn, e := grpc.NewClient("passthrough:///training", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	cli := trainingv1.NewBuddyTrainingClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, e := cli.Discover(ctx, &emptypb.Empty{})
	if e != nil || len(out.Nearby) != 1 || !out.Nearby[0].Interactable || !out.State.CopperKnown {
		t.Fatal(out, e)
	}
	state, e := cli.List(ctx, &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID})
	if e != nil || state.ActionInFlight || state.Offer.NpcGuid != trainerGUID {
		t.Fatal(state, e)
	}
}

func TestManifestDescriptorAndDependencies(t *testing.T) {
	root := t.TempDir()
	for _, module := range []string{"buddy-world", "buddy-actions", "buddy-trade", "buddy-training"} {
		dir := filepath.Join(root, module)
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			data, e := os.ReadFile(filepath.Join("..", module, name))
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	registry, e := moddisc.Load(root)
	if e != nil {
		t.Fatal(e)
	}
	m, ok := registry.Lookup("buddy-training")
	if !ok || !m.Requires("buddy-world") || !m.Requires("buddy-actions") || !m.Requires("buddy-trade") {
		t.Fatal("missing dependencies")
	}
	for _, alias := range []string{"discover", "state", "list", "buy"} {
		if _, ok := m.RPC(alias); !ok {
			t.Fatal("missing alias", alias)
		}
	}
}
