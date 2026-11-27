package buddynav

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/o0olele/detour-go/detour"
)

func TestGroundCorridorSpliceKeepsOrderedSharedPortal(t *testing.T) {
	prefix := []detour.DtPolyRef{1, 2, 3, 4}
	suffix := []detour.DtPolyRef{4, 8, 2, 7}
	got := joinGroundStrips(prefix, suffix)
	if !slices.Equal(got, []detour.DtPolyRef{1, 2, 7}) || !slices.Equal(prefix, []detour.DtPolyRef{1, 2, 3, 4}) {
		t.Fatalf("joined %v, prefix %v", got, prefix)
	}
}

func TestRetainedGroundTripAndDisplacement(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("requires installed build 12340 map assets")
	}
	ctx := context.Background()
	start := Point{-5248.4316, -583.0111, 409.1023}
	goal := Point{-5421.91015625, -2930.010009765625, 347.25}
	c, err := PlanGroundCorridorContext(ctx, root, 0, start, goal)
	if err != nil {
		t.Fatal(err)
	}
	search := c.search
	if c.PolygonCount < 2 {
		t.Fatal("expected a multi-tile polygon route")
	}
	cur := start
	legs := 0
	length := 0.
	for distance(cur, c.Destination()) > EndpointTolerance {
		prevCount := c.PolygonCount
		points, err := c.PathAhead(ctx, cur, goal)
		if err != nil || len(points) < 2 || distance(cur, points[0]) > EndpointTolerance {
			t.Fatal(legs, cur, err, len(points))
		}
		if c.search != search || c.PolygonCount > prevCount {
			t.Fatal("lost streamed mesh or failed to trim a stationary goal")
		}
		for i := 1; i < len(points); i++ {
			length += distance(points[i-1], points[i])
		}
		cur = points[len(points)-1]
		legs++
		if legs > 100 {
			t.Fatal("route failed to converge", cur)
		}
	}
	t.Logf("retained map 0, legs %d, path %.1f yd, final gap %.3f", legs, length, distance(cur, goal))
	// Authoritative relocation outside the consumed corridor must reanchor on this same mesh.
	changed := Point{-5256.0586, -502.1603, 386.11105}
	points, err := c.PathAhead(ctx, changed, goal)
	if err != nil || len(points) < 2 || distance(points[0], changed) > EndpointTolerance || c.search != search {
		t.Fatal("displaced rider", err, len(points))
	}
	old := c.Destination()
	raised := goal
	raised.Z += 100
	if _, err = c.PathAhead(ctx, changed, raised); err == nil || c.Destination() != old {
		t.Fatal("unsupported goal replaced valid corridor", err, c.Destination())
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = c.PathAhead(canceled, changed, goal); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
