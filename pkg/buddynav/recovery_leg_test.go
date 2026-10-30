package buddynav

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
)

func TestRecoveryWindowHasSeparateTargetBudgetWithoutChangingNormalRouteCaps(t *testing.T) {
	a, b := Point{-9388.031, 131.43484, 62.028873}, Point{-9829.6, 123.46666, 4.391315}
	if _, err := newMapWindow(0, a, b); err == nil {
		t.Fatal("normal240ydcap changed")
	}
	w, err := newRecoveryWindow(0, a, b)
	if err != nil || !w.contains(a) || !w.contains(b) || len(w.meshTiles()) > 9 {
		t.Fatal(w, err)
	}
	for _, tc := range []struct {
		mid  uint32
		a, b Point
	}{
		{2, a, b}, {0, Point{float32(math.NaN()), 0, 0}, b}, {0, a, Point{18000, 0, 0}},
		{0, a, Point{a.X + 1201, a.Y, a.Z}},
	} {
		if _, err := newRecoveryWindow(tc.mid, tc.a, tc.b); err == nil {
			t.Fatal("unbounded/unknownrecovery accepted", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PlanRecoveryLegContext(ctx, "", 0, a, b); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := PlanRecoveryLegContext(context.Background(), t.TempDir(), 0, a, b); err == nil {
		t.Fatal("missingassetsbecameroute")
	}
}

func TestRecoveryWaypointUsesBoundCorridorDetailFloorNotInterpolatedZ(t *testing.T) {
	p := detailHillMesh(t, true)
	a, b := Point{-8899, -90, 90.4}, Point{-8881, -90, 90.4}
	r, err := p.findPath(a, b)
	if err != nil {
		t.Fatal(err)
	}
	raw := []Point{{r.Points[0].X, r.Points[0].Y, r.Points[0].Z}, {r.Points[1].X, r.Points[1].Y, r.Points[1].Z}}
	q, err := recoveryCorridorPoint(p, raw, 9)
	if err != nil || math.Abs(float64(q.X+8890)) > .01 || math.Abs(float64(q.Z-94)) > .01 {
		t.Fatal("waypointguessedrawchordZ", q, err)
	}
	p.routeSurfaces = nil
	if _, err := recoveryCorridorPoint(p, raw, 9); err == nil {
		t.Fatal("missingcorridorbindingaccepted")
	}
}

func TestRecoveryTopologyCannotAuthorizePartialWallsOrBadPhysicalFloors(t *testing.T) {
	for _, mode := range []string{"partial", "floor", "wall", "deep_water"} {
		t.Run(mode, func(t *testing.T) {
			p := detailHillMesh(t)
			a, b := Point{-8899, -90, 90.4}, Point{-8881, -90, 90.4}
			if mode == "partial" {
				p = surfaceRetryFixture(t, "wall")
				a, b = Point{-8898, -99, 90}, Point{-8898, -91, 90}
			}
			w, err := newRecoveryWindow(0, a, b)
			if err != nil {
				t.Fatal(err)
			}
			physicalCalls := 0
			_, err = recoveryLeg(context.Background(), p, w, a, b, func(goal Point) ([]Point, error) {
				physicalCalls++
				h := &hillBackend{planner: p}
				switch mode {
				case "floor":
					h.floorFn = func(_ Point, height float32) (float32, error) { return height + 3, nil }
				case "wall":
					h.walkErr = errors.New("realwall")
				case "deep_water":
					for _, s := range p.routeSurfaces {
						p.mesh.SetPolyFlags(s.ref, 8)
					}
					h.floorFn = func(_ Point, height float32) (float32, error) { return height - 1.9, nil }
				}
				return planWithin(context.Background(), h, w.contains, "testcurrentmap", a, goal)
			})
			if err == nil || physicalCalls > 3 {
				t.Fatal("topologybecamemovementauthority", err, physicalCalls)
			}
			if mode == "partial" && physicalCalls != 0 {
				t.Fatal("partialcorridorreachedphysicalmovementplanner")
			}
		})
	}
}

func TestRecoveryLegOutputStillRequiresDenseBoundedProgressingRoute(t *testing.T) {
	a, b := Point{-9388, 131, 62}, Point{-9380, 131, 62}
	w, _ := newRecoveryWindow(0, a, b)
	valid := []Point{}
	for i := float32(0); i <= 8; i++ {
		valid = append(valid, Point{a.X + i, a.Y, a.Z})
	}
	if !validRecoveryLeg(valid, w, a, b, b) {
		t.Fatal("validdensetestlegrejected")
	}
	for _, mode := range []string{"jump", "layer", "no_progress", "endpoint", "nonfinite", "length"} {
		path := append([]Point(nil), valid...)
		switch mode {
		case "jump":
			path = []Point{a, b}
		case "layer":
			path[3].Z += 1.5
		case "no_progress":
			path = []Point{a, a}
		case "endpoint":
			path = path[:3]
		case "nonfinite":
			path[3].X = float32(math.NaN())
		case "length":
			for i := 0; i < 100; i++ {
				path = append(path, a, b)
			}
		}
		if validRecoveryLeg(path, w, a, b, b) {
			t.Fatal("invalidrecoverylegaccepted", mode)
		}
	}
}

// Actual saved post-public-GM-revive surface poses and earlier death poses.
// These prove static routes only, not a normal ghost/reclaim acceptance test.
func TestPhysicalRecoveryLegsFromRecordedSurfacePosesToFargodeep(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_RECOVERY_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set independent current-mapFargodeepclientassets")
	}
	fixtures := []struct {
		name          string
		start, corpse Point
	}{
		{"Arlen", Point{-9388.031, 131.43484, 62.028873}, Point{-9829.6, 123.46666, 4.391315}},
		{"Mira", Point{-9385.513, 127.46519, 61.328217}, Point{-9829.732, 125.2, 4.8503733}},
		{"Rook", Point{-9385.159, 123.892136, 61.22384}, Point{-9760.889, 90.70736, 12.300861}},
	}
	for _, fixture := range fixtures {
		for _, early := range []bool{false, true} {
			t.Run(fixture.name+map[bool]string{false: "_exact", true: "_early075"}[early], func(t *testing.T) {
				position := fixture.start
				total := 0.0
				legs := 0
				for distance(position, fixture.corpse) > 3 && legs < 16 {
					path, err := PlanRecoveryLegContext(context.Background(), dir, 0, position, fixture.corpse)
					if err != nil {
						t.Fatal("recordedrecoveryroutefailed", position, err)
					}
					var length float64
					for i := 1; i < len(path); i++ {
						length += distance(path[i-1], path[i])
					}
					if length > RecoveryMaxLegLength || len(path) > MaxPoints {
						t.Fatal("legcapchanged", length, len(path))
					}
					next := path[len(path)-1]
					if early {
						remaining := .75
						for i := len(path) - 1; i > 0; i-- {
							d := distance(path[i], path[i-1])
							if d < remaining {
								remaining -= d
								continue
							}
							f := float32(remaining / d)
							a, b := path[i], path[i-1]
							next = Point{a.X + (b.X-a.X)*f, a.Y + (b.Y-a.Y)*f, a.Z + (b.Z-a.Z)*f}
							break
						}
					}
					if distance(next, position) < 1 {
						t.Fatal("recoverymade no verifiedprogress")
					}
					position = next
					total += length
					legs++
				}
				if distance(position, fixture.corpse) > 3 {
					t.Fatal("didnotreachcorpse", position, legs)
				}
				t.Logf("%s legs%d total%.3f end%+v corpseDistance%.4f", fixture.name, legs, total, position, distance(position, fixture.corpse))
			})
		}
	}
}
