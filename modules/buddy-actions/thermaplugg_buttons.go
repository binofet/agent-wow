package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const (
	thermapluggMap           = 90
	thermapluggEntry         = 7800
	thermapluggButtonReach   = 4.9
	thermapluggConfirmWindow = 800 * time.Millisecond
)

// Each button closes the face wired to it. The inward offset is needed because
// the wall-mounted game objects do not themselves lie on the ground mesh.
var thermapluggFaceButtons = [...]struct{ face, button uint32 }{
	{142211, 142214}, {142210, 142215}, {142209, 142216},
	{142208, 142217}, {142213, 142218}, {142212, 142219},
}

type thermapluggButtons struct {
	faceEntry, buttonEntry uint32
	face, button           uint64
	path                   []position
	index                  int
	pathRevision           uint64
	approaching            bool
	verifyAfter            time.Time
	retryAfter             [len(thermapluggFaceButtons)]time.Time
	nextScan               time.Time
}

func (s *Service) thermapluggActiveLocked(w *worldv1.SnapshotResponse) bool {
	if s.independent || !s.state.Assisting || safeSelfCombat(w, s.selfGUID) != "" ||
		w.Self.MapId != thermapluggMap || !w.Self.ClassKnown || w.Self.ClassId != 4 ||
		!w.GetOwner().GetPresent() || !w.Owner.MapKnown || w.Owner.MapId != thermapluggMap {
		return false
	}
	active := func(c *worldv1.CreatureState) bool {
		return c != nil && c.Entry == thermapluggEntry && c.Present && c.MapKnown && c.MapId == thermapluggMap &&
			c.AliveKnown && c.Alive && c.HealthKnown && c.Health > 0 && c.UnitFlagsKnown && c.UnitFlags&0x80000 != 0
	}
	if active(w.OwnerTarget) {
		return true
	}
	for _, c := range w.NearbyCreatures {
		if active(c) {
			return true
		}
	}
	return false
}

func (s *Service) thermapluggObject(ctx context.Context, entry uint32) (*worldv1.GameObjectState, *worldv1.SnapshotResponse) {
	w, err := s.readWorldSnapshotRequest(ctx, &worldv1.SnapshotRequest{GameObjectEntry: entry})
	if err != nil {
		return nil, nil
	}
	for _, object := range w.RequestedGameObjects {
		if object.Entry == entry && object.Guid>>48 == 0xf110 && object.Present && object.MapKnown &&
			object.MapId == thermapluggMap && object.TypeKnown && object.GameObjectType == 1 && object.StateKnown &&
			object.PositionKnown && finite(thermapluggObjectPosition(object)) {
			return object, w
		}
	}
	return nil, w
}

func thermapluggObjectPosition(object *worldv1.GameObjectState) position {
	return position{x: object.X, y: object.Y, z: object.Z}
}

func thermapluggButtonUsable(object *worldv1.GameObjectState) bool {
	// Busy, locked and unselectable objects cannot accept this interaction.
	return object != nil && object.FlagsKnown && object.Flags&(0x1|0x2|0x10) == 0
}

func (s *Service) thermapluggMovementAllowedLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	r := &s.thermaplugg
	if !s.thermapluggActiveLocked(w) {
		return false
	}
	face, observed := s.thermapluggObject(ctx, r.faceEntry)
	return face.GetGuid() == r.face && face.GetState() == 0 && s.thermapluggActiveLocked(observed) &&
		observed.Self.PositionRevision == s.positionRevision
}

func (s *Service) clearThermapluggButtonLocked(ctx context.Context) bool {
	r := &s.thermaplugg
	hadPath := len(r.path) != 0
	r.faceEntry, r.buttonEntry, r.face, r.button = 0, 0, 0, 0
	r.path, r.index, r.pathRevision, r.verifyAfter = nil, 0, 0, time.Time{}
	return !hadPath || s.stopLocked(ctx)
}

func (s *Service) retryThermapluggButtonLocked(ctx context.Context) bool {
	r := &s.thermaplugg
	for i, pair := range thermapluggFaceButtons {
		if pair.face == r.faceEntry {
			r.retryAfter[i] = s.now().Add(time.Second)
			break
		}
	}
	return s.clearThermapluggButtonLocked(ctx)
}

func (s *Service) selectThermapluggButtonLocked(ctx context.Context) {
	r := &s.thermaplugg
	if s.now().Before(r.nextScan) {
		return
	}
	r.nextScan = s.now().Add(400 * time.Millisecond)
	observe, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	closest := math.Inf(1)
	for i, pair := range thermapluggFaceButtons {
		if observe.Err() != nil {
			break
		}
		if s.now().Before(r.retryAfter[i]) {
			continue
		}
		face, _ := s.thermapluggObject(observe, pair.face)
		if face == nil || face.State != 0 {
			continue
		}
		button, _ := s.thermapluggObject(observe, pair.button)
		if !thermapluggButtonUsable(button) {
			continue
		}
		if d := distance(s.pos, thermapluggObjectPosition(button)); d < closest {
			r.faceEntry, r.buttonEntry, r.face, r.button = pair.face, pair.button, face.Guid, button.Guid
			closest = d
		}
	}
}

func (s *Service) thermapluggButtonPath(ctx context.Context, button position) ([]position, error) {
	// Aim into the room at floor level. The central floor is on the walkable
	// side of every column; the navmesh still validates every route segment.
	direction := math.Atan2(670.159-float64(button.y), -531.324-float64(button.x))
	goal := position{
		x: button.x + float32(4.5*math.Cos(direction)),
		y: button.y + float32(4.5*math.Sin(direction)),
		z: button.z - 1.25,
	}
	path, err := s.mapNavigationPath(ctx, s.navDataDir, thermapluggMap, s.pos, goal)
	if err == nil && distance(path[len(path)-1], button) <= thermapluggButtonReach {
		return path, nil
	}
	return s.interactionNavigationPath(ctx, s.navDataDir, thermapluggMap, s.pos, button)
}

func (s *Service) thermapluggButtonsTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	r := &s.thermaplugg
	if !s.thermapluggActiveLocked(w) || !s.refreshPositionLocked(w) {
		return !s.clearThermapluggButtonLocked(ctx)
	}
	if r.face == 0 {
		s.selectThermapluggButtonLocked(ctx)
		if r.face == 0 {
			return false
		}
	}
	face, observed := s.thermapluggObject(ctx, r.faceEntry)
	if face.GetGuid() != r.face || !s.thermapluggActiveLocked(observed) {
		return !s.retryThermapluggButtonLocked(ctx)
	}
	if face.State != 0 {
		confirmed := face.State == 1 && !r.verifyAfter.IsZero()
		stopped := s.clearThermapluggButtonLocked(ctx)
		if confirmed {
			s.state.Reason = "thermaplugg_bomb_chute_closed"
		}
		return confirmed || !stopped
	}
	if !r.verifyAfter.IsZero() && s.now().Before(r.verifyAfter) {
		// Normal combat can continue while the server reports the result.
		return false
	}
	button, observed := s.thermapluggObject(ctx, r.buttonEntry)
	if button.GetGuid() != r.button || !thermapluggButtonUsable(button) ||
		!s.thermapluggActiveLocked(observed) || !s.refreshPositionLocked(observed) {
		return !s.retryThermapluggButtonLocked(ctx)
	}
	to := thermapluggObjectPosition(button)
	if distance(s.pos, to) <= thermapluggButtonReach {
		if !s.stopLocked(ctx) {
			return true
		}
		r.path, r.index = nil, 0
		verifyFace, _ := s.thermapluggObject(ctx, r.faceEntry)
		verifyButton, fresh := s.thermapluggObject(ctx, r.buttonEntry)
		if verifyFace.GetGuid() != r.face || verifyFace.GetState() != 0 || verifyButton.GetGuid() != r.button ||
			!thermapluggButtonUsable(verifyButton) || !s.thermapluggActiveLocked(fresh) || !s.refreshPositionLocked(fresh) ||
			distance(s.pos, thermapluggObjectPosition(verifyButton)) > thermapluggButtonReach {
			return !s.retryThermapluggButtonLocked(ctx)
		}
		var payload [8]byte
		binary.LittleEndian.PutUint64(payload[:], r.button)
		r.verifyAfter = s.now().Add(thermapluggConfirmWindow)
		if !s.sendLocked(ctx, opcode.CMSGGameobjUse, payload[:]) {
			return true
		}
		s.state.Reason = "thermaplugg_waiting_for_bomb_chute"
		return true
	}
	if len(r.path) == 0 || r.pathRevision != s.posRevision {
		if !s.stopLocked(ctx) {
			return true
		}
		plan, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		path, err := s.thermapluggButtonPath(plan, to)
		cancel()
		if err != nil {
			return !s.retryThermapluggButtonLocked(ctx)
		}
		r.path, r.index = path, 0
	}
	r.approaching = true
	moved := s.navigationStepWithinLocked(ctx, r.path, &r.index, finite)
	r.approaching = false
	if !moved || r.index >= len(r.path) && !s.state.Moving {
		return !s.retryThermapluggButtonLocked(ctx)
	}
	r.pathRevision = s.posRevision
	s.state.Reason = "thermaplugg_approaching_bomb_button"
	return true
}
