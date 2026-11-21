package buddynav

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestBodyProbeSettlesFromMeasuredUnsupportedStartThenCanHop(t *testing.T) {
	const measuredFloor float32 = 8.26
	start := Point{X: 1784, Y: -406, Z: 9.382388}
	floorQueries := 0
	floor := func(q Point, above, below float32) (float32, bool, error) {
		floorQueries++
		return measuredFloor, measuredFloor <= q.Z+above && measuredFloor >= q.Z-below, nil
	}
	clear := func(Point, Point) (bool, error) { return true, nil }
	points, err := planBodyMotion(context.Background(), start, 0, 7, 200*time.Millisecond, false, floor, clear)
	if err != nil || len(points) < 3 {
		t.Fatal("measured floor below unsupported start was not settled", points, err)
	}
	if points[0].Point != start || points[0].At != 0 || floorQueries == 0 {
		t.Fatal("settling replaced the actual starting pose or omitted physical measurement", points)
	}
	descending := 0
	for i, sample := range points {
		if sample.Point.Z < measuredFloor-.001 {
			t.Fatal("settling penetrated the measured physical floor", sample)
		}
		if i == 0 || sample.Point.Z >= points[i-1].Point.Z || sample.Point.Z <= measuredFloor+.001 {
			continue
		}
		descending++
		expected := float64(start.Z) - BodyGravity*sample.At.Seconds()*sample.At.Seconds()/2
		if math.Abs(float64(sample.Point.Z)-expected) > .003 {
			t.Fatal("unsupported pose was snapped instead of following normal gravity", sample, expected)
		}
	}
	landed := points[len(points)-1]
	if descending < 2 || landed.Point.Z != measuredFloor || landed.At <= 0 {
		t.Fatal("no normal descent and supported landing were produced", points)
	}
	// A later caller-selected hop must use the locally settled pose. The
	// planner gets that exact result, not a synthetic authoritative revision.
	hop, err := planBodyMotion(context.Background(), landed.Point, 0, 7, 0, true, floor, clear)
	if err != nil || len(hop) < 3 || hop[0].Point != landed.Point || hop[len(hop)-1].Point.Z != measuredFloor {
		t.Fatal("settled body could not perform a later ordinary hop and landing", hop, err)
	}
	rises := false
	for _, sample := range hop {
		rises = rises || sample.Point.Z > measuredFloor+.1
	}
	if !rises {
		t.Fatal("later hop did not leave the measured floor", hop)
	}
}

func TestBodyProbeUnsupportedStartDoesNotInventMissingFloor(t *testing.T) {
	missing := func(Point, float32, float32) (float32, bool, error) { return 0, false, nil }
	clear := func(Point, Point) (bool, error) { return true, nil }
	if points, err := planBodyMotion(context.Background(), Point{Z: 9.382388}, 0, 7, 200*time.Millisecond, false, missing, clear); err == nil {
		t.Fatal("unknown floor was converted to a landing", points)
	}
}
