package buddynav

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopiedShipDepartureFindsActualWaterWithoutGroundCorridor(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_SURFACE_TEST_DATA_DIR for copied ship and world geometry")
	}
	ctx := context.Background()
	path, err := LoadTransportPath(ctx, dir, 292, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := LoadSurfaceMesh(ctx, dir, 3015)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := path.Sample(10000)
	if err != nil {
		t.Fatal(err)
	}
	tr := SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}
	inside := Point{X: 1.0764359, Y: 4.6327205, Z: 6.0980916}
	outside := Point{Y: 65}
	lip, err := mesh.BoardingEntry(ctx, outside, inside)
	if err != nil {
		t.Fatal(err)
	}
	start, target := tr.World(lip), tr.World(outside)
	bearing := math.Atan2(float64(target.Y-start.Y), float64(target.X-start.X))
	rows, err := PlanSurfaceBodyContext(ctx, dir, sample.MapID, start, bearing, 7, 500*time.Millisecond, true, true,
		MovingSurface{Transform: tr, Model: mesh.Collision()})
	if err != nil || len(rows) < 2 {
		t.Fatal("copied deck departure did not reach measured contact", err)
	}
	last := rows[len(rows)-1]
	if !last.Swimming || last.At <= 0 || last.FallTime <= 0 || last.Point.Z >= start.Z {
		t.Fatal("departure did not end in the copied sea liquid", last)
	}
	for n, row := range rows {
		if !worldPoint(row.Point) || n > 0 && row.At <= rows[n-1].At || n < len(rows)-1 && row.Swimming {
			t.Fatal("invalid or premature water contact", n, row)
		}
	}
	deck := tr.World(inside)
	gravity, err := PlanSurfaceGravityContext(ctx, dir, sample.MapID, deck, bearing, 0, BodyJumpVelocity,
		MovingSurface{Transform: tr, Model: mesh.Collision()})
	if err != nil || len(gravity) < 2 || gravity[len(gravity)-1].Swimming || distance(gravity[len(gravity)-1].Point, deck) > .05 {
		t.Fatal("released, actually launched jump did not land on its own deck", gravity, err)
	}
	if _, err := PlanSurfaceGravityContext(ctx, dir, sample.MapID, deck, bearing, 0, 0,
		MovingSurface{Transform: tr, Model: mesh.Collision()}); err == nil {
		t.Fatal("stationary supported body was turned into a new jump")
	}
}

func TestPhysicalWaterContactWithoutLiquidDataReturnsError(t *testing.T) {
	w := mapWindow{mapID: 0, x: 32, y: 32}
	floors := newFloorReader(filepath.Join(t.TempDir(), "vmaps"))
	floors.window = &w
	p := &planner{mapID: 0, floors: floors}
	if _, _, err := p.deepWaterBetween(context.Background(), Point{}, 1, -1); err == nil {
		t.Fatal("missing installed liquid polygons established a water landing")
	}
}
