package buddyactions

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

// A retained strip is static guidance. It has no token and cannot initiate a
// write. The movement operation still admits the actual body before every frame.
type travelSurface interface {
	Destination() buddynav.Point
	PathAhead(context.Context, buddynav.Point, buddynav.Point) ([]buddynav.Point, error)
}

type openTravelSurface func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (travelSurface, error)

func openGroundTravel(ctx context.Context, root string, mapID uint32, from, goal buddynav.Point) (travelSurface, error) {
	c, err := buddynav.PlanGroundCorridorContext(ctx, root, mapID, from, goal)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

type travelMap struct {
	root, session string
	mapID         uint32
	self, visit   uint64
}

func (s *Service) travelMapLocked(mapID uint32) travelMap {
	return travelMap{root: s.navDataDir, session: s.sessionID, mapID: mapID, self: s.selfGUID, visit: s.lastWorld.GetVisitRevision()}
}

type travelStrip struct {
	scope   travelMap
	goal    position
	surface travelSurface
	mu      sync.Mutex // Detour and the corridor cursor belong to one query at a time.
}

// An advisory prefix can be consumed only from its original observed pose; a
// changed pose uses the retained graph or a fresh search under the same guards.
type travelPreview struct {
	strip                 *travelStrip
	from, end             position
	revision, rawRevision uint64
	path                  []position
}

func (s *Service) travelCandidateLocked(scope travelMap, from, goal position, preview bool) (*travelStrip, []position) {
	c := s.travelPreview
	if c.strip == nil || c.strip.scope != scope {
		return nil, nil
	}
	if preview && c.path != nil && sameTravelPoint(c.from, from) && sameTravelPoint(c.end, goal) &&
		s.clientPositionLocked().Revision == c.revision && s.lastWorld.GetSelf().GetPositionRevision() == c.rawRevision {
		return c.strip, c.path
	}
	if sameTravelPoint(c.strip.goal, goal) {
		return c.strip, nil
	}
	return nil, nil
}

func sameTravelPoint(a, b position) bool { return a.x == b.x && a.y == b.y && a.z == b.z }

func (c *travelStrip) next(ctx context.Context, from position) ([]position, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	points, err := c.surface.PathAhead(ctx, swimPoint(from), swimPoint(c.goal))
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = navPosition(p)
	}
	if _, ok := validRouteLeg(path, from, c.goal); !ok {
		return nil, errors.New("ground corridor returned an invalid prefix")
	}
	return path, nil
}

func prepareTravelStrip(ctx context.Context, open openTravelSurface, scope travelMap, from, goal position, cached *travelStrip) (*travelStrip, []position, error) {
	if cached != nil {
		path, err := cached.next(ctx, from)
		return cached, path, err
	}
	if open == nil {
		return nil, nil, errors.New("ground corridor planner unavailable")
	}
	surface, err := open(ctx, scope.root, scope.mapID, swimPoint(from), swimPoint(goal))
	if err != nil {
		return nil, nil, err
	}
	if surface == nil {
		return nil, nil, errors.New("ground corridor unavailable")
	}
	c := &travelStrip{scope: scope, goal: goal, surface: surface}
	path, err := c.next(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return c, path, nil
}

// Checked by the existing ground movement writer at its normal prewrite gate.
type travelGroundStrideKey struct{}

func travelCheckpointDelay(pos position, path []position, index int, speed float64) time.Duration {
	delay := explorationCheckpointDelay(speed)
	_, _, used, ok := followStride(pos, path, index, speed*delay.Seconds(), 2)
	if ok && used > 0 {
		return min(delay, max(time.Millisecond, time.Duration(math.Ceil(used/speed*float64(time.Second)))))
	}
	return delay
}
