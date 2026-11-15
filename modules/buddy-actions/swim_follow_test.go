package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestFollowCrossesDeepWaterAndReturnsToGround(t *testing.T) {
	ctx := context.Background()
	s, f, advance := rig(t)
	s.SetNavigationDataDir("/client-data")
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		return nil, errors.New("navigation water depth exceeds shallow wading limit")
	}
	s.planSwimPath = func(_ context.Context, _ string, _ uint32, a, b buddynav.Point) ([]buddynav.SwimPoint, error) {
		samples := []buddynav.SwimPoint{{Point: a}}
		for x := float32(1); x < b.X; x++ {
			wet := x >= 3 && x <= 7 && x != 5
			z := float32(0)
			if wet {
				z = -.6
			}
			samples = append(samples, buddynav.SwimPoint{Point: buddynav.Point{X: x, Z: z}, Swimming: wet})
		}
		return append(samples, buddynav.SwimPoint{Point: b}), nil
	}
	toggle(t, s, true)
	for i := 0; i < 25; i++ {
		advance(200 * time.Millisecond)
		s.Tick(ctx)
		if distance(s.pos, position{x: 10}) <= .005 && !s.state.Moving {
			break
		}
	}

	var changes []uint32
	for _, packet := range f.packets {
		_, n, err := readGUID(packet.Payload)
		if err != nil {
			t.Fatal(err)
		}
		swimming := binary.LittleEndian.Uint32(packet.Payload[n:])&swimMovementFlag != 0
		switch packet.Opcode {
		case opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim:
			changes = append(changes, packet.Opcode)
			if swimming != (packet.Opcode == opcode.MSGMoveStartSwim) {
				t.Fatal("transition packet has the wrong medium")
			}
		}
	}
	want := []uint32{opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim, opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim}
	if !slices.Equal(changes, want) || s.swimming || distance(s.pos, position{x: 10}) > .005 {
		t.Fatalf("wanted two water stretches and dry arrival, got transitions %v, position %+v, reason %s", changes, s.pos, s.state.Reason)
	}
}

func TestFollowStopsInWaterWhenOwnerLeavesMap(t *testing.T) {
	ctx := context.Background()
	s, f, advance := rig(t)
	s.SetNavigationDataDir("/client-data")
	f.world.Self.MovementFlags = swimMovementFlag
	f.world.Owner.MovementFlags = swimMovementFlag
	s.planSwimPath = func(_ context.Context, _ string, _ uint32, a, b buddynav.Point) ([]buddynav.SwimPoint, error) {
		return []buddynav.SwimPoint{{Point: a, Swimming: true}, {Point: b, Swimming: true}}, nil
	}
	toggle(t, s, true)
	advance(200 * time.Millisecond)
	s.Tick(ctx)
	f.world.Owner.MapId = 48
	advance(200 * time.Millisecond)
	s.Tick(ctx)

	if len(f.packets) != 2 || f.packets[0].Opcode != opcode.MSGMoveStartForward || f.packets[1].Opcode != opcode.MSGMoveStop {
		t.Fatalf("expected a single swim start and stop, got %+v", f.packets)
	}
	_, n, _ := readGUID(f.packets[1].Payload)
	body := f.packets[1].Payload[n:]
	if len(body) != 34 || binary.LittleEndian.Uint32(body) != swimMovementFlag {
		t.Fatalf("STOP did not preserve the observed swim state: %x", body)
	}
}
