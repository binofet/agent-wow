package buddynav

import (
	"context"
	"fmt"
	"time"
)

// LineOfSightContext checks the client map's static collision geometry
// between two world points. Callers supply eye or body height. The server
// still decides spell LOS, including dynamic objects and doors.
func LineOfSightContext(ctx context.Context, dataDir string, mapID uint32, from, to Point) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	window, err := newMapWindow(mapID, from, to)
	if err != nil {
		return false, err
	}
	dir, err := mapDataDirectory(dataDir)
	if err != nil {
		return false, err
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-currentMapPlanners.gate:
	}
	defer func() { currentMapPlanners.gate <- struct{}{} }()
	planner, err := cachedMapPlanner(ctx, dir, window)
	if err != nil {
		return false, err
	}
	return planner.floors.lineOfSight(ctx, from, to)
}

func (f *floorReader) lineOfSight(ctx context.Context, from, to Point) (bool, error) {
	length := float32(distance(from, to))
	if length < .01 {
		return true, ctx.Err()
	}
	origin := Point{vmapMid - from.X, vmapMid - from.Y, from.Z}
	end := Point{vmapMid - to.X, vmapMid - to.Y, to.Z}
	dir := Point{(from.X - to.X) / length, (from.Y - to.Y) / length, (to.Z - from.Z) / length}
	x1, y1 := mapGrid(from)
	x2, y2 := mapGrid(to)
	for x := min(x1, x2); x <= max(x1, x2); x++ {
		for y := min(y1, y2); y <= max(y1, y2); y++ {
			spawns, err := f.spawns(fmt.Sprintf("%03d_%02d_%02d.vmtile", f.mapID, y, x))
			if err != nil {
				return false, err
			}
			blocked, err := f.sightBlocked(ctx, origin, end, dir, length, spawns)
			if err != nil || blocked {
				return false, err
			}
			if !f.globalTiled {
				return true, nil // The map uses one shared model list for every tile.
			}
		}
	}
	return true, nil
}

func (f *floorReader) sightBlocked(ctx context.Context, origin, end, dir Point, length float32, spawns []floorSpawn) (bool, error) {
	for i, spawn := range spawns {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		if max(origin.X, end.X) < spawn.lo.X || min(origin.X, end.X) > spawn.hi.X ||
			max(origin.Y, end.Y) < spawn.lo.Y || min(origin.Y, end.Y) > spawn.hi.Y ||
			max(origin.Z, end.Z) < spawn.lo.Z || min(origin.Z, end.Z) > spawn.hi.Z {
			continue
		}
		model, err := f.model(spawn.name)
		if err != nil {
			return false, err
		}
		local := floorSub(origin, spawn.pos)
		local = floorMul(spawn.inv, Point{local.X / spawn.scale, local.Y / spawn.scale, local.Z / spawn.scale})
		direction := floorMul(spawn.inv, dir)
		reach := length / spawn.scale
		for i := range model.groups {
			_, hit, err := model.groups[i].ray(ctx, local, direction, reach, true)
			if err != nil || hit {
				return hit, err
			}
		}
	}
	return false, ctx.Err()
}
