package buddyactions

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"
)

func TestCastAndMovementGolden(t *testing.T) {
	if got := hex.EncodeToString(castPacket(7)); got != "070208000000020000000103" {
		t.Fatalf("cast payload %s", got)
	}
	got := hex.EncodeToString(movementPacket(1, 0x12345678, position{1, 2, 3, 0.5}, true))
	if got != "0101010000000000785634120000803f00000040000040400000003f00000000" {
		t.Fatalf("movement payload %s", got)
	}
	stopped := movementPacket(1, 1, position{}, false)
	if !bytes.Equal(stopped[2:8], make([]byte, 6)) {
		t.Fatalf("stop has movement flags: %x", stopped)
	}
}

func TestPackedGUIDRoundTripAndTruncation(t *testing.T) {
	for _, guid := range []uint64{0, 1, 3, 0xab00000012003456, ^uint64(0)} {
		wire := packedGUID(guid)
		got, n, err := readGUID(wire)
		if err != nil || got != guid || n != len(wire) {
			t.Fatalf("guid=%x got=%x n=%d err=%v", guid, got, n, err)
		}
		for length := 0; length < len(wire); length++ {
			if _, _, err := readGUID(wire[:length]); err == nil {
				t.Fatalf("accepted truncated %x", wire[:length])
			}
		}
	}
}

func TestKnownSpellsAtomicValidation(t *testing.T) {
	known := []byte{0, 1, 0, 0x02, 0x08, 0, 0, 0, 0, 0, 0}
	if value, err := initialKnown(known); err != nil || !value {
		t.Fatalf("known=%v err=%v", value, err)
	}
	for length := 0; length < len(known); length++ {
		if _, err := initialKnown(known[:length]); err == nil {
			t.Fatalf("accepted truncated initial spells %d", length)
		}
	}
	if _, err := initialKnown(append(known, 0)); err == nil {
		t.Fatal("accepted extra tail")
	}
	if value, err := initialKnown([]byte{0, 0, 0, 0, 0}); err != nil || value {
		t.Fatalf("empty known=%v err=%v", value, err)
	}
}

func TestFinitePositionAndCastEventBounds(t *testing.T) {
	for _, p := range []position{{float32(math.NaN()), 0, 0, 0}, {0, float32(math.Inf(1)), 0, 0}, {40001, 0, 0, 0}} {
		if finite(p) {
			t.Fatalf("accepted %v", p)
		}
	}
	for length := 0; length < 17; length++ {
		if _, _, _, _, err := castEvent(make([]byte, length)); err == nil && length < 15 {
			t.Fatalf("accepted short cast %d", length)
		}
	}
}

func FuzzPacketDecoders(f *testing.F) {
	f.Add([]byte{0, 1, 0, 2, 8, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, p []byte) {
		_, _, _ = readGUID(p)
		_, _ = initialKnown(p)
		_, _, _, _, _ = castEvent(p)
	})
}
