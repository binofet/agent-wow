package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func questApproachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachQuestRequest) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.QuestGivers = []*worldv1.CreatureState{{Guid: approachCorpseGUID, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: 8, HealthKnown: true, Health: 100, AliveKnown: true, Alive: true, NpcFlags: 2}}
	toggle(t, s, true)
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests"})
	if err != nil {
		t.Fatal(err)
	}
	s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); return ctx.Err() }
	return s, f, advance, &actionsv1.ApproachQuestRequest{Token: lease.Token, NpcGuid: approachCorpseGUID}
}

func TestQuestApproachCompletesSynchronouslyWithBoundedNormalStepsAndStop(t *testing.T) {
	s, f, _, r := questApproachRig(t)
	got, err := s.ApproachQuest(context.Background(), r)
	if err != nil || got.Reason != "quest_approach_within_distance" || got.Moving || got.MotionUnconfirmed || !got.Following || got.InteractionOwner != "quests" || !got.Position.Estimated || math.Abs(float64(got.Position.X-5)) > 0.001 {
		t.Fatal(got, err)
	}
	if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 4 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(f.packets)
	}
	previousX, previousClock := float32(0), uint32(0)
	for _, p := range f.packets {
		_, n, e := readGUID(p.Payload)
		if e != nil || len(p.Payload[n:]) != 30 {
			t.Fatal("not normal movement", p)
		}
		x := math.Float32frombits(binary.LittleEndian.Uint32(p.Payload[n+10:]))
		clock := binary.LittleEndian.Uint32(p.Payload[n+6:])
		if x < previousX || x-previousX > 1.401 || clock <= previousClock {
			t.Fatal("step exceeded elapsed speed or shared clock", p)
		}
		previousX, previousClock = x, clock
	}
}

func TestQuestApproachRefusesUnobservedMovingUnsafeOrFarNPCs(t *testing.T) {
	for _, mode := range []string{"missing", "dead", "notquestgiver", "map", "moving", "spline", "transport", "invalid", "far", "vertical", "combat", "npccombat", "speed", "unknown", "cast", "lease", "self_missing", "self_dead", "self_unknown_flags", "self_swimming", "self_unsafe"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := questApproachRig(t)
			npc := f.world.QuestGivers[0]
			switch mode {
			case "missing":
				f.world.QuestGivers = nil
			case "dead":
				npc.Alive = false
			case "notquestgiver":
				npc.NpcFlags = 0
			case "map":
				f.world.Self.MapId, f.world.Owner.MapId, npc.MapId = 1, 1, 1
			case "moving":
				npc.MovementFlags = 1
			case "spline":
				npc.Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, AgeMs: 2000, DurationMs: 1000}
			case "transport":
				npc.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, TransportRelative: true}
			case "invalid":
				npc.X = float32(math.NaN())
			case "far":
				npc.X = 8.1
			case "vertical":
				npc.Z = 1.6
			case "combat":
				f.world.Self.UnitFlags = 0x80000
			case "npccombat":
				npc.UnitFlags = 0x80000
			case "speed":
				f.world.Self.RunSpeed = 6
			case "unknown":
				npc.PositionKnown = false
			case "cast":
				s.state.CastPending = true
			case "lease":
				advance(14500 * time.Millisecond)
			case "self_missing":
				f.world.Self.Present = false
			case "self_dead":
				f.world.Self.Alive = false
			case "self_unknown_flags":
				f.world.Self.MovementFlagsKnown = false
			case "self_swimming":
				f.world.Self.MovementFlags = 0x2000
			case "self_unsafe":
				f.world.Self.UnitFlags = 4
			}
			got, err := s.ApproachQuest(context.Background(), r)
			if err != nil || got.Moving || len(f.packets) != 0 || got.Reason == "quest_approach_within_distance" {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestQuestApproachStopsOnCancellationCombatLeaseLossAndNPCMovement(t *testing.T) {
	for _, mode := range []string{"cancel", "combat", "npc_moved", "npc_moving", "expiry", "release", "late", "correction"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := questApproachRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				advance(d)
				switch mode {
				case "cancel":
					cancel()
				case "combat":
					f.world.Self.UnitFlags = 0x80000
				case "npc_moved":
					f.world.QuestGivers[0].PositionRevision++
				case "npc_moving":
					f.world.QuestGivers[0].MovementFlags = 1
				case "expiry":
					advance(16 * time.Second)
				case "release":
					s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "quests", Token: r.Token})
				case "late":
					advance(time.Second)
				case "correction":
					f.world.Self.X = 100
					f.world.Self.PositionRevision++
				}
				return ctx.Err()
			}
			got, err := s.ApproachQuest(ctx, r)
			if mode == "cancel" && status.Code(err) != codes.Canceled || mode == "release" && status.Code(err) != codes.FailedPrecondition {
				t.Fatal(got, err)
			}
			if got.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("failed approach left owned movement active", got, err, f.packets)
			}
			if mode == "correction" && got.Position.X != 100 {
				t.Fatal("STOP ignored authoritative correction", got)
			}
		})
	}
}

func TestQuestApproachIsIndependentOfOwnerPresencePositionAndActivity(t *testing.T) {
	for _, mode := range []string{"nil", "absent", "far", "afk", "combat", "othermap"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := questApproachRig(t)
			switch mode {
			case "nil":
				f.world.Owner = nil
			case "absent":
				f.world.Owner.Present = false
			case "far":
				f.world.Owner.X = 1000
			case "afk":
				f.world.Owner.PlayerFlags = 2
			case "combat":
				f.world.Owner.UnitFlags = 0x80000
			case "othermap":
				f.world.Owner.MapId = 1
			}
			got, err := s.ApproachQuest(context.Background(), r)
			if err != nil || got.Reason != "quest_approach_within_distance" || got.Moving || got.MotionUnconfirmed || got.InteractionOwner != "quests" || math.Abs(float64(got.Position.X-5)) > 0.001 || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestQuestApproachRejectsDuplicateRPCAndNeverStopsReplacementLease(t *testing.T) {
	s, f, _, r := questApproachRig(t)
	s.approachDelay = func(ctx context.Context, _ time.Duration) error {
		if _, err := s.ApproachQuest(ctx, r); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("duplicate approach accepted", err)
		}
		if _, err := s.ReleaseInteraction(ctx, &actionsv1.InteractionRelease{Owner: "quests", Token: r.Token}); err != nil {
			t.Fatal(err)
		}
		lease, err := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "loot"})
		if err != nil {
			t.Fatal(err)
		}
		f.world.LootableCorpses = []*worldv1.CreatureState{{Guid: approachCorpseGUID, Present: true, MapKnown: true, PositionKnown: true, X: 12, HealthKnown: true, AliveKnown: true, DynamicFlags: 1}}
		if got, err := s.ApproachLoot(ctx, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: approachCorpseGUID}); err != nil || !got.Moving {
			t.Fatal(got, err)
		}
		return nil
	}
	got, err := s.ApproachQuest(context.Background(), r)
	if status.Code(err) != codes.FailedPrecondition || got.InteractionOwner != "loot" || !got.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveStartForward) != 2 {
		t.Fatal("old quest cleanup affected replacement loot lease", got, err, f.packets)
	}
}

func TestQuestApproachRequiresOwnTokenAndDoesNotMoveWhenAlreadyInRange(t *testing.T) {
	s, f, _, r := questApproachRig(t)
	bad := &actionsv1.ApproachQuestRequest{Token: "wrong", NpcGuid: r.NpcGuid}
	if _, err := s.ApproachQuest(context.Background(), bad); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	f.world.QuestGivers[0].X, f.world.Owner.X = 2, 2
	got, err := s.ApproachQuest(context.Background(), r)
	if err != nil || got.Reason != "quest_approach_within_distance" || got.Moving || len(f.packets) != 0 {
		t.Fatal(got, err, f.packets)
	}
}

func navQuestApproachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachQuestRequest) {
	t.Helper()
	s, f, advance, r := questApproachRig(t)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8900, -150, 82
	f.world.Self.PositionRevision++
	f.world.Owner = nil
	c := f.world.QuestGivers[0]
	c.X, c.Y, c.Z = -8894, -150, 82
	s.SetNavigationDataDir("/fixture/nav")
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		return []position{from, {x: from.x, y: from.y + 1, z: from.z}, {x: to.x, y: from.y + 1, z: from.z}, to}, nil
	}
	return s, f, advance, r
}

func TestQuestApproachNavigationRetainsWallCornersAndStopsWithinRange(t *testing.T) {
	s, f, _, r := navQuestApproachRig(t)
	calls := 0
	original := s.planPath
	s.planPath = func(ctx context.Context, root string, from, to position) ([]position, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 251*time.Millisecond {
			t.Fatal("unbounded planner")
		}
		return original(ctx, root, from, to)
	}
	got, err := s.ApproachQuest(context.Background(), r)
	if err != nil || got.Reason != "quest_approach_within_distance" || got.Moving || got.MotionUnconfirmed || !got.NavAvailable || calls != 1 {
		t.Fatal(got, err, calls)
	}
	previous := position{x: -8900, y: -150, z: 82}
	for _, packet := range f.packets {
		_, n, _ := readGUID(packet.Payload)
		p := position{x: math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[n+10:])), y: math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[n+14:])), z: math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[n+18:]))}
		if distance(previous, p) > 1.401 || p.x != previous.x && p.y != previous.y {
			t.Fatal("cut unverified corner", previous, p)
		}
		previous = p
	}
	if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("missing normal start/stop")
	}
}

func TestQuestApproachNavigationRejectsBlockedOffmeshAndExcessiveRoutes(t *testing.T) {
	for _, mode := range []string{"blocked", "offmesh_start", "long", "point_limit", "partial"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r := navQuestApproachRig(t)
			s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
				switch mode {
				case "blocked":
					return nil, errors.New("ground surface ray blocked")
				case "offmesh_start":
					return nil, errors.New("find navigation path: start: sample is outside ground polygon")
				case "long":
					return []position{from, {x: from.x, y: from.y + 2, z: from.z}, {x: to.x, y: from.y + 2, z: from.z}, to}, nil
				case "point_limit":
					p := make([]position, 257)
					for i := range p {
						p[i] = from
					}
					p[len(p)-1] = to
					return p, nil
				case "partial":
					to.x -= 3
					return []position{from, to}, nil
				}
				panic("unreachable")
			}
			got, err := s.ApproachQuest(context.Background(), r)
			if err != nil || got.Moving || got.NavAvailable || got.Reason != "quest_approach_navigation_unavailable" || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestQuestApproachNavigationRevalidatesBeforeFirstMovement(t *testing.T) {
	for _, mode := range []string{"npc_moved", "self_corrected", "combat", "lease_expired"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := navQuestApproachRig(t)
			original := s.planPath
			s.planPath = func(ctx context.Context, root string, from, to position) ([]position, error) {
				path, err := original(ctx, root, from, to)
				switch mode {
				case "npc_moved":
					f.world.QuestGivers[0].PositionRevision++
				case "self_corrected":
					f.world.Self.X += .2
					f.world.Self.PositionRevision++
				case "combat":
					f.world.Self.UnitFlags = 0x80000
				case "lease_expired":
					advance(16 * time.Second)
				}
				return path, err
			}
			got, _ := s.ApproachQuest(context.Background(), r)
			if got.Moving || len(f.packets) != 0 || got.Reason == "quest_approach_within_distance" {
				t.Fatal("acted before fresh postplan gates", got, f.packets)
			}
		})
	}
}

func TestQuestApproachNavigationStopsOnMidRouteNPCChange(t *testing.T) {
	s, f, advance, r := navQuestApproachRig(t)
	s.approachDelay = func(ctx context.Context, d time.Duration) error {
		advance(d)
		f.world.QuestGivers[0].PositionRevision++
		return ctx.Err()
	}
	got, err := s.ApproachQuest(context.Background(), r)
	if err != nil || got.Moving || got.Reason != "quest_approach_npc_position_changed" || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
}
