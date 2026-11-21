package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// MoveBody executes one caller-selected input. It neither chooses a direction
// nor retries a failed route. Local physical proof does not call FindPath.
func (s *Service) MoveBody(ctx context.Context, r *actionsv1.BodyMovementRequest) (*actionsv1.State, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetSequenceId() == 0 ||
		math.IsNaN(float64(r.BearingRadians)) || math.IsInf(float64(r.BearingRadians), 0) || math.Abs(float64(r.BearingRadians)) > math.Pi+.001 ||
		!r.Jump && (r.DurationMs == 0 || r.DurationMs > 500) {
		return nil, status.Error(codes.InvalidArgument, "current body, visit, sequence and bounded relative input required")
	}
	token, supplied := modcb.IncomingPolicySpell(ctx)
	var decoded [32]byte
	if n, err := hex.Decode(decoded[:], []byte(token)); !supplied || err != nil || n != len(decoded) || hex.EncodeToString(decoded[:]) != token {
		return nil, status.Error(codes.PermissionDenied, "authenticated policy body movement required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || s.bodyMoving {
		return nil, status.Error(codes.FailedPrecondition, "current connected and available own body required")
	}
	w, err := s.snapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || r.VisitId != s.encounterVisit.revision || !s.admitsPolicyEncounterLocked(ctx, w) ||
		safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) || s.clientControlLost || s.clientControlRestoring || s.state.MotionUnconfirmed ||
		s.state.CastPending || s.state.Traveling || s.state.InteractionOwner != "" || s.recovery.state.Active || s.crowdControl.active ||
		!w.Self.MovementFlagsKnown || !movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags) ||
		r.Jump && w.Self.MovementFlags&0x20000000 != 0 {
		return nil, status.Error(codes.FailedPrecondition, "current controllable ground body and its Lua writer required")
	}
	if old := s.state.BodyMovement; old != nil && old.SequenceId == r.SequenceId && old.VisitId == r.VisitId {
		return s.stateLocked(), nil
	}
	speed := observedRunSpeed(w.Self)
	if speed == 0 {
		return nil, status.Error(codes.FailedPrecondition, "observed normal movement speed required")
	}
	view := &actionsv1.BodyMovementState{SequenceId: r.SequenceId, VisitId: r.VisitId, Phase: "checking_local_physics"}
	s.state.BodyMovement = view
	from, revision, mapID := s.pos, s.positionRevision, s.posMap
	bearing := float64(from.o) + float64(r.BearingRadians)
	plan := s.bodyMotionPlan
	if plan == nil {
		plan = buddynav.PlanBodyMotionContext
	}
	s.bodyMoving = true
	ownerEpoch, priorUntil := s.hostEncounterEpoch, s.hostEncounterUntil
	reservedUntil := s.now().Add(3 * time.Second)
	// Step cannot publish a new ownership decision during a synchronous
	// dispatch. Reserve only this bounded input; explicit revocation changes
	// the epoch and still fences every subsequent packet.
	if s.hostEncounterKey != "" && s.hostEncounterUntil.Before(reservedUntil) {
		s.hostEncounterUntil = reservedUntil
	}
	defer func() {
		s.bodyMoving = false
		s.lastStep = s.now()
		if s.hostEncounterEpoch == ownerEpoch && s.hostEncounterVisit == r.VisitId && s.hostEncounterUntil.Equal(reservedUntil) {
			s.hostEncounterUntil = priorUntil
			if ctx.Err() == nil && !s.state.MotionUnconfirmed && s.encounterVisit.bound && s.encounterVisit.revision == r.VisitId {
				s.hostEncounterUntil = s.now().Add(encounterLeaseDuration)
			}
		}
	}()
	dataDir := s.navDataDir
	s.mu.Unlock()
	points, err := plan(ctx, dataDir, mapID, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, bearing, speed, time.Duration(r.DurationMs)*time.Millisecond, r.Jump)
	s.mu.Lock()
	if err != nil || len(points) < 2 || len(points) > 24 {
		view.Phase = "local_physics_refused"
		if err != nil {
			view.Detail = navigationDiagnostic(err, dataDir)
		} else {
			view.Detail = "local physics returned an invalid finite trajectory"
		}
		return s.stateLocked(), nil
	}
	settling := points[0].Settling
	airborne := r.Jump || settling
	horizontalSpeed, verticalSpeed := speed, float64(buddynav.BodyJumpVelocity)
	if settling {
		horizontalSpeed, verticalSpeed = 0, 0
	}
	ctx = modcb.ForwardPolicySpell(ctx)
	passive := w.Self.MovementFlags & movementflags.PassiveModifiers
	var motionClock uint32
	valid := func() bool {
		fresh, e := s.snapshot(ctx)
		owner := s.hostEncounterKey == "" || s.trustedEncounterHostLocked(ctx) && s.hostEncounterVisit == r.VisitId && s.hostEncounterEpoch == ownerEpoch
		if e != nil || s.closed || !owner || !s.matchesCurrentEncounterVisitLocked(fresh) || fresh.VisitRevision != r.VisitId ||
			fresh.Self.PositionRevision != revision || fresh.Self.MapId != mapID ||
			safeSelfCombat(fresh, s.selfGUID) != "" || s.clientControlLost || s.clientControlRestoring {
			if s.state.Moving && (e != nil || fresh.GetSelf().GetPositionRevision() == revision) {
				s.state.MotionUnconfirmed = true
			}
			if e == nil {
				s.refreshPositionLocked(fresh)
			}
			view.Phase = "body_or_authoritative_position_changed"
			return false
		}
		w = fresh
		return true
	}
	write := func(code uint32, p position, elapsed time.Duration, falling, moving bool) bool {
		if ctx.Err() != nil {
			view.Phase = "body_input_interrupted"
			s.state.MotionUnconfirmed = s.state.MotionUnconfirmed || s.state.Moving
			return false
		}
		if !valid() {
			return false
		}
		clock, e := s.session.GetClock(ctx, &emptypb.Empty{})
		if e != nil || clock == nil {
			view.Phase = "body_clock_or_position_unavailable"
			s.state.MotionUnconfirmed = s.state.MotionUnconfirmed || s.state.Moving
			return false
		}
		if !valid() {
			return false
		}
		p.o = float32(math.Remainder(bearing, 2*math.Pi))
		stamp := clock.ClientTimeMs
		if elapsed > 0 {
			stamp = motionClock + uint32(elapsed/time.Millisecond)
		}
		packet := bodyMovementPacket(s.selfGUID, stamp, p, passive, elapsed, bearing, horizontalSpeed, verticalSpeed, falling, moving)
		if _, e := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: code, Payload: packet}); e != nil {
			view.Phase, s.state.MotionUnconfirmed = "transport_unconfirmed_no_retry", true
			s.publishClientMovementLocked(ctx, nil, p, false)
			return false
		}
		view.TransportWritten, view.PositionEstimated = true, true
		if code == opcode.MSGMoveStartForward || code == opcode.MSGMoveJump || falling && elapsed == 0 {
			motionClock = stamp
		}
		view.X, view.Y, view.Z = p.x, p.y, p.z
		s.state.TransportWrites++
		s.pos, s.posEstimated, s.posAt, s.state.Moving = p, true, s.now(), moving || falling
		s.posRevision++
		s.publishClientMovementLocked(ctx, w, p, true)
		return true
	}
	if settling {
		if !write(opcode.MSGMoveHeartbeat, from, 0, true, false) {
			return s.stateLocked(), nil
		}
	} else {
		if !write(opcode.MSGMoveSetFacing, from, 0, false, false) || !write(opcode.MSGMoveStartForward, from, 0, false, true) {
			return s.stateLocked(), nil
		}
		if r.Jump && !write(opcode.MSGMoveJump, from, 0, true, true) {
			return s.stateLocked(), nil
		}
	}
	view.Phase = "executing_body_input"
	started := s.now()
	for i, sample := range points[1:] {
		wait := sample.At - s.now().Sub(started)
		if wait > 0 {
			s.mu.Unlock()
			err = s.waitBodyMotion(ctx, wait)
			s.mu.Lock()
			if err != nil {
				view.Phase, s.state.MotionUnconfirmed = "body_input_interrupted", true
				return s.stateLocked(), nil
			}
		}
		last := i == len(points)-2
		code := uint32(opcode.MSGMoveHeartbeat)
		if last && airborne {
			code = opcode.MSGMoveFallLand
		}
		point := position{x: sample.Point.X, y: sample.Point.Y, z: sample.Point.Z}
		if !write(code, point, sample.At, airborne && !last, !settling) {
			return s.stateLocked(), nil
		}
	}
	if write(opcode.MSGMoveStop, s.pos, 0, false, false) {
		view.Phase = "body_input_written"
		if settling {
			view.Phase = "body_settled_written"
		}
		s.followNav.Reset()
		s.targetMovement.nav.Reset()
	}
	return s.stateLocked(), nil
}

func (s *Service) waitBodyMotion(ctx context.Context, duration time.Duration) error {
	if s.bodyMotionWait != nil {
		return s.bodyMotionWait(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func bodyMovementPacket(guid uint64, clock uint32, p position, passive uint32, elapsed time.Duration, bearing, speed, verticalSpeed float64, falling, moving bool) []byte {
	packet := movementPacket(guid, clock, p, moving)
	n := len(packedGUID(guid))
	flags := binary.LittleEndian.Uint32(packet[n:]) | passive
	if falling {
		flags |= 0x1000
	}
	binary.LittleEndian.PutUint32(packet[n:], flags)
	binary.LittleEndian.PutUint32(packet[n+26:], uint32(elapsed/time.Millisecond))
	if falling {
		for _, value := range []float32{float32(verticalSpeed), float32(math.Sin(bearing)), float32(math.Cos(bearing)), float32(speed)} {
			packet = binary.LittleEndian.AppendUint32(packet, math.Float32bits(value))
		}
	}
	return packet
}
