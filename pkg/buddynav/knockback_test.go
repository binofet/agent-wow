package buddynav

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCopiedImmersedKnockbackCannotClaimDryLakebed(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to copied client geometry")
	}
	start := Point{X: 446.6661, Y: 40.353012, Z: -132.54245}
	// This impulse stays under the surface. The air trajectory would otherwise
	// fall through the water column and report the solid lakebed as dry.
	if path, err := PlanKnockbackContext(context.Background(), dir, 349, start, 0, 0, 3); err == nil || !strings.Contains(err.Error(), "submerged") || len(path) != 0 {
		t.Fatal(path, err)
	}
	path, err := PlanKnockbackContext(context.Background(), dir, 349, start, 0, 0, 9)
	if err != nil || len(path) < 2 || !path[len(path)-1].Swimming || path[len(path)-1].Point.Z <= start.Z {
		t.Fatal("measured emergence and reentry", path, err)
	}
}
