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

// Official public playercreateinfo.sql race7/class9 and creature.sql entry658.
// These are static client/source fixtures, never claimed live observations.
var coldridgeSpawn = Point{-6240, 331, 383}
var coldridgeSten = Point{-6236.74, 331.113, 382.911}

func TestColdridgePolicyIsSealedAndDoesNotChangeLegacyRegions(t *testing.T) {
	if name, err := RegionColdridge.name(); err != nil || name != "Coldridge" {
		t.Fatal(name, err)
	}
	if err := validateRegionRequest(0, RegionColdridge, coldridgeSpawn, coldridgeSten); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Point{testStart, cryptStart, {-6400, 331, 383}, {-5999, 331, 383}, {-6240, -1, 383}, {-6240, 701, 383}, {-6240, 331, 319}, {-6240, 331, 451}, {float32(math.NaN()), 331, 383}} {
		if _, err := PlanRegionContext(context.Background(), "", 0, RegionColdridge, coldridgeSpawn, bad); err == nil || strings.Contains(err.Error(), "directory") {
			t.Fatalf("unreviewed endpoint reached assets: %+v %v", bad, err)
		}
	}
	if err := validateRegionRequest(1, RegionColdridge, coldridgeSpawn, coldridgeSten); err == nil {
		t.Fatal("Coldridge admitted map1")
	}
	for _, region := range []Region{RegionNorthshire, RegionDeathknellCrypt} {
		if err := validateRegionRequest(0, region, coldridgeSpawn, coldridgeSten); err == nil {
			t.Fatal("existing region expanded into Coldridge", region)
		}
	}
	if _, err := Plan("", 0, coldridgeSpawn, coldridgeSten); err == nil || !strings.Contains(err.Error(), "Northshire") {
		t.Fatal("default planner selected another region", err)
	}
	if tiles, err := RegionColdridge.meshTiles(); err != nil || !reflect.DeepEqual(tiles, []string{"0004330.mmtile", "0004331.mmtile"}) {
		t.Fatal(tiles, err)
	}
	count := 0
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			if RegionColdridge.allowsFloorTile(fmt.Sprintf("000_%02d_%02d.vmtile", x, y)) {
				count++
			}
		}
	}
	if count != 2 || RegionColdridge.allowsFloorTile("../000_31_43.vmtile") || RegionColdridge.allowsFloorTile("000_32_43.vmtile") || RegionNorthshire.allowsFloorTile("000_31_43.vmtile") || RegionDeathknellCrypt.allowsFloorTile("000_31_43.vmtile") {
		t.Fatal("Coldridge collision policy is not exactly two separate tiles")
	}
}

func TestColdridgeRejectsRawOrPhysicalPathsOutsideItsBounds(t *testing.T) {
	a, b := coldridgeSpawn, coldridgeSten
	f := fakeRoute(a, Point{-6399.5, 331, 383}, b)
	if _, err := planRegion(context.Background(), f, RegionColdridge, a, b); err == nil || len(f.samples) != 0 {
		t.Fatal("raw path escaped sealed region", err)
	}
	a.Z, b.Z = 320.2, 320.2
	f = fakeRoute(a, b)
	f.floorFn = func(Point, float32, float32) (float32, error) { return 319.9, nil }
	if _, err := planRegion(context.Background(), f, RegionColdridge, a, b); err == nil || !strings.Contains(err.Error(), "Coldridge bounds") {
		t.Fatal("physical floor escaped sealed region", err)
	}
	reader := newFloorReaderRegion(t.TempDir(), RegionColdridge)
	if _, known, err := reader.height(testStart, testStart.Z); err == nil || known || strings.Contains(err.Error(), "unavailable") {
		t.Fatal("outside-region collision query read an asset", known, err)
	}
}

func TestPhysicalColdridgeStarterRoutes(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_COLDRIDGE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_COLDRIDGE_TEST_DATA_DIR to independent static Coldridge assets")
	}
	// Public creature.sql source spawns for actual entry705 Ragged Young Wolf.
	// A live controller still requires current observed NPCs, never these coords.
	fixtures := []Point{coldridgeSten, {-6287.25, 328.127, 376.722}, {-6272.15, 285.559, 382.647}, {-6252.84, 387.126, 383.278}, {-6279.53, 279.643, 382.529}}
	for i, destination := range fixtures {
		for _, reverse := range []bool{false, true} {
			start, end := coldridgeSpawn, destination
			if reverse {
				start, end = end, start
			}
			route, err := PlanRegionContext(context.Background(), dir, 0, RegionColdridge, start, end)
			if err != nil {
				t.Fatalf("static fixture%d reverse=%v: %v", i, reverse, err)
			}
			if len(route) < 2 || len(route) > MaxPoints || distance(route[0], start) > EndpointTolerance || distance(route[len(route)-1], end) > EndpointTolerance {
				t.Fatal("static route endpoint/point budget", len(route))
			}
			var length, maxStep, maxZ float64
			for j, q := range route {
				if !RegionColdridge.contains(q) {
					t.Fatal("static route left Coldridge", q)
				}
				if j > 0 {
					step, dz := distance(route[j-1], q), math.Abs(float64(q.Z-route[j-1].Z))
					length, maxStep, maxZ = length+step, max(maxStep, step), max(maxZ, dz)
					if step > 2 || dz > 1 {
						t.Fatal("static route loosened physical segment bounds", step, dz)
					}
				}
			}
			if length > MaxRouteLength {
				t.Fatal("static route exceeded generic route budget", length)
			}
			t.Logf("offline Coldridge fixture%d reverse=%v points=%d length=%.5f maxStep=%.5f maxDZ=%.5f", i, reverse, len(route), length, maxStep, maxZ)
		}
	}
}
