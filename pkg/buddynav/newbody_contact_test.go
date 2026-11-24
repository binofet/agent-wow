package buddynav

import (
	"context"
	"testing"
	"time"
)

func TestBodyContactMeasuredFloorSlightlyAboveHeldStartKeepsOriginalPose(t *testing.T) {
	// Held numerical facts from the physical sampler, not a navmesh projection:
	// observed start92.13921, measured support92.13925, drop−3.814697e−05.
	start := Point{X: 10, Y: 20, Z: 92.13921}
	measured := float32(92.13925)
	floorReads, rays := 0, 0
	floor := func(q Point, above, below float32) (float32, bool, error) {
		floorReads++
		if floorReads == 1 && q != start {
			t.Fatal("the physical sampler did not receive the exact start", q, start)
		}
		return measured, measured <= q.Z+above && measured >= q.Z-below, nil
	}
	points, err := planBodyMotion(context.Background(), start, 0, 7, 100*time.Millisecond, false, floor, func(Point, Point) (bool, error) {
		rays++
		return true, nil
	})
	if err != nil || len(points) != 3 || points[0].Point != start || points[0].At != 0 {
		t.Fatal("normal contact rejected or replaced the exact observed start", points, err)
	}
	for _, p := range points[1:] {
		if p.Point.Z != measured || p.Settling || p.Point.X <= start.X || p.Point.Y != start.Y {
			t.Fatal("later WALK samples did not use actual measured support", p)
		}
	}
	if rays != 18 || floorReads != 3 {
		t.Fatal("contact bypassed the nine-ray corridor or physical samples", rays, floorReads)
	}
}

func TestBodyContactRejectsFloorAboveBandAndPreservesPositiveSettling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		drop     float32
		refuse   bool
		settling bool
	}{
		{"small support above", -.024, false, false},
		{"support above contact band", -.026, true, false},
		{"small support below", .024, false, false},
		{"positive gravity settling", .1, false, true},
		{"outside gravity envelope", float32(BodyGravity*BodyMotionHorizon.Seconds()*BodyMotionHorizon.Seconds()/2) + .1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := Point{Z: 100}
			floorZ := start.Z - tc.drop
			rays := 0
			points, err := planBodyMotion(context.Background(), start, 0, 7, 100*time.Millisecond, false,
				func(Point, float32, float32) (float32, bool, error) { return floorZ, true, nil },
				func(Point, Point) (bool, error) { rays++; return true, nil })
			if tc.refuse {
				if err == nil || len(points) != 0 || rays != 0 {
					t.Fatal("unsupported contact reached movement/collision execution", points, rays, err)
				}
				return
			}
			if err != nil || len(points) < 2 || points[0].Point != start || points[len(points)-1].Point.Z != floorZ || rays != 9*(len(points)-1) {
				t.Fatal("supported contact/settling changed", points, rays, err)
			}
			for _, p := range points {
				if p.Settling != tc.settling || tc.settling && (p.Point.X != start.X || p.Point.Y != start.Y) {
					t.Fatal("contact changed ordinary gravity-only settling", p)
				}
			}
		})
	}
}
