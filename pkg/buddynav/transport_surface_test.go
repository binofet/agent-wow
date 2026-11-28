package buddynav

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestTransportSurfacePassengerFramesPreserveMotionAndYaw(t *testing.T) {
	for _, yaw := range []float32{0, .2, 1.7, -2.8} {
		boat := SurfaceTransform{Origin: Point{-3900, -600, 4}, Yaw: yaw, Scale: 1}
		local := Point{7, -3, 6}
		world := boat.World(local)
		inferred, err := PassengerTransform(world, local, yaw+.3, .3)
		if err != nil || distance(inferred.Origin, boat.Origin) > .001 {
			t.Fatalf("passenger transform lost its received hull origin: %+v, %v", inferred, err)
		}
		other := Point{-9, 4, 6}
		if distance(inferred.World(other), boat.World(other)) > .001 || distance(inferred.Local(boat.World(other)), other) > .001 {
			t.Fatalf("hull moved between world/local frames at yaw %v", yaw)
		}
		boat.Origin.X += 100
		later, err := PassengerTransform(boat.World(local), local, yaw+.3, .3)
		if err != nil || distance(later.Origin, inferred.Origin) < 99 {
			t.Fatal("a later received passenger frame reused the old dock origin")
		}
	}
	if _, err := PassengerTransform(Point{X: float32(math.NaN())}, Point{}, 0, 0); err == nil {
		t.Fatal("nonfinite passenger frame accepted")
	}
}

func TestTransportSurfaceActualCollisionAndRecastMeshes(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if root == "" {
		t.Skip("requires installed collision and generated surface assets")
	}
	ctx := context.Background()
	for _, id := range []uint32{476, 3015, 7087} {
		surface, err := LoadTransportSurface(ctx, root, id)
		if err != nil {
			t.Fatal(err)
		}
		mesh, err := LoadSurfaceMesh(ctx, root, id)
		if err != nil {
			t.Fatal(err)
		}
		var centers []Point
		tile := mesh.mesh.GetTile(0)
		for _, a := range tile.Polys[:tile.Header.PolyCount] {
			center := Point{}
			for _, v := range a.Verts[:a.VertCount] {
				p := surfacePoint(tile.Verts[int(v)*3:])
				center.X += p.X / float32(a.VertCount)
				center.Y += p.Y / float32(a.VertCount)
				center.Z += p.Z / float32(a.VertCount)
			}
			centers = append(centers, center)
		}
		var floorPoint, destination Point
		var routeFound bool
	search:
		for i, start := range centers {
			for _, target := range centers[i+1:] {
				if distance(start, target) < 10 {
					continue
				}
				path, err := mesh.Route(ctx, start, target)
				if err != nil || len(path) < 2 {
					continue // Mast tops and separate floors can be disconnected.
				}
				for _, step := range path {
					h, contact, err := surface.Height(ctx, step, .6, .6)
					if err != nil || !contact || math.Abs(float64(h-step.Z)) > .6 {
						t.Fatalf("Detour deck route lacks actual hull support: display %d point %+v height %v: %v", id, step, h, err)
					}
				}
				floorPoint, destination, routeFound = start, target, true
				break search
			}
		}
		if !routeFound {
			t.Fatalf("no actual continuous ten-yard deck corridor in display %d", id)
		}
		t.Logf("display %d, %s, %d Recast polygons, deck corridor %+v to %+v", id, surface.ModelName, mesh.polygons, floorPoint, destination)
	}
}
