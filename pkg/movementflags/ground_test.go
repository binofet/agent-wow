package movementflags

import "testing"

func TestPassiveModifiersAreNotActualAirborneMovement(t *testing.T) {
	// Independent primary enum values, not derived from the production mask.
	passive := []uint32{0, 0x10000000, 0x20000000, 0x30000000, 0x40000000, 0x50000000, 0x60000000, 0x70000000}
	for _, status := range passive {
		for _, ground := range []uint32{0, 1, 0x10, 0x100, 0x1ff} {
			if !GroundCompatible(status|ground, 0) {
				t.Fatalf("passive status %08x hid ordinary flags %08x", status, ground)
			}
		}
		for bit := uint(9); bit < 32; bit++ {
			if bit >= 28 && bit <= 30 {
				continue
			}
			if GroundCompatible(status|1<<bit, 0) {
				t.Fatalf("passive status admitted unsupported movement bit %d", bit)
			}
		}
		for _, extra := range []uint32{1, 2, 0x20, 0xffff, 0xffffffff} {
			if GroundCompatible(status, extra) {
				t.Fatal("extra movement flags became known ground mode")
			}
		}
	}
}
