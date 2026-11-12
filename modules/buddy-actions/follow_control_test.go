package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func formationRig(t *testing.T, guid uint64, classID uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.selfGUID, f.world.Self.Guid, f.world.Self.ClassId = guid, guid, classID
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8900, -140, 82
	f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = -8900, -140, 82
	s.SetNavigationDataDir("/client/navigation")
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		return []position{from, to}, nil
	}
	return s, f, advance
}

func TestFormationFollowSeparatesStackedPartyThenStaysStillThroughIdleTurns(t *testing.T) {
	var final []position
	firstStarts := map[time.Duration]bool{}
	for _, who := range []struct {
		guid    uint64
		classID uint32
	}{{1, 5}, {4, 8}, {5, 4}} {
		s, f, advance := formationRig(t, who.guid, who.classID)
		toggle(t, s, true)
		started := false
		for tick := 0; tick < 40; tick++ {
			s.Tick(context.Background())
			if s.state.Moving && !started {
				firstStarts[time.Duration(tick)*200*time.Millisecond] = true
				started = true
			}
			advance(200 * time.Millisecond)
		}
		if !started || s.state.Moving || s.state.Reason != "within_personal_follow_space" {
			t.Fatal("stacked actor did not settle", who, s.stateLocked())
		}
		if distance(s.pos, playerPosition(f.world.Owner)) < 2.5 {
			t.Fatal("actor still stands at owner", who, s.pos)
		}
		for _, other := range final {
			if distance(other, s.pos) < 2 {
				t.Fatal("party still overlapping", other, s.pos)
			}
		}
		final = append(final, s.pos)
		count := len(f.packets)
		for i := 0; i < 20; i++ {
			f.world.Owner.Orientation += 0.4
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
		}
		f.world.Owner.X += 0.2
		s.Tick(context.Background())
		if len(f.packets) != count {
			t.Fatal("idle facing or tiny adjustment caused shuffle", who)
		}
	}
	if len(firstStarts) < 2 || firstStarts[0] {
		t.Fatal("party starts together without reaction delay", firstStarts)
	}
}

func TestFormationFollowSupportsHillsButNeverFallsBackAcrossBlockedMesh(t *testing.T) {
	s, f, advance := formationRig(t, 1, 5)
	f.world.Owner.X += 12
	f.world.Owner.Z += 3
	toggle(t, s, true)
	for i := 0; i < 5; i++ {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if !s.state.Moving || s.pos.z <= 82 {
		t.Fatal("formation retained flat-only gate", s.stateLocked())
	}
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		return nil, errors.New("blocked ground corridor")
	}
	s.followNav.Reset()
	f.world.Owner.X += 3
	s.Tick(context.Background())
	if s.state.Moving || s.state.NavAvailable {
		t.Fatal("blocked mesh fell through to direct motion", s.stateLocked())
	}
	last := f.packets[len(f.packets)-1]
	if last.Opcode != opcode.MSGMoveStop {
		t.Fatal("blocked route did not stop")
	}
}

func TestFormationFollowKeepsHealingAndLateTickStopPriority(t *testing.T) {
	s, f, advance := formationRig(t, 1, 5)
	learn(s)
	toggle(t, s, true)
	if _, err := s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if !s.state.CastPending || s.state.Moving {
		t.Fatal("formation displaced a healer", s.stateLocked())
	}
	for _, packet := range f.packets {
		if packet.Opcode == opcode.MSGMoveStartForward || packet.Opcode == opcode.MSGMoveHeartbeat {
			t.Fatal("formation moved during healing")
		}
	}
	s, _, advance = formationRig(t, 4, 8)
	toggle(t, s, true)
	for i := 0; i < 5; i++ {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if !s.state.Moving {
		t.Fatal("formation never started")
	}
	advance(time.Second)
	s.Tick(context.Background())
	if s.state.Moving || !s.state.Following || s.state.Reason != "follow_tick_late" || math.IsNaN(float64(s.pos.x)) {
		t.Fatal("late formation failed to stop", s.stateLocked())
	}
}

func TestFormationCatchUpUsesVerifiedRoutesUpTo200Yards(t *testing.T) {
	for _, gap := range []float32{90, 150, 200, 201} {
		s, f, advance := formationRig(t, 4, 8)
		f.world.Owner.X += gap
		plans := 0
		s.planMapPath = func(_ context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
			if mapID != f.world.Self.MapId {
				t.Fatal("follow planned on a different map", mapID)
			}
			plans++
			// The route first goes sideways. Movement must use this verified
			// segment rather than a direct chord toward the distant owner.
			return []position{from, {x: from.x + 3, y: from.y + 5, z: from.z}, {x: to.x, y: from.y + 5, z: to.z}, to}, nil
		}
		toggle(t, s, true)
		for i := 0; i < 5; i++ {
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
		}
		dx, dy := float64(s.pos.x-f.world.Self.X), float64(s.pos.y-f.world.Self.Y)
		if gap <= 200 && (plans != 1 || !s.state.Moving || !s.state.NavAvailable || dx <= 0 || dy <= 0 || math.Abs(dx*5-dy*3) > .01 || distance(s.pos, playerPosition(f.world.Self)) > 5.6) {
			t.Fatal("observed owner was not followed along the bounded verified segment", gap, s.stateLocked(), s.pos, plans)
		}
		if gap > 200 && (plans != 0 || s.state.Moving || len(f.packets) != 0 || s.state.Reason != "formation_owner_outside_supported_range") {
			t.Fatal("follow exceeded its catch-up bound", gap, s.stateLocked(), plans)
		}
	}
}

func TestDistantFormationCatchUpNeverMovesOnPartialOrOverBudgetRoute(t *testing.T) {
	for _, mode := range []string{"partial_error", "partial_endpoint", "over_budget_detour", "missing_assets"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := formationRig(t, 4, 8)
			f.world.Owner.X += 150
			plans := 0
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				plans++
				switch mode {
				case "partial_error":
					return nil, errors.New("find navigation path: navigation polygon path is partial, exhausted, or unavailable")
				case "partial_endpoint":
					to.x -= 10
					return []position{from, to}, nil
				case "over_budget_detour":
					return []position{from, {x: from.x, y: from.y + 200, z: from.z}, to}, nil
				default:
					return nil, errors.New("installed navigation tile window missing")
				}
			}
			toggle(t, s, true)
			for range 7 {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if plans == 0 || s.state.Moving || s.state.NavAvailable || len(f.packets) != 0 || distance(s.pos, playerPosition(f.world.Self)) != 0 {
				t.Fatal("unverified distant follow produced movement", mode, s.stateLocked(), f.packets, plans)
			}
		})
	}
}

func TestDistantFormationCatchUpCrossesFormerNorthshireFenceOnVerifiedRoute(t *testing.T) {
	s, f, advance := formationRig(t, 4, 8)
	f.world.Self.X = -9590
	f.world.Owner.X = -9740
	plans := 0
	s.planMapPath = func(_ context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
		if mapID != 0 {
			t.Fatal("wrong observed map", mapID)
		}
		plans++
		return []position{from, to}, nil
	}
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		t.Fatal("normal catch-up used the regional planner")
		return nil, nil
	}
	toggle(t, s, true)
	for range 24 {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if plans != 1 || !s.state.Moving || !s.state.NavAvailable || s.pos.x >= -9599 || countPackets(f, opcode.MSGMoveHeartbeat) == 0 {
		t.Fatal("normal verified catch-up retained the former area fence", s.stateLocked(), s.pos, plans)
	}
}

func TestFormationFollowsVerifiedStairsInsteadOfSettlingOnFloorBelow(t *testing.T) {
	s, f, advance := formationRig(t, 1, 5)
	f.world.Owner.Z = 113.24
	// Start directly beneath the requested personal slot, not just beneath
	// Joel. Horizontal-only hysteresis previously treated this as arrival.
	frame := followFormation{}
	slot := frame.Target(s.now(), 1, 5, playerPosition(f.world.Owner), false).Position
	f.world.Self.X, f.world.Self.Y = slot.x, slot.y
	plans := 0
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		plans++
		return []position{from, {x: from.x + 8, y: from.y, z: from.z}, {x: from.x + 8, y: from.y + 8, z: 92}, {x: from.x - 8, y: from.y + 8, z: 102}, {x: from.x - 8, y: from.y, z: to.z}, to}, nil
	}
	toggle(t, s, true)
	started := false
	for range 140 {
		s.Tick(context.Background())
		started = started || s.state.Moving
		advance(200 * time.Millisecond)
	}
	if plans != 1 || !started || s.state.Moving || !followAtTarget(s.pos, slot, 1) || math.Abs(float64(s.pos.z-f.world.Owner.Z)) > 1.5 {
		t.Fatal("stairs not followed", s.stateLocked(), plans)
	}
}

func TestMultiFloorFollowStillRequiresCompleteNavigationAndLegacyStaysFlat(t *testing.T) {
	for _, mode := range []string{"blocked_nav", "no_nav"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := formationRig(t, 1, 5)
			f.world.Owner.Z = 113.24
			if mode == "no_nav" {
				s.navDataDir = ""
			}
			plans := 0
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				plans++
				return nil, errors.New("ground surface ray blocked")
			}
			toggle(t, s, true)
			for range 6 {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if s.state.Moving || len(f.packets) != 0 || !s.state.Following {
				t.Fatal(s.stateLocked(), f.packets)
			}
			if mode == "no_nav" && (plans != 0 || s.state.Reason != "follow_target_too_far_or_vertical") {
				t.Fatal("legacy follow broadened", s.stateLocked(), plans)
			}
			if mode == "blocked_nav" && (plans == 0 || s.state.NavAvailable) {
				t.Fatal("missing route accepted", s.stateLocked(), plans)
			}
		})
	}
}
