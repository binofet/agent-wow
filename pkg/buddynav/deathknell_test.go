package buddynav

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// These optional tests read independently copied public client assets only.
// They never connect to the game server or claim a character walked the route.
func TestPhysicalDeathknellCryptRoutes(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_DEATHKNELL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_DEATHKNELL_TEST_DATA_DIR to independent static Deathknell assets")
	}
	for _, from := range []Point{cryptStart, {1676.71, 1678.31, 121.67}} {
		route, err := PlanRegionContext(context.Background(), dir, 0, RegionDeathknellCrypt, from, cryptSurface)
		if err != nil {
			t.Fatal(err)
		}
		if len(route) < 2 || len(route) > MaxPoints || distance(route[0], from) > EndpointTolerance || distance(route[len(route)-1], cryptSurface) > EndpointTolerance {
			t.Fatal("crypt endpoints or point budget failed")
		}
		var length float64
		wentDownStairs, reachedSurface := false, false
		for i, q := range route {
			if !RegionDeathknellCrypt.contains(q) {
				t.Fatalf("crypt route escaped sealed bounds: %+v", q)
			}
			wentDownStairs = wentDownStairs || q.X < 1646 && q.Y < 1670
			reachedSurface = reachedSurface || q.Z > 140
			if i > 0 {
				d := distance(route[i-1], q)
				length += d
				if d > 2 || math.Abs(float64(q.Z-route[i-1].Z)) > 1 {
					t.Fatal("physical stairs exceeded unchanged segment limits")
				}
			}
		}
		if !wentDownStairs || !reachedSurface || length < 80 || length > MaxRouteLength {
			t.Fatalf("crypt route skipped stairs or broke budget: points=%d length=%g", len(route), length)
		}
		t.Logf("offline crypt route points=%d length=%.5f", len(route), length)
	}
	if _, err := PlanContext(context.Background(), dir, 0, cryptStart, cryptSurface); err == nil {
		t.Fatal("legacy API admitted the Deathknell route")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := PlanRegionContext(context.Background(), dir, 0, RegionDeathknellCrypt, cryptStart, cryptSurface); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestPhysicalNamedRegionsUseSeparateMeshesAtSameAssetRoot(t *testing.T) {
	cryptDir, northDir := os.Getenv("BUDDY_NAV_DEATHKNELL_TEST_DATA_DIR"), os.Getenv("BUDDY_NAV_TEST_DATA_DIR")
	if cryptDir == "" || northDir == "" {
		t.Skip("independent Northshire and Deathknell static assets required")
	}
	isolatedRegionCache(t)
	dir := regionAssetDirectories(t)
	for _, source := range []string{northDir, cryptDir} {
		for _, sub := range []string{"mmaps", "maps", "vmaps"} {
			entries, err := os.ReadDir(filepath.Join(source, sub))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				target := filepath.Join(dir, sub, entry.Name())
				if _, err := os.Lstat(target); err == nil {
					continue
				}
				original, err := filepath.Abs(filepath.Join(source, sub, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, target); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	north, err := cachedPlannerRegion(dir, RegionNorthshire)
	if err != nil {
		t.Fatal(err)
	}
	crypt, err := cachedPlannerRegion(dir, RegionDeathknellCrypt)
	if err != nil {
		t.Fatal(err)
	}
	if north == crypt || north.mesh == crypt.mesh || north.query == crypt.query || north.floors == crypt.floors || north.gate == crypt.gate {
		t.Fatal("named regions share mutable planner state")
	}
	if north.region != RegionNorthshire || crypt.region != RegionDeathknellCrypt || north.floors.region != RegionNorthshire || crypt.floors.region != RegionDeathknellCrypt {
		t.Fatal("planner lost its sealed region policy")
	}
	alias := filepath.Join(t.TempDir(), "same-client-assets")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if again, err := cachedPlannerRegion(alias, RegionDeathknellCrypt); err != nil || again != crypt || len(planners.items) != 2 {
		t.Fatalf("canonical alias cache mismatch: %v", err)
	}
	if _, err := PlanRegionContext(context.Background(), dir, 0, RegionDeathknellCrypt, cryptStart, cryptSurface); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(dir, 0, Point{-8914.757, -131.393, 80.94807}, Point{-8902.59, -162.606, 82.0223}); err != nil {
		t.Fatalf("Northshire defaults changed with Deathknell assets present: %v", err)
	}
}
