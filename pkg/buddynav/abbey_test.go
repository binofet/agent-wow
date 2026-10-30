package buddynav

import (
	"context"
	"math"
	"os"
	"testing"
)

// Optional acceptance uses independently extracted public client assets only.
// Fixtures cover the outside Abbey position and Brother Neals's observed floor.
// This does not contact the realm, issue movement, or claim live acceptance.
func TestPhysicalAbbeyTowerRouteBothWays(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_FLOOR_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_FLOOR_TEST_DATA_DIR to independent static Abbey assets")
	}
	willem, neals := Point{-8950, -131, 83.45}, Point{-8902.13, -181.646, 113.156204}
	for _, pair := range [][2]Point{{willem, neals}, {neals, willem}} {
		route, err := PlanContext(context.Background(), dir, 0, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(route) < 2 || len(route) > 256 || distance(route[0], pair[0]) > EndpointTolerance || distance(route[len(route)-1], pair[1]) > EndpointTolerance {
			t.Fatal("invalid tower endpoints or sample budget")
		}
		var length, maxStep, maxDz float64
		landing, upper := false, false
		for i, q := range route {
			landing = landing || q.Z > 97 && q.Z < 99
			upper = upper || q.Z > 112
			if i == 0 {
				continue
			}
			step, dz := distance(route[i-1], q), math.Abs(float64(q.Z-route[i-1].Z))
			length += step
			maxStep = math.Max(maxStep, step)
			maxDz = math.Max(maxDz, dz)
			if step > 2 || dz > 1 {
				t.Fatal("tower path exceeded physical step limits", step, dz)
			}
		}
		if length <= 180 || length > 240 || !landing || !upper {
			t.Fatal("tower fixture did not exercise the extended complete stair route", length, landing, upper)
		}
		t.Logf("from=%+v to=%+v length=%.5f samples=%d maxStep=%.5f maxDz=%.5f", pair[0], pair[1], length, len(route), maxStep, maxDz)
	}
}
