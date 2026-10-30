package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func explorationRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.TravelToRequest) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Owner = nil
	f.world.Self.MapId = 1
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 1500, 1500, 80
	s.SetNavigationDataDir("/fixture/client-data")
	s.planMapPath = func(_ context.Context, _ string, mapID uint32, a, b position) ([]position, error) {
		if mapID != f.world.Self.MapId {
			t.Fatal("wrong actual map", mapID)
		}
		return []position{a, {x: a.x, y: a.y + 6, z: a.z}, {x: b.x, y: a.y + 6, z: a.z}, b}, nil
	}
	s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); return ctx.Err() }
	held, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.TravelToRequest{Token: held.Token, MapId: 1, X: 1515, Y: 1500, Z: 80}
}

func TestExplorationTravelUsesOnlyVerifiedGroundRouteWithoutNPCProof(t *testing.T) {
	for _, mapID := range []uint32{0, 1, 33, 530, 571} {
		s, f, _, r := explorationRig(t)
		f.world.Self.MapId, r.MapId = mapID, mapID
		s.state.Following, s.followUntil = true, s.now().Add(lease)
		got, err := s.TravelTo(context.Background(), r)
		if err != nil || got.Moving || got.Traveling || got.MotionUnconfirmed || got.Reason != "exploration_within_distance" || !got.NavAvailable || !got.Following || got.InteractionOwner != "exploration" || got.TravelNpcGuid != 0 {
			t.Fatal(mapID, got, err)
		}
		if distance(s.pos, position{x: r.X, y: r.Y, z: r.Z}) > 3 || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
			t.Fatal("route did not stop near requested destination", s.pos, f.packets)
		}
		if len(f.lookups) != 0 || len(f.world.QuestGivers) != 0 || len(f.world.NearbyCreatures) != 0 || f.world.RequestedCreature != nil {
			t.Fatal("movement invented/queried an NPC")
		}
		previous := playerPosition(f.world.Self)
		for _, packet := range f.packets {
			if packet.Opcode != opcode.MSGMoveStartForward && packet.Opcode != opcode.MSGMoveHeartbeat && packet.Opcode != opcode.MSGMoveStop {
				t.Fatal("non-movement authority", packet)
			}
			_, n, err := readGUID(packet.Payload)
			if err != nil || len(packet.Payload[n:]) != 30 {
				t.Fatal(packet)
			}
			data := packet.Payload[n:]
			p := position{x: math.Float32frombits(binary.LittleEndian.Uint32(data[10:])), y: math.Float32frombits(binary.LittleEndian.Uint32(data[14:])), z: math.Float32frombits(binary.LittleEndian.Uint32(data[18:]))}
			if distance(previous, p) > 1.401 {
				t.Fatal("unbounded movement", previous, p)
			}
			if p.x > 1500 && p.x < 1515 && p.y != 1506 {
				t.Fatal("cut verified corner", p)
			}
			previous = p
		}
	}
}

func TestExplorationOptionalArrivalDistancePreservesDefaultAndSupportsPrecision(t *testing.T) {
	for _, radius := range []float32{0, 3, .75, .5} {
		t.Run(fmt.Sprint(radius), func(t *testing.T) {
			s, f, _, r := explorationRig(t)
			r.ArrivalDistance = radius
			got, err := s.TravelTo(context.Background(), r)
			allowed := float64(radius)
			if allowed == 0 {
				allowed = 3
			}
			actual := distance(s.pos, position{x: r.X, y: r.Y, z: r.Z})
			if err != nil || got.Reason != "exploration_within_distance" || got.Moving || got.Traveling || actual > allowed || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(radius, got, err, s.pos, actual)
			}
			if allowed == 3 && actual <= .75 {
				t.Fatal("default unexpectedly changed its3yd stop", actual)
			}
		})
	}
}

func TestExplorationPreciseArrivalDoesNotTakeDefaultThreeYardShortcut(t *testing.T) {
	for _, radius := range []float32{0, .75} {
		s, f, _, r := explorationRig(t)
		r.X, r.ArrivalDistance = 1502, radius
		plans := 0
		s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
			plans++
			// A caller changing its request cannot enlarge the captured stop.
			r.ArrivalDistance = 3
			return []position{from, {x: from.x, y: from.y + 6, z: from.z}, {x: to.x, y: from.y + 6, z: to.z}, to}, nil
		}
		got, err := s.TravelTo(context.Background(), r)
		if err != nil || got.Reason != "exploration_within_distance" {
			t.Fatal(got, err)
		}
		if radius == 0 {
			if plans != 0 || len(f.packets) != 0 {
				t.Fatal("default already-near behavior changed", plans, f.packets)
			}
		} else if plans != 1 || countPackets(f, opcode.MSGMoveStartForward) != 1 || distance(s.pos, position{x: 1502, y: 1500, z: 80}) > .75 {
			t.Fatal("precise arrival skipped complete route or used mutated radius", plans, s.pos, f.packets)
		}
	}
}

func TestExplorationRejectsInvalidArrivalRadiusBeforePlanningOrMovement(t *testing.T) {
	for _, radius := range []float32{-.1, .499, 3.001, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		s, f, _, r := explorationRig(t)
		r.ArrivalDistance = radius
		s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
			t.Fatal("invalid radius reached planner")
			return nil, nil
		}
		if got, err := s.TravelTo(context.Background(), r); status.Code(err) != codes.InvalidArgument || got != nil || len(f.packets) != 0 {
			t.Fatal(radius, got, err, f.packets)
		}
	}
}

func TestExplorationPreciseArrivalRejectsNormalizedEndpointOutsideRadiusBeforeWrite(t *testing.T) {
	for _, offset := range []position{{x: 1}, {z: 1}, {x: .75}, {z: .5}} {
		s, f, _, r := explorationRig(t)
		r.ArrivalDistance = .75
		s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
			to.x += offset.x
			to.z += offset.z
			return []position{from, to}, nil
		}
		got, err := s.TravelTo(context.Background(), r)
		if err != nil || got.Moving || got.Traveling {
			t.Fatal(offset, got, err)
		}
		if distance(position{}, offset) > .75 {
			if got.NavAvailable || got.Reason != "exploration_navigation_endpoint_outside_arrival_distance" || len(f.packets) != 0 {
				t.Fatal("normalized endpoint bypassed precision", offset, got, f.packets)
			}
		} else if !got.NavAvailable || got.Reason != "exploration_within_distance" || distance(s.pos, position{x: r.X, y: r.Y, z: r.Z}) > .75 {
			t.Fatal("valid normalized ground endpoint rejected", offset, got, s.pos)
		}
	}
}

func TestExplorationPreciseArrivalRetainsCancelAndOwnedStopCleanup(t *testing.T) {
	s, f, advance, r := explorationRig(t)
	r.ArrivalDistance = .75
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.approachDelay = func(call context.Context, d time.Duration) error { advance(d); cancel(); return call.Err() }
	got, err := s.TravelTo(ctx, r)
	if status.Code(err) != codes.Canceled || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
		t.Fatal(got, err, f.packets)
	}
}

func TestExplorationRejectsInvalidDestinationLeaseAndMapBeforePlanning(t *testing.T) {
	for _, mode := range []string{"nil", "empty_token", "foreign_token", "wrong_owner", "different_map", "unknown_map", "nonfinite", "infinite", "world_bounds", "too_far", "unknown_position", "dead", "combat", "falling", "speed", "casting", "unconfirmed_motion"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := explorationRig(t)
			switch mode {
			case "nil":
				r = nil
			case "empty_token":
				r.Token = ""
			case "foreign_token":
				r.Token = "foreign"
			case "wrong_owner":
				s.state.InteractionOwner = "quests"
			case "different_map":
				r.MapId = 530
			case "unknown_map":
				f.world.Self.MapKnown = false
			case "nonfinite":
				r.X = float32(math.NaN())
			case "infinite":
				r.Y = float32(math.Inf(1))
			case "world_bounds":
				r.Z = 40001
			case "too_far":
				r.X = 1700.1
			case "unknown_position":
				f.world.Self.PositionKnown = false
			case "dead":
				f.world.Self.Alive = false
			case "combat":
				f.world.Self.UnitFlags = 0x80000
			case "falling":
				f.world.Self.MovementFlags = 0x1000
			case "speed":
				f.world.Self.RunSpeed = 14
			case "casting":
				s.state.CastPending = true
			case "unconfirmed_motion":
				s.state.MotionUnconfirmed = true
			}
			s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				t.Fatal("unsafe destination reached planner")
				return nil, nil
			}
			got, _ := s.TravelTo(context.Background(), r)
			if len(f.packets) != 0 || got != nil && (got.Moving || got.Traveling || got.TravelNpcGuid != 0) {
				t.Fatal(got, f.packets)
			}
		})
	}
}

func TestExplorationMissingMalformedAndOverBudgetNavigationNeverFallsBack(t *testing.T) {
	for _, mode := range []string{"assets", "error", "partial", "points", "detour", "nonfinite"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := explorationRig(t)
			s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				switch mode {
				case "error":
					return nil, errors.New("missing navigation tile")
				case "partial":
					b.x -= 4
					return []position{a, b}, nil
				case "points":
					p := make([]position, 257)
					for i := range p {
						p[i] = a
					}
					p[256] = b
					return p, nil
				case "detour":
					return []position{a, {x: a.x + 130, y: a.y + 100, z: a.z}, b}, nil
				default:
					return []position{a, {x: float32(math.NaN())}, b}, nil
				}
			}
			if mode == "assets" {
				s.navDataDir = ""
				s.planMapPath = planMapNavigation
			}
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || got.NavAvailable || got.Reason != "exploration_navigation_unavailable" || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestExplorationRevalidatesMapAndRevisionAfterPlanningAndClock(t *testing.T) {
	for _, mode := range []string{"planning_map", "planning_revision", "clock_map", "clock_revision", "transfer_pending"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := explorationRig(t)
			change := func() {
				switch mode {
				case "planning_map", "clock_map":
					f.world.Self.MapId = 530
					f.world.Self.X = 2000
				case "transfer_pending":
					f.world.Self.Present = false
					f.world.Self.MapKnown = false
					f.world.Self.PositionKnown = false
				default:
					f.world.Self.X++
				}
				f.world.Self.PositionRevision++
			}
			if mode == "planning_map" || mode == "planning_revision" {
				s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
					change()
					return []position{a, b}, nil
				}
			} else {
				f.onClock = func() { f.onClock = nil; change() }
			}
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || len(f.packets) != 0 || got.Reason == "exploration_within_distance" {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestExplorationStopsOnCancelExpiryCorrectionAndCombat(t *testing.T) {
	for _, mode := range []string{"cancel", "expiry", "correction", "combat", "map"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.approachDelay = func(call context.Context, d time.Duration) error {
				advance(d)
				switch mode {
				case "cancel":
					cancel()
				case "expiry":
					advance(29 * time.Second)
				case "correction":
					f.world.Self.X += 40
					f.world.Self.PositionRevision++
				case "combat":
					f.world.Self.UnitFlags = 0x80000
				case "map":
					f.world.Self.MapId = 571
					f.world.Self.X = 2000
					f.world.Self.PositionRevision++
				}
				return call.Err()
			}
			got, err := s.TravelTo(ctx, r)
			if mode == "cancel" && status.Code(err) != codes.Canceled || mode != "cancel" && err != nil || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
			if mode == "map" && (s.posMap != 571 || s.pos.x != 2000) {
				t.Fatal("STOP did not rebase transfer", s.pos, s.posMap)
			}
		})
	}
}

func TestExplorationCleanupNeverStopsReplacementLeaseAndRejectsConcurrentTravel(t *testing.T) {
	s, f, advance, r := explorationRig(t)
	s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
		if _, err := s.TravelTo(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("concurrent path accepted", err)
		}
		return []position{a, b}, nil
	}
	replacement := ""
	s.approachDelay = func(call context.Context, d time.Duration) error {
		advance(d)
		if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
			t.Fatal(err)
		}
		lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 30000})
		if err != nil {
			t.Fatal(err)
		}
		replacement = lease.Token
		// A different owned controller's new movement is outside old cleanup authority.
		s.state.Moving = true
		s.state.Traveling = false
		s.state.TravelNpcGuid = 777
		return nil
	}
	got, err := s.TravelTo(context.Background(), r)
	if err != nil || got.Traveling || got.TravelNpcGuid != 777 || !got.Moving || got.InteractionOwner != "quests" || s.interactionToken != replacement || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
}

func TestExplorationRegistryAliasAndGeneratedMovementOnlyRequest(t *testing.T) {
	root := t.TempDir()
	for _, module := range []string{"buddy-world", "buddy-party", "buddy-actions"} {
		dir := filepath.Join(root, module)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			data, err := os.ReadFile(filepath.Join("..", module, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing actions")
	}
	method, ok := actions.RPC("travel_to")
	if !ok || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/TravelTo" {
		t.Fatal(method, ok)
	}
	request := method.Descriptor().Input()
	if request.FullName() != "warcraftbuddy.actions.v1.TravelToRequest" || request.Fields().Len() != 6 || request.Fields().ByName("npc_guid") != nil {
		t.Fatal("wrong movement contract", request)
	}
	for _, name := range []string{"token", "map_id", "x", "y", "z", "arrival_distance"} {
		if request.Fields().ByName(protoreflect.Name(name)) == nil {
			t.Fatal("missing field", name)
		}
	}
}

func TestExplorationLeaseExpiringDuringClockNeverWritesFurtherTravel(t *testing.T) {
	for _, afterStart := range []bool{false, true} {
		s, f, advance, r := explorationRig(t)
		expire := func() { f.onClock = nil; advance(31 * time.Second) }
		if afterStart {
			s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); f.onClock = expire; return ctx.Err() }
		} else {
			f.onClock = expire
		}
		got, err := s.TravelTo(context.Background(), r)
		want := 0
		if afterStart {
			want = 1
		}
		if err != nil || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStartForward) != want || countPackets(f, opcode.MSGMoveStop) != want || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || got.Reason == "exploration_within_distance" {
			t.Fatal(afterStart, got, err, f.packets)
		}
	}
}

func TestExplorationExplicitStopDuringPlanningNeverStarts(t *testing.T) {
	for _, assist := range []bool{false, true} {
		for _, planFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("assist=%t/plan_error=%t", assist, planFails), func(t *testing.T) {
				s, f, _, r := explorationRig(t)
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
				got, err := s.TravelTo(context.Background(), r)
				if err != nil || got.Reason != "exploration_interrupted_by_controller_change" || got.Moving || got.Traveling || got.NavAvailable || len(f.packets) != 0 {
					t.Fatal(got, err, f.packets)
				}
			})
		}
	}
}

func TestExplorationExplicitStopBetweenStepsNeverRestarts(t *testing.T) {
	for _, assist := range []bool{false, true} {
		t.Run(fmt.Sprintf("assist=%t", assist), func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
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
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got.Reason != "exploration_interrupted_by_controller_change" || got.Moving || got.Traveling || got.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestExplorationPlannerDoesNotOverwriteReplacementOwner(t *testing.T) {
	for _, owner := range []string{"quests", "exploration"} {
		for _, planFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("owner=%s/plan_error=%t", owner, planFails), func(t *testing.T) {
				s, f, _, r := explorationRig(t)
				replacement := ""
				s.planMapPath = func(ctx context.Context, _ string, _ uint32, a, b position) ([]position, error) {
					if _, err := s.ReleaseInteraction(ctx, &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
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
				got, err := s.TravelTo(context.Background(), r)
				if err != nil || got.Reason != "replacement_reason" || got.NavReason != "replacement_nav" || !got.NavAvailable || !got.Moving || !got.Traveling || got.TravelNpcGuid != 778 || got.InteractionOwner != owner || s.interactionToken != replacement || len(f.packets) != 0 {
					t.Fatal(got, err, f.packets)
				}
			})
		}
	}
}

type explorationSetupContext struct {
	context.Context
	once      sync.Once
	interrupt func()
}

func (c *explorationSetupContext) Deadline() (time.Time, bool) {
	c.once.Do(c.interrupt)
	return c.Context.Deadline()
}

func TestExplorationReservationHonorsStopAndReplacement(t *testing.T) {
	for _, mode := range []string{"follow", "assist", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := explorationRig(t)
			replacement := ""
			ctx := &explorationSetupContext{Context: context.Background(), interrupt: func() {
				if mode == "replacement" {
					if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
						t.Fatal(err)
					}
					lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
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
			got, err := s.TravelTo(ctx, r)
			if err != nil || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
			if mode == "replacement" {
				if got.Reason != "replacement_reason" || got.NavReason != "replacement_nav" || !got.NavAvailable || !got.Moving || !got.Traveling || got.TravelNpcGuid != 779 || s.interactionToken != replacement {
					t.Fatal(got)
				}
			} else if got.Moving || got.Traveling || got.Reason != "exploration_interrupted_by_controller_change" {
				t.Fatal(got)
			}
		})
	}
}

func TestExplorationCanceledPlannerAndDelayKeepCancellationAndCleanup(t *testing.T) {
	for _, planning := range []bool{false, true} {
		t.Run(fmt.Sprintf("planning=%t", planning), func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
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
			got, err := s.TravelTo(ctx, r)
			want := 0
			if !planning {
				want = 1
			}
			if status.Code(err) != codes.Canceled || got.Reason != "exploration_canceled_or_time_limit" || got.Moving || got.Traveling || got.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != want || countPackets(f, opcode.MSGMoveStop) != want {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestExplorationIdleStopBetweenStepsDoesNotRestart(t *testing.T) {
	for _, assist := range []bool{false, true} {
		t.Run(fmt.Sprintf("assist=%t", assist), func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
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
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || got.Reason != "exploration_interrupted_by_controller_change" || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestExplorationCancellationAfterReplacementPreservesNewController(t *testing.T) {
	s, f, advance, r := explorationRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replacement := ""
	s.approachDelay = func(call context.Context, d time.Duration) error {
		advance(d)
		if _, err := s.ReleaseInteraction(call, &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
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
	got, err := s.TravelTo(ctx, r)
	if status.Code(err) != codes.Canceled || got.Reason != "replacement_reason" || got.NavReason != "replacement_nav" || !got.NavAvailable || !got.Moving || !got.Traveling || got.TravelNpcGuid != 780 || s.interactionToken != replacement || s.questApproaching || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
}

func TestExplorationReleasedOrExpiredIdleReservationReturnsIdle(t *testing.T) {
	for _, duringPlan := range []bool{false, true} {
		for _, expire := range []bool{false, true} {
			t.Run(fmt.Sprintf("planning=%t/expiry=%t", duringPlan, expire), func(t *testing.T) {
				s, f, advance, r := explorationRig(t)
				interrupt := func() {
					if expire {
						advance(31 * time.Second)
						s.Tick(context.Background())
					} else {
						if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
							t.Fatal(err)
						}
					}
				}
				var ctx context.Context = context.Background()
				if duringPlan {
					s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
						interrupt()
						return nil, errors.New("old planner stopped")
					}
				} else {
					ctx = &explorationSetupContext{Context: ctx, interrupt: interrupt}
					s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
						t.Fatal("revoked reservation reached planner")
						return nil, nil
					}
				}
				got, err := s.TravelTo(ctx, r)
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

func TestExplorationCanceledInitialReservationReturnsCanceled(t *testing.T) {
	s, f, _, r := explorationRig(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &explorationSetupContext{Context: parent, interrupt: cancel}
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		t.Fatal("canceled reservation reached planner")
		return nil, nil
	}
	got, err := s.TravelTo(ctx, r)
	if status.Code(err) != codes.Canceled || got.Reason != "exploration_canceled_or_time_limit" || got.Moving || got.Traveling || s.questApproaching || len(f.packets) != 0 {
		t.Fatal(got, err, f.packets)
	}
}
