package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/o0olele/detour-go/detour"
)

// Wrath caps falling speed after the initial constant-gravity phase.
const normalTerminalVelocity = 60.148003
const bodyWaterReportInterval = 100 * time.Millisecond

// A moving body traverses at most one verified horizontal route. Gravity is
// checked continuously in sub-yard chords, independently of report cadence.
func BodyLandingSampleLimit(speed float64) int {
	if speed == 0 {
		// With forward released only vertical map extent and terminal speed
		// bound elapsed time. No horizontal route limit caps a vertical fall.
		return 12 + int(math.Ceil((2*worldVerticalLimit/normalTerminalVelocity+(normalTerminalVelocity+BodyJumpVelocity)/BodyGravity)/bodyWaterReportInterval.Seconds()))
	}
	if speed < .1 || speed > 14 || math.IsNaN(speed) {
		return 0
	}
	return 12 + int(math.Ceil(MaxRouteLength/speed/bodyWaterReportInterval.Seconds()))
}

// BodyFlightInRange validates the same spatial envelope on timed movement
// reports, including forward walk-offs with zero initial vertical speed.
func BodyFlightInRange(elapsed time.Duration, speed, initial float64) bool {
	t := elapsed.Seconds()
	return t >= 0 && speed*t <= MaxRouteLength+.01 && math.Abs(bodyElevation(t, initial)) <= 2*worldVerticalLimit+.01
}

func bodyElevation(seconds, initial float64) float64 {
	accelerating := math.Max(0, math.Min(seconds, (normalTerminalVelocity+initial)/BodyGravity))
	return initial*accelerating - BodyGravity*accelerating*accelerating/2 - normalTerminalVelocity*(seconds-accelerating)
}

// Find the first contact as the body falls along its actual horizontal momentum.
// Geometry bounds the work. The ordinary short-hop horizon is not a landing.
func traceBodyLanding(ctx context.Context, points []BodyMotionSample, bearing, speed, initial float64, floor bodyFloor, clear bodyClear, water bodyWater) ([]BodyMotionSample, error) {
	return traceBodyFall(ctx, points, bearing, speed, initial, 0, false, floor, clear, water)
}

func traceBodyFall(ctx context.Context, points []BodyMotionSample, bearing, speed, initial float64, age time.Duration, settling bool, floor bodyFloor, clear bodyClear, water bodyWater) ([]BodyMotionSample, error) {
	takeoff := points[len(points)-1]
	offset := age.Seconds()
	startHeight := bodyElevation(offset, initial)
	dx, dy := speed*math.Cos(bearing), speed*math.Sin(bearing)
	pose := func(t float64) Point {
		return Point{takeoff.Point.X + float32(dx*t), takeoff.Point.Y + float32(dy*t), takeoff.Point.Z + float32(bodyElevation(offset+t, initial)-startHeight)}
	}
	previous := takeoff.Point
	up := math.Max(0, initial-BodyGravity*offset)
	ceiling := takeoff.Point.Z + float32(up*up/(2*BodyGravity)) + .025
	for t := 0.; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Keep checks within a yard even at terminal velocity. Do not jump
		// between a high ledge and a floor or water layer below it.
		vz := math.Max(-normalTerminalVelocity, initial-BodyGravity*(offset+t))
		step := math.Min(.1, 1/math.Hypot(speed, math.Abs(vz)+BodyGravity*.1))
		next := t + step
		q := pose(next)
		if !worldPoint(q) || horizontalDistance(takeoff.Point, q) > MaxRouteLength {
			return nil, errors.New("no solid or swimmable landing in the verified map corridor")
		}
		// Query up from the lower endpoint to the preceding feet. Taking
		// the highest surface preserves platforms above pools and rooms below them.
		contact := func(p Point) (float32, bool, bool, error) {
			top := max(previous.Z, p.Z) + .025
			h, solid, err := floor(p, top-p.Z, .025)
			if err != nil {
				return 0, false, false, err
			}
			if !solid && ceiling > top {
				// Rising MAP terrain can pass completely above the short
				// falling-feet band in one chord. Check the swept column
				// for a surface newly above us. A layer already overhead
				// at the preceding XY is not a floor under this body.
				for layer, n := ceiling, 0; layer > top; n++ {
					if n >= maxGroundCandidates {
						return 0, false, false, errors.New("overlapping landing surfaces exceed the bounded query")
					}
					upper, above, err := floor(p, layer-p.Z, .025)
					if err != nil {
						return 0, false, false, err
					}
					if !above || upper <= top {
						break
					}
					// A thin VMAP bridge can begin above a body already
					// passing under it. Its real triangle is covered by the
					// swept body rays; do not promote overhead into a landing.
					low, high := p, p
					low.Z, high.Z = upper-.05, upper+.05
					open, err := clear(low, high)
					if err != nil {
						return 0, false, false, err
					}
					if !open {
						layer = upper - .025
						continue
					}
					priorHeight, overhead, err := floor(previous, layer-previous.Z, .025)
					if err != nil {
						return 0, false, false, err
					}
					if !overhead || priorHeight <= previous.Z+.025 {
						h, solid = upper, true
						break
					}
					layer = min(priorHeight, upper) - .025
				}
			}
			w, wet, err := water(p, top+swimImmersion, p.Z-.025+swimImmersion)
			if err != nil {
				return 0, false, false, err
			}
			if wet && (!solid || w > h) {
				return w, true, true, nil
			}
			return h, false, solid, nil
		}
		h, wet, hit, err := contact(q)
		if err != nil {
			return nil, err
		}
		landed := hit && q.Z <= h && q.Z < previous.Z
		if hit && !wet && q.Z < h && q.Z >= previous.Z {
			return nil, errors.New("body hits solid terrain during ascent")
		}
		if landed {
			// Localize the crossing in time and XY, including a sloping floor.
			lo, hi := t, next
			for i := 0; i < 12; i++ {
				mid := (lo + hi) / 2
				m := pose(mid)
				level, swimming, measured, err := contact(m)
				if err != nil {
					return nil, err
				}
				if measured && m.Z <= level {
					hi, h, wet = mid, level, swimming
				} else {
					lo = mid
				}
			}
			next, q = hi, pose(hi)
			if math.Abs(float64(q.Z-h)) > .05 {
				return nil, errors.New("body meets a steep solid face before its landing")
			}
			q.Z = h
		}
		if err := clearBodyMotionSegment(previous, q, bearing, clear); err != nil {
			return nil, fmt.Errorf("body landing corridor at %gs: %v to %v: %w", next, previous, q, err)
		}
		elapsed := time.Duration(next * float64(time.Second))
		if landed && !wet && !settling && elapsed > BodyMotionHorizon {
			return nil, fmt.Errorf("first contact is solid ground outside the short landing envelope at %v", q)
		}
		if landed || takeoff.At+elapsed-points[len(points)-1].At >= bodyWaterReportInterval {
			points = append(points, BodyMotionSample{At: takeoff.At + elapsed, Point: q, Settling: settling, FallTime: age + elapsed, Swimming: landed && wet})
		}
		if landed {
			return points, nil
		}
		previous, t = q, next
	}
}

// An impulse which never emerges from a deep pool cannot turn an underwater
// lakebed into a dry landing. Check the water column at the actual endpoint;
// a solid roof can separate the body from an unrelated pool overhead.
func verifyDryImpulseContact(q Point, water bodyWater, clear bodyClear) error {
	low := q
	low.Z += swimBodyRayHeight
	top := float32(worldVerticalLimit)
	for layer := 0; layer < maxGroundCandidates; layer++ {
		surface, found, err := water(q, top, q.Z+swimImmersion+.025)
		if err != nil || !found {
			return err
		}
		high := q
		high.Z = surface + swimBodyRayHeight
		open, err := clear(high, low)
		if err != nil {
			return err
		}
		if open {
			return errors.New("submerged impulse did not make an airborne water entry or dry contact")
		}
		top = surface + swimImmersion - .025
	}
	return errors.New("overlapping liquid layers exceed the impulse contact query")
}

// Lower pools can overlap an entirely separate shallow upper layer at the
// same XY. Inspect only the swept band, using the exact water polygon and
// the same floor/depth rules as the normal swim planner.
func (p *planner) deepWaterBetween(ctx context.Context, q Point, top, bottom float32) (float32, bool, error) {
	if top < bottom {
		return 0, false, nil
	}
	if !p.floors.window.contains(q) {
		return 0, false, errors.New("water landing leaves the verified map window")
	}
	mid := q
	mid.Z = (top + bottom) / 2
	v, ext := recast(mid), [3]float32{.02, (top-bottom)/2 + .02, .02}
	var refs [maxGroundCandidates]detour.DtPolyRef
	var count int
	st := p.query.QueryPolygons(v[:], ext[:], p.filter, refs[:], &count, len(refs))
	if detour.DtStatusFailed(st) || st&detour.DT_BUFFER_TOO_SMALL != 0 || count < 0 || count > len(refs) {
		return 0, false, errors.New("water landing polygon query is incomplete")
	}
	var surface float32
	found := false
	for _, ref := range refs[:count] {
		var flags uint16
		if detour.DtStatusFailed(p.mesh.GetPolyFlags(ref, &flags)) || !walkSurfaceFlags(flags) || flags&8 == 0 {
			continue
		}
		h, err := p.groundHeightAt(ref, q)
		if err != nil || h < bottom || h > top || found && h <= surface {
			continue
		}
		terrain, known, err := p.terrain(q)
		if err != nil {
			return 0, false, err
		}
		if !known {
			terrain = float32(math.NaN())
		}
		bed, err := p.floor(ctx, q, h, terrain)
		if err != nil && !errors.Is(err, errNoPhysicalFloor) {
			return 0, false, err
		}
		if errors.Is(err, errNoPhysicalFloor) || h-bed > MaxWadingDepth {
			surface, found = h, true
		}
	}
	return surface - swimImmersion, found, nil
}
