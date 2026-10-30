package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const approachCorpseGUID = uint64(0xf130000033000001)

func approachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.LootableCorpses = []*worldv1.CreatureState{{Guid: approachCorpseGUID, Present: true, MapKnown: true, PositionKnown: true, X: 12, HealthKnown: true, AliveKnown: true, DynamicFlags: 1}}
	toggle(t, s, true)
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot"})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: approachCorpseGUID}
}
func TestLootApproachUsesBoundedNormalStepsAndStops(t *testing.T) {
	s, f, advance, r := approachRig(t)
	last := float32(0)
	for i := 0; i < 10; i++ {
		got, err := s.ApproachLoot(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if got.Position.X-last > 1.401 {
			t.Fatalf("unbounded step %+v", got)
		}
		last = got.Position.X
		if got.Reason == "loot_approach_within_distance" {
			break
		}
		advance(200 * time.Millisecond)
	}
	got := state(t, s)
	if got.Moving || !got.Following || got.Position.X < 8.99 || got.Position.X > 9.01 {
		t.Fatalf("approach %+v", got)
	}
	if f.packets[0].Opcode != opcode.MSGMoveStartForward || f.packets[len(f.packets)-1].Opcode != opcode.MSGMoveStop {
		t.Fatal("missing ordinary start/stop")
	}
}
func TestLootApproachRejectsUnsafeTargetsAndWrongLease(t *testing.T) {
	for _, mode := range []string{"token", "living", "notlootable", "far", "vertical", "combat", "afk", "ownerfar", "map", "unknown", "transport"} {
		s, f, _, r := approachRig(t)
		switch mode {
		case "token":
			r.Token = "wrong"
		case "living":
			f.world.LootableCorpses[0].Alive = true
		case "notlootable":
			f.world.LootableCorpses[0].DynamicFlags = 0
		case "far":
			f.world.LootableCorpses[0].X = 13
		case "vertical":
			f.world.LootableCorpses[0].Z = 2
		case "combat":
			f.world.Self.UnitFlags = 0x80000
		case "afk":
			f.world.Owner.PlayerFlags = 2
		case "ownerfar":
			f.world.Owner.X = 31
		case "map":
			f.world.Self.MapId = 1
			f.world.Owner.MapId = 1
			f.world.LootableCorpses[0].MapId = 1
		case "unknown":
			f.world.LootableCorpses[0].PositionKnown = false
		case "transport":
			f.world.LootableCorpses[0].Trajectory = &worldv1.CreatureTrajectory{Known: true, TransportRelative: true}
		}
		got, err := s.ApproachLoot(context.Background(), r)
		if mode == "token" {
			if err == nil {
				t.Fatal("wrong token")
			}
		} else if err != nil || got.Moving {
			t.Fatalf("%s %+v %v", mode, got, err)
		}
		if len(f.packets) != 0 {
			t.Fatalf("unsafe %s emitted packets", mode)
		}
	}
}
func TestLootApproachLateCallStopsWithoutLeapAndCorrectionRebases(t *testing.T) {
	for _, correction := range []bool{false, true} {
		s, f, advance, r := approachRig(t)
		_, _ = s.ApproachLoot(context.Background(), r)
		advance(time.Second)
		if correction {
			f.world.Self.PositionRevision++
			f.world.Self.X = 100
		}
		got, err := s.ApproachLoot(context.Background(), r)
		if err != nil || got.Moving || !got.Following || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop {
			t.Fatalf("late %+v %v", got, err)
		}
		_, n, _ := readGUID(f.packets[1].Payload)
		x := math.Float32frombits(binary.LittleEndian.Uint32(f.packets[1].Payload[n+10:]))
		if correction && x != 100 || !correction && x != 0 {
			t.Fatalf("stale STOP x%v", x)
		}
	}
}
