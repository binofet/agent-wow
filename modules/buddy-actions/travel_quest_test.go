package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func travelRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachQuestRequest) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8933, -136, 81
	f.world.Owner = nil
	const giverGUID = uint64(0xf130000337000001)
	f.world.QuestGivers = []*worldv1.CreatureState{{Guid: giverGUID, Entry: 823, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: -8913, Y: -136, Z: 81, HealthKnown: true, Health: 100, AliveKnown: true, Alive: true, NpcFlags: 2}}
	s.SetNavigationDataDir("/fixture/client-data")
	// Synthetic navigation fixture deliberately bends around an obstacle. The
	// production planner never substitutes these test coordinates for a mesh.
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		return []position{from, {x: from.x, y: from.y + 6, z: from.z}, {x: to.x, y: from.y + 6, z: from.z}, to}, nil
	}
	s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); return ctx.Err() }
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachQuestRequest{Token: lease.Token, NpcGuid: giverGUID}
}

func TestQuestTravelKnownLandmarksDoNotFabricateObservedNPCs(t *testing.T) {
	for _, entry := range []uint64{196, 197, 823} {
		t.Run(strconv.FormatUint(entry, 10), func(t *testing.T) {
			s, f, _, r := travelRig(t)
			r.NpcGuid = 0xf130000000000001 | entry<<24
			dest, _, ok := northshireQuestLandmark(r.NpcGuid)
			if !ok {
				t.Fatal("missing fixed landmark")
			}
			f.world.QuestGivers = nil
			f.world.Self.X, f.world.Self.Y, f.world.Self.Z = dest.x+10, dest.y, dest.z
			f.world.Self.PositionRevision++
			var planned position
			s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
				planned = to
				return []position{from, to}, nil
			}
			got, err := s.TravelQuest(context.Background(), r)
			if err != nil || got.Reason != "quest_travel_landmark_reached_requires_observation" || got.Moving || got.Traveling || !got.NavAvailable || planned != dest || len(f.world.QuestGivers) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(got, err, planned, dest, f.world.QuestGivers)
			}
		})
	}
}

func TestQuestTravelObservedMillyUsesActualPositionWithoutOwner(t *testing.T) {
	s, f, _, r := travelRig(t)
	const millyGUID = uint64(0xf130002450000123)
	r.NpcGuid = millyGUID
	c := f.world.QuestGivers[0]
	c.Guid, c.Entry = millyGUID, 9296
	actual := position{x: c.X, y: c.Y, z: c.Z, o: c.Orientation}
	planned := false
	oldPlanner := s.planPath
	s.planPath = func(ctx context.Context, root string, from, to position) ([]position, error) {
		planned = true
		if to != actual {
			t.Fatal("generic giver did not use its actual observation", to, actual)
		}
		return oldPlanner(ctx, root, from, to)
	}
	got, err := s.TravelQuest(context.Background(), r)
	if err != nil || !planned || got.Reason != "quest_travel_within_distance" || got.Moving || got.Traveling || got.MotionUnconfirmed || !got.NavAvailable || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, planned)
	}
}

func TestQuestTravelObservedGiverCanUseGroundStandoffButLandmarkCannot(t *testing.T) {
	for _, landmark := range []bool{false, true} {
		s, f, _, r := travelRig(t)
		center := position{x: f.world.QuestGivers[0].X, y: f.world.QuestGivers[0].Y, z: f.world.QuestGivers[0].Z}
		if landmark {
			f.world.QuestGivers = nil
			center, _, _ = northshireQuestLandmark(r.NpcGuid)
		}
		calls := 0
		s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
			calls++
			if to == center {
				return nil, errors.New("find navigation path: destination: sample is outside ground polygon")
			}
			return []position{from, to}, nil
		}
		got, err := s.TravelQuest(context.Background(), r)
		if err != nil || got.Moving || got.Traveling {
			t.Fatal(got, err)
		}
		if landmark {
			if calls != 1 || got.NavAvailable || len(f.packets) != 0 || got.Reason != "quest_travel_navigation_unavailable" {
				t.Fatal("unobserved landmark gained interaction fallback", got, calls)
			}
		} else if calls != 2 || !got.NavAvailable || got.Reason != "quest_travel_within_distance" || distance(s.pos, center) > 3 || countPackets(f, opcode.MSGMoveStop) != 1 {
			t.Fatal(got, calls, s.pos)
		}
	}
}

func TestQuestStandoffRevalidatesActualGiverBeforeFirstMovementWrite(t *testing.T) {
	s, f, _, r := travelRig(t)
	npc := f.world.QuestGivers[0]
	center := position{x: npc.X, y: npc.Y, z: npc.Z}
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if to == center {
			return nil, errors.New("find navigation path: destination: sample is outside ground polygon")
		}
		npc.PositionRevision++
		return []position{from, to}, nil
	}
	got, err := s.TravelQuest(context.Background(), r)
	if err != nil || got.Moving || got.Traveling || got.Reason != "quest_travel_giver_position_changed" || len(f.packets) != 0 {
		t.Fatal(got, err, f.packets)
	}
}

func TestQuestTravelUnobservedMillyNeverGetsLandmarkFallback(t *testing.T) {
	s, f, _, r := travelRig(t)
	r.NpcGuid = 0xf130002450000123
	f.world.QuestGivers = nil
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		t.Fatal("unobserved generic giver was sent to navigation")
		return nil, nil
	}
	got, err := s.TravelQuest(context.Background(), r)
	if err != nil || got.Reason != "quest_travel_current_observed_giver_required" || got.Moving || got.Traveling || len(f.packets) != 0 {
		t.Fatal(got, err, f.packets)
	}
}

func TestQuestTravelObservedGenericGiverRetainsAllSafetyGates(t *testing.T) {
	for _, mode := range []string{"entry_mismatch", "absent", "position_unknown", "map_unknown", "other_map", "dead", "health_unknown", "not_questgiver", "combat", "moving", "spline", "trajectory", "invalid_position", "conflicting_view"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := travelRig(t)
			r.NpcGuid = 0xf130002450000123
			c := f.world.QuestGivers[0]
			c.Guid, c.Entry = r.NpcGuid, 9296
			switch mode {
			case "entry_mismatch":
				c.Entry = 823
			case "absent":
				c.Present = false
			case "position_unknown":
				c.PositionKnown = false
			case "map_unknown":
				c.MapKnown = false
			case "other_map":
				c.MapId = 1
			case "dead":
				c.Alive, c.Health = false, 0
			case "health_unknown":
				c.HealthKnown = false
			case "not_questgiver":
				c.NpcFlags = 0
			case "combat":
				c.UnitFlags = 0x80000
			case "moving":
				c.MovementFlags = 1
			case "spline":
				c.MovementFlags = 0x08000000
			case "trajectory":
				c.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: false}
			case "invalid_position":
				c.X = 40001
			case "conflicting_view":
				other := &worldv1.CreatureState{Guid: c.Guid, Entry: c.Entry, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: c.PositionRevision, X: c.X + 1, Y: c.Y, Z: c.Z, HealthKnown: true, Health: 100, AliveKnown: true, Alive: true, NpcFlags: 2}
				f.world.NearbyCreatures = []*worldv1.CreatureState{other}
			}
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				t.Fatal("unsafe generic giver started navigation")
				return nil, nil
			}
			got, err := s.TravelQuest(context.Background(), r)
			if err != nil || got.Moving || got.Traveling || len(f.packets) != 0 || got.Reason == "quest_travel_within_distance" {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestQuestTravelGenericGiverLossStopsBeforeAnyHeartbeat(t *testing.T) {
	s, f, advance, r := travelRig(t)
	r.NpcGuid = 0xf130002450000123
	f.world.QuestGivers[0].Guid, f.world.QuestGivers[0].Entry = r.NpcGuid, 9296
	s.approachDelay = func(ctx context.Context, d time.Duration) error {
		advance(d)
		f.world.QuestGivers = nil
		return ctx.Err()
	}
	got, err := s.TravelQuest(context.Background(), r)
	if err != nil || got.Reason != "quest_travel_current_observed_giver_required" || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
}

func TestQuestTravelLandmarkRejectsOtherGUIDsAndObservedUnsafeNPC(t *testing.T) {
	for _, mode := range []string{"other_entry", "entry_mismatch", "dead", "moving", "nearby_not_questgiver", "corpse", "owner_target"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := travelRig(t)
			c := f.world.QuestGivers[0]
			switch mode {
			case "other_entry":
				r.NpcGuid = 0xf13000012b000001
				f.world.QuestGivers = nil
			case "entry_mismatch":
				c.Entry = 196
			case "dead":
				c.Alive = false
				c.Health = 0
			case "moving":
				c.MovementFlags = 1
			case "nearby_not_questgiver":
				c.NpcFlags = 0
				f.world.QuestGivers = nil
				f.world.NearbyCreatures = []*worldv1.CreatureState{c}
			case "corpse":
				c.Alive = false
				c.Health = 0
				f.world.QuestGivers = nil
				f.world.LootableCorpses = []*worldv1.CreatureState{c}
			case "owner_target":
				c.UnitFlags = 0x80000
				f.world.QuestGivers = nil
				f.world.OwnerTarget = c
			}
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				t.Fatal("unsafe NPC used landmark fallback")
				return nil, nil
			}
			got, err := s.TravelQuest(context.Background(), r)
			if err != nil || got.Moving || len(f.packets) != 0 || strings.Contains(got.Reason, "landmark") {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestQuestTravelLandmarkStopsAsSoonAsActualNPCReappears(t *testing.T) {
	s, f, advance, r := travelRig(t)
	c := f.world.QuestGivers[0]
	f.world.QuestGivers = nil
	s.approachDelay = func(ctx context.Context, d time.Duration) error {
		advance(d)
		f.world.QuestGivers = []*worldv1.CreatureState{c}
		return ctx.Err()
	}
	got, err := s.TravelQuest(context.Background(), r)
	if err != nil || got.Reason != "quest_travel_landmark_giver_reobserved" || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
		t.Fatal(got, err, f.packets)
	}
}

func TestQuestTravelLandmarkStopsOnExpiryCombatAndTeleport(t *testing.T) {
	for _, mode := range []string{"expiry", "combat", "teleport"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := travelRig(t)
			f.world.QuestGivers = nil
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				advance(d)
				switch mode {
				case "expiry":
					advance(31 * time.Second)
				case "combat":
					f.world.Self.UnitFlags = 0x80000
				case "teleport":
					f.world.Self.X += 10
					f.world.Self.PositionRevision++
				}
				return ctx.Err()
			}
			got, err := s.TravelQuest(context.Background(), r)
			if err != nil || got.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestNavigationFailureDiagnosticRetainsStageButBoundsAndRedactsPaths(t *testing.T) {
	root := "/private/client-assets"
	err := errors.New("navigation ground sample: " + root + "/maps: bad polygon\n" + strings.Repeat("x", 400))
	got := navigationDiagnostic(err, root)
	if !strings.Contains(got, "navigation ground sample") || !strings.Contains(got, "<client-data>/maps") || strings.Contains(got, root) || strings.ContainsAny(got, "\n\r\t") || len(got) > 240 {
		t.Fatal(got)
	}
	s, _, _, r := travelRig(t)
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		return nil, errors.New("ground polygon height unavailable")
	}
	state, err := s.TravelQuest(context.Background(), r)
	if err != nil || !strings.Contains(state.NavReason, "ground polygon height unavailable") || state.NavAvailable || state.Moving {
		t.Fatal(state, err)
	}
}

func TestQuestTravelFollowsMeshCornersAndStopsWithoutOwner(t *testing.T) {
	s, f, _, r := travelRig(t)
	got, err := s.TravelQuest(context.Background(), r)
	if err != nil || got.Traveling || got.Moving || got.MotionUnconfirmed || got.Reason != "quest_travel_within_distance" || !got.NavAvailable || got.InteractionOwner != "quests" {
		t.Fatal(got, err)
	}
	previous := position{x: -8933, y: -136, z: 81}
	for _, p := range f.packets {
		_, n, _ := readGUID(p.Payload)
		v := position{x: math.Float32frombits(binary.LittleEndian.Uint32(p.Payload[n+10:])), y: math.Float32frombits(binary.LittleEndian.Uint32(p.Payload[n+14:])), z: math.Float32frombits(binary.LittleEndian.Uint32(p.Payload[n+18:]))}
		if distance(previous, v) > 1.401 || v.x != previous.x && v.y != previous.y {
			t.Fatal("step exceeded speed or cut a mesh corner", previous, v)
		}
		previous = v
	}
	if countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveStartForward) != 1 {
		t.Fatal("missing normal start/STOP", f.packets)
	}
}

func TestQuestTravelRejectsMissingPartialLongAndOutOfBoundsRoutes(t *testing.T) {
	for _, mode := range []string{"assets", "partial", "long", "outside", "entry", "moving", "combat", "planning_teleport"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := travelRig(t)
			switch mode {
			case "assets":
				s.navDataDir = ""
				s.planMapPath = planMapNavigation
			case "partial":
				s.planPath = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("partial mesh")
				}
			case "long":
				s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
					return []position{a, {x: -8750, y: -136, z: 81}, b}, nil
				}
			case "outside":
				s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
					return []position{a, {x: -9100, y: -136, z: 81}, b}, nil
				}
			case "entry":
				f.world.QuestGivers[0].Entry = 999
			case "moving":
				f.world.QuestGivers[0].MovementFlags = 1
			case "combat":
				f.world.Self.UnitFlags = 0x80000
			case "planning_teleport":
				s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
					f.world.Self.X += 10
					f.world.Self.PositionRevision++
					return []position{a, b}, nil
				}
			}
			got, err := s.TravelQuest(context.Background(), r)
			if err != nil || got.Traveling || got.Moving || len(f.packets) != 0 || got.Reason == "quest_travel_within_distance" {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestQuestTravelStopsOnCancellationExpiryCombatTargetLossAndTeleport(t *testing.T) {
	for _, mode := range []string{"cancel", "expiry", "combat", "target", "teleport"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := travelRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				advance(d)
				switch mode {
				case "cancel":
					cancel()
				case "expiry":
					advance(31 * time.Second)
				case "combat":
					f.world.Self.UnitFlags = 0x80000
				case "target":
					f.world.QuestGivers = nil
				case "teleport":
					f.world.Self.X += 10
					f.world.Self.PositionRevision++
				}
				return ctx.Err()
			}
			got, err := s.TravelQuest(ctx, r)
			if mode == "cancel" && status.Code(err) != codes.Canceled {
				t.Fatal(err)
			}
			if got.Traveling || got.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}
