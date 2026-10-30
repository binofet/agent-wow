package buddynav

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestAdjacentCorridorBoundsAndExactNineCollisionTiles(t *testing.T) {
	for _, q := range []Point{{-9599, -700, 40}, {-8400, 200, 180}, {-9460, -100, 60}, {-9100, -550, 80}} {
		if !inBounds(q) {
			t.Fatal("corridorfixtureoutsidebounds", q)
		}
	}
	for _, q := range []Point{{-9599.01, -100, 60}, {-8400, -700.01, 60}, {-8399.99, -100, 60}, {-9460, 200.01, 60}, {-9460, -100, 39.99}} {
		if inBounds(q) {
			t.Fatal("unboundedcorridor", q)
		}
	}
	count := 0
	for x := 46; x <= 50; x++ {
		for y := 30; y <= 34; y++ {
			ok := allowedFloorTile(fmt.Sprintf("000_%02d_%02d.vmtile", y, x))
			want := x >= 47 && x <= 49 && y >= 31 && y <= 33
			if ok != want {
				t.Fatalf("unexpectedtilepermission x%d y%d", x, y)
			}
			if ok {
				count++
			}
		}
	}
	if count != 9 {
		t.Fatal("notexactly9tiles")
	}
}

// Coordinates here are static asset fixtures, not observations or coordinates
// supplied to a live actuator. Each floor is derived independently from the
// copied client terrain/collision and complete mesh route before testing.
func TestPhysicalVineyardGoldshireCorridor(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_FLOOR_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("setstaticclientassets")
	}
	p, err := cachedPlanner(dir)
	if err != nil {
		t.Fatal(err)
	}
	positions := []Point{{-9027.586, -235.64706, 0}, {-9090, -310, 0}, {-9170, -340, 0}, {-9260, -280, 0}, {-9360, -200, 0}, {-9464, -67, 58.8}}
	for i, q := range positions {
		terrain, ok := p.terrain(q)
		if !ok {
			t.Fatalf("fixtureterrainunavailable%+v", q)
		}
		if q.Z == 0 {
			q.Z = terrain
		}
		h, err := p.ground(q)
		if err != nil {
			t.Fatalf("fixtureground%+v: %v", q, err)
		}
		q.Z, err = p.floor(context.Background(), q, h, terrain)
		if err != nil {
			t.Fatalf("fixturefloor%+v: %v", q, err)
		}
		positions[i] = q
	}
	for i := 1; i < len(positions); i++ {
		path, err := Plan(dir, 0, positions[i-1], positions[i])
		if err != nil {
			t.Fatalf("corridorsegment%+v -> %+v: %v", positions[i-1], positions[i], err)
		}
		var length float64
		for j := 1; j < len(path); j++ {
			length += distance(path[j-1], path[j])
		}
		if length > MaxRouteLength {
			t.Fatal("unboundedcorridorsegment")
		}
		t.Logf("corridorfrom=%+v to=%+v points=%d length=%.3f", positions[i-1], positions[i], len(path), length)
	}
}
