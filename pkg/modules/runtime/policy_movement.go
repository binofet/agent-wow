package modrt

import (
	"context"
	"encoding/binary"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type policyMovement struct {
	self         uint64
	stop         bool
	body         bool
	jump         bool
	landing      bool
	followsOwner bool
	airborne     bool
	idleBound    bool
	unknown      bool
	sequence     uint64
	durationMs   uint32
	surface      *policySurfaceInput
	phase        string
	detail       string
}

func (p *SessionPolicy) resolveBodyMovement(action map[string]any) (PolicyDispatch, error) {
	sessionID, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	visit, _ := action["visitId"].(string)
	sequence, _ := action["sequenceId"].(string)
	selfID, _ := strconv.ParseUint(self, 10, 64)
	visitID, visitErr := strconv.ParseUint(visit, 10, 64)
	sequenceID, sequenceErr := strconv.ParseUint(sequence, 10, 64)
	bearing, _ := action["bearingRadians"].(float64)
	duration, _ := action["durationMs"].(float64)
	jump, _ := action["jump"].(bool)
	if sessionID != p.vm.Status().SessionID || self != p.body || selfID == 0 || visitErr != nil || visitID == 0 ||
		sequenceErr != nil || sequenceID == 0 || visit != strconv.FormatUint(visitID, 10) || sequence != strconv.FormatUint(sequenceID, 10) ||
		math.IsNaN(bearing) || math.IsInf(bearing, 0) || math.Abs(bearing) > math.Pi+.001 ||
		math.IsNaN(duration) || math.IsInf(duration, 0) || duration != math.Trunc(duration) || duration < 0 || duration > 500 || !jump && duration < 1 {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current body, visit, sequence and bounded relative input required")
	}
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(sessionID), "self_guid": protoreflect.ValueOfUint64(selfID),
		"visit_id": protoreflect.ValueOfUint64(visitID), "sequence_id": protoreflect.ValueOfUint64(sequenceID),
		"bearing_radians": protoreflect.ValueOfFloat32(float32(bearing)), "duration_ms": protoreflect.ValueOfUint32(uint32(duration)), "jump": protoreflect.ValueOfBool(jump),
	}
	landing := false
	if raw, supplied := action["landing"]; supplied {
		mode, ok := raw.(string)
		if !ok || mode != "ground_or_water" && mode != "follow_water" || mode == "follow_water" && (jump || action["idleToken"] != nil) {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "unsupported body landing mode")
		}
		_, method, err := p.manager.lookup(actionsModule, "move_body", true)
		if err != nil {
			return PolicyDispatch{}, err
		}
		field := method.Descriptor().Input().Fields().ByName("landing")
		if field == nil || field.Number() != 9 || field.Kind() != protoreflect.StringKind || field.IsList() || field.IsMap() {
			return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "installed body input lacks measured landings")
		}
		values["landing"] = protoreflect.ValueOfString(mode)
		landing = true
	}
	if raw, supplied := action["idleToken"]; supplied {
		token, ok := raw.(string)
		if !ok || token == "" || len(token) > 128 || strings.TrimSpace(token) != token {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact bounded idle interaction token required")
		}
		_, method, err := p.manager.lookup(actionsModule, "move_body", true)
		if err != nil {
			return PolicyDispatch{}, err
		}
		field := method.Descriptor().Input().Fields().ByName("idle_token")
		// An absent or retagged binding could be ignored by the native server,
		// accidentally invoking its legacy tokenless path instead.
		if method.Path() != actionsv1.BuddyActions_MoveBody_FullMethodName ||
			method.Descriptor().Input().FullName() != (&actionsv1.BodyMovementRequest{}).ProtoReflect().Descriptor().FullName() ||
			field == nil || field.Number() != 8 || field.Kind() != protoreflect.StringKind || field.IsList() || field.IsMap() {
			return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "installed body input lacks the exact idle interaction binding")
		}
		values["idle_token"] = protoreflect.ValueOfString(token)
	}
	op := &policySpell{vm: p.vm, capability: "move_body", encounterVisit: visitID,
		movement: &policyMovement{self: selfID, body: true, jump: jump, landing: landing, followsOwner: action["landing"] == "follow_water", sequence: sequenceID, durationMs: uint32(duration)}}
	_, op.movement.idleBound = action["idleToken"]
	dispatch, err := p.preparePolicyDispatch("move_body", values, op)
	if err == nil {
		dispatch.spell = nil
	}
	return dispatch, err
}

func (p *SessionPolicy) resolveMovement(action map[string]any, capability string) (PolicyDispatch, error) {
	sessionID, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	target, _ := action["targetGuid"].(string)
	visit, _ := action["visitId"].(string)
	selfGUID, _ := strconv.ParseUint(self, 10, 64)
	targetGUID, targetErr := strconv.ParseUint(target, 10, 64)
	visitID, visitErr := strconv.ParseUint(visit, 10, 64)
	radius, _ := action["rangeYards"].(float64)
	stop := capability == "stop_movement"
	if sessionID != p.vm.Status().SessionID || self != p.body || selfGUID == 0 || targetErr != nil || targetGUID == 0 || target != strconv.FormatUint(targetGUID, 10) ||
		visitErr != nil || visitID == 0 || visit != strconv.FormatUint(visitID, 10) || math.IsNaN(radius) || math.IsInf(radius, 0) || radius > math.MaxFloat32 || !stop && radius <= 0 {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session, body, exact target, visit and finite positive range required")
	}
	los, _ := action["requireLos"].(bool)
	behind, _ := action["behind"].(bool)
	values := map[protoreflect.Name]protoreflect.Value{
		"session_id": protoreflect.ValueOfString(sessionID), "self_guid": protoreflect.ValueOfUint64(selfGUID),
		"target_guid": protoreflect.ValueOfUint64(targetGUID), "visit_id": protoreflect.ValueOfUint64(visitID),
		"range_yards": protoreflect.ValueOfFloat32(float32(radius)), "stop": protoreflect.ValueOfBool(stop),
		"require_los": protoreflect.ValueOfBool(los), "behind": protoreflect.ValueOfBool(behind),
	}
	if raw, exists := action["minRangeYards"]; exists {
		minimum, ok := raw.(float64)
		if !ok || math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 0 || minimum >= radius {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "finite minimum below maximum range required")
		}
		values["min_range_yards"] = protoreflect.ValueOfFloat32(float32(minimum))
	}
	if raw, exists := action["clearanceTargetGuids"]; exists {
		ids, ok := raw.([]any)
		clearance, valid := action["clearanceYards"].(float64)
		if !ok || len(ids) == 0 || !valid || math.IsNaN(clearance) || math.IsInf(clearance, 0) || clearance <= 0 || clearance > math.MaxFloat32 {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact clearance targets and finite positive distance required")
		}
		template := (&actionsv1.ApproachTargetRequest{}).ProtoReflect()
		field := template.Descriptor().Fields().ByName("clearance_target_guids")
		list := template.NewField(field).List()
		seen := map[uint64]bool{}
		for _, v := range ids {
			raw, ok := v.(string)
			id, err := strconv.ParseUint(raw, 10, 64)
			if !ok || err != nil || id == 0 || id == selfGUID || seen[id] || raw != strconv.FormatUint(id, 10) {
				return PolicyDispatch{}, status.Error(codes.InvalidArgument, "canonical distinct clearance GUIDs required")
			}
			seen[id] = true
			list.Append(protoreflect.ValueOfUint64(id))
		}
		values["clearance_target_guids"] = protoreflect.ValueOfList(list)
		values["clearance_yards"] = protoreflect.ValueOfFloat32(float32(clearance))
	}
	op := &policySpell{vm: p.vm, capability: capability, target: targetGUID, encounterVisit: visitID, movement: &policyMovement{self: selfGUID, stop: stop}}
	dispatch, err := p.preparePolicyDispatch("approach_target", values, op)
	if err == nil {
		// Motion is a physical step, not a cast awaiting a server spell effect.
		// A fresh world pose determines the next step; it never holds the spell
		// action ledger or claims that a navigation estimate was a confirmed hit.
		dispatch.spell = nil
	}
	return dispatch, err
}

func (m *Manager) invokePolicyMovement(ctx context.Context, inst *instance, md moddisc.Method, req proto.Message, op *policySpell) (behavior.ActionOutcome, map[string]any) {
	outcome, payload := m.invokePolicySpell(ctx, inst, md, req, op)
	m.policySpells.mu.Lock()
	delete(m.policySpells.active, op.token)
	if (op.movement.surface != nil || op.movement.landing || op.capability == "movement_points_step") && outcome == behavior.ActionUnknown {
		op.movement.unknown = true
	}
	if (op.movement.surface != nil || op.movement.idleBound || op.movement.landing || op.capability == "release_interaction" || op.capability == "movement_points_step") && op.movement.unknown {
		payload["reconciliationRequired"] = true
		if op.capability == "movement_points_step" {
			payload["motion_unconfirmed"] = true
		}
	}
	m.policySpells.mu.Unlock()
	if outcome == behavior.ActionTransportAccepted || outcome == behavior.ActionUnknown {
		outcome = behavior.ActionSettledUnverified
	}
	return outcome, payload
}

func routeScopeBound(scope *modv1.TransportScope) bool {
	return scope != nil && scope.ConnectionId != "" && scope.VisitBound && scope.VisitRevision != 0 &&
		scope.MotionRevision != 0 && scope.SelfGuid != 0 && !scope.SplineActive
}

// The native Actions handler attaches each physical source. Lua supplies the
// selected operation and its bound dispatch token.
func (m *Manager) writePolicyScopedMovement(ctx context.Context, token, sessionID, module string, code uint32, payload []byte, scope *modv1.TransportScope) (bool, error) {
	registry := &m.policySpells
	registry.mu.Lock()
	follows, route, _ := modcb.IncomingRouteBodyInput(ctx)
	op := registry.active[token]
	hull, birth, packets, validHull, hasHull := modcb.IncomingRouteHull(ctx)
	visit, sequence, selection := modcb.IncomingSurfaceBodySelection(ctx)
	surface := route && !follows && hasHull && validHull && selection && visit != 0 && sequence != 0 && op != nil && op.capability == "move_body" && op.encounterVisit == visit && op.movement != nil && op.movement.sequence == sequence && op.movement.body && !op.movement.followsOwner
	if (hasHull || selection) && !surface || op != nil && op.movement != nil && op.movement.surface != nil && !surface ||
		route && !surface && (follows || op == nil || op.capability != "travel_to" || op.movement == nil || !op.movement.idleBound) {
		registry.mu.Unlock()
		return true, modcb.PacketNotDispatchedError("route frame is outside the active travel dispatch")
	}
	if op == nil || op.movement == nil || !surface && !op.movement.idleBound && !op.movement.landing && !(op.capability == "release_interaction" && op.movement.stop) {
		registry.mu.Unlock()
		return false, nil
	}
	movement := op.movement
	allowed := surface || (movement.idleBound || movement.landing) && op.capability == "move_body" && movement.body || movement.idleBound && op.capability == "travel_to" && !movement.body ||
		op.capability == "release_interaction" && movement.stop
	attempt := modcb.IncomingMovementAttempt(ctx)
	if !op.admitted || !allowed || (movement.landing || route) && attempt == "" ||
		op.origin.Writer != "lua" || op.origin.ActionID == "" || op.origin.SessionID != sessionID || op.origin.Module != module || module != actionsModule ||
		movement.unknown || scope == nil || scope.ConnectionId == "" || !scope.VisitBound || scope.VisitRevision == 0 || scope.MotionRevision == 0 || scope.SelfGuid != movement.self ||
		(!route && !op.matchesMovementPacket(code, payload) || route && !routeScopeBound(scope)) || ctx.Err() != nil || m.session.SendScopedPacket == nil {
		registry.mu.Unlock()
		return true, modcb.PacketNotDispatchedError("scoped callback is outside the exact idle movement dispatch")
	}
	var next policySurfaceInput
	if surface {
		var valid bool
		next, valid = op.nextSurfaceInput(code, payload, scope, hull, birth, packets, attempt)
		if !valid {
			registry.mu.Unlock()
			return true, modcb.PacketNotDispatchedError("frame is outside the selected passenger body input")
		}
		if movement.surface == nil {
			movement.surface = &policySurfaceInput{}
		}
		movement.surface.writing = true
	}
	op.attempted = true
	origin := op.origin
	if route {
		ctx = modsession.WithRouteBodyInput(ctx, false)
		origin.MovementAttempt = attempt
	} else if movement.landing {
		ctx = modsession.WithBodyInput(ctx)
		if !movement.followsOwner || movement.airborne {
			ctx = modsession.WithSelfBodyInput(ctx)
		}
		origin.MovementAttempt = attempt
	}
	registry.mu.Unlock()
	err := m.session.SendScopedPacket(modsession.WithWriteOrigin(ctx, origin), code, append([]byte(nil), payload...), proto.Clone(scope).(*modv1.TransportScope))
	registry.mu.Lock()
	if surface {
		if err == nil {
			next.attempts[attempt] = struct{}{}
			*movement.surface = next
		} else {
			movement.surface.writing = false
			if !movement.surface.started && modcb.PacketWasNotDispatched(err) {
				movement.surface = nil
			}
		}
	}
	movement.unknown = movement.unknown || err != nil && !modcb.PacketWasNotDispatched(err)
	if err == nil && movement.landing && (code == opcode.MSGMoveHeartbeat || code == opcode.MSGMoveJump) {
		offset := 1 + bits.OnesCount8(payload[0])
		if len(payload) >= offset+4 && binary.LittleEndian.Uint32(payload[offset:])&0x1000 != 0 {
			movement.airborne = true
		}
	}
	op.written = op.written || err == nil
	if modcb.PacketWasNotDispatched(err) && !op.written && !movement.unknown {
		op.attempted = false
	}
	registry.mu.Unlock()
	return true, err
}

func (op *policySpell) movementPayload(phase string, written bool) map[string]any {
	if op.movement.body && op.movement.phase != "" {
		phase = op.movement.phase
	}
	return map[string]any{"capability": op.capability, "phase": phase, "self_guid": strconv.FormatUint(op.movement.self, 10),
		"target_guid": strconv.FormatUint(op.target, 10), "visit_id": strconv.FormatUint(op.encounterVisit, 10),
		"sequence_id":       strconv.FormatUint(op.movement.sequence, 10),
		"transport_written": written, "effect_observed": false, "detail": op.movement.detail}
}

func (op *policySpell) matchesMovementPacket(code uint32, body []byte) bool {
	if op.movement != nil && op.movement.surface != nil {
		return false
	}
	if op.capability == "release_interaction" {
		return op.matchesInteractionStop(code, body)
	}
	switch code {
	case opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop, opcode.MSGMoveSetFacing:
	case opcode.MSGMoveJump:
		if !op.movement.body || !op.movement.jump {
			return false
		}
	case opcode.MSGMoveStartSwim:
		if !op.movement.body || !op.movement.landing {
			return false
		}
	case opcode.MSGMoveFallLand:
		if !op.movement.body {
			return false
		}
	default:
		return false
	}
	if op.movement.stop && code != opcode.MSGMoveStop {
		return false
	}
	if len(body) < 1 {
		return false
	}
	n := 1 + bits.OnesCount8(body[0])
	// MovementInfo: flags4, extraFlags2, time4, position/orientation16,
	// fallTime4. Ground packets have no optional transport/spline/swim tail.
	if len(body) < n+30 {
		return false
	}
	var guid uint64
	index := 1
	for shift := uint(0); shift < 8; shift++ {
		if body[0]&(1<<shift) != 0 {
			guid |= uint64(body[index]) << (shift * 8)
			index++
		}
	}
	flags := binary.LittleEndian.Uint32(body[n:])
	if flags&movementinfo.SwimmingFlag != 0 || code == opcode.MSGMoveStartSwim {
		i, size, err := movementinfo.DecodePrefix(body[n:])
		return guid == op.movement.self && op.movement.body && op.movement.landing &&
			(code == opcode.MSGMoveStartSwim || code == opcode.MSGMoveStop) && err == nil && size == len(body)-n && (i.BodyWaterEntry(false) || code == opcode.MSGMoveStartSwim && i.BodyWaterEntry(true))
	}
	if binary.LittleEndian.Uint16(body[n+4:]) != 0 {
		return false
	}
	// Allow only the requested forward motion and observed passive ground
	// modifiers, plus normal jumping or gravity settling for an explicit body
	// input. A route-only movement cannot introduce airborne packets.
	allowedFlags := uint32(1) | movementflags.PassiveModifiers
	falling := flags&0x1000 != 0
	if op.movement.body {
		allowedFlags |= 0x1000
	}
	if guid != op.movement.self || flags & ^allowedFlags != 0 ||
		!falling && len(body) != n+30 || falling && len(body) != n+46 ||
		code == opcode.MSGMoveJump && !falling || code == opcode.MSGMoveFallLand && falling {
		return false
	}
	if falling {
		values := [4]float64{}
		for i := range values {
			values[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(body[n+30+i*4:])))
			if math.IsNaN(values[i]) || math.IsInf(values[i], 0) {
				return false
			}
		}
		jump := op.movement.jump && math.Abs(values[0]-7.955547) <= .0001 && values[3] >= .1 && values[3] <= 14
		settling := code == opcode.MSGMoveHeartbeat && (values[0] == 0 || op.movement.landing && op.movement.jump && math.Abs(values[0]-buddynav.BodyJumpVelocity) <= .0001) && values[3] == 0 && flags&1 == 0
		walkOff := code == opcode.MSGMoveHeartbeat && op.movement.landing && !op.movement.jump && values[0] == 0 && flags&1 != 0 && values[3] >= .1 && values[3] <= 14
		fall := time.Duration(binary.LittleEndian.Uint32(body[n+26:])) * time.Millisecond
		if (!jump && !settling && !walkOff) || code == opcode.MSGMoveJump && !jump ||
			math.Abs(values[1]*values[1]+values[2]*values[2]-1) > .001 ||
			(fall > buddynav.BodyMotionHorizon && (!op.movement.landing || !buddynav.BodyFlightInRange(fall, values[3], values[0]))) {
			return false
		}
	}
	for offset := n + 10; offset < n+26; offset += 4 {
		value := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[offset:])))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

type policySurfaceInput struct {
	connection                                  string
	visit, hull, birth, packets                 uint64
	mapID, start, last                          uint32
	started, airborne, landed, stopped, writing bool
	attached, swimming                          bool
	fallAge                                     uint32
	attempts                                    map[string]struct{}
}

// A surface callback belongs to exactly one already admitted finite Lua
// input. Core independently verifies the source bytes, hull lifetime and STOP.
func (op *policySpell) nextSurfaceInput(code uint32, payload []byte, scope *modv1.TransportScope, hull, birth, packets uint64, attempt string) (policySurfaceInput, bool) {
	move := op.movement
	in := policySurfaceInput{}
	if move.surface != nil {
		in = *move.surface
	}
	info, ok := movementinfo.OwnFrame(payload, move.self)
	_, reused := in.attempts[attempt]
	if !ok || !buddynav.RouteBodyFrame(code, payload, move.self) || !modcb.ValidMovementAttempt(attempt) || in.writing || in.stopped || reused {
		return in, false
	}
	if !in.started {
		if op.attempted || op.written || info.FallTime != 0 || !info.TransportPresent || info.Transport.GUID != hull || move.jump && code != opcode.MSGMoveJump || !move.jump && code != opcode.MSGMoveStartForward {
			return in, false
		}
		in = policySurfaceInput{connection: scope.ConnectionId, visit: scope.VisitRevision, mapID: scope.MapId, hull: hull, birth: birth, start: info.Time, started: true, attempts: make(map[string]struct{})}
	} else {
		if in.connection != scope.ConnectionId || in.visit != scope.VisitRevision || in.mapID != scope.MapId || in.hull != hull || in.birth != birth || packets < in.packets || int32(info.Time-in.last) < 0 {
			return in, false
		}
		switch code {
		case opcode.MSGMoveHeartbeat:
			if in.landed || info.Time == in.last || !info.TransportPresent && !info.JumpPresent || in.airborne && !info.JumpPresent {
				return in, false
			}
			if !info.JumpPresent && info.Time-in.start > move.durationMs {
				return in, false
			}
		case opcode.MSGMoveStartSwim, opcode.MSGMoveFallLand:
			if in.landed || !in.airborne || code == opcode.MSGMoveStartSwim && !move.landing {
				return in, false
			}
			in.landed = true
		case opcode.MSGMoveStop:
			if in.airborne || info.TransportPresent != in.attached || (info.Flags&movementinfo.SwimmingFlag != 0) != in.swimming || !info.TransportPresent && !in.landed {
				return in, false
			}
			in.stopped = true
		default:
			return in, false
		}
	}
	if info.JumpPresent {
		if !move.jump && (!move.landing || info.Jump.Velocity != 0) || move.jump && math.Abs(float64(info.Jump.Velocity)-buddynav.BodyJumpVelocity) > .0001 {
			return in, false
		}
		if in.airborne && info.FallTime < in.fallAge || !move.landing && time.Duration(info.FallTime)*time.Millisecond > buddynav.BodyMotionHorizon {
			return in, false
		}
	}
	// A selected finite action cannot keep an otherwise plausible series of
	// zero-age airborne reports alive beyond the physical world envelope.
	if info.JumpPresent || in.landed && code != opcode.MSGMoveStop {
		flight := time.Duration(info.Time-in.start) * time.Millisecond
		if !move.jump {
			flight = max(0, flight-time.Duration(move.durationMs)*time.Millisecond)
		}
		if !move.landing && flight > buddynav.BodyMotionHorizon || !buddynav.BodyFlightInRange(flight, 0, float64(info.Jump.Velocity)) {
			return in, false
		}
	}
	in.airborne = info.JumpPresent
	in.attached = info.TransportPresent
	in.swimming = info.Flags&movementinfo.SwimmingFlag != 0
	in.fallAge = info.FallTime
	in.packets = packets
	in.last = info.Time
	return in, true
}
