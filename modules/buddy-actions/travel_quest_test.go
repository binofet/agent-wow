package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
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
	f.world.QuestGivers = []*worldv1.CreatureState{{Guid: approachCorpseGUID, Entry: 823, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: -8913, Y: -136, Z: 81, HealthKnown: true, Health: 100, AliveKnown: true, Alive: true, NpcFlags: 2}}
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
	return s, f, advance, &actionsv1.ApproachQuestRequest{Token: lease.Token, NpcGuid: approachCorpseGUID}
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
