package buddynav

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestCopiedKharanosInnGroundApproach(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_INN_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_INN_TEST_DATA_DIR to copied client navigation assets")
	}
	ctx := context.Background()
	physician := Point{-5604.49, -521.427, 399.741}
	for _, tc := range []struct {
		name  string
		start Point
	}{
		{"village", Point{-5595.5776, -483.8108, 396.99075}},
		{"entrance", Point{-5583.1997, -520.5333, 400.76227}},
		{"stalled_leg", Point{-5583.3784, -520.7121, 400.76227}},
		{"upper_exterior", Point{-5602.415, -512.66943, 401.56863}},
		{"outside_door", Point{-5583.468, -516.7897, 402.35635}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateMapCache(t)
			path, err := PlanLegContext(ctx, dir, 0, tc.start, physician)
			if err != nil || len(path) < 2 || len(path) > MaxPoints || groundRouteLength(path) > LegMaxLength {
				t.Fatalf("normal ground leg returned %d points: %v", len(path), err)
			}
			end := path[len(path)-1]
			if distance(tc.start, path[0]) > EndpointTolerance || distance(end, physician) > 3 {
				t.Fatal("incomplete approach", path[0], end)
			}
			for i := 1; i < len(path); i++ {
				a, b := path[i-1], path[i]
				if distance(a, b) > 2 || math.Abs(float64(a.Z-b.Z)) > 1 {
					t.Fatal("unsupported step", a, b)
				}
				bearing := math.Atan2(float64(b.Y-a.Y), float64(b.X-a.X))
				if e := clearBodyMotionSegment(a, b, bearing, func(p, q Point) (bool, error) { return LineOfSightContext(ctx, dir, 0, p, q) }); e != nil {
					t.Fatalf("static collision on segment %d: %v", i, e)
				}
			}
			path, err = PlanInteractionContext(ctx, dir, 0, tc.start, physician)
			if err != nil || !validInteractionRoute(path, tc.start, physician, physician) {
				t.Fatal("ordinary interaction approach", len(path), err)
			}
			eye, other := path[len(path)-1], physician
			eye.Z += 1.5
			other.Z += 1.5
			visible, err := LineOfSightContext(ctx, dir, 0, eye, other)
			if err != nil || !visible {
				t.Fatal("interaction line of sight", eye, other, err)
			}
		})
	}
}
