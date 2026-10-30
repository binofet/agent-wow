package buddyactions

import (
	"context"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func trainerTravelRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachQuestRequest) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Owner = nil
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -9460, 70, 58
	const guid = uint64(0xf130000202000001)
	f.world.NearbyCreatures = []*worldv1.CreatureState{{Guid: guid, Entry: 514, Present: true, MapKnown: true, MapId: 0, PositionKnown: true, PositionRevision: 1, X: -9435, Y: 70, Z: 58, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, NpcFlags: 0x50}}
	s.SetNavigationDataDir("/fixture/client-data")
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		return []position{from, {x: from.x, y: from.y + 6, z: from.z}, {x: to.x, y: from.y + 6, z: from.z}, to}, nil
	}
	s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); return ctx.Err() }
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "training", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachQuestRequest{Token: lease.Token, NpcGuid: guid}
}

func TestTrainerTravelUsesObservedProfessionTrainerWithoutQuestFlagOrOwner(t *testing.T) {
	s, f, _, r := trainerTravelRig(t)
	s.state.Following, s.followUntil = true, s.now().Add(lease)
	got, err := s.TravelTrainer(context.Background(), r)
	if err != nil || got.Reason != "trainer_travel_within_distance" || got.Moving || got.Traveling || got.MotionUnconfirmed || !got.NavAvailable || !got.Following || got.InteractionOwner != "training" || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
	if distance(s.pos, position{x: -9435, y: 70, z: 58}) > 3 || len(f.lookups) == 0 {
		t.Fatal("not at an actually observed trainer", s.pos, f.lookups)
	}
}

func TestTrainerTravelExactLookupRetainsRealTrainerOutsideNearbyRanking(t *testing.T) {
	s, f, _, r := trainerTravelRig(t)
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{r.NpcGuid: f.world.NearbyCreatures[0]}
	f.world.NearbyCreatures = nil
	got, err := s.TravelTrainer(context.Background(), r)
	if err != nil || got.Reason != "trainer_travel_within_distance" {
		t.Fatal(got, err)
	}
}

func TestTrainerTravelNeverFallsBackWithoutActualNAVOrTrainer(t *testing.T) {
	for _, mode := range []string{"no_nav", "planner_error", "bad_route", "absent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := trainerTravelRig(t)
			switch mode {
			case "no_nav":
				s.navDataDir = ""
				s.planMapPath = planMapNavigation
			case "planner_error":
				s.planPath = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("no path")
				}
			case "bad_route":
				s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
					return []position{from, {x: -9700}, to}, nil
				}
			case "absent":
				f.world.NearbyCreatures = nil
			}
			got, err := s.TravelTrainer(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || got.Reason == "trainer_travel_within_distance" || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestTrainerTravelRequiresSafeStationaryConsistentActualTeacher(t *testing.T) {
	for _, mode := range []string{"entry", "dead", "map", "position", "health", "class_only", "moving", "spline", "combat", "invalid_position", "speed", "views"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := trainerTravelRig(t)
			c := f.world.NearbyCreatures[0]
			switch mode {
			case "entry":
				c.Entry++
			case "dead":
				c.Alive = false
			case "map":
				c.MapId = 1
			case "position":
				c.PositionKnown = false
			case "health":
				c.HealthKnown = false
			case "class_only":
				c.NpcFlags = 0x30
			case "moving":
				c.MovementFlags = 1
			case "spline":
				c.Trajectory = &worldv1.CreatureTrajectory{Known: true}
			case "combat":
				c.UnitFlags = 0x80000
			case "invalid_position":
				c.X = 40001
			case "speed":
				f.world.Self.RunSpeed = 14
			case "views":
				other := proto.Clone(c).(*worldv1.CreatureState)
				other.X++
				f.world.QuestGivers = []*worldv1.CreatureState{other}
			}
			got, err := s.TravelTrainer(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestTrainerTravelStopOnLostNPCExpiryCorrectionAndCancellation(t *testing.T) {
	for _, mode := range []string{"lost", "expiry", "correction", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := trainerTravelRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.approachDelay = func(call context.Context, d time.Duration) error {
				advance(d)
				switch mode {
				case "lost":
					f.world.NearbyCreatures = nil
				case "expiry":
					advance(29 * time.Second)
				case "correction":
					f.world.Self.X++
					f.world.Self.PositionRevision++
				case "cancel":
					cancel()
				}
				return call.Err()
			}
			got, err := s.TravelTrainer(ctx, r)
			if mode == "cancel" && status.Code(err) != codes.Canceled || mode != "cancel" && err != nil || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestTrainerTravelInvalidOrForeignLeaseNeverWrites(t *testing.T) {
	s, f, _, r := trainerTravelRig(t)
	for _, request := range []*actionsv1.ApproachQuestRequest{nil, {}, {Token: r.Token, NpcGuid: 3}, {Token: "other", NpcGuid: r.NpcGuid}} {
		if _, err := s.TravelTrainer(context.Background(), request); err == nil {
			t.Fatal("accepted invalid or foreign lease", request)
		}
	}
	if len(f.packets) != 0 {
		t.Fatal(f.packets)
	}
}

func TestProfessionsInteractionOwnerIsExclusiveAndRetainsDefaultLease(t *testing.T) {
	s, _, _ := rig(t)
	got, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions"})
	if err != nil || got.State.InteractionOwner != "professions" || got.State.InteractionLeaseRemainingMs != 15000 {
		t.Fatal(got, err)
	}
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "training"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "professions", Token: got.Token}); err != nil {
		t.Fatal(err)
	}
}
