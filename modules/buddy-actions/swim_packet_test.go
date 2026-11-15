package buddyactions

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

func TestSwimMovementPacketEncodesPitchBeforeFallTime(t *testing.T) {
	const guid, clientClock = 0x40301, 2026
	pose := position{x: -557, y: 352, z: -47, o: math.Pi / 2}
	for _, test := range []struct {
		name  string
		flags uint32
		pitch float32
	}{
		{"start forward", swimMovementFlag | 1, 0.25},
		{"vertical ascent", swimMovementFlag | swimAscendingFlag, math.Pi / 2},
		{"dive while moving", swimMovementFlag | swimDescendingFlag | 1, -math.Pi / 2},
		{"stop in water", swimMovementFlag | movementflags.PassiveModifiers, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			packet := swimMovementPacket(guid, clientClock, pose, test.pitch, test.flags)
			mover, off, err := readGUID(packet)
			if err != nil || mover != guid || len(packet)-off != 34 {
				t.Fatalf("wrong swimming MovementInfo envelope %x", packet)
			}
			body := packet[off:]
			if binary.LittleEndian.Uint32(body) != test.flags || binary.LittleEndian.Uint16(body[4:]) != 0 ||
				binary.LittleEndian.Uint32(body[6:]) != clientClock || binary.LittleEndian.Uint32(body[30:]) != 0 {
				t.Fatalf("wrong flags, time or fallTime %x", body)
			}
			for i, value := range []float32{pose.x, pose.y, pose.z, pose.o, test.pitch} {
				if binary.LittleEndian.Uint32(body[10+4*i:]) != math.Float32bits(value) {
					t.Fatalf("wrong pose or pitch field %d: %x", i, body)
				}
			}
		})
	}
}

func TestSwimMovementPacketRejectsIncompatibleMotionAndInvalidPosition(t *testing.T) {
	for _, flags := range []uint32{0, 1, swimMovementFlag | swimAscendingFlag | swimDescendingFlag,
		swimMovementFlag | 0x3, swimMovementFlag | 0xc, swimMovementFlag | 0x400, swimMovementFlag | 0x200,
		swimMovementFlag | 0x800, swimMovementFlag | 0x1000, swimMovementFlag | 0x02000000} {
		if got := swimMovementPacket(1, 22, position{z: -5}, 0, flags); got != nil {
			t.Fatalf("accepted incompatible swim flags %#x", flags)
		}
	}
	for _, pitch := range []float32{2, -2, float32(math.NaN()), float32(math.Inf(1))} {
		if got := swimMovementPacket(1, 22, position{z: -5}, pitch, swimMovementFlag); got != nil {
			t.Fatalf("accepted invalid pitch %v", pitch)
		}
	}
	if got := swimMovementPacket(1, 22, position{z: float32(math.NaN())}, 0, swimMovementFlag); got != nil {
		t.Fatal("accepted nonfinite position")
	}
}
