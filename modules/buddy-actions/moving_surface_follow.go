package buddyactions

import (
	"context"
	"errors"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const passengerFlag uint32 = 0x200

type movingSurfaceBody struct {
	visit, rawPositionRevision uint64
	hullObjectRevision         uint64
	packet                     *modv1.WorldPacket
	info                       movementinfo.Info
	uncertain                  bool
}

// This is mechanical state of the ordinary follow actuator. Lua owns its
// selected board/ride/leave goal, just as it owns the existing formation.
type movingSurfaceFollow struct {
	goal            *actionsv1.FollowFormationRequest
	until           time.Time
	hostEpoch       uint64
	phase           string
	jumpRequired    bool
	body            *movingSurfaceBody
	meshes          map[uint32]*buddynav.SurfaceMesh
	paths           map[uint64]*buddynav.TransportPath
	pathKeys        map[uint64]surfacePathKey
	plan            []buddynav.BodyMotionSample
	planAt          time.Time
	planIndex       int
	planTransform   buddynav.SurfaceTransform
	planBearing     float64
	planSpeed       float64
	planJump        bool
	planWasAttached bool
	planDetached    bool
	planWorldOffset buddynav.Point
	lastAttempt     string
	planSource      *surfaceMotionSource
}

type surfacePathKey struct {
	objectRevision            uint64
	path, speed, acceleration uint32
}

func receivedPassenger(p *worldv1.PlayerState, visit uint64) (*worldv1.PassengerMovementFacts, bool) {
	frame := p.GetPassengerMovement()
	return frame, frame != nil && frame.VisitRevision == visit && frame.Revision != 0 && frame.Info != nil &&
		p.GetPresent() && p.GetAliveKnown() && p.GetAlive() && p.GetMovementFlagsKnown()
}

func (s *Service) surfaceOwnFrameLocked(ctx context.Context, w *worldv1.SnapshotResponse) (bool, uint64, bool, uint64) {
	if b := s.surfaceFollow.body; b != nil && b.visit == w.VisitRevision && b.rawPositionRevision == w.GetSelf().GetPositionRevision() {
		if _, err := s.currentSurfaceReportLocked(ctx, w); err != nil {
			return false, 0, false, 0
		}
		guid := uint64(0)
		if b.info.TransportPresent {
			guid = b.info.Transport.GUID
		}
		return false, 0, true, guid
	}
	if frame, known := receivedPassenger(w.Self, w.VisitRevision); known {
		return true, frame.Info.GetTransport().GetGuid(), false, 0
	}
	// A received complete ordinary source establishes an unboarded body.
	if raw := w.GetSelfMovementFacts(); raw.GetKnown() && raw.GetVisitRevision() == w.VisitRevision && raw.Info != nil {
		return true, raw.Info.GetTransport().GetGuid(), false, 0
	}
	return false, 0, false, 0
}

func (s *Service) surfaceObservationLocked(ctx context.Context, w *worldv1.SnapshotResponse) *actionsv1.MovingSurfaceObservation {
	known, guid, written, writtenGUID := s.surfaceOwnFrameLocked(ctx, w)
	view := &actionsv1.MovingSurfaceObservation{Supported: true, SelfAttachmentKnown: known,
		SelfTransportGuid: guid, SelfWriteKnown: written, SelfWrittenTransportGuid: writtenGUID,
		Phase: s.surfaceFollow.phase, JumpRequired: s.surfaceFollow.jumpRequired}
	if p, ok := receivedPassenger(w.GetSelf(), w.VisitRevision); ok {
		view.Self = proto.Clone(p).(*worldv1.PassengerMovementFacts)
	}
	if p, ok := receivedPassenger(w.GetOwner(), w.VisitRevision); ok {
		view.Owner = proto.Clone(p).(*worldv1.PassengerMovementFacts)
	}
	for _, hull := range w.GetMovingTransports() {
		view.Transports = append(view.Transports, proto.Clone(hull).(*worldv1.GameObjectState))
	}
	return view
}

func movingHull(w *worldv1.SnapshotResponse, guid uint64) *worldv1.GameObjectState {
	for _, hull := range w.GetMovingTransports() {
		if hull.GetGuid() == guid && hull.Present && hull.MapKnown && hull.MapId == w.GetSelf().GetMapId() &&
			hull.TypeKnown && hull.GameObjectType == 15 && hull.MovingTransport != nil {
			return hull
		}
	}
	return nil
}

func (s *Service) setSurfaceFormationLocked(ctx context.Context, r *actionsv1.FollowFormationRequest, w *worldv1.SnapshotResponse) (*emptypb.Empty, error) {
	owner, known := receivedPassenger(w.Owner, w.VisitRevision)
	if !known || r.OwnerPassengerRevision != owner.Revision || r.TransportGuid == 0 || movingHull(w, r.TransportGuid) == nil {
		return nil, status.Error(codes.FailedPrecondition, "current received passenger and actual ship required")
	}
	selfKnown, selfGUID, written, writtenGUID := s.surfaceOwnFrameLocked(ctx, w)
	if written {
		selfKnown, selfGUID = true, writtenGUID
	}
	ownerGUID := owner.Info.GetTransport().GetGuid()
	switch r.SurfaceMode {
	case "board":
		if !selfKnown || selfGUID != 0 || ownerGUID != r.TransportGuid {
			return nil, status.Error(codes.FailedPrecondition, "unboarded self and received owner attachment required")
		}
	case "ride":
		if !selfKnown || selfGUID != r.TransportGuid || ownerGUID != r.TransportGuid {
			return nil, status.Error(codes.FailedPrecondition, "matching own frame and received owner ship required")
		}
	case "leave":
		if !selfKnown || selfGUID != r.TransportGuid || owner.Info.Transport != nil {
			return nil, status.Error(codes.FailedPrecondition, "own ship and received owner departure required")
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "surface mode must be board, ride or leave")
	}
	s.surfaceFollow.goal = proto.Clone(r).(*actionsv1.FollowFormationRequest)
	s.surfaceFollow.until, s.surfaceFollow.hostEpoch = s.now().Add(encounterLeaseDuration), s.hostEncounterEpoch
	s.followGoal = followFormationGoal{}
	return &emptypb.Empty{}, nil
}

func surfacePoint(p position) buddynav.Point { return buddynav.Point{X: p.x, Y: p.y, Z: p.z} }
func surfacePosition(p buddynav.Point, facing float32) position {
	return position{x: p.X, y: p.Y, z: p.Z, o: facing}
}

func (s *Service) surfaceMeshLocked(ctx context.Context, hull *worldv1.GameObjectState) (*buddynav.SurfaceMesh, error) {
	facts := hull.GetMovingTransport()
	if facts.DisplayId == nil || facts.Scale == nil || *facts.Scale != 1 {
		return nil, errors.New("moving model requires its observed display and matching body-scale bake")
	}
	if s.surfaceFollow.meshes == nil {
		s.surfaceFollow.meshes = make(map[uint32]*buddynav.SurfaceMesh)
	}
	mesh := s.surfaceFollow.meshes[*facts.DisplayId]
	if mesh == nil {
		var err error
		mesh, err = buddynav.LoadSurfaceMesh(ctx, s.navDataDir, *facts.DisplayId)
		if err != nil {
			return nil, err
		}
		s.surfaceFollow.meshes[*facts.DisplayId] = mesh
	}
	return mesh, nil
}

func (s *Service) requestSurfaceTemplateLocked(ctx context.Context, w *worldv1.SnapshotResponse, hull *worldv1.GameObjectState) error {
	facts := hull.MovingTransport
	if facts.TemplateQueryPhase != "" {
		return errors.New("awaiting the original moving-transport template reply")
	}
	request, err := anypb.New(&worldv1.QueryMovingTransportTemplateRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID,
		VisitRevision: w.VisitRevision, Guid: hull.Guid, Entry: hull.Entry, ObjectRevision: facts.ObjectRevision})
	if err != nil {
		return err
	}
	_, err = s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "buddy-world", Method: "query_moving_transport_template", Request: request})
	return err
}

func (s *Service) surfacePoseLocked(ctx context.Context, w *worldv1.SnapshotResponse, hull *worldv1.GameObjectState) (buddynav.SurfaceTransform, uint32, error) {
	facts := hull.MovingTransport
	template := facts.GetTemplate()
	if template == nil {
		if err := s.requestSurfaceTemplateLocked(ctx, w, hull); err != nil {
			return buddynav.SurfaceTransform{}, 0, err
		}
		return buddynav.SurfaceTransform{}, 0, errors.New("waiting for the original ship template response")
	}
	if template.Guid != hull.Guid || template.Entry != hull.Entry || template.SelfGuid != s.selfGUID || template.VisitRevision != w.VisitRevision {
		return buddynav.SurfaceTransform{}, 0, errors.New("ship template does not belong to this observed body, visit and hull")
	}
	if s.surfaceFollow.paths == nil {
		s.surfaceFollow.paths = make(map[uint64]*buddynav.TransportPath)
	}
	if s.surfaceFollow.pathKeys == nil {
		s.surfaceFollow.pathKeys = make(map[uint64]surfacePathKey)
	}
	key := surfacePathKey{objectRevision: facts.ObjectRevision, path: template.TaxiPathId, speed: template.MoveSpeed, acceleration: template.AccelerationRate}
	path := s.surfaceFollow.paths[hull.Guid]
	if path == nil || s.surfaceFollow.pathKeys[hull.Guid] != key {
		var err error
		path, err = buddynav.LoadTransportPath(ctx, s.navDataDir, template.TaxiPathId, float64(template.MoveSpeed), float64(template.AccelerationRate))
		if err != nil {
			return buddynav.SurfaceTransform{}, 0, err
		}
		s.surfaceFollow.paths[hull.Guid] = path
		s.surfaceFollow.pathKeys[hull.Guid] = key
	}
	if facts.PathProgressMs == nil || facts.PeriodMs == nil || *facts.PeriodMs != path.Period() || facts.PositionRevision == 0 || !hull.PositionKnown {
		return buddynav.SurfaceTransform{}, 0, errors.New("ship clock, period and same-frame hull checkpoint required")
	}
	clock, age := *facts.PathProgressMs, facts.MovementAgeMs
	witness := buddynav.SurfaceTransform{Origin: buddynav.Point{X: hull.X, Y: hull.Y, Z: hull.Z}, Yaw: hull.Orientation, Scale: 1}
	// The full paired passenger frame supplies another actually received
	// hull checkpoint. Prefer its newer measurement to extrapolating an old
	// CREATE for a vessel whose position was not dirtied during the trip.
	players := []*worldv1.PlayerState{w.Self, w.Owner}
	players = append(players, w.NearbyPlayers...)
	for _, player := range players {
		frame, known := receivedPassenger(player, w.VisitRevision)
		info := frame.GetInfo()
		attachment := info.GetTransport()
		if !known || attachment.GetGuid() != hull.Guid || frame.AgeMs >= age ||
			!player.GetMapKnown() || player.GetMapId() != hull.MapId || !player.GetPositionKnown() || info.GetFlags()&passengerFlag == 0 {
			continue
		}
		transform, err := buddynav.PassengerTransform(buddynav.Point{X: info.X, Y: info.Y, Z: info.Z},
			buddynav.Point{X: attachment.X, Y: attachment.Y, Z: attachment.Z}, info.Orientation, attachment.Orientation)
		if err != nil {
			return buddynav.SurfaceTransform{}, 0, err
		}
		clock, age, witness = attachment.Time, frame.AgeMs, transform
	}
	checkpoint, err := path.Sample(uint64(clock))
	if err != nil || checkpoint.MapID != hull.MapId || !witness.Valid() {
		return buddynav.SurfaceTransform{}, 0, errors.New("ship checkpoint map differs from its client path")
	}
	// The on-wire progress is uint32. Crossing its wrap changes the path
	// phase; wait for the receiving-side checkpoint instead of casting an
	// unbounded prediction to a different wire clock.
	if age > math.MaxUint32-uint64(clock) {
		return buddynav.SurfaceTransform{}, 0, errors.New("ship clock wrapped without a new received checkpoint")
	}
	if facts.PathFraction != nil && facts.PathFractionRevision != 0 && facts.PathFractionAgeMs < age {
		// The high sixteen bits are the server's narrowed full-progress
		// expression, not an independent absolute phase. They can disprove
		// an older full-clock prediction, but cannot manufacture a new pose.
		atScalar := uint64(clock) + age - facts.PathFractionAgeMs
		expected := uint32(float32(atScalar)/float32(path.Period())*65535) & 0xffff
		delta := int32(expected) - int32(*facts.PathFraction)
		if delta > 32768 {
			delta -= 65536
		} else if delta < -32768 {
			delta += 65536
		}
		if *facts.PathFraction > 65535 || delta < -1 || delta > 1 {
			return buddynav.SurfaceTransform{}, 0, errors.New("newer received ship scalar contradicts the calculated clock")
		}
	}
	progress := uint64(clock) + age
	sample, err := path.Sample(progress)
	if err != nil || sample.Transfer || sample.MapID != w.Self.MapId || path.CrossesTransfer(uint64(clock), progress) {
		return buddynav.SurfaceTransform{}, 0, errors.New("ship transform crosses a transfer without a receiving-side hull witness")
	}
	if checkpoint.Stopped && sample.Stopped && age < uint64(path.Period()) && checkpoint.Position == sample.Position {
		// The realm retains its last physical tick during a dock dwell. Keep
		// that actually received hull pose for the same uninterrupted dwell.
		return witness, uint32(progress), nil
	}
	if distance(surfacePosition(checkpoint.Position, checkpoint.Yaw), surfacePosition(witness.Origin, witness.Yaw)) > .02 ||
		math.Abs(math.Remainder(float64(checkpoint.Yaw)-float64(witness.Yaw), 2*math.Pi)) > .01 {
		return buddynav.SurfaceTransform{}, 0, errors.New("client ship path does not match the received hull checkpoint")
	}
	return buddynav.SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}, uint32(progress), nil
}
