package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

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
	return finite(p) && p.x >= -9050 && p.x <= -8650 && p.y >= -500 && p.y <= 200 && p.z >= 40 && p.z <= 180
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
	return total <= 180
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
	for *index < len(path) && distance(s.pos, path[*index]) <= 0.05 {
		*index++
	}
	if *index >= len(path) {
		return s.stopLocked(ctx)
	}
	p := s.pos
	if !s.state.Moving {
		p.o = float32(math.Atan2(float64(path[*index].y-p.y), float64(path[*index].x-p.x)))
		return s.movementLocked(ctx, opcode.MSGMoveStartForward, p, true)
	}
	elapsed := s.now().Sub(s.posAt).Seconds()
	if elapsed < 0 || elapsed > 0.6 {
		s.stopLocked(ctx)
		return false
	}
	if elapsed < 0.15 {
		return true
	}
	remaining := 7 * elapsed
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
	if p.o < 0 {
		p.o += 2 * math.Pi
	}
	if !northshire(p) || math.Abs(float64(p.z-s.pos.z)) > 2 || distance(s.pos, p) > 7*elapsed+0.01 {
		s.stopLocked(ctx)
		return false
	}
	return s.movementLocked(ctx, opcode.MSGMoveHeartbeat, p, true)
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
