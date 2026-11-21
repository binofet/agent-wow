package buddynav

import (
	"context"
	"math"
	"testing"
	"time"
)

func envelopeFloor(height func(Point) float32) func(Point, float32, float32) (float32, bool, error) {
	return func(q Point, above, below float32) (float32, bool, error) {
		h := height(q)
		return h, h <= q.Z+above && h >= q.Z-below, nil
	}
}

func TestBodyProbeGroundSamplesFollowCumulativeMeasuredSlope(t *testing.T) {
	floor := envelopeFloor(func(q Point) float32 { return -.8 * q.X })
	points, err := planBodyMotion(context.Background(), Point{}, 0, 7, 500*time.Millisecond, false, floor, func(Point, Point) (bool, error) { return true, nil })
	if err != nil || len(points) < 3 {
		t.Fatal("continuous measured slope was refused against the original start height", points, err)
	}
	for i, sample := range points {
		if math.Abs(float64(sample.Point.Z+.8*sample.Point.X)) > .001 || sample.Settling {
			t.Fatal("ground sample left measured support or became a fall", sample)
		}
		if i > 0 && math.Abs(float64(sample.Point.Z-points[i-1].Point.Z)) > .5 {
			t.Fatal("test slope contains an unsupported individual step", sample)
		}
	}
	if points[len(points)-1].Point.Z >= -1 {
		t.Fatal("fixture failed to exercise cumulative descent beyond one yard", points)
	}
}

func TestBodyProbeHopQueriesWholeFiniteLandingEnvelope(t *testing.T) {
	const lowerFloor float32 = -1.3703375
	largestGap := float32(0)
	floor := envelopeFloor(func(q Point) float32 {
		if q.X == 0 {
			return 0
		}
		if q.Z-lowerFloor > largestGap {
			largestGap = q.Z - lowerFloor
		}
		return lowerFloor
	})
	points, err := planBodyMotion(context.Background(), Point{}, 0, 7, 0, true, floor, func(Point, Point) (bool, error) { return true, nil })
	if err != nil || len(points) < 3 {
		t.Fatal("reachable lower landing was hidden by a fixed apex query depth", points, err)
	}
	last := points[len(points)-1]
	if last.Point.Z != lowerFloor || last.At > BodyMotionHorizon || largestGap <= 3 {
		t.Fatal("hop did not exercise the measured 3.009-yard apex gap and lower landing", points, largestGap)
	}
	if math.Abs(float64(largestGap)-3.0092678) > .002 {
		t.Fatal("fixture no longer reproduces the measured apex depth", largestGap)
	}
}

func TestBodyProbeRefusesLandingOutsideNormalFlightHorizon(t *testing.T) {
	seconds := BodyMotionHorizon.Seconds()
	outside := float32(BodyJumpVelocity*seconds-BodyGravity*seconds*seconds/2) - .5
	floor := envelopeFloor(func(q Point) float32 {
		if q.X == 0 {
			return 0
		}
		return outside
	})
	if points, err := planBodyMotion(context.Background(), Point{}, 0, 7, 0, true, floor, func(Point, Point) (bool, error) { return true, nil }); err == nil {
		t.Fatal("finite floor measurement authorized a landing outside normal flight time", points, outside)
	}
}

func TestBodyProbeExpandedFloorEnvelopeStillChecksCeilingCollision(t *testing.T) {
	floor := envelopeFloor(func(Point) float32 { return 0 })
	ceiling := func(a, b Point) (bool, error) { return math.Max(float64(a.Z), float64(b.Z)) <= 1.9, nil }
	if points, err := planBodyMotion(context.Background(), Point{}, 0, 7, 0, true, floor, ceiling); err == nil {
		t.Fatal("deeper floor search bypassed measured body/ceiling collision", points)
	}
}
