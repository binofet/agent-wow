package buddytraining

import (
	"context"
	"sync"
	"testing"

	trainingv1 "github.com/agent-wow/agent-wow/modules/buddy-training/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type selectedTrainerSession struct {
	*fakeSession
	mu2      sync.Mutex
	selected *worldv1.CreatureState
	requests []*worldv1.SnapshotRequest
	foreign  bool
}

func (f *selectedTrainerSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module != "buddy-world" || r.Method != "snapshot" {
		return f.fakeSession.InvokeModule(ctx, r, opts...)
	}
	q := &worldv1.SnapshotRequest{}
	if err := r.Request.UnmarshalTo(q); err != nil {
		return nil, err
	}
	f.mu2.Lock()
	defer f.mu2.Unlock()
	f.requests = append(f.requests, proto.Clone(q).(*worldv1.SnapshotRequest))
	f.fakeSession.mu.Lock()
	w := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	f.fakeSession.mu.Unlock()
	if q.CreatureGuid != 0 && f.selected != nil {
		w.RequestedCreature = proto.Clone(f.selected).(*worldv1.CreatureState)
		if f.foreign {
			w.RequestedCreature.Guid++
		}
	}
	a, e := anypb.New(w)
	return &modv1.InvokeModuleResponse{Result: a}, e
}
func TestProfessionDiscoveryIncludesObservedTrainerOutsideNearest32(t *testing.T) {
	f := fixture()
	trainer := proto.Clone(f.world.NearbyCreatures[0]).(*worldv1.CreatureState)
	trainer.NpcFlags = 0x51
	f.world.TrainerCreatures = []*worldv1.CreatureState{trainer}
	f.world.Self.X = 100 // stale server pose, client estimate remains at0.
	f.world.NearbyCreatures = nil
	for i := uint64(1); i <= 32; i++ {
		f.world.NearbyCreatures = append(f.world.NearbyCreatures, &worldv1.CreatureState{Guid: 0xf130005c86000000 | i, Entry: 23686, Present: true})
	}
	s := NewService(f, "body", 1)
	r, e := s.DiscoverProfessions(context.Background(), &emptypb.Empty{})
	if e != nil || len(r.Nearby) != 1 || !r.Nearby[0].Interactable || r.Nearby[0].Distance != 1 || r.Nearby[0].Creature.Guid != trainerGUID {
		t.Fatal(r, e)
	}
}
func TestSelectedTrainerGateRequestsExactRetainedGuidAndKeepsRangeSafety(t *testing.T) {
	for _, mode := range []string{"success", "missing", "foreign", "far", "dead", "map", "moving", "wrong_role"} {
		t.Run(mode, func(t *testing.T) {
			base := fixture()
			trainer := proto.Clone(base.world.NearbyCreatures[0]).(*worldv1.CreatureState)
			trainer.NpcFlags = 0x51
			base.world.NearbyCreatures = nil
			f := &selectedTrainerSession{fakeSession: base, selected: trainer}
			s := NewService(f, "body", 1)
			switch mode {
			case "missing":
				f.selected = nil
			case "foreign":
				f.foreign = true
			case "far":
				trainer.X = 5
			case "dead":
				trainer.Alive = false
			case "map":
				trainer.MapId = 1
			case "moving":
				trainer.MovementFlags = 1
			case "wrong_role":
				trainer.NpcFlags = 0x30
			}
			f.onSend = func(_ context.Context, q *modv1.SendPacketRequest) error {
				if q.Opcode == opcode.CMSGTrainerList {
					spec, _ := professionForSkill(185)
					deliver(s, opcode.SMSGTrainerList, professionWire(spec, 0))
				}
				return nil
			}
			r, e := s.List(context.Background(), &trainingv1.ListRequest{SessionId: "body", NpcGuid: trainerGUID, TrainerType: 2})
			if (e == nil) != (mode == "success") {
				t.Fatal(mode, r, e)
			}
			if mode != "success" && status.Code(e) != codes.FailedPrecondition {
				t.Fatal(mode, e)
			}
			if len(f.requests) != 1 || f.requests[0].CreatureGuid != trainerGUID {
				t.Fatal("selected gate omitted exact lookup", f.requests)
			}
			base.mu.Lock()
			defer base.mu.Unlock()
			if len(base.packets) != map[bool]int{true: 1, false: 0}[mode == "success"] || base.lease || base.acquired != base.released {
				t.Fatal(mode, base.packets, base.lease)
			}
		})
	}
}
func TestTrainerProjectionAndExactLookupContradictionsFailClosed(t *testing.T) {
	for _, mode := range []string{"projection", "requested", "requested_list", "cleared_flags", "oversize_trainers", "oversize_requested"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture()
			c := proto.Clone(f.world.NearbyCreatures[0]).(*worldv1.CreatureState)
			switch mode {
			case "projection":
				c.X++
				f.world.TrainerCreatures = []*worldv1.CreatureState{c}
			case "requested":
				c.Alive = false
				f.world.RequestedCreature = c
			case "requested_list":
				c.MapId = 1
				f.world.RequestedCreatures = []*worldv1.CreatureState{c}
			case "cleared_flags":
				c.NpcFlags = 0
				f.world.TrainerCreatures = []*worldv1.CreatureState{c}
			case "oversize_trainers":
				for range 65 {
					f.world.TrainerCreatures = append(f.world.TrainerCreatures, c)
				}
			case "oversize_requested":
				for range 33 {
					f.world.RequestedCreatures = append(f.world.RequestedCreatures, c)
				}
			}
			s := NewService(f, "body", 1)
			if _, e := s.Discover(context.Background(), &emptypb.Empty{}); status.Code(e) != codes.FailedPrecondition {
				t.Fatal(mode, e)
			}
			if len(f.packets) != 0 {
				t.Fatal("contradiction reached gameplay", f.packets)
			}
		})
	}
}

func TestPublishedTrainerDiscoveryPreservesConsumerBoundAndNearestClientTeacher(t *testing.T) {
	f := fixture()
	f.world.NearbyCreatures = nil
	f.actions.Position.X = 128
	for i := uint64(1); i <= 128; i++ {
		c := &worldv1.CreatureState{Guid: 0xf130000155000000 | i, Entry: 341, NpcFlags: 0x30,
			Present: true, MapKnown: true, PositionKnown: true, X: float32(i),
			HealthKnown: true, Health: 100, AliveKnown: true, Alive: true}
		if i <= 64 {
			f.world.QuestGivers = append(f.world.QuestGivers, c)
		} else {
			f.world.TrainerCreatures = append(f.world.TrainerCreatures, c)
		}
	}
	s := NewService(f, "body", 1)
	r, err := s.Discover(context.Background(), &emptypb.Empty{})
	if err != nil || len(r.Nearby) != 97 || r.Nearby[0].Creature.Guid != 0xf130000155000080 ||
		!r.Nearby[0].Interactable || r.Nearby[0].Distance != 0 {
		t.Fatal("published bound or current nearest trainer was lost", r, err)
	}
	for i := 1; i < len(r.Nearby); i++ {
		if r.Nearby[i].Distance < r.Nearby[i-1].Distance {
			t.Fatal("published discovery is not ordered by actual client distance")
		}
	}
}
