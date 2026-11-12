package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
)

// Recorded client poses and nearby formation destinations exercise Stormwind's
// raised WMO floor. They are static geometry fixtures, not live target authority.
func TestStormwindUnsupportedFloorDetours(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_STORMWIND_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set independent client Stormwind-regression assets")
	}
	start := Point{-8803.106, 746.7388, 97.5528}
	for _, goal := range []Point{{-8790.543, 764.86505, 96.338326}, {-8800.543, 764.86505, 96.338326}} {
		t.Run(fmt.Sprint(goal), func(t *testing.T) {
			w, err := newMapWindow(0, start, goal)
			if err != nil {
				t.Fatal(err)
			}
			func() {
				<-currentMapPlanners.gate
				defer func() { currentMapPlanners.gate <- struct{}{} }()
				p, err := cachedMapPlanner(context.Background(), dir, w)
				if err != nil {
					t.Fatal(err)
				}
				// The original mesh-only corridor must still fail independent
				// floor proof; accepting it is not this regression's remedy.
				_, err = planWithin(context.Background(), p, w.contains, "static Stormwind fixture", start, goal)
				var gap *physicalFloorGap
				if !errors.As(err, &gap) {
					t.Fatal("original corridor no longer reproduces unsupported floor", err)
				}
			}()
			path, err := PlanMapContext(context.Background(), dir, 0, start, goal)
			if err != nil {
				t.Fatal(err)
			}
			var length float64
			for i := 1; i < len(path); i++ {
				step := distance(path[i-1], path[i])
				length += step
				if step > 2 || math.Abs(float64(path[i].Z-path[i-1].Z)) > 1 {
					t.Fatal("detour relaxed physical segment bounds", path[i-1], path[i])
				}
			}
			if len(path) < 2 || len(path) > MaxPoints || length > MaxRouteLength || distance(path[0], start) > 2 || distance(path[len(path)-1], goal) > 2 {
				t.Fatal("detour relaxed route or endpoint bounds", path)
			}
			t.Logf("physically verified detour: points=%d length=%.3f", len(path), length)
		})
	}
	// This goal itself has no supporting floor. Blocking a corridor must never
	// manufacture a replacement endpoint or snap down to terrain 38yd below.
	_, err := PlanMapContext(context.Background(), dir, 0, start, Point{-8798.242, 753.8619, 97.151886})
	if err == nil {
		t.Fatal("accepted unsupported destination")
	}
}
