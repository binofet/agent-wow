package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestClientDiscoveryPublishesOnlyActualWrittenMovementAndRetainsRawPose(t *testing.T) {
	s, f, _ := rig(t)
	toggle(t, s, true)
	rawX, rawRevision := f.world.Self.X, f.world.Self.PositionRevision
	s.Tick(context.Background())
	toggle(t, s, false)
	if len(f.movementObservations) != 2 {
		t.Fatal(f.movementObservations)
	}
	for i, r := range f.movementObservations {
		packet := f.packets[i]
		_, n, err := readGUID(packet.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Valid || r.SelfGuid != s.selfGUID || r.MapId != 0 || r.ServerPositionRevision != rawRevision || r.Sequence != uint64(i+1) || r.X != math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[n+10:])) || r.Y != s.pos.y || r.Z != s.pos.z {
			t.Fatal(r, packet)
		}
	}
	if f.world.Self.X != rawX || f.world.Self.PositionRevision != rawRevision || !s.state.ClientMovementObservationConfirmed || s.state.ClientMovementObservationFailures != 0 {
		t.Fatal("discovery mutated raw authority", f.world.Self, s.stateLocked())
	}
}

func TestClientDiscoveryAmbiguousMovementInvalidatesAndCallbackFailureDoesNotInventTransport(t *testing.T) {
	for _, mode := range []string{"ambiguous", "callback_failure", "clock_failure", "correction", "unknown_map"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			toggle(t, s, true)
			switch mode {
			case "ambiguous":
				f.failSend = true
			case "callback_failure":
				f.failMovementObservation = true
			case "clock_failure":
				f.failClock = true
			case "correction":
				f.onClock = func() { f.world.Self.PositionRevision++; f.world.Self.X = 100 }
			case "unknown_map":
				f.onClock = func() { f.world.Self.MapKnown = false }
			}
			s.Tick(context.Background())
			switch mode {
			case "ambiguous":
				if len(f.movementObservations) != 1 || f.movementObservations[0].Valid || !s.state.MotionUnconfirmed || s.state.TransportWrites != 0 {
					t.Fatal(s.stateLocked(), f.movementObservations)
				}
			case "callback_failure":
				if !s.state.Moving || s.state.MotionUnconfirmed || s.state.TransportWrites != 1 || s.state.ClientMovementObservationConfirmed || s.state.ClientMovementObservationFailures != 1 {
					t.Fatal(s.stateLocked())
				}
				f.failMovementObservation = false
				toggle(t, s, false)
				if countPackets(f, opcode.MSGMoveStop) != 1 || s.state.MotionUnconfirmed || !s.state.ClientMovementObservationConfirmed || len(f.movementObservations) != 2 || f.movementObservations[1].Sequence <= f.movementObservations[0].Sequence {
					t.Fatal(s.stateLocked(), f.movementObservations)
				}
			default:
				if len(f.movementObservations) != 0 || len(f.packets) != 0 {
					t.Fatal("published unwritten pose", mode, f.packets, f.movementObservations)
				}
			}
		})
	}
}

func TestClientDiscoveryCorrectionStopUsesNewRawBaseline(t *testing.T) {
	s, f, _ := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	f.world.Self.X = 100
	f.world.Self.MapId = 1
	f.world.Self.PositionRevision++
	toggle(t, s, false)
	r := f.movementObservations[len(f.movementObservations)-1]
	if !r.Valid || r.X != 100 || r.MapId != 1 || r.ServerPositionRevision != f.world.Self.PositionRevision || f.packets[len(f.packets)-1].Opcode != opcode.MSGMoveStop {
		t.Fatal(r, f.packets)
	}
}
