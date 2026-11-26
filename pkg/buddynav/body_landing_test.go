package buddynav

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func ledgeFloor(levels ...float32) bodyFloor {
	return func(p Point, above, below float32) (float32, bool, error) {
		if p.X <= .71 {
			if p.Z+above >= 0 && p.Z-below <= 0 {
				return 0, true, nil
			}
		}
		for _, h := range levels {
			if h <= p.Z+above && h >= p.Z-below {
				return h, true, nil
			}
		}
		return 0, false, nil
	}
}

func ledgeWater(surface float32, deep bool) bodyWater {
	return func(p Point, top, bottom float32) (float32, bool, error) {
		return surface - swimImmersion, deep && p.X > .71 && surface <= top && surface >= bottom, nil
	}
}

func TestBodyLandingMeasuredWalkOffAndJump(t *testing.T) {
	clear := func(Point, Point) (bool, error) { return true, nil }
	for _, scenario := range []struct {
		name  string
		jump  bool
		depth float32
	}{
		{"walk", false, 34}, {"jump", true, 34}, {"terminal_velocity", false, 160}, {"deep_vertical_pool", false, 700},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			checked := 0
			clearChord := func(a, b Point) (bool, error) {
				checked++
				if distance(a, b) > 1.01 {
					t.Fatal("unchecked geometry gap", a, b)
				}
				return true, nil
			}
			points, err := planBodyInput(context.Background(), Point{}, 0, 7, 500*time.Millisecond, scenario.jump,
				ledgeFloor(-scenario.depth-12), clearChord, ledgeWater(-scenario.depth, true))
			if err != nil {
				t.Fatal(err)
			}
			last := points[len(points)-1]
			if checked < 9*len(points) || len(points) > BodyLandingSampleLimit(7) || !BodyFlightInRange(last.FallTime, 7, 0) {
				t.Fatal("invalid collision or report budget", checked, len(points), last)
			}
			if !last.Swimming || last.FallTime <= BodyMotionHorizon || last.Point.Z != -scenario.depth-swimImmersion || math.Abs(float64(last.Point.X)-7*last.At.Seconds()) > .001 {
				t.Fatal("not a physical first water contact", last)
			}
			initial := 0.
			if scenario.jump {
				initial = BodyJumpVelocity
			}
			if math.Abs(bodyElevation(last.FallTime.Seconds(), initial)-float64(last.Point.Z)) > .002 {
				t.Fatal("wrong gravity flight time", last)
			}
			for i := 1; i < len(points); i++ {
				p, prev := points[i], points[i-1]
				if p.At <= prev.At || p.At-prev.At > 2*bodyWaterReportInterval || i < len(points)-1 && p.Swimming {
					t.Fatal("discontinuous fall", p, prev)
				}
			}
		})
	}
	if p, err := planBodyMotion(context.Background(), Point{}, 0, 7, 500*time.Millisecond, false, ledgeFloor(-50), clear); err == nil || p != nil {
		t.Fatal("ordinary ground probe was changed into a cliff crossing", p, err)
	}
}

func TestBodyLandingFirstSolidContactAndBoundaries(t *testing.T) {
	clear := func(Point, Point) (bool, error) { return true, nil }
	for _, tc := range []struct {
		name  string
		floor bodyFloor
		water bodyWater
		short bool
	}{
		{"solid_shelf_before_deep_water", ledgeFloor(-17, -60), ledgeWater(-40, true), false},
		{"shallow_surface_over_bed", ledgeFloor(-30.4, -65), ledgeWater(-30, false), false},
		{"bare_dry_floor", ledgeFloor(-22), ledgeWater(-50, false), false},
		{"safe_solid_before_pool", ledgeFloor(-2, -60), ledgeWater(-40, true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, jump := range []bool{false, true} {
				p, err := planBodyInput(context.Background(), Point{}, 0, 7, 500*time.Millisecond, jump, tc.floor, clear, tc.water)
				if tc.short {
					if err != nil || len(p) == 0 || p[len(p)-1].Swimming || p[len(p)-1].Point.Z != -2 || p[len(p)-1].FallTime > BodyMotionHorizon {
						t.Fatalf("short solid landing: %v %v", p, err)
					}
				} else if p != nil || err == nil || !strings.Contains(err.Error(), "first contact is solid ground") {
					t.Fatal("admitted a long dry first contact", p, err)
				}
			}
		})
	}
	blocked := func(a, b Point) (bool, error) { return b.X <= 5, nil }
	if p, err := planBodyInput(context.Background(), Point{}, 0, 7, 500*time.Millisecond, false, ledgeFloor(-80), blocked, ledgeWater(-34, true)); err == nil || p != nil {
		t.Fatal("crossed a wall", p, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := planBodyInput(ctx, Point{}, 0, 7, 500*time.Millisecond, true, ledgeFloor(-80), clear, ledgeWater(-34, true)); err != context.Canceled || p != nil {
		t.Fatal("ignored cancellation", p, err)
	}
	if p, err := planBodyInput(context.Background(), Point{}, 0, 7, 500*time.Millisecond, false, ledgeFloor(), clear, ledgeWater(-5000, true)); err == nil || p != nil {
		t.Fatal("left measured spatial envelope", p, err)
	}
}

// Optional offline replay against copied client navigation assets.
func TestBodyLandingMaraudonMeasuredLedge(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("copied client navigation assets not supplied")
	}
	from := Point{477.82443, 51.545467, -96.227425}
	bearing := math.Atan2(40.353012-float64(from.Y), 446.6661-float64(from.X))
	for pass := 0; pass < 2; pass++ {
		p, err := PlanBodyLandingContext(context.Background(), root, 349, from, bearing, 7, 500*time.Millisecond, false)
		if err != nil {
			t.Fatal(err)
		}
		last := p[len(p)-1]
		if pass == 0 {
			if last.Swimming || last.FallTime != 0 || math.Abs(float64(last.Point.X)-474.5305) > .02 {
				t.Fatal("upper floor approach", last)
			}
			from = last.Point
		} else if !last.Swimming || last.FallTime < 1800*time.Millisecond || last.FallTime > 2*time.Second || math.Abs(float64(last.Point.Z)+130.951) > .1 {
			t.Fatal("pool contact", last)
		}
	}
}

func TestBodyLandingCrossesBelowWalkwayButCannotEnterRisingTerrain(t *testing.T) {
	for _, ridge := range []bool{false, true} {
		const roof = float32(-4)
		covered := func(x float32) bool { return x >= 8 && x <= 18 }
		floor := func(p Point, above, below float32) (float32, bool, error) {
			ground := float32(-110)
			if p.X <= .71 {
				ground = 0
			}
			if ridge && p.X >= 7.73 && p.X < 16 {
				ground = min(-2, float32(-110+(p.X-7.73)*26))
			}
			heights := []float32{ground}
			if covered(p.X) {
				heights = append([]float32{roof}, heights...)
			}
			for _, h := range heights {
				if h <= p.Z+above && h >= p.Z-below {
					return h, true, nil
				}
			}
			return 0, false, nil
		}
		clear := func(a, b Point) (bool, error) {
			if a.Z == b.Z || (a.Z-roof)*(b.Z-roof) > 0 {
				return true, nil
			}
			x := a.X + (b.X-a.X)*(roof-a.Z)/(b.Z-a.Z)
			return !covered(x), nil
		}
		water := func(p Point, top, bottom float32) (float32, bool, error) {
			return -88 - swimImmersion, p.X > 18 && top >= -88 && bottom <= -88, nil
		}
		points, err := planBodyInput(context.Background(), Point{}, 0, 7, 500*time.Millisecond, false, floor, clear, water)
		if ridge && (points != nil || err == nil) {
			t.Fatal("water path crossed solid rising terrain", points, err)
		}
		if !ridge && (err != nil || len(points) == 0 || !points[len(points)-1].Swimming) {
			t.Fatal("clear lower passage beneath a walkway was blocked", err)
		}
	}
}

func TestReleasedGravityKeepsFallAgeAndMeetsFirstSolid(t *testing.T) {
	clear := func(Point, Point) (bool, error) { return true, nil }
	from := Point{X: 2, Z: -8}
	for _, age := range []time.Duration{0, 700 * time.Millisecond} {
		p, err := traceBodyFall(context.Background(), []BodyMotionSample{{Point: from, Settling: true, FallTime: age}}, 0, 0, 0, age, true, ledgeFloor(-45, -90), clear, ledgeWater(-72, true))
		if err != nil {
			t.Fatal(err)
		}
		last := p[len(p)-1]
		if last.Point != (Point{X: 2, Z: -45}) || last.Swimming || last.FallTime != age+last.At || !last.Settling {
			t.Fatal("lost solid or original age", age, last)
		}
		for _, q := range p {
			if q.Point.X != from.X || q.Point.Y != from.Y {
				t.Fatal("retained unrequested horizontal input", q)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := traceBodyFall(ctx, []BodyMotionSample{{Point: from, Settling: true}}, 0, 0, 0, 0, true, ledgeFloor(-45), clear, ledgeWater(-72, true)); err == nil {
		t.Fatal("canceled gravity proceeded")
	}
}

func TestReleasedGravityMaraudonStandingOverShelf(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to copied client geometry")
	}
	from := Point{469.89444, 50.35286, -99.962814}
	p, err := PlanBodyGravityContext(context.Background(), root, 349, from, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := p[len(p)-1]
	if last.Swimming || last.Point.X != from.X || last.Point.Y != from.Y || math.Abs(float64(last.Point.Z+110.44878)) > .025 {
		t.Fatal("missed measured contact above pool", last)
	}
}
