package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func vendorTravelRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachQuestRequest) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Owner = nil
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -9460, 70, 58
	const guid = uint64(0xf130000202000001)
	f.world.NearbyCreatures = []*worldv1.CreatureState{{Guid: guid, Entry: 514, Present: true, MapKnown: true, MapId: 0, PositionKnown: true, PositionRevision: 1, X: -9435, Y: 70, Z: 58, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, NpcFlags: 0x80}}
	s.SetNavigationDataDir("/fixture/client-data")
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		return []position{from, {x: from.x, y: from.y + 6, z: from.z}, {x: to.x, y: from.y + 6, z: from.z}, to}, nil
	}
	s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); return ctx.Err() }
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachQuestRequest{Token: lease.Token, NpcGuid: guid}
}

func TestVendorTravelUsesActualMapAndComplete150YardRouteWithoutSales(t *testing.T) {
	s, f, _, r := vendorTravelRig(t)
	f.world.Self.MapId = 530
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 1500, 1500, 80
	vendor := f.world.NearbyCreatures[0]
	vendor.MapId = 530
	vendor.X, vendor.Y, vendor.Z = 1650, 1500, 80
	plans := 0
	s.planMapPath = func(_ context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
		if mapID != 530 {
			t.Fatal("vendor route used another map", mapID)
		}
		plans++
		return []position{from, {x: from.x, y: from.y + 6, z: from.z}, {x: to.x, y: from.y + 6, z: to.z}, to}, nil
	}
	s.state.Following, s.followUntil = true, s.now().Add(lease)
	got, err := s.TravelVendor(context.Background(), r)
	if err != nil || plans != 1 || got.Reason != "vendor_travel_within_distance" || got.Moving || got.Traveling || got.MotionUnconfirmed || !got.Following || distance(s.pos, position{vendor.X, vendor.Y, vendor.Z, 0}) > 3 {
		t.Fatal(got, err, s.pos, plans)
	}
	for _, packet := range f.packets {
		if packet.Opcode != opcode.MSGMoveStartForward && packet.Opcode != opcode.MSGMoveHeartbeat && packet.Opcode != opcode.MSGMoveStop {
			t.Fatal("vendor movement performed a non-movement action", packet)
		}
	}
}

func TestVendorTravelRequiresTradeOwnerAndNeverAcceptsTrainerRoleAsVendor(t *testing.T) {
	s, f, _, r := vendorTravelRig(t)
	f.world.NearbyCreatures[0].NpcFlags = 0x50
	got, err := s.TravelVendor(context.Background(), r)
	if err != nil || got.Reason != "vendor_travel_observed_stationary_vendor_required" || len(f.packets) != 0 {
		t.Fatal(got, err, f.packets)
	}
	if _, err = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "trade", Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	held, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "training", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TravelVendor(context.Background(), &actionsv1.ApproachQuestRequest{Token: held.Token, NpcGuid: r.NpcGuid}); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal(err, f.packets)
	}
}

func TestVendorTravelRegistryPreservesObservedNPCRequestAndExistingAliases(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"buddy-world", "buddy-party", "buddy-actions"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			p, err := os.ReadFile(filepath.Join("..", name, file))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, file), p, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	module, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing actions")
	}
	for _, alias := range []string{"travel_vendor", "travel_trainer", "travel_quest", "travel_to", "follow", "assist", "status"} {
		if _, ok := module.RPC(alias); !ok {
			t.Fatal("lost alias", alias)
		}
	}
	method, ok := module.RPC("travel_vendor")
	if !ok || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/TravelVendor" {
		t.Fatal(method, ok)
	}
	input := method.Descriptor().Input()
	if input.FullName() != "warcraftbuddy.actions.v1.ApproachQuestRequest" || input.Fields().Len() != 2 || input.Fields().ByName("npc_guid") == nil || input.Fields().ByName("token") == nil || input.Fields().ByName("x") != nil {
		t.Fatal("vendor gained arbitrary destination fields", input)
	}
}

func TestVendorTravelUsesObservedVendorWithoutQuestFlagOrOwner(t *testing.T) {
	s, f, _, r := vendorTravelRig(t)
	s.state.Following, s.followUntil = true, s.now().Add(lease)
	got, err := s.TravelVendor(context.Background(), r)
	if err != nil || got.Reason != "vendor_travel_within_distance" || got.Moving || got.Traveling || got.MotionUnconfirmed || !got.NavAvailable || !got.Following || got.InteractionOwner != "trade" || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
	if distance(s.pos, position{x: -9435, y: 70, z: 58}) > 3 || len(f.lookups) == 0 {
		t.Fatal("not at an actually observed vendor", s.pos, f.lookups)
	}
}

func TestVendorTravelUsesVerifiedNearbyStandoffWithoutMovingObservedVendor(t *testing.T) {
	s, f, _, r := vendorTravelRig(t)
	npc := playerPosition(f.world.Self)
	npc.x, npc.y, npc.z = f.world.NearbyCreatures[0].X, f.world.NearbyCreatures[0].Y, f.world.NearbyCreatures[0].Z
	calls := 0
	s.planMapPath = func(_ context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
		calls++
		if mapID != f.world.Self.MapId || distance(to, npc) > 2.501 || to.z != npc.z {
			t.Fatal("standoff changed observed layer/map", mapID, to, npc)
		}
		if to == npc {
			return nil, errors.New("find navigation path: destination: sample is outside ground polygon")
		}
		return []position{from, {x: from.x, y: from.y + 6, z: from.z}, {x: to.x, y: from.y + 6, z: from.z}, to}, nil
	}
	got, err := s.TravelVendor(context.Background(), r)
	if err != nil || calls != 2 || got.Moving || got.Traveling || got.Reason != "vendor_travel_within_distance" || !got.NavAvailable || distance(s.pos, npc) > 3 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, calls, s.pos)
	}
	if f.world.NearbyCreatures[0].X != npc.x || f.world.NearbyCreatures[0].PositionRevision != 1 {
		t.Fatal("standoff fabricated vendor observation")
	}
}

func TestVendorStandoffStillRejectsChangedActualNPCMapSelfOrLeaseAfterPlanning(t *testing.T) {
	for _, mode := range []string{"npc", "self", "map", "lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := vendorTravelRig(t)
			npc := f.world.NearbyCreatures[0]
			center := position{x: npc.X, y: npc.Y, z: npc.Z}
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				if to == center {
					return nil, errors.New("find navigation path: destination: sample is outside ground polygon")
				}
				switch mode {
				case "npc":
					npc.X++
					npc.PositionRevision++
				case "self":
					f.world.Self.X++
					f.world.Self.PositionRevision++
				case "map":
					f.world.Self.MapId = 1
					f.world.Self.PositionRevision++
				case "lease":
					advance(31 * time.Second)
				}
				return []position{from, to}, nil
			}
			got, err := s.TravelVendor(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || got.Reason == "vendor_travel_within_distance" || len(f.packets) != 0 {
				t.Fatal(mode, got, err, f.packets)
			}
		})
	}
}

func TestVendorTravelExactLookupRetainsRealVendorOutsideNearbyRanking(t *testing.T) {
	s, f, _, r := vendorTravelRig(t)
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{r.NpcGuid: f.world.NearbyCreatures[0]}
	f.world.NearbyCreatures = nil
	got, err := s.TravelVendor(context.Background(), r)
	if err != nil || got.Reason != "vendor_travel_within_distance" {
		t.Fatal(got, err)
	}
}

func TestVendorTravelNeverFallsBackWithoutActualNAVOrVendor(t *testing.T) {
	for _, mode := range []string{"no_nav", "planner_error", "partial_nav", "bad_route", "absent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := vendorTravelRig(t)
			switch mode {
			case "no_nav":
				s.navDataDir = ""
				s.planMapPath = planMapNavigation
			case "planner_error":
				s.planPath = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("no path")
				}
			case "partial_nav":
				s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
					return nil, errors.New("find navigation path: navigation polygon path is partial, exhausted, or unavailable")
				}
			case "bad_route":
				s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
					return []position{from, {x: -9700}, to}, nil
				}
			case "absent":
				f.world.NearbyCreatures = nil
			}
			got, err := s.TravelVendor(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || got.Reason == "vendor_travel_within_distance" || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestVendorTravelRequiresSafeStationaryConsistentActualVendor(t *testing.T) {
	for _, mode := range []string{"entry", "dead", "map", "position", "health", "class_only", "moving", "spline", "combat", "invalid_position", "speed", "views"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := vendorTravelRig(t)
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
			got, err := s.TravelVendor(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestVendorTravelStopOnLostNPCExpiryCorrectionAndCancellation(t *testing.T) {
	for _, mode := range []string{"lost", "expiry", "correction", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := vendorTravelRig(t)
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
			got, err := s.TravelVendor(ctx, r)
			if mode == "cancel" && status.Code(err) != codes.Canceled || mode != "cancel" && err != nil || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestVendorTravelInvalidOrForeignLeaseNeverWrites(t *testing.T) {
	s, f, _, r := vendorTravelRig(t)
	for _, request := range []*actionsv1.ApproachQuestRequest{nil, {}, {Token: r.Token, NpcGuid: 3}, {Token: "other", NpcGuid: r.NpcGuid}} {
		if _, err := s.TravelVendor(context.Background(), request); err == nil {
			t.Fatal("accepted invalid or foreign lease", request)
		}
	}
	if len(f.packets) != 0 {
		t.Fatal(f.packets)
	}
}

func TestVendorTravelExplicitStopDuringPlanningNeverStarts(t *testing.T) {
	for _, assist := range []bool{false, true} {
		for _, planFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("assist=%t/plan_error=%t", assist, planFails), func(t *testing.T) {
				s, f, _, r := vendorTravelRig(t)
				s.planMapPath = func(ctx context.Context, _ string, _ uint32, a, b position) ([]position, error) {
					var got *actionsv1.State
					var err error
					if assist {
						got, err = s.Assist(ctx, &actionsv1.ToggleRequest{Enabled: false})
					} else {
						got, err = s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: false})
					}
					if err != nil || got.Moving {
						t.Fatal("explicit stop failed", got, err)
					}
					if planFails {
						return nil, errors.New("late planner failure")
					}
					return []position{a, b}, nil
				}
				got, err := s.TravelVendor(context.Background(), r)
				if err != nil || got.Reason != "vendor_travel_interrupted_by_controller_change" || got.Moving || got.Traveling || got.NavAvailable || len(f.packets) != 0 {
					t.Fatal(got, err, f.packets)
				}
			})
		}
	}
}

func TestVendorTravelExplicitStopBetweenStepsNeverRestarts(t *testing.T) {
	for _, assist := range []bool{false, true} {
		t.Run(fmt.Sprintf("assist=%t", assist), func(t *testing.T) {
			s, f, advance, r := vendorTravelRig(t)
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				advance(d)
				var got *actionsv1.State
				var err error
				if assist {
					got, err = s.Assist(ctx, &actionsv1.ToggleRequest{Enabled: false})
				} else {
					got, err = s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: false})
				}
				if err != nil || got.Moving {
					t.Fatal("explicit stop failed", got, err)
				}
				return ctx.Err()
			}
			got, err := s.TravelVendor(context.Background(), r)
			if err != nil || got.Reason != "vendor_travel_interrupted_by_controller_change" || got.Moving || got.Traveling || got.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestVendorTravelPlannerDoesNotOverwriteReplacementOwner(t *testing.T) {
	for _, owner := range []string{"quests", "trade"} {
		for _, planFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("owner=%s/plan_error=%t", owner, planFails), func(t *testing.T) {
				s, f, _, r := vendorTravelRig(t)
				replacement := ""
				planCalls := 0
				s.planMapPath = func(ctx context.Context, _ string, _ uint32, a, b position) ([]position, error) {
					planCalls++
					if planCalls > 1 {
						return nil, errors.New("old planner failure")
					}
					if _, err := s.ReleaseInteraction(ctx, &actionsv1.InteractionRelease{Owner: "trade", Token: r.Token}); err != nil {
						t.Fatal(err)
					}
					lease, err := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: owner, LeaseMs: 30000})
					if err != nil {
						t.Fatal(err)
					}
					replacement = lease.Token
					s.state.Reason = "replacement_reason"
					s.state.NavReason = "replacement_nav"
					s.state.NavAvailable = true
					s.state.Moving = true
					s.state.Traveling = true
					s.state.TravelNpcGuid = 778
					if planFails {
						return nil, errors.New("old planner failure")
					}
					return []position{a, b}, nil
				}
				got, err := s.TravelVendor(context.Background(), r)
				if err != nil || got.Reason != "replacement_reason" || got.NavReason != "replacement_nav" || !got.NavAvailable || !got.Moving || !got.Traveling || got.TravelNpcGuid != 778 || got.InteractionOwner != owner || s.interactionToken != replacement || len(f.packets) != 0 {
					t.Fatal(got, err, f.packets)
				}
			})
		}
	}
}

type vendorTravelSetupContext struct {
	context.Context
	once      sync.Once
	interrupt func()
}

func (c *vendorTravelSetupContext) Deadline() (time.Time, bool) {
	c.once.Do(c.interrupt)
	return c.Context.Deadline()
}

func TestVendorTravelReservationHonorsStopAndReplacement(t *testing.T) {
	for _, mode := range []string{"follow", "assist", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := vendorTravelRig(t)
			replacement := ""
			ctx := &vendorTravelSetupContext{Context: context.Background(), interrupt: func() {
				if mode == "replacement" {
					if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "trade", Token: r.Token}); err != nil {
						t.Fatal(err)
					}
					lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: 30000})
					if err != nil {
						t.Fatal(err)
					}
					replacement = lease.Token
					s.state.Reason = "replacement_reason"
					s.state.NavReason = "replacement_nav"
					s.state.NavAvailable = true
					s.state.Moving = true
					s.state.Traveling = true
					s.state.TravelNpcGuid = 779
				} else if mode == "assist" {
					if _, err := s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
						t.Fatal(err)
					}
				}
			}}
			s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				t.Fatal("interrupted reservation reached planner")
				return nil, nil
			}
			got, err := s.TravelVendor(ctx, r)
			if err != nil || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
			if mode == "replacement" {
				if got.Reason != "replacement_reason" || got.NavReason != "replacement_nav" || !got.NavAvailable || !got.Moving || !got.Traveling || got.TravelNpcGuid != 779 || s.interactionToken != replacement {
					t.Fatal(got)
				}
			} else if got.Moving || got.Traveling || got.Reason != "vendor_travel_interrupted_by_controller_change" {
				t.Fatal(got)
			}
		})
	}
}

func TestVendorTravelCanceledPlannerAndDelayKeepCancellationAndCleanup(t *testing.T) {
	for _, planning := range []bool{false, true} {
		t.Run(fmt.Sprintf("planning=%t", planning), func(t *testing.T) {
			s, f, advance, r := vendorTravelRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if planning {
				s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
					cancel()
					return nil, context.Canceled
				}
			} else {
				s.approachDelay = func(call context.Context, d time.Duration) error { advance(d); cancel(); return call.Err() }
			}
			got, err := s.TravelVendor(ctx, r)
			want := 0
			if !planning {
				want = 1
			}
			if status.Code(err) != codes.Canceled || got.Reason != "vendor_travel_canceled_or_time_limit" || got.Moving || got.Traveling || got.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != want || countPackets(f, opcode.MSGMoveStop) != want {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestVendorTravelIdleStopBetweenStepsDoesNotRestart(t *testing.T) {
	for _, assist := range []bool{false, true} {
		t.Run(fmt.Sprintf("assist=%t", assist), func(t *testing.T) {
			s, f, advance, r := vendorTravelRig(t)
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				advance(d)
				// Model a frame that is already stopped before the explicit disable.
				// The subsequent stop writes no packet and must still revoke travel.
				s.mu.Lock()
				stopped := s.stopLocked(ctx)
				s.mu.Unlock()
				if !stopped {
					t.Fatal("initial frame stop failed")
				}
				var got *actionsv1.State
				var err error
				if assist {
					got, err = s.Assist(ctx, &actionsv1.ToggleRequest{Enabled: false})
				} else {
					got, err = s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: false})
				}
				if err != nil || got.Moving {
					t.Fatal(got, err)
				}
				return ctx.Err()
			}
			got, err := s.TravelVendor(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || got.Reason != "vendor_travel_interrupted_by_controller_change" || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestVendorTravelCancellationAfterReplacementPreservesNewController(t *testing.T) {
	s, f, advance, r := vendorTravelRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replacement := ""
	s.approachDelay = func(call context.Context, d time.Duration) error {
		advance(d)
		if _, err := s.ReleaseInteraction(call, &actionsv1.InteractionRelease{Owner: "trade", Token: r.Token}); err != nil {
			t.Fatal(err)
		}
		lease, err := s.AcquireInteraction(call, &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 30000})
		if err != nil {
			t.Fatal(err)
		}
		replacement = lease.Token
		s.state.Moving = true
		s.state.Traveling = true
		s.state.TravelNpcGuid = 780
		s.state.Reason = "replacement_reason"
		s.state.NavReason = "replacement_nav"
		s.state.NavAvailable = true
		cancel()
		return call.Err()
	}
	got, err := s.TravelVendor(ctx, r)
	if status.Code(err) != codes.Canceled || got.Reason != "replacement_reason" || got.NavReason != "replacement_nav" || !got.NavAvailable || !got.Moving || !got.Traveling || got.TravelNpcGuid != 780 || s.interactionToken != replacement || s.questApproaching || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
}

func TestVendorTravelReleasedOrExpiredIdleReservationReturnsIdle(t *testing.T) {
	for _, duringPlan := range []bool{false, true} {
		for _, expire := range []bool{false, true} {
			t.Run(fmt.Sprintf("planning=%t/expiry=%t", duringPlan, expire), func(t *testing.T) {
				s, f, advance, r := vendorTravelRig(t)
				interrupt := func() {
					if expire {
						advance(31 * time.Second)
						s.Tick(context.Background())
					} else {
						if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "trade", Token: r.Token}); err != nil {
							t.Fatal(err)
						}
					}
				}
				var ctx context.Context = context.Background()
				if duringPlan {
					interrupted := false
					s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
						if !interrupted {
							interrupted = true
							interrupt()
						}
						return nil, errors.New("old planner stopped")
					}
				} else {
					ctx = &vendorTravelSetupContext{Context: ctx, interrupt: interrupt}
					s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
						t.Fatal("revoked reservation reached planner")
						return nil, nil
					}
				}
				got, err := s.TravelVendor(ctx, r)
				if err != nil || got.Traveling || got.Moving || s.questApproaching || len(f.packets) != 0 {
					t.Fatal(got, err, f.packets)
				}
				s.Tick(context.Background())
				after := state(t, s)
				if after.InteractionOwner != "" || after.Traveling || after.Moving {
					t.Fatal("released or expired operation did not become idle", after)
				}
			})
		}
	}
}

func TestVendorTravelCanceledInitialReservationReturnsCanceled(t *testing.T) {
	s, f, _, r := vendorTravelRig(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &vendorTravelSetupContext{Context: parent, interrupt: cancel}
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		t.Fatal("canceled reservation reached planner")
		return nil, nil
	}
	got, err := s.TravelVendor(ctx, r)
	if status.Code(err) != codes.Canceled || got.Reason != "vendor_travel_canceled_or_time_limit" || got.Moving || got.Traveling || s.questApproaching || len(f.packets) != 0 {
		t.Fatal(got, err, f.packets)
	}
}
