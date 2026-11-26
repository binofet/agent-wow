package buddynav

import (
	"context"
	"math"
	"os"
	"testing"
	"time"
)

func TestDestinationWaterEntryCopiedMap(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("copied client navigation assets not supplied")
	}
	pool := Point{444.1598, 48.505165, -132.1898}
	for _, tc := range []struct {
		name     string
		from, to Point
	}{
		{"west", Point{474.95, 54.56, -96.19}, pool},
		{"north", Point{479.38, 49.15, -96.24}, pool},
		{"south", Point{479.77, 55.86, -96.26}, pool},
		{"east", Point{476.93, 55.58, -96.22}, pool},
		{"detour_to_bank", Point{503.7, 156.4, -94.05}, Point{387.66702, 30.071173, -130.93233}},
		{"offset", Point{482, 70, -96.31287}, pool},
		{"shelf", Point{469.89444, 50.35286, -110.44878}, pool},
		{"bank", Point{475.6561, 50.766533, -96.19031}, Point{391, 27.2, -130.93}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			r, err := PlanWaterEntryContext(context.Background(), dir, 349, tc.from, tc.to, 7)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("planning=%v approach=%d fall=%v onward=%d", time.Since(start), len(r.Approach), r.Fall[len(r.Fall)-1].At, len(r.Onward))
			if tc.name == "offset" && math.Abs(r.Bearing-math.Atan2(float64(tc.to.Y-tc.from.Y), float64(tc.to.X-tc.from.X))) < .2 {
				t.Fatal("obstructed direct heading selected", r.Bearing)
			}
			contact := r.Fall[len(r.Fall)-1]
			end := r.Onward[len(r.Onward)-1]
			if len(r.Approach) == 0 || distance(r.Approach[0], tc.from) > .025 || !contact.Swimming || contact.FallTime <= 0 || !r.Onward[0].Swimming || distance(contact.Point, r.Onward[0].Point) > .025 || distance(end.Point, tc.to) > EndpointTolerance || (tc.name == "bank" || tc.name == "detour_to_bank") == end.Swimming {
				t.Fatal("disconnected destination or incorrect contact medium", contact, end)
			}
		})
	}
}
