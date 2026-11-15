package buddynav

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestObservedSwimTrailValidatesEveryChord(t *testing.T) {
	points := []Point{{0, 0, -1}, {1, 2, -2}, {2, 4, -2}}
	var checked [][2]Point
	clear := func(_ context.Context, a, b Point) (bool, error) {
		checked = append(checked, [2]Point{a, b})
		return true, nil
	}
	if err := validateSwimTrail(context.Background(), points, worldPoint, clear); err != nil || len(checked) != 2 {
		t.Fatal(checked, err)
	}
	if checked[0][0].Z != points[0].Z+swimBodyRayHeight || checked[0][1] != checked[1][0] {
		t.Fatal("collision proof skipped a breadcrumb or failed to use body height", checked)
	}
	if err := validateSwimTrail(context.Background(), []Point{{0, 0, -1}, {6, 0, -1}}, worldPoint, clear); err == nil || !strings.Contains(err.Error(), "observation gap") {
		t.Fatal("accepted a long gap", err)
	}
	blocked := func(context.Context, Point, Point) (bool, error) { return false, nil }
	if err := validateSwimTrail(context.Background(), points, worldPoint, blocked); err == nil || !strings.Contains(err.Error(), "solid collision") {
		t.Fatal("accepted a blocked chord", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateSwimTrail(ctx, points, worldPoint, clear); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
}

// Optional replay of public client navigation geometry against the user's
// observed dive. The trace format exposes XYZ and normal movement flags only.
func TestBlackfathomDeepsObservedDiveTrail(t *testing.T) {
	dir, path := os.Getenv("BUDDY_NAV_BFD_TEST_DATA_DIR"), os.Getenv("BUDDY_NAV_BFD_DIVE_TEST_FILE")
	if dir == "" || path == "" {
		t.Skip("set the installed Blackfathom assets and observed swim trail")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var frames []struct {
		Point
		Flags uint32
	}
	if err := json.Unmarshal(data, &frames); err != nil {
		t.Fatal(err)
	}
	var points []Point
	for _, frame := range frames {
		if frame.Flags&0x00200000 == 0 {
			break
		}
		points = append(points, frame.Point)
	}
	if err := ValidateSwimTrailContext(context.Background(), dir, 48, points); err != nil {
		t.Fatalf("actual Blackfathom dive failed geometry proof for %d observations: %v", len(points), err)
	}
}
