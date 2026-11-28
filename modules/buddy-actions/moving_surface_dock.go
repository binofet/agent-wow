package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func surfaceDockWalk(g *actionsv1.FollowFormationRequest) bool {
	return g.GetSurfaceMode() == "board" || g.GetSurfaceMode() == "leave" && !g.GetJump()
}

// Recheck the original dwell through the remaining finite input, including
// time spent waiting for the clock or the final World read.
func (sf *movingSurfaceFollow) dockWalkCurrent(path *buddynav.TransportPath, tr buddynav.SurfaceTransform, progress uint32, now time.Time) bool {
	from := progress
	remaining := time.Millisecond
	if sf.planSource != nil && len(sf.plan) > 0 {
		from = sf.planSource.firstProgress
		if progress < from || !surfaceCheckpointMatches(buddynav.TransportPathSample{Position: sf.planTransform.Origin, Yaw: sf.planTransform.Yaw}, tr) {
			return false
		}
		remaining = max(remaining, sf.planAt.Add(sf.plan[len(sf.plan)-1].At).Sub(now)) + time.Duration(progress-from)*time.Millisecond
	}
	return dockInput(path, from, remaining)
}

// Prove an ordinary walk reaches the other support while the hull is still
// docked. Only the first finite input is retained; subsequent inputs start
// again at the body's actual written frame.
func (s *Service) measureDockWalk(ctx context.Context, w *worldv1.SnapshotResponse, g *actionsv1.FollowFormationRequest,
	mesh *buddynav.SurfaceMesh, tr buddynav.SurfaceTransform, progress uint32, start, toward buddynav.Point, speed float64) ([]buddynav.BodyMotionSample, error) {
	path := s.surfaceFollow.paths[g.TransportGuid]
	board := g.SurfaceMode == "board"
	var first []buddynav.BodyMotionSample
	var elapsed time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before := math.Hypot(float64(toward.X-start.X), float64(toward.Y-start.Y))
		if before < .001 {
			return nil, errors.New("walk reached the edge without a change of support")
		}
		rows, err := s.measureSurfaceCrossing(ctx, w, g, mesh, tr, progress, start, toward, speed, false)
		if err != nil {
			return nil, err
		}
		last := rows[len(rows)-1]
		if last.Swimming {
			return nil, errors.New("deck opening leads to water")
		}
		elapsed += last.At
		if !dockInput(path, progress, elapsed) {
			return nil, errors.New("walk exceeds the current dock dwell")
		}
		onDeck, err := hullFoot(ctx, mesh.Collision(), tr.Local(last.Point))
		if err != nil {
			return nil, err
		}
		if first == nil {
			first = rows
		}
		if onDeck == board {
			if board {
				_, err = mesh.Route(ctx, tr.Local(last.Point), surfaceOwnerPoint(w))
			} else {
				// The landing must join the actual owner's static shore.
				_, err = buddynav.PlanLegContext(ctx, s.navDataDir, w.Self.MapId, last.Point, surfaceOwnerPoint(w))
			}
			if err != nil {
				return nil, err
			}
			return first, nil
		}
		after := math.Hypot(float64(toward.X-last.Point.X), float64(toward.Y-last.Point.Y))
		if after >= before-.001 {
			return nil, errors.New("walk made no progress toward the dock opening")
		}
		start = last.Point
	}
}

// A shore approach can have a turn at the gangway. Find a real landing
// outside the edge, check the walking ascent in the requested direction,
// then join it using the existing static dock path and physical body sweep.
func (s *Service) approachDockOpening(ctx context.Context, w *worldv1.SnapshotResponse, g *actionsv1.FollowFormationRequest,
	mesh *buddynav.SurfaceMesh, tr buddynav.SurfaceTransform, progress uint32, shore, edge, normal buddynav.Point, speed float64) ([]buddynav.BodyMotionSample, float64, error) {
	bearing := float64(tr.Yaw) + math.Atan2(float64(normal.Y), float64(normal.X))
	rows, err := buddynav.PlanSurfaceBodyContext(ctx, s.navDataDir, w.Self.MapId, edge, bearing, speed, 500*time.Millisecond, false, true, buddynav.MovingSurface{Transform: tr, Model: mesh.Collision()})
	if err != nil {
		return nil, 0, err
	}
	if len(rows) < 2 || rows[0].Settling {
		return nil, 0, errors.New("dock opening lacks supported walking input")
	}
	last := rows[len(rows)-1]
	stage := last.Point
	onDeck, err := hullFoot(ctx, mesh.Collision(), tr.Local(stage))
	if err != nil {
		return nil, 0, err
	}
	if last.Swimming || onDeck {
		return nil, 0, errors.New("edge does not join a dry dock")
	}
	if _, err = s.measureDockWalk(ctx, w, g, mesh, tr, progress, stage, edge, speed); err != nil {
		return nil, 0, err
	}
	path, err := buddynav.PlanLegContext(ctx, s.navDataDir, w.Self.MapId, shore, stage)
	if err != nil {
		return nil, 0, err
	}
	if len(path) < 2 {
		return nil, 0, errors.New("no shore approach to the dock opening")
	}
	// The map path supplies a waypoint. Combined dock and hull collision
	// still verifies the actual one-body input from the current position.
	target := path[1]
	for _, q := range path[2:] {
		if surfaceDistance(shore, q) > speed*.5 {
			break
		}
		target = q
	}
	step, err := s.measureSurfaceCrossing(ctx, w, g, mesh, tr, progress, shore, target, speed, false)
	if err != nil {
		return nil, 0, err
	}
	return step, surfaceBearing(shore, target), nil
}

// Search the connected hull boundary in either direction. A nearby polygon
// alone says nothing about whether the dock meets the deck at that point.
func (s *Service) planSurfaceDockWalk(ctx context.Context, w *worldv1.SnapshotResponse, g *actionsv1.FollowFormationRequest,
	mesh *buddynav.SurfaceMesh, tr buddynav.SurfaceTransform, progress uint32, start, target buddynav.Point, speed float64) ([]buddynav.BodyMotionSample, float64, bool, error) {
	board := g.SurfaceMode == "board"
	deck, shore := start, target
	if board {
		deck, shore = target, start
	}
	search, cancel := context.WithTimeout(ctx, actionsTickBudget/2)
	defer cancel()
	var firstFailure error
	var input []buddynav.BodyMotionSample
	var heading float64
	var crossingFromBody bool
	route, err := mesh.DepartureRoute(search, tr.Local(deck), tr.Local(shore), func(edge, normal buddynav.Point, length float64) bool {
		from, to := tr.World(edge), shore
		at := progress
		if board {
			from, to = shore, from
		} else {
			approach := time.Duration(length / speed * float64(time.Second))
			if !dockInput(s.surfaceFollow.paths[g.TransportGuid], progress, approach) {
				if firstFailure == nil {
					firstFailure = errors.New("deck walk exceeds the dock dwell")
				}
				return false
			}
			at += uint32(approach / time.Millisecond)
		}
		rows, e := s.measureDockWalk(search, w, g, mesh, tr, at, from, to, speed)
		bearing := surfaceBearing(from, to)
		if board && e != nil {
			rows, bearing, e = s.approachDockOpening(search, w, g, mesh, tr, progress, shore, to, normal, speed)
		}
		if !board && e != nil {
			to = tr.World(buddynav.Point{X: edge.X + normal.X*float32(speed*.5), Y: edge.Y + normal.Y*float32(speed*.5), Z: edge.Z})
			rows, e = s.measureDockWalk(search, w, g, mesh, tr, at, from, to, speed)
			bearing = surfaceBearing(from, to)
		}
		crossingFromBody = board
		if !board && e == nil && surfaceDistance(start, from) <= .45 {
			bodyRows, bodyErr := s.measureDockWalk(search, w, g, mesh, tr, progress, start, to, speed)
			if bodyErr == nil {
				rows, bearing, crossingFromBody = bodyRows, surfaceBearing(start, to), true
			} else if length <= .01 {
				e = bodyErr
			}
		}
		if e != nil {
			if firstFailure == nil {
				firstFailure = e
			}
			return false
		}
		input, heading = rows, bearing
		return true
	}, &s.surfaceFollow.exitSearch)
	if err != nil {
		if firstFailure != nil {
			return nil, 0, false, fmt.Errorf("%w: %v", err, firstFailure)
		}
		return nil, 0, false, err
	}
	if crossingFromBody {
		return input, heading, false, nil
	}
	steps := deckInput(route, tr, speed, 0)
	if len(steps) < 2 {
		return nil, 0, true, errors.New("no grounded step to the selected dock opening")
	}
	return steps, 0, true, nil
}
