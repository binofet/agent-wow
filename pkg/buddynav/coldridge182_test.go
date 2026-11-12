package buddynav

import (
	"context"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

// These are saved public-client observations from qualification-q182-20261030T2207,
// not destinations supplied to a live controller by this test.
var coldridge182Stage = Point{-6361.6, 568.8, 385.89725}
var coldridge182Observed = Point{-6446.56, 531.03973, 385.32617}
var coldridge182Destination = Point{-6444.943, 525.7038, 386.20703}

func TestColdridge182RegionIsSeparateAndSealed(t *testing.T) {
	r := RegionColdridgeTrollCave
	if name, err := r.name(); err != nil || name != "Coldridge troll approach" {
		t.Fatal(name, err)
	}
	for _, p := range []Point{coldridge182Stage, coldridge182Observed, coldridge182Destination, {-6450.7256, 526.10016, 385.5855}} {
		if !r.Contains(p) {
			t.Fatal("recorded approach point is outside its reviewed policy", p)
		}
	}
	if RegionColdridge.Contains(coldridge182Observed) || r.Contains(Point{-6222.4404, 686.27026, 384.9266}) {
		t.Fatal("old combat region expanded or stale baseline widened the new combat region")
	}
	for _, p := range []Point{{-6452.01, 550, 385}, {-6354.99, 550, 385}, {-6400, 524.99, 385}, {-6400, 570.01, 385}, {-6400, 550, 382.99}, {-6400, 550, 389.01}, {float32(math.NaN()), 550, 385}} {
		if _, err := PlanRegionContext(context.Background(), "", 0, r, coldridge182Stage, p); err == nil || strings.Contains(err.Error(), "directory") {
			t.Fatal("unreviewed endpoint reached asset access", p, err)
		}
	}
	if err := validateRegionRequest(1, r, coldridge182Stage, coldridge182Observed); err == nil {
		t.Fatal("troll approach admitted another map")
	}
	want := []string{"0004330.mmtile", "0004331.mmtile", "0004430.mmtile", "0004431.mmtile"}
	if got, err := r.meshTiles(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("troll approach mesh allowlist changed", got, err)
	}
	count := 0
	for x := range 64 {
		for y := range 64 {
			if r.allowsFloorTile(fmt.Sprintf("000_%02d_%02d.vmtile", x, y)) {
				count++
			}
		}
	}
	if count != 4 || r.allowsFloorTile("../000_31_44.vmtile") || r.allowsFloorTile("000_32_44.vmtile") || RegionColdridge.allowsFloorTile("000_31_44.vmtile") {
		t.Fatal("new collision scope leaked into the old region or another tile")
	}
}

func TestColdridge182RejectsRawAndPhysicalRouteEscapes(t *testing.T) {
	a, b := coldridge182Stage, coldridge182Observed
	f := fakeRoute(a, Point{-6400, 571, 385}, b)
	if _, err := planRegion(context.Background(), f, RegionColdridgeTrollCave, a, b); err == nil || len(f.samples) != 0 {
		t.Fatal("raw path escaped before physical sampling", err)
	}
	a, b = Point{-6400, 550, 383.2}, Point{-6401, 550, 383.2}
	f = fakeRoute(a, b)
	f.floorFn = func(Point, float32, float32) (float32, error) { return 382.9, nil }
	if _, err := planRegion(context.Background(), f, RegionColdridgeTrollCave, a, b); err == nil || !strings.Contains(err.Error(), "Coldridge troll approach bounds") {
		t.Fatal("normalized physical floor escaped the new policy", err)
	}
}

func TestPhysicalColdridge182ObservedApproach(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_COLDRIDGE_182_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_COLDRIDGE_182_TEST_DATA_DIR to pinned independent client assets")
	}
	for i, end := range []Point{coldridge182Observed, coldridge182Destination} {
		for _, reverse := range []bool{false, true} {
			a, b := coldridge182Stage, end
			if reverse {
				a, b = b, a
			}
			path, err := PlanRegionContext(context.Background(), dir, 0, RegionColdridgeTrollCave, a, b)
			if err != nil {
				t.Fatalf("observed fixture%d reverse=%v: %v", i, reverse, err)
			}
			if len(path) < 2 || len(path) > MaxPoints || distance(path[0], a) > EndpointTolerance || distance(path[len(path)-1], b) > EndpointTolerance {
				t.Fatal("route endpoint or sample budget failed", len(path))
			}
			var length, maxStep, maxDZ float64
			for j, p := range path {
				if !RegionColdridgeTrollCave.Contains(p) {
					t.Fatal("physical route left its quest182 region", p)
				}
				if j > 0 {
					d, dz := distance(path[j-1], p), math.Abs(float64(path[j-1].Z-p.Z))
					length, maxStep, maxDZ = length+d, max(maxStep, d), max(maxDZ, dz)
					if d > 2 || dz > 1 {
						t.Fatal("physical segment limits changed", d, dz)
					}
				}
			}
			if length > MaxRouteLength {
				t.Fatal("route exceeded the 240-yard budget", length)
			}
			t.Logf("offline182 fixture%d reverse=%v points=%d length=%.5f maxStep=%.5f maxDZ=%.5f", i, reverse, len(path), length, maxStep, maxDZ)
		}
	}
}
