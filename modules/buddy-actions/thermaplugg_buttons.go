package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const (
	thermapluggMap         = 90
	thermapluggEntry       = 7800
	thermapluggButtonReach = 4.9
)

// Public encounter wiring, not a duty, priority or target-selection policy.
var thermapluggFaceButtons = [...]struct{ face, button uint32 }{
	{142211, 142214}, {142210, 142215}, {142209, 142216},
	{142208, 142217}, {142213, 142218}, {142212, 142219},
}

type thermapluggAttempt struct {
	session                              string
	self, visit                          uint64
	face, button                         uint64
	faceEntry, buttonEntry               uint32
	attempted, pending, written, unknown bool
	sentAtMs                             int64
}

type thermapluggButtons struct {
	faceEntry, buttonEntry uint32
	face, button           uint64
	path                   []position
	index                  int
	pathRevision           uint64
	approaching            bool
	bound                  thermapluggBinding
	executing              *thermapluggExecution
	attempt                *thermapluggAttempt
	memory                 any
	feedback               string
}

func thermapluggObjectPosition(object *worldv1.GameObjectState) position {
	return position{x: object.X, y: object.Y, z: object.Z, o: object.Orientation}
}

func thermapluggObjectValid(object *worldv1.GameObjectState, entry uint32) bool {
	return object != nil && object.Entry == entry && object.Guid>>48 == 0xf110 && object.Guid&0xffffff != 0 &&
		object.Present && object.MapKnown && object.MapId == thermapluggMap && object.TypeKnown && object.GameObjectType == 1 &&
		object.StateKnown && object.PositionKnown && finite(thermapluggObjectPosition(object))
}

func thermapluggButtonUsable(object *worldv1.GameObjectState) bool {
	return object != nil && object.FlagsKnown && object.Flags&(0x1|0x2|0x10) == 0
}

func (s *Service) thermapluggObject(ctx context.Context, entry uint32) (*worldv1.GameObjectState, *worldv1.SnapshotResponse) {
	w, err := s.readWorldSnapshotRequest(ctx, &worldv1.SnapshotRequest{GameObjectEntry: entry})
	if err != nil {
		return nil, nil
	}
	var found *worldv1.GameObjectState
	for _, object := range w.RequestedGameObjects {
		if object.GetEntry() == entry {
			// An ambiguous or incomplete row cannot select an object implicitly.
			if found != nil || !thermapluggObjectValid(object, entry) {
				return nil, w
			}
			found = object
		}
	}
	return found, w
}

func (s *Service) thermapluggMovementAllowedLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	x := s.thermaplugg.executing
	if x == nil || !x.binding.current(s, ctx, w, true) {
		return false
	}
	face, first := s.thermapluggObject(ctx, x.face.Entry)
	button, last := s.thermapluggObject(ctx, x.button.Entry)
	return x.binding.current(s, ctx, first, true) && x.binding.current(s, ctx, last, true) &&
		x.sameSelection(s) && thermapluggSameObject(face, x.face) && thermapluggSameObject(button, x.button) &&
		face.State == 0 && thermapluggButtonUsable(button) && s.thermapluggPolicySourceCurrent(x.source)
}

// STOP an already-owned physical approach without discarding its unresolved use.
func (s *Service) stopThermapluggApproachLocked(ctx context.Context) bool {
	r := &s.thermaplugg
	if len(r.path) != 0 {
		if !r.bound.identityCurrent(s, ctx) || !s.stopLocked(ctx) {
			return false
		}
	}
	r.path, r.index, r.pathRevision = nil, 0, 0
	return true
}

func (s *Service) clearThermapluggButtonLocked(ctx context.Context) bool {
	if !s.stopThermapluggApproachLocked(ctx) {
		return false
	}
	r := &s.thermaplugg
	if r.attempt != nil && r.attempt.pending {
		return true // Lua memory/reset/clear never releases physical use custody.
	}
	r.faceEntry, r.buttonEntry, r.face, r.button = 0, 0, 0, 0
	return true
}

func (s *Service) thermapluggButtonPath(ctx context.Context, button position) ([]position, error) {
	// Fixed interaction geometry moves inward from wall-mounted objects. Every
	// route still passes complete navigation and ordinary movement checks.
	direction := math.Atan2(670.159-float64(button.y), -531.324-float64(button.x))
	goal := position{x: button.x + float32(4.5*math.Cos(direction)), y: button.y + float32(4.5*math.Sin(direction)), z: button.z - 1.25}
	path, err := s.mapNavigationPath(ctx, s.navDataDir, thermapluggMap, s.pos, goal)
	if err == nil && len(path) >= 2 && validMapNavigationPath(path, s.pos, goal) && distance(path[len(path)-1], button) <= thermapluggButtonReach {
		return path, nil
	}
	return s.interactionNavigationPath(ctx, s.navDataDir, thermapluggMap, s.pos, button)
}

func (s *Service) thermapluggButtonsTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	r := &s.thermaplugg
	if w.GetSelf().GetMapId() != thermapluggMap && r.face == 0 && r.attempt == nil && len(r.path) == 0 {
		return false
	}
	selected, observed, valid := s.thermapluggDecisionLocked(ctx, w)
	if !valid {
		s.state.Reason = "thermaplugg_policy_unavailable_or_changed"
		return !s.stopThermapluggApproachLocked(ctx)
	}
	switch selected.kind {
	case "idle", "wait":
		return !s.stopThermapluggApproachLocked(ctx)
	case "clear":
		if !s.clearThermapluggButtonLocked(ctx) {
			return true
		}
		r.feedback = ""
		return false
	case "confirmed":
		a := r.attempt
		if a == nil || !a.pending || !selected.matchesAttempt(a) || !a.sameBody(s, observed) || selected.face.State != 1 {
			return !s.stopThermapluggApproachLocked(ctx)
		}
		if !s.stopThermapluggApproachLocked(ctx) {
			return true
		}
		a.pending, r.feedback = false, ""
		s.clearThermapluggButtonLocked(ctx)
		s.state.Reason = "thermaplugg_bomb_chute_closed"
		return true
	}
	// Successful/ambiguous use waits for this exact face's actual closure. No
	// source edit, elapsed timer, new choice or memory reset can replay it.
	if r.attempt != nil && r.attempt.pending {
		return !s.stopThermapluggApproachLocked(ctx)
	}
	if r.face != selected.face.Guid || r.button != selected.button.Guid {
		if !s.clearThermapluggButtonLocked(ctx) {
			return true
		}
		r.face, r.button = selected.face.Guid, selected.button.Guid
		r.faceEntry, r.buttonEntry = selected.face.Entry, selected.button.Entry
	}
	x := selected.execution
	r.bound, r.executing = x.binding, &x
	defer func() { r.executing = nil }()
	to := thermapluggObjectPosition(selected.button)
	if distance(s.pos, to) <= thermapluggButtonReach {
		var stopped bool
		observed, stopped = s.stopThermapluggExecutionLocked(ctx, &x)
		if !stopped || !s.thermapluggMovementAllowedLocked(ctx, observed) || !x.binding.current(s, ctx, observed, true) || distance(s.pos, to) > thermapluggButtonReach {
			r.feedback = "interaction_observation_changed"
			return false
		}
		now := s.now() // Last local callback; native bindings are checked after it.
		if !x.binding.current(s, ctx, observed, true) || !x.sameSelection(s) || !s.thermapluggPolicySourceCurrent(x.source) {
			r.feedback = "interaction_observation_changed"
			return false
		}
		a := &thermapluggAttempt{session: s.sessionID, self: s.selfGUID, visit: observed.VisitRevision,
			face: r.face, button: r.button, faceEntry: r.faceEntry, buttonEntry: r.buttonEntry, attempted: true, pending: true, sentAtMs: now.UnixMilli()}
		r.attempt = a // Reserve before the sole use callback, including lost replies.
		err := s.sendPacketLocked(ctx, opcode.CMSGGameobjUse, binary.LittleEndian.AppendUint64(nil, a.button))
		a.written, a.unknown = err == nil, err != nil && !modcb.PacketWasNotDispatched(err)
		if modcb.PacketWasNotDispatched(err) {
			a.attempted, a.pending, r.feedback = false, false, "use_not_dispatched"
		}
		s.state.Reason = "thermaplugg_waiting_for_bomb_chute"
		return true
	}
	if len(r.path) == 0 || r.pathRevision != s.posRevision {
		var stopped bool
		observed, stopped = s.stopThermapluggExecutionLocked(ctx, &x)
		if !stopped {
			r.feedback = "route_observation_changed"
			return false
		}
		plan, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		path, err := s.thermapluggButtonPath(plan, to)
		cancel()
		if err != nil || len(path) < 2 || !validMapNavigationPath(path, s.pos, path[len(path)-1]) || distance(path[len(path)-1], to) > thermapluggButtonReach ||
			!x.binding.current(s, ctx, observed, true) || !s.thermapluggPolicySourceCurrent(x.source) {
			r.feedback = "route_unavailable_or_changed"
			return !s.stopThermapluggApproachLocked(ctx)
		}
		r.path, r.index = path, 0
	}
	r.approaching = true
	moved := s.navigationStepWithinLocked(ctx, r.path, &r.index, finite)
	r.approaching = false
	if !moved || r.index >= len(r.path) && !s.state.Moving {
		r.feedback = "route_step_not_completed"
		return !s.stopThermapluggApproachLocked(ctx)
	}
	r.pathRevision = s.posRevision
	s.state.Reason = "thermaplugg_approaching_bomb_button"
	return true
}
