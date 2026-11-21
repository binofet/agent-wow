package buddynav

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBodyProbeUsesPhysicalSamplesWithoutAFullRouteAndHopsAReachableLedge(t *testing.T) {
	floor := func(q Point, above, below float32) (float32, bool, error) {
		h := float32(0)
		if q.X >= 2 {
			h = 1.05
		}
		return h, h <= q.Z+above && h >= q.Z-below, nil
	}
	clear := func(Point, Point) (bool, error) { return true, nil }
	if _, err := planBodyMotion(context.Background(), Point{}, 0, 7, 500*time.Millisecond, false, floor, clear); err == nil {
		t.Fatal("ground input crossed a ledge")
	}
	points, err := planBodyMotion(context.Background(), Point{}, 0, 7, 0, true, floor, clear)
	if err != nil || len(points) < 3 || points[len(points)-1].Point.Z != 1.05 || points[len(points)-1].At >= time.Second {
		t.Fatal("normal hop did not land on reachable physical floor", points, err)
	}
	for _, sample := range points {
		if sample.Point.X > 7*float32(sample.At.Seconds())+.001 {
			t.Fatal("probe exceeded observed speed", sample)
		}
	}
}

func TestBodyProbeRejectsWallMissingFloorAndUnreachableLanding(t *testing.T) {
	flat := func(q Point, above, below float32) (float32, bool, error) { return 0, true, nil }
	for _, mode := range []string{"wall", "missing_floor", "high_floor"} {
		t.Run(mode, func(t *testing.T) {
			floor := flat
			clear := func(Point, Point) (bool, error) { return true, nil }
			switch mode {
			case "wall":
				clear = func(Point, Point) (bool, error) { return false, nil }
			case "missing_floor":
				floor = func(Point, float32, float32) (float32, bool, error) { return 0, false, errors.New("missing") }
			case "high_floor":
				floor = func(q Point, a, b float32) (float32, bool, error) {
					if q.X > 1 {
						return 3, true, nil
					}
					return 0, true, nil
				}
			}
			if _, err := planBodyMotion(context.Background(), Point{}, 0, 7, 0, true, floor, clear); err == nil {
				t.Fatal("unproved body probe was accepted")
			}
		})
	}
}
