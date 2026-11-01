package buddynav

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
)

func TestPlanLegSharesExistingRecoveryBoundsAndCancellation(t *testing.T) {
	if LegMaxTargetDistance != RecoveryMaxTargetDistance || LegMaxLength != RecoveryMaxLegLength || MaxRouteLength != 240 {
		t.Fatal("generic guidance changed existing movement budgets")
	}
	start := Point{-8900, -100, 90}
	for _, goal := range []Point{{-8890, -100, 90}, {-7699, -100, 90}, {float32(math.NaN()), 0, 0}} {
		for _, canceled := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			if canceled {
				cancel()
			}
			_, generic := PlanLegContext(ctx, t.TempDir(), 0, start, goal)
			_, recovery := PlanRecoveryLegContext(ctx, t.TempDir(), 0, start, goal)
			cancel()
			if generic == nil || recovery == nil || errors.Is(generic, context.Canceled) != errors.Is(recovery, context.Canceled) {
				t.Fatal("missing proof or cancellation changed", generic, recovery)
			}
		}
	}
}

// This is a static recorded client pose and goal-data reference, not evidence
// of live NPC presence, quest eligibility, movement or arrival.
func TestPlanLegRecordedStormwindHandInAndNormalReplan(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_STORMWIND_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set copied client Stormwind assets")
	}
	start, goal := Point{-8849.944, 725.56256, 97.61866}, Point{-8884.88, 675.547, 100.263}
	path, err := PlanLegContext(context.Background(), dir, 0, start, goal)
	if err != nil {
		t.Fatal(err)
	}
	w, err := newRecoveryWindow(0, start, goal)
	if err != nil || !validRecoveryLeg(path, w, start, path[len(path)-1], goal) || distance(path[len(path)-1], goal) > EndpointTolerance {
		t.Fatal("leg lost endpoint or normal physical bounds", path, err)
	}
	end := path[len(path)-1]
	replanned, err := PlanMapContext(context.Background(), dir, 0, start, end)
	if err != nil || len(replanned) < 2 || distance(replanned[len(replanned)-1], end) > EndpointTolerance {
		t.Fatal("normal movement cannot independently replan endpoint", replanned, err)
	}
	var length float64
	for i := 1; i < len(path); i++ {
		length += distance(path[i-1], path[i])
	}
	t.Logf("static verified leg points=%d length=%.3f endpoint=%+v normalReplanPoints=%d", len(path), length, end, len(replanned))
}
