package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func (s *Service) SetNavigationDataDir(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if filepath.IsAbs(path) {
		s.navDataDir = filepath.Clean(path)
	}
}

func northshire(p position) bool {
	return finite(p) && p.x >= -9599 && p.x <= -8400 && p.y >= -700 && p.y <= 200 && p.z >= 40 && p.z <= 180
}

func planNavigation(ctx context.Context, root string, from, to position) ([]position, error) {
	points, err := buddynav.PlanContext(ctx, root, 0, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: to.x, Y: to.y, Z: to.z})
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = position{x: p.X, y: p.Y, z: p.Z}
	}
	return path, nil
}

// planMapNavigation is the production planner. Legacy planNavigation remains
// restricted to the explicitly regional practice controllers.
func planMapNavigation(ctx context.Context, root string, mapID uint32, from, to position) ([]position, error) {
	points, err := buddynav.PlanMapContext(ctx, root, mapID, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: to.x, Y: to.y, Z: to.z})
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = position{x: p.X, y: p.Y, z: p.Z}
	}
	return path, nil
}

func validMapNavigationPath(path []position, from, to position) bool {
	if !finite(from) || !finite(to) || len(path) < 2 || len(path) > 256 || distance(path[0], from) > 2 || distance(path[len(path)-1], to) > 2 {
		return false
	}
	total := distance(from, path[0])
	for i, p := range path {
		if !finite(p) {
			return false
		}
		if i > 0 {
			total += distance(path[i-1], p)
		}
	}
	return total <= buddynav.MaxRouteLength
}

func (s *Service) mapNavigationPath(ctx context.Context, root string, mapID uint32, from, to position) ([]position, error) {
	if s.planMapPath == nil {
		return nil, errors.New("client navigation data is unavailable")
	}
	path, err := s.planMapPath(ctx, root, mapID, from, to)
	if err != nil {
		return nil, fmt.Errorf("client navmesh planner: %w", err)
	}
	if !validMapNavigationPath(path, from, to) {
		return nil, errors.New("client navmesh route failed controller endpoint, length or finite-position validation")
	}
	return path, nil
}

// Interaction goals come only from each caller's current observed NPC. Nearby
// standoffs still use the exact same actual-map planner and complete route
// checks; they grant neither NPC presence nor interaction success.
func (s *Service) interactionNavigationPath(ctx context.Context, root string, mapID uint32, from, npc position) ([]position, error) {
	point := func(p position) buddynav.Point { return buddynav.Point{X: p.x, Y: p.y, Z: p.z} }
	points, err := buddynav.PlanInteractionWith(ctx, point(from), point(npc), func(call context.Context, a, b buddynav.Point) ([]buddynav.Point, error) {
		path, err := s.mapNavigationPath(call, root, mapID, position{x: a.X, y: a.Y, z: a.Z}, position{x: b.X, y: b.Y, z: b.Z})
		if err != nil {
			return nil, err
		}
		out := make([]buddynav.Point, len(path))
		for i, p := range path {
			out[i] = point(p)
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = position{x: p.X, y: p.Y, z: p.Z}
	}
	return path, nil
}

func (s *Service) mapNavigationStepLocked(ctx context.Context, path []position, index *int) bool {
	return s.navigationStepWithinLocked(ctx, path, index, finite)
}

func validNavigationPath(path []position, from, to position) bool {
	if len(path) < 2 || len(path) > 2048 || distance(path[0], from) > 2 || distance(path[len(path)-1], to) > 2 {
		return false
	}
	total := distance(from, path[0])
	for i, p := range path {
		if !northshire(p) {
			return false
		}
		if i > 0 {
			total += distance(path[i-1], p)
		}
	}
	return total <= buddynav.MaxRouteLength
}

func (s *Service) navigationPath(ctx context.Context, root string, from, to position) ([]position, error) {
	if root == "" || s.planPath == nil {
		return nil, errors.New("client navigation data is unavailable")
	}
	path, err := s.planPath(ctx, root, from, to)
	if err != nil {
		return nil, fmt.Errorf("client navmesh planner: %w", err)
	}
	if !validNavigationPath(path, from, to) {
		return nil, errors.New("client navmesh route failed controller endpoint, length or bounds validation")
	}
	return path, nil
}

// Only local planner errors reach this diagnostic. Redact the configured root,
// remove control/non-ASCII text and cap output; never expose game packet data.
func navigationDiagnostic(err error, root string) string {
	if err == nil {
		return "client_navmesh_failure_unknown"
	}
	message := err.Error()
	if root != "" {
		message = strings.ReplaceAll(message, root, "<client-data>")
	}
	var out strings.Builder
	for _, r := range message {
		if out.Len() >= 240 {
			break
		}
		if r < 32 || r > 126 {
			out.WriteByte(' ')
		} else {
			out.WriteByte(byte(r))
		}
	}
	return out.String()
}

// The route was produced by the client navmesh. Each transport write advances
// by at most seven units per elapsed second along its ordered 3D segments.
func (s *Service) navigationStepLocked(ctx context.Context, path []position, index *int) bool {
	return s.navigationStepWithinLocked(ctx, path, index, northshire)
}

func (s *Service) navigationStepWithinLocked(ctx context.Context, path []position, index *int, inside func(position) bool) bool {
	return s.navigationStepAtSpeedLocked(ctx, path, index, inside, 7)
}

func (s *Service) navigationStepAtSpeedLocked(ctx context.Context, path []position, index *int, inside func(position) bool, speed float64) bool {
	ok, _ := s.navigationStepResultLocked(ctx, path, index, inside, speed, false)
	return ok
}

// A proof belongs to one proposed exploration packet, not a reusable movement
// permission. Every existing final World read checks its displacement against
// both the captured interval speed and the newly observed speed.
type navigationSpeedProofKey struct{}
type navigationSpeedProof struct {
	from           position
	elapsed, speed float64
	refused        bool
}

var errNavigationSpeedChanged = errors.New("exploration observed speed no longer permits proposed checkpoint")

func navigationSpeedAllowed(ctx context.Context, w *worldv1.SnapshotResponse, p position) bool {
	proof, _ := ctx.Value(navigationSpeedProofKey{}).(*navigationSpeedProof)
	if proof == nil {
		return true
	}
	speed := observedRunSpeed(w.GetSelf())
	if speed <= 0 || distance(proof.from, p) > math.Min(proof.speed, speed)*proof.elapsed+0.01 {
		proof.refused = true
		return false
	}
	return true
}

// Exploration revalidates its body, lease and route before every checkpoint.
// A delayed checkpoint may discard elapsed backlog, never extrapolate farther
// than the existing 600ms bound. Other controllers retain their late STOP.
func (s *Service) navigationStepResultLocked(ctx context.Context, path []position, index *int, inside func(position) bool, speed float64, boundedDelay bool) (bool, string) {
	beforeWrite := s.transportSequence
	var speedProof *navigationSpeedProof
	write := func(ok bool, op uint32) (bool, string) {
		if ok {
			return true, ""
		}
		if s.state.MotionUnconfirmed {
			return false, "movement_transport_unconfirmed"
		}
		if speedProof != nil && speedProof.refused {
			return false, "observed_run_speed_changed"
		}
		if failure := s.state.LastTransportFailure; failure != nil && failure.Sequence > beforeWrite && failure.Opcode == op && !failure.OutcomeUnknown {
			return false, "movement_not_dispatched"
		}
		if ctx.Err() != nil {
			return false, "canceled_or_time_limit"
		}
		return false, "movement_prewrite_refused"
	}
	stop := func(reason string) (bool, string) {
		if !s.stopLocked(ctx) {
			if s.state.MotionUnconfirmed {
				return false, reason + "_stop_unconfirmed"
			}
			return false, reason + "_stop_not_dispatched"
		}
		return false, reason
	}
	for *index < len(path) && distance(s.pos, path[*index]) <= 0.05 {
		*index++
	}
	if *index >= len(path) {
		return write(s.stopLocked(ctx), opcode.MSGMoveStop)
	}
	p := s.pos
	if !s.state.Moving {
		p.o = float32(math.Atan2(float64(path[*index].y-p.y), float64(path[*index].x-p.x)))
		return write(s.movementLocked(ctx, opcode.MSGMoveStartForward, p, true), opcode.MSGMoveStartForward)
	}
	elapsed := s.now().Sub(s.posAt).Seconds()
	if elapsed < 0 {
		return stop("clock_regressed")
	}
	late := elapsed > 0.6
	if late {
		if !boundedDelay {
			return stop("checkpoint_late")
		}
		elapsed = 0.6
	}
	_, strip := ctx.Value(travelGroundStrideKey{}).(travelMap)
	if elapsed < .15 && (!strip || elapsed+.000001 < travelCheckpointDelay(s.pos, path, *index, speed).Seconds()) {
		return true, ""
	}
	if strip {
		next, nextIndex, _, ok := followStride(p, path, *index, speed*elapsed, 2)
		if !ok {
			return stop("step_geometry_invalid")
		}
		p, *index = next, nextIndex
	} else {
		remaining := speed * elapsed
		for *index < len(path) && remaining > 0.001 {
			target := path[*index]
			d := distance(p, target)
			if d <= 0.05 {
				*index++
				continue
			}
			step := math.Min(remaining, d)
			p.o = float32(math.Atan2(float64(target.y-p.y), float64(target.x-p.x)))
			fraction := float32(step / d)
			p.x += (target.x - p.x) * fraction
			p.y += (target.y - p.y) * fraction
			p.z += (target.z - p.z) * fraction
			remaining -= step
			if step == d {
				*index++
				// A single movement packet must not cut across a navmesh corner.
				// Stop at this verified sample; the next checkpoint turns along
				// the next verified segment instead of sending an untested chord.
				break
			}
		}
	}
	if p.o < 0 {
		p.o += 2 * math.Pi
	}
	if !inside(p) || math.Abs(float64(p.z-s.pos.z)) > 2 || distance(s.pos, p) > speed*elapsed+0.01 {
		return stop("step_geometry_invalid")
	}
	if boundedDelay {
		speedProof = &navigationSpeedProof{from: s.pos, elapsed: elapsed, speed: speed}
		ctx = context.WithValue(ctx, navigationSpeedProofKey{}, speedProof)
	}
	ok, reason := write(s.movementLocked(ctx, opcode.MSGMoveHeartbeat, p, true), opcode.MSGMoveHeartbeat)
	if ok && late {
		return true, "checkpoint_delay_bounded"
	}
	return ok, reason
}

func routeRemaining(from position, path []position, index int) float64 {
	if index >= len(path) {
		return 0
	}
	d := distance(from, path[index])
	for i := index + 1; i < len(path); i++ {
		d += distance(path[i-1], path[i])
	}
	return d
}

// A verified route that still bends around a wall does not justify an early
// ranged pull across that wall. Walk until the remaining ground route follows
// the direct horizontal corridor; the server still validates spell LOS.
func directRemainingRoute(from, to position, path []position, index int) bool {
	dx, dy := float64(to.x-from.x), float64(to.y-from.y)
	length := math.Hypot(dx, dy)
	if length < 0.01 {
		return routeRemaining(from, path, index) < 0.1
	}
	for _, p := range path[index:] {
		px, py := float64(p.x-from.x), float64(p.y-from.y)
		if math.Abs(px*dy-py*dx)/length > 0.15 {
			return false
		}
	}
	return true
}
