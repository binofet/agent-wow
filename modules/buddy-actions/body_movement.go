package buddyactions

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The optional parent lease permits this one normal body input while retaining
// its exact already-admitted idle custody. It does not acquire, renew, release
// or call the generic idle guard while its own physical movement is active.
func (s *Service) bodyIdleLeaseCurrentLocked(r *actionsv1.BodyMovementRequest, now time.Time) bool {
	b := s.idleInteraction
	return r.IdleToken != "" && !s.closed && s.sessionID == r.SessionId && s.selfGUID == r.SelfGuid &&
		s.encounterVisit.bound && s.encounterVisit.revision == r.VisitId && s.state.InteractionOwner == "exploration" &&
		!s.state.InteractionHarvest && len(s.state.HarvestGuardGuids) == 0 && s.interactionUntil.Sub(now) >= time.Second &&
		subtle.ConstantTimeCompare([]byte(r.IdleToken), []byte(s.interactionToken)) == 1 &&
		b.sessionID == r.SessionId && b.selfGUID == r.SelfGuid && b.visit == r.VisitId && b.token == r.IdleToken
}

// MoveBody executes one caller-selected input. It neither chooses a direction
// nor retries a failed route. Local physical proof does not call FindPath.
func (s *Service) MoveBody(ctx context.Context, r *actionsv1.BodyMovementRequest) (*actionsv1.State, error) {
	if r.GetLanding() == "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	} else if r.Landing != "ground_or_water" && r.Landing != "follow_water" || r.Landing == "follow_water" && (r.Jump || r.IdleToken != "") {
		return nil, status.Error(codes.InvalidArgument, "unknown or incompatible body landing mode")
	}
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetSequenceId() == 0 ||
		len(r.GetIdleToken()) > 128 ||
		math.IsNaN(float64(r.BearingRadians)) || math.IsInf(float64(r.BearingRadians), 0) || math.Abs(float64(r.BearingRadians)) > math.Pi+.001 ||
		!r.Jump && (r.DurationMs == 0 || r.DurationMs > 500) {
		return nil, status.Error(codes.InvalidArgument, "current body, visit, sequence and bounded relative input required")
	}
	r = proto.Clone(r).(*actionsv1.BodyMovementRequest)
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
		s.state.CastPending || s.state.Traveling || r.IdleToken == "" && s.state.InteractionOwner != "" ||
		r.IdleToken != "" && !s.bodyIdleLeaseCurrentLocked(r, s.now()) || s.recovery.state.Active ||
		!w.Self.MovementFlagsKnown || !movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags) ||
		r.Jump && w.Self.MovementFlags&0x20000000 != 0 || r.Landing != "" && w.Self.MovementFlags&movementflags.PassiveModifiers != 0 {
		return nil, status.Error(codes.FailedPrecondition, "current controllable ground body and its Lua writer required")
	}
	if old := s.state.BodyMovement; old != nil && old.SequenceId == r.SequenceId && old.VisitId == r.VisitId {
		return s.stateLocked(), nil
	}
	speed := observedRunSpeed(w.Self)
	if speed == 0 {
		return nil, status.Error(codes.FailedPrecondition, "observed normal movement speed required")
	}
	from, revision, mapID := s.pos, s.positionRevision, s.posMap
	bearing := float64(from.o) + float64(r.BearingRadians)
	followRevision, followedGUID := s.followRevision, w.GetOwner().GetGuid()
	var followedPosition position
	if r.Landing == "follow_water" && w.GetOwner() != nil {
		followedPosition = playerPosition(w.Owner)
	}
	followWaterCurrent := func(fresh *worldv1.SnapshotResponse, landingStarted bool) bool {
		owner := fresh.GetOwner()
		if !s.state.Following || !s.now().Before(s.followUntil) || s.followRevision != followRevision || s.movementOwner.owner != "" || followedGUID == 0 ||
			owner.GetGuid() != 0 && owner.GetGuid() != followedGUID {
			return false
		}
		// Once the confirmed forward input starts a completely measured crossing,
		// a visibility gap cannot change gravity or the fixed landing. New input
		// still needs a current swimmer. Revocation/correction fences every frame.
		if !owner.GetPresent() {
			return landingStarted
		}
		if !landingStarted && (safePlayerMovement(fresh, s.selfGUID, true) != "" || distance(followedPosition, playerPosition(owner)) > 1.5) {
			return false
		}
		return owner.GetGuid() == followedGUID && owner.GetAliveKnown() && owner.GetAlive() &&
			owner.GetMapKnown() && owner.GetMapId() == mapID && owner.GetPositionKnown() && !owner.GetPositionInferred() && finite(playerPosition(owner)) &&
			owner.GetMovementFlagsKnown() && swimFollowCompatible(owner.GetMovementFlags(), owner.GetExtraMovementFlags()) && owner.GetZ() < from.z
	}
	if r.Landing != "" && s.swimming || r.Landing == "follow_water" && (s.state.Moving || !followWaterCurrent(w, false)) {
		return nil, status.Error(codes.FailedPrecondition, "current ground body and observed lower follow destination required")
	}
	if r.Landing == "follow_water" {
		bearing = math.Atan2(float64(w.Owner.Y-from.y), float64(w.Owner.X-from.x))
	}
	view := &actionsv1.BodyMovementState{SequenceId: r.SequenceId, VisitId: r.VisitId, Phase: "checking_local_physics"}
	s.state.BodyMovement = view
	var motion *idleMotionScope
	if r.IdleToken != "" || r.Landing != "" {
		ctx = modcb.ForwardPolicySpell(ctx)
		motion, err = s.newBodyMotionScopeLocked(ctx, r.IdleToken, w, false, r.Landing != "")
		if err != nil {
			view.Phase, view.Detail = "body_scope_not_dispatched", boundedPolicyDiagnostic(err.Error(), 240)
			return s.stateLocked(), nil
		}
		ctx = context.WithValue(ctx, idleMotionScopeKey{}, motion)
	}
	plan := s.bodyMotionPlan
	if plan == nil {
		plan = buddynav.PlanBodyMotionContext
		if r.Landing != "" {
			plan = buddynav.PlanBodyLandingContext
		}
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
	trail := s.swimTrail
	if r.Landing == "follow_water" && trail.visit == r.VisitId {
		trail.points = slices.Clone(trail.points)
	} else {
		trail.points = nil
	}
	swimPlan := s.planSwimPath
	if swimPlan == nil {
		swimPlan = buddynav.PlanSwimContext
	}
	s.mu.Unlock()
	points, err := plan(ctx, dataDir, mapID, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, bearing, speed, time.Duration(r.DurationMs)*time.Millisecond, r.Jump)
	if r.Landing == "follow_water" && err == nil && len(points) >= 2 {
		// A stationary follower can take one supported tap up to the lip.
		// Prove the next input reaches water and that this pool connects
		// to the observed destination before writing even the approach.
		last := points[len(points)-1]
		if !last.Swimming {
			var next []buddynav.BodyMotionSample
			next, err = plan(ctx, dataDir, mapID, last.Point, bearing, speed, time.Duration(r.DurationMs)*time.Millisecond, false)
			if err == nil && len(next) >= 2 {
				last = next[len(next)-1]
			}
		}
		if err == nil && !last.Swimming {
			err = errors.New("forward approach does not reach a measured water crossing")
		}
		if err == nil {
			var path []buddynav.SwimPoint
			path, err = swimPlan(ctx, dataDir, mapID, last.Point, swimPoint(followedPosition))
			if disconnectedSwimRoute(err) && len(trail.points) > 1 {
				if joined := trail.routeFrom(ctx, dataDir, mapID, last.Point, swimPoint(followedPosition), true, w.GetOwner(), swimPlan); len(joined) > 0 {
					path, err = joined, nil
				}
			}
			if err == nil && (len(path) < 2 || !path[0].Swimming || trailDistance(path[0].Point, last.Point) > 2 ||
				trailDistance(path[len(path)-1].Point, swimPoint(followedPosition)) > 2) {
				err = errors.New("water landing does not connect to the observed follow destination")
			}
		}
	}
	s.mu.Lock()
	maxSamples := 24
	if r.Landing != "" {
		maxSamples = buddynav.BodyLandingSampleLimit(speed)
	}
	if err != nil || len(points) < 2 || len(points) > maxSamples {
		view.Phase = "local_physics_refused"
		if err != nil {
			view.Detail = navigationDiagnostic(err, dataDir)
		} else {
			view.Detail = "local physics returned an invalid finite trajectory"
		}
		return s.stateLocked(), nil
	}
	if deadline, bounded := ctx.Deadline(); r.Landing != "" && bounded && time.Until(deadline) < points[len(points)-1].At {
		view.Phase, view.Detail = "body_scope_not_dispatched", "remaining RPC deadline cannot cover the measured landing"
		return s.stateLocked(), nil
	}
	if r.Landing != "" && s.hostEncounterKey != "" && s.hostEncounterEpoch == ownerEpoch && s.hostEncounterVisit == r.VisitId && s.hostEncounterUntil.Equal(reservedUntil) {
		reservedUntil = s.now().Add(points[len(points)-1].At + encounterLeaseDuration)
		s.hostEncounterUntil = reservedUntil
	}
	check := func(landingStarted, ownFalling, ownSwimming bool) (*worldv1.SnapshotResponse, bool) {
		compatible := func(flags, extra uint32) bool {
			if r.Landing != "" && extra == 0 && (ownFalling && (flags == 0x1000 || flags == 0x1001) ||
				ownSwimming && (flags == swimMovementFlag || flags == swimMovementFlag|1)) {
				return true
			}
			return movementflags.GroundCompatible(flags, extra)
		}

		fresh, e := s.snapshot(ctx)
		owner := s.hostEncounterKey == "" || s.trustedEncounterHostLocked(ctx) && s.hostEncounterVisit == r.VisitId && s.hostEncounterEpoch == ownerEpoch
		leaseCurrent := true
		if r.IdleToken != "" {
			now := s.now()
			leaseCurrent = s.bodyIdleLeaseCurrentLocked(r, now) && s.idleMotionCurrentLocked(motion, fresh, now)
		}
		if e != nil || s.closed || !owner || !leaseCurrent || r.Landing == "follow_water" && !followWaterCurrent(fresh, landingStarted) || r.Landing != "" && fresh.GetSelf().GetMovementFlags()&movementflags.PassiveModifiers != 0 ||
			!s.matchesCurrentEncounterVisitLocked(fresh) || fresh.VisitRevision != r.VisitId ||
			fresh.Self.PositionRevision != revision || fresh.Self.MapId != mapID ||
			safeSelfMovement(fresh, s.selfGUID, compatible) != "" || s.clientControlLost || s.clientControlRestoring {
			if s.state.Moving && (e != nil || fresh.GetSelf().GetPositionRevision() == revision) {
				s.state.MotionUnconfirmed = true
			}
			if e == nil {
				s.refreshPositionLocked(fresh)
			}
			view.Phase = "body_or_authoritative_position_changed"
			return nil, false
		}
		return fresh, true
	}
	ctx = modcb.ForwardPolicySpell(ctx)
	if s.executeBodyTrajectoryLocked(ctx, from, points, bearing, speed, r.Jump, r.Landing != "", w.Self.MovementFlags&movementflags.PassiveModifiers, view, motion, check) && r.Landing == "follow_water" && !s.swimming {
		s.state.Reason = "follow_water_approach_written"
	}
	return s.stateLocked(), nil
}

// The same physical writer clocks a finite input or the measured airborne leg
// of a destination route. Its caller supplies the existing movement authority.
func (s *Service) executeBodyTrajectoryLocked(ctx context.Context, from position, points []buddynav.BodyMotionSample, bearing, speed float64, jump, landing bool, passive uint32, view *actionsv1.BodyMovementState, motion *idleMotionScope, check func(bool, bool, bool) (*worldv1.SnapshotResponse, bool)) bool {
	settling := points[0].Settling
	airborne := jump || settling
	horizontalSpeed, verticalSpeed := speed, float64(buddynav.BodyJumpVelocity)
	if !jump {
		verticalSpeed = 0
	}
	if settling {
		horizontalSpeed, verticalSpeed = 0, 0
	}
	var motionClock uint32
	landingStarted := false
	ownFalling, ownSwimming := false, false
	fallTime := points[0].FallTime
	if settling && fallTime > 0 && s.destinationFlight != nil {
		verticalSpeed = destinationGravitySource(s.lastWorld, s.pos).initial
	}
	sourceAge := fallTime
	inWater := false
	var write func(uint32, position, time.Duration, bool, bool) bool
	write = func(code uint32, p position, elapsed time.Duration, falling, moving bool) bool {
		if ctx.Err() != nil {
			view.Phase = "body_input_interrupted"
			s.state.MotionUnconfirmed = s.state.MotionUnconfirmed || s.state.Moving
			return false
		}
		if _, ok := check(landingStarted, ownFalling, ownSwimming); !ok {
			return false
		}
		clock, e := s.session.GetClock(ctx, &emptypb.Empty{})
		if e != nil || clock == nil {
			view.Phase = "body_clock_or_position_unavailable"
			s.state.MotionUnconfirmed = s.state.MotionUnconfirmed || s.state.Moving
			return false
		}
		w, current := check(landingStarted, ownFalling, ownSwimming)
		if !current {
			return false
		}
		if f := s.destinationFlight; f != nil && f.release && !f.airborne && code != opcode.MSGMoveStop {
			if view.TransportWritten && s.state.Moving {
				write(opcode.MSGMoveStop, s.pos, 0, false, false)
			}
			return false
		}
		if guard, ok := ctx.Value(movementPointsBodyGuardKey{}).(func(position, bool) bool); ok && !guard(p, moving) {
			view.Phase = "movement_points_prewrite_changed"
			s.state.MotionUnconfirmed = s.state.MotionUnconfirmed || s.state.Moving
			// A revoked next leg never authorizes travel, but this exact input
			// still owes STOP after its own START. Reuse all ordinary clock,
			// body/visit/epoch checks; keep uncertainty even if STOP is written.
			if moving && view.TransportWritten && s.state.Moving {
				write(opcode.MSGMoveStop, s.pos, 0, false, false)
				view.Phase = "movement_points_prewrite_changed"
			}
			return false
		}
		p.o = float32(math.Remainder(bearing, 2*math.Pi))
		stamp := clock.ClientTimeMs
		if elapsed > 0 {
			stamp = motionClock + uint32(elapsed/time.Millisecond)
		}
		packetTime := elapsed
		if landing && (!settling || s.destinationFlight != nil) {
			packetTime = fallTime
		}
		packet := bodyMovementPacket(s.selfGUID, stamp, p, passive, packetTime, bearing, horizontalSpeed, verticalSpeed, falling, moving)
		if inWater {
			flags := passive | swimMovementFlag
			if moving {
				flags |= 1
			}
			packet = swimMovementPacket(s.selfGUID, stamp, p, 0, flags)
		}
		var sendErr error
		if motion != nil {
			sendErr = s.sendPacketLocked(ctx, code, packet)
		} else {
			_, sendErr = s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: code, Payload: packet})
		}
		if sendErr != nil {
			if motion != nil && motion.writes == 0 && modcb.PacketWasNotDispatched(sendErr) {
				view.Phase, view.Detail = "body_scope_not_dispatched", boundedPolicyDiagnostic(sendErr.Error(), 240)
				return false
			}
			view.Phase, s.state.MotionUnconfirmed = "transport_unconfirmed_no_retry", true
			if motion != nil {
				view.Phase = "body_scope_unconfirmed_no_retry"
				view.TransportWritten = motion.writes > 0
			}
			view.Detail = boundedPolicyDiagnostic(sendErr.Error(), 240)
			s.publishClientMovementLocked(ctx, nil, p, false)
			return false
		}
		view.TransportWritten, view.PositionEstimated = true, true
		if code == opcode.MSGMoveStartForward && points[len(points)-1].Swimming {
			landingStarted = true
		}
		ownFalling, ownSwimming = ownFalling || falling, inWater
		if f := s.destinationFlight; f != nil && falling {
			f.airborne = true
		}
		if code == opcode.MSGMoveStartForward || code == opcode.MSGMoveJump || falling && elapsed == 0 {
			motionClock = stamp
		}
		view.X, view.Y, view.Z = p.x, p.y, p.z
		if motion == nil {
			s.state.TransportWrites++
		}
		s.pos, s.posEstimated, s.posAt, s.state.Moving = p, true, s.now(), moving || falling
		s.swimming = inWater
		s.posRevision++
		s.publishClientMovementLocked(ctx, w, p, true)
		return true
	}
	if settling {
		if !write(opcode.MSGMoveHeartbeat, from, 0, true, false) {
			return false
		}
	} else {
		if !write(opcode.MSGMoveSetFacing, from, 0, false, false) || !write(opcode.MSGMoveStartForward, from, 0, false, true) {
			return false
		}
		if jump && !write(opcode.MSGMoveJump, from, 0, true, true) {
			return false
		}
	}
	view.Phase = "executing_body_input"
	started := s.now()
	for i := 1; i < len(points); i++ {
		sample := points[i]
		wait := sample.At - s.now().Sub(started)
		if wait > 0 {
			s.mu.Unlock()
			err := s.waitBodyMotion(ctx, wait)
			s.mu.Lock()
			if err != nil {
				view.Phase, s.state.MotionUnconfirmed = "body_input_interrupted", true
				return false
			}
		}
		last := i == len(points)-1
		if f := s.destinationFlight; f != nil && f.release && f.airborne && !settling && !last {
			// Motion continues at its last written velocity up to the release
			// report. From that measured point onward only gravity remains.
			vertical, e := buddynav.PlanBodyGravityContext(ctx, s.navDataDir, s.posMap, sample.Point, bearing, sample.FallTime, verticalSpeed)
			if e != nil || len(vertical) < 2 {
				view.Phase = "released_fall_geometry_unavailable"
				s.state.MotionUnconfirmed = true
				return false
			}
			fallTime, horizontalSpeed, settling, airborne = sample.FallTime, 0, true, true
			p := navPosition(sample.Point)
			if !write(opcode.MSGMoveHeartbeat, p, sample.At, true, false) {
				return false
			}
			for j := range vertical {
				vertical[j].At += sample.At
			}
			points, i = vertical, 0
			continue
		}
		code := uint32(opcode.MSGMoveHeartbeat)
		fallTime = sample.FallTime
		if settling && fallTime == 0 && !last {
			fallTime = sourceAge + sample.At
		}
		inWater = sample.Swimming
		flying := airborne || fallTime > 0
		if inWater {
			code = opcode.MSGMoveStartSwim
		} else if last && flying {
			code = opcode.MSGMoveFallLand
		}
		point := position{x: sample.Point.X, y: sample.Point.Y, z: sample.Point.Z}
		if !write(code, point, sample.At, flying && !last, !settling) {
			return false
		}
	}
	fallTime = 0
	if write(opcode.MSGMoveStop, s.pos, 0, false, false) {
		view.Phase = "body_input_written"
		if settling {
			view.Phase = "body_settled_written"
		}
		s.followNav.Reset()
		s.targetMovement.nav.Reset()
		return true
	}
	return false
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
