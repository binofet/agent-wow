package buddynav

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MaxSwimTrailStep permits roughly one second of normal swimming between
// consecutive observations. A longer gap needs a separate planned join.
const MaxSwimTrailStep = 5.0

// ValidateSwimTrailContext checks every chord of a server-observed swimming
// trail against the current map's static collision geometry. The caller must
// supply consecutive poses from one observed swimmer and map. An unobstructed
// line alone does not establish that an otherwise unknown point is in water.
func ValidateSwimTrailContext(ctx context.Context, dataDir string, mapID uint32, points []Point) (err error) {
	if len(points) < 2 || len(points) > MaxPoints {
		return errors.New("swim trail requires two or more bounded route samples")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			err = errors.New("invalid swim trail collision data or query")
		}
	}()
	window, err := newMapWindow(mapID, points[0], points[len(points)-1])
	if err != nil {
		return err
	}
	directory, err := mapDataDirectory(dataDir)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-currentMapPlanners.gate:
	}
	defer func() { currentMapPlanners.gate <- struct{}{} }()
	planner, err := cachedMapPlanner(ctx, directory, window)
	if err != nil {
		return err
	}
	return validateSwimTrail(ctx, points, window.contains, planner.swimSegmentClear)
}

func validateSwimTrail(ctx context.Context, points []Point, contains func(Point) bool,
	clear func(context.Context, Point, Point) (bool, error)) error {
	var length float64
	for i, point := range points {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !contains(point) {
			return fmt.Errorf("swim trail sample %d is outside the verified map window", i)
		}
		if i == 0 {
			continue
		}
		from, to := points[i-1], point
		step := distance(from, to)
		length += step
		if step > MaxSwimTrailStep || length > MaxRouteLength {
			return fmt.Errorf("swim trail geometry exceeds the route or observation gap at sample %d", i)
		}
		from.Z += swimBodyRayHeight
		to.Z += swimBodyRayHeight
		visible, err := clear(ctx, from, to)
		if err != nil {
			return fmt.Errorf("swim trail collision at sample %d: %w", i, err)
		}
		if !visible {
			return fmt.Errorf("swim trail chord %d intersects solid collision", i)
		}
	}
	return nil
}
