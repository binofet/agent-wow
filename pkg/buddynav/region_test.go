package buddynav

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var cryptStart = Point{1674.8425, 1678.3024, 121.3337}
var cryptSurface = Point{1681.1973, 1671.4958, 136.40988}

func TestNamedRegionsRejectOtherRegionAndUnknownPolicies(t *testing.T) {
	for _, tc := range []struct {
		region     Region
		start, end Point
	}{
		{RegionNorthshire, cryptStart, cryptSurface},
		{RegionDeathknellCrypt, testStart, testEnd},
		{RegionDeathknellCrypt, cryptStart, Point{1843.32, 1639.9, 97.8113}},
		{Region(3), cryptStart, cryptSurface},
		{Region(255), testStart, testEnd},
	} {
		if _, err := PlanRegionContext(context.Background(), "", 0, tc.region, tc.start, tc.end); err == nil || strings.Contains(err.Error(), "directory") {
			t.Fatalf("policy must fail before reading assets: %+v: %v", tc, err)
		}
	}
	if _, err := Plan("", 0, cryptStart, cryptSurface); err == nil || !strings.Contains(err.Error(), "Northshire") {
		t.Fatalf("legacy Plan admitted crypt: %v", err)
	}
	for _, region := range []Region{RegionNorthshire, RegionDeathknellCrypt} {
		start, end := testStart, testEnd
		if region == RegionDeathknellCrypt {
			start, end = cryptStart, cryptSurface
		}
		if err := validateRegionRequest(0, region, start, end); err != nil {
			t.Fatal(err)
		}
		if err := validateRegionRequest(1, region, start, end); err == nil {
			t.Fatal("region admitted map1")
		}
		start.X = float32(math.NaN())
		if err := validateRegionRequest(0, region, start, end); err == nil {
			t.Fatal("region admitted nonfinite endpoint")
		}
	}
}

func TestCryptRawAndNormalizedPointsCannotEscapeRegion(t *testing.T) {
	a, b := Point{1670, 1670, 130}, Point{1673, 1670, 130}
	for _, outside := range []Point{{1641, 1670, 130}, {1671, 1682, 130}, {1671, 1670, 146}} {
		f := fakeRoute(a, outside, b)
		if _, err := planRegion(context.Background(), f, RegionDeathknellCrypt, a, b); err == nil || len(f.samples) != 0 {
			t.Fatalf("raw escape accepted or sampled: point=%+v err=%v", outside, err)
		}
	}
	for _, height := range []struct{ mesh, floor float32 }{{144.8, 145.1}, {118.2, 117.9}} {
		start, end := a, b
		start.Z, end.Z = height.mesh, height.mesh
		f := fakeRoute(start, end)
		f.floorFn = func(Point, float32, float32) (float32, error) { return height.floor, nil }
		if _, err := planRegion(context.Background(), f, RegionDeathknellCrypt, start, end); err == nil || !strings.Contains(err.Error(), "Deathknell crypt bounds") {
			t.Fatalf("normalized floor escaped region: %+v: %v", height, err)
		}
	}
	f := fakeRoute(a, b)
	if _, err := planRegion(context.Background(), f, Region(255), a, b); err == nil || len(f.samples) != 0 {
		t.Fatal("unknown region reached geometry sampling")
	}
}

func TestNamedRegionTileAllowlistsAreExactAndIndependent(t *testing.T) {
	for _, tc := range []struct {
		region Region
		meshes []string
		floors int
	}{
		{RegionNorthshire, []string{"0004731.mmtile", "0004732.mmtile", "0004733.mmtile", "0004831.mmtile", "0004832.mmtile", "0004833.mmtile", "0004931.mmtile", "0004932.mmtile", "0004933.mmtile"}, 9},
		{RegionDeathknellCrypt, []string{"0002828.mmtile"}, 1},
	} {
		tiles, err := tc.region.meshTiles()
		if err != nil || !reflect.DeepEqual(tiles, tc.meshes) {
			t.Fatalf("unexpected mesh policy: %v %v", tiles, err)
		}
		tiles[0] = "caller-mutation"
		again, _ := tc.region.meshTiles()
		if !reflect.DeepEqual(again, tc.meshes) {
			t.Fatal("tile policy shares mutable storage")
		}
		count := 0
		for x := 0; x < 64; x++ {
			for y := 0; y < 64; y++ {
				if tc.region.allowsFloorTile(fmt.Sprintf("000_%02d_%02d.vmtile", x, y)) {
					count++
				}
			}
		}
		if count != tc.floors {
			t.Fatalf("floor whitelist region=%d count=%d", tc.region, count)
		}
	}
	if RegionNorthshire.allowsFloorTile("000_28_28.vmtile") || RegionDeathknellCrypt.allowsFloorTile("000_32_48.vmtile") || Region(255).allowsFloorTile("000_28_28.vmtile") {
		t.Fatal("region admitted another or forged collision tile")
	}
	for _, bad := range []string{"../000_28_28.vmtile", "001_28_28.vmtile", "000_28_29.vmtile"} {
		if RegionDeathknellCrypt.allowsFloorTile(bad) {
			t.Fatalf("unsafe floor name accepted: %s", bad)
		}
	}
}

func TestRegionFloorReadersFailClosedBeforeOtherTileReads(t *testing.T) {
	for _, tc := range []struct {
		region Region
		point  Point
	}{
		{RegionNorthshire, cryptStart}, {RegionDeathknellCrypt, testStart}, {Region(255), cryptStart},
	} {
		f := newFloorReaderRegion(t.TempDir(), tc.region)
		if _, known, err := f.height(tc.point, tc.point.Z); err == nil || known || strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("floor policy did not reject before asset read: %+v: %v", tc, err)
		}
	}
}

func regionAssetDirectories(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"mmaps", "maps", "vmaps"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func isolatedRegionCache(t *testing.T) {
	t.Helper()
	planners.Lock()
	previous := planners.items
	planners.items = make(map[plannerCacheKey]*planner)
	planners.Unlock()
	t.Cleanup(func() {
		planners.Lock()
		planners.items = previous
		planners.Unlock()
	})
}

func TestRegionCacheUsesCanonicalDirectoryAndSeparatePolicy(t *testing.T) {
	isolatedRegionCache(t)
	dir := regionAssetDirectories(t)
	alias := filepath.Join(t.TempDir(), "asset-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	north, crypt := &planner{region: RegionNorthshire}, &planner{region: RegionDeathknellCrypt}
	planners.items[plannerCacheKey{dir, RegionNorthshire}] = north
	planners.items[plannerCacheKey{dir, RegionDeathknellCrypt}] = crypt
	for _, path := range []string{dir, alias, filepath.Join(alias, ".")} {
		for region, want := range map[Region]*planner{RegionNorthshire: north, RegionDeathknellCrypt: crypt} {
			got, err := cachedPlannerRegion(path, region)
			if err != nil || got != want {
				t.Fatalf("cache crossed region/directory identity: path=%s region=%d err=%v", path, region, err)
			}
		}
	}
	if got, err := cachedPlanner(alias); err != nil || got != north {
		t.Fatal("legacy cache wrapper stopped defaulting to Northshire")
	}
	if len(planners.items) != 2 {
		t.Fatal("alias directory consumed a second cache slot")
	}
	other := regionAssetDirectories(t)
	for _, region := range []Region{RegionNorthshire, RegionDeathknellCrypt} {
		planners.items[plannerCacheKey{other, region}] = &planner{region: region}
	}
	if _, err := cachedPlannerRegion(regionAssetDirectories(t), RegionDeathknellCrypt); err == nil || !strings.Contains(err.Error(), "cache limit") {
		t.Fatalf("four-entry total cache cap not retained: %v", err)
	}
}

func TestRegionMeshLoaderRejectsMissingCorruptAndUnlistedAssets(t *testing.T) {
	for _, region := range []Region{RegionNorthshire, RegionDeathknellCrypt} {
		t.Run(fmt.Sprintf("region%d", region), func(t *testing.T) {
			dir := t.TempDir()
			params := make([]byte, 28)
			for i, v := range []float32{0, 0, 0, 533.3333, 533.3333} {
				binary.LittleEndian.PutUint32(params[i*4:], math.Float32bits(v))
			}
			binary.LittleEndian.PutUint32(params[20:], 1)
			binary.LittleEndian.PutUint32(params[24:], 128)
			if err := os.WriteFile(filepath.Join(dir, "000.mmap"), params, 0600); err != nil {
				t.Fatal(err)
			}
			other := "0002828.mmtile"
			if region == RegionDeathknellCrypt {
				other = "0004832.mmtile"
			}
			if err := os.WriteFile(filepath.Join(dir, other), []byte("not a mesh"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadMeshRegion(dir, region); err == nil || !strings.Contains(err.Error(), "tiles are missing") {
				t.Fatalf("unlisted tile was read or admitted: %v", err)
			}
			tiles, _ := region.meshTiles()
			if err := os.WriteFile(filepath.Join(dir, tiles[0]), validTileHeader()[:16], 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadMeshRegion(dir, region); err == nil {
				t.Fatal("truncated region mesh accepted")
			}
		})
	}
}
