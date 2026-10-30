// Package buddyactions implements an explicitly enabled, bounded controller.
// It has no language model, arbitrary target/spell selector, teleport or pathfinder.
package buddyactions

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	lease      = 4 * time.Hour
	limitation = "Direct outdoor flat-ground follow only: no collision, navigation, terrain or local LOS query; spell transport and server spell-go are not health/damage confirmation."
)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	GetClock(context.Context, *emptypb.Empty, ...grpc.CallOption) (*modv1.ClockResponse, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}

type Service struct {
	actionsv1.UnimplementedBuddyActionsServer
	mu                                                       sync.Mutex
	session                                                  sessionAPI
	selfGUID                                                 uint64
	now                                                      func() time.Time
	state                                                    actionsv1.State
	closed                                                   bool
	followUntil, healingUntil, lastStep, nextHeal, castUntil time.Time
	pos                                                      position
	posKnown                                                 bool
	positionRevision                                         uint64
	castCount                                                byte
	lastWorld                                                *worldv1.SnapshotResponse
	posMap                                                   uint32
	posEstimated                                             bool
	posAt                                                    time.Time
	posRevision                                              uint64
	interactionToken                                         string
	interactionUntil                                         time.Time
	assistUntil, nextAssist                                  time.Time
	pendingSpell                                             uint32
	pendingTarget, failedAssistTarget                        uint64
	weaponQueries                                            map[uint32]bool
	weapons                                                  map[uint32]weaponProof
	weaponOrder                                              []uint32
	improvedSinister1, improvedSinister2                     bool
	autoTarget                                               uint64
	autoStopping                                             bool
}

func NewService(session sessionAPI, selfGUID uint64) *Service {
	return &Service{session: session, selfGUID: selfGUID, now: time.Now, state: actionsv1.State{Reason: "disabled", Limitation: limitation}, weaponQueries: make(map[uint32]bool), weapons: make(map[uint32]weaponProof)}
}

func (s *Service) stateLocked() *actionsv1.State {
	s.expireInteractionLocked()
	s.updateClassLocked()
	out := proto.Clone(&s.state).(*actionsv1.State)
	now := s.now()
	if s.state.Following && now.Before(s.followUntil) {
		out.FollowLeaseRemainingMs = uint64(s.followUntil.Sub(now).Milliseconds())
	}
	if s.state.Healing && now.Before(s.healingUntil) {
		out.HealingLeaseRemainingMs = uint64(s.healingUntil.Sub(now).Milliseconds())
	}
	if s.state.Assisting && now.Before(s.assistUntil) {
		out.AssistLeaseRemainingMs = uint64(s.assistUntil.Sub(now).Milliseconds())
	}
	out.Position = s.clientPositionLocked()
	if s.state.InteractionOwner != "" {
		if remaining := s.interactionUntil.Sub(now); remaining > 0 {
			out.InteractionLeaseRemainingMs = uint64(remaining.Milliseconds())
		}
	}
	return out
}

func validInteractionOwner(owner string) bool {
	return owner == "loot" || owner == "quests" || owner == "trade"
}
func (s *Service) expireInteractionLocked() {
	if s.state.InteractionOwner != "" && !s.now().Before(s.interactionUntil) {
		// An approach must stop before its expired lease can be discarded.
		// Tick owns the bounded transport write; status remains read-only.
		if s.state.Moving {
			return
		}
		s.state.InteractionOwner = ""
		s.interactionToken = ""
		s.lastStep = s.now()
	}
}

func (s *Service) AcquireInteraction(ctx context.Context, r *actionsv1.InteractionRequest) (*actionsv1.InteractionLease, error) {
	if r == nil || !validInteractionOwner(r.Owner) {
		return nil, status.Error(codes.InvalidArgument, "interaction owner must be loot, quests, or trade")
	}
	duration := r.LeaseMs
	if duration == 0 {
		duration = 15000
	}
	if duration < 1000 || duration > 30000 {
		return nil, status.Error(codes.InvalidArgument, "interaction lease must be1000..30000ms")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireInteractionLocked()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if s.state.InteractionOwner != "" {
		if !s.now().Before(s.interactionUntil) {
			return nil, status.Error(codes.FailedPrecondition, "expired interaction awaits movement stop")
		}
		if r.Owner != s.state.InteractionOwner || r.Token == "" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 {
			return nil, status.Error(codes.FailedPrecondition, "another interaction is active")
		}
		s.interactionUntil = s.now().Add(time.Duration(duration) * time.Millisecond)
		return &actionsv1.InteractionLease{Token: s.interactionToken, State: s.stateLocked()}, nil
	}
	if r.Token != "" {
		return nil, status.Error(codes.FailedPrecondition, "interaction lease expired; acquire a new lease")
	}
	if s.state.CastPending || s.state.MotionUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "casting or movement state is unconfirmed")
	}
	if !s.stopAutoAttackLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "auto attack stop awaits server confirmation")
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "world observations unavailable")
	}
	if !s.clientPositionLocked().Known {
		return nil, status.Error(codes.FailedPrecondition, "current client position is unknown")
	}
	// A server correction can arrive between controller ticks. Rebase before
	// writing STOP so an interaction never writes the superseded estimate.
	s.refreshPositionLocked(w)
	if !s.stopLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "movement stop could not be written")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, status.Error(codes.Internal, "interaction token generation failed")
	}
	s.interactionToken = hex.EncodeToString(token[:])
	s.interactionUntil = s.now().Add(time.Duration(duration) * time.Millisecond)
	s.state.InteractionOwner = r.Owner
	s.lastStep = s.now()
	return &actionsv1.InteractionLease{Token: s.interactionToken, State: s.stateLocked()}, nil
}

func (s *Service) ReleaseInteraction(ctx context.Context, r *actionsv1.InteractionRelease) (*actionsv1.State, error) {
	if r == nil || !validInteractionOwner(r.Owner) || r.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "interaction owner and token are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireInteractionLocked()
	if s.state.InteractionOwner != r.Owner || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 {
		return nil, status.Error(codes.FailedPrecondition, "interaction lease does not match")
	}
	if !s.stopLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "interaction movement stop unconfirmed")
	}
	s.state.InteractionOwner = ""
	s.interactionToken = ""
	s.lastStep = s.now()
	return s.stateLocked(), nil
}

func (s *Service) clientPositionLocked() *actionsv1.ClientPosition {
	result := &actionsv1.ClientPosition{}
	if s.lastWorld == nil || s.lastWorld.Self == nil {
		return result
	}
	w := s.lastWorld.Self
	if w.Guid != s.selfGUID || !w.Present || !w.MapKnown || !w.PositionKnown || !finite(playerPosition(w)) {
		return result
	}
	result.Known = true
	result.MapKnown = true
	result.MapId = w.MapId
	result.X = w.X
	result.Y = w.Y
	result.Z = w.Z
	result.Orientation = w.Orientation
	result.AgeMs = w.PositionAgeMs
	result.Revision = w.PositionRevision
	if s.posKnown && s.posMap == w.MapId && s.positionRevision == w.PositionRevision && !s.state.MotionUnconfirmed {
		result.X = s.pos.x
		result.Y = s.pos.y
		result.Z = s.pos.z
		result.Orientation = s.pos.o
		result.Estimated = s.posEstimated
		result.Revision = s.posRevision
		if age := s.now().Sub(s.posAt); age > 0 {
			result.AgeMs = uint64(age.Milliseconds())
		} else {
			result.AgeMs = 0
		}
	}
	if s.state.MotionUnconfirmed {
		result.Known = false
	}
	return result
}

func (s *Service) Follow(ctx context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "toggle request is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if r.Enabled {
		if s.state.MotionUnconfirmed {
			s.state.Reason = "motion_unconfirmed_explicit_stop_required"
			return s.stateLocked(), nil
		}
		s.state.Following = true
		s.followUntil = s.now().Add(lease)
		s.lastStep = s.now()
		s.state.Reason = "follow_enabled_waiting_for_safe_observations"
	} else {
		s.state.Following = false
		if s.explicitStopLocked(ctx) {
			s.state.Reason = "follow_disabled"
		}
	}
	return s.stateLocked(), nil
}

func (s *Service) Healing(ctx context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "toggle request is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	s.state.Healing = r.Enabled
	if r.Enabled && s.state.MotionUnconfirmed {
		s.state.Healing = false
		s.state.Reason = "motion_unconfirmed_explicit_stop_required"
		return s.stateLocked(), nil
	}
	if r.Enabled {
		s.healingUntil = s.now().Add(lease)
		s.state.Reason = "healing_enabled_waiting_for_safe_observations"
	} else {
		s.state.Reason = "healing_disabled"
	}
	return s.stateLocked(), nil
}

func (s *Service) Assist(ctx context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "toggle request is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if r.Enabled && s.state.MotionUnconfirmed {
		s.state.Reason = "motion_unconfirmed_explicit_stop_required"
		return s.stateLocked(), nil
	}
	s.state.Assisting = r.Enabled
	if r.Enabled {
		s.assistUntil = s.now().Add(lease)
		s.lastStep = s.now()
		s.failedAssistTarget = 0
		s.state.Reason = "assist_enabled_waiting_for_observed_hostile_target"
	} else {
		s.state.Reason = "assist_disabled"
		// An explicit user disable authorizes one fresh stop attempt after an
		// ambiguous write; ordinary controller ticks never retry that write.
		s.autoStopping = false
		s.state.AutoAttackUnconfirmed = false
		if !s.stopAutoAttackLocked(ctx) {
			s.state.Reason = "auto_attack_stop_waiting_for_server"
		}
		if !s.explicitStopLocked(ctx) {
			s.state.Reason = "motion_unconfirmed_explicit_stop_required"
		}
		s.lastStep = s.now()
	}
	return s.stateLocked(), nil
}

func (s *Service) HealOnce(ctx context.Context, _ *emptypb.Empty) (*actionsv1.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	s.expireInteractionLocked()
	if s.state.InteractionOwner != "" {
		s.state.Reason = "interaction_active_heal_once_blocked"
		return s.stateLocked(), nil
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		s.state.Reason = "observations_unavailable"
		return s.stateLocked(), nil
	}
	s.healLocked(ctx, snapshot)
	return s.stateLocked(), nil
}

func (s *Service) Status(ctx context.Context, _ *emptypb.Empty) (*actionsv1.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read-only refresh: active movement is not echoed back to its sender, so
	// report the client's estimate only while its authoritative base is valid.
	if _, err := s.snapshot(ctx); err != nil {
		s.lastWorld = nil
	}
	return s.stateLocked(), nil
}

func (s *Service) BeforeLogout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.autoStopping = false
	s.state.AutoAttackUnconfirmed = false
	s.stopAutoAttackLocked(ctx)
	s.state.InteractionOwner = ""
	s.interactionToken = ""
	s.state.Following = false
	s.state.Healing = false
	s.state.Assisting = false
	s.explicitStopLocked(ctx)
	s.state.Reason = "session_closing"
	return &emptypb.Empty{}, nil
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			call, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			s.Tick(call)
			cancel()
		}
	}
}

func (s *Service) snapshot(ctx context.Context) (*worldv1.SnapshotResponse, error) {
	if s.session == nil {
		return nil, errors.New("session callback unavailable")
	}
	request, err := anypb.New(&worldv1.SnapshotRequest{})
	if err != nil {
		return nil, err
	}
	result, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "buddy-world", Method: "snapshot", Request: request})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Result == nil {
		return nil, errors.New("missing world snapshot")
	}
	out := &worldv1.SnapshotResponse{}
	if err := result.Result.UnmarshalTo(out); err != nil {
		return nil, err
	}
	s.lastWorld = out
	s.updateClassLocked()
	return out, nil
}

func playerPosition(p *worldv1.PlayerState) position { return position{p.X, p.Y, p.Z, p.Orientation} }
func distance(a, b position) float64 {
	return math.Sqrt(math.Pow(float64(a.x-b.x), 2) + math.Pow(float64(a.y-b.y), 2) + math.Pow(float64(a.z-b.z), 2))
}

func safePlayers(snapshot *worldv1.SnapshotResponse, selfGUID uint64) string {
	if snapshot == nil || snapshot.Self == nil || snapshot.Owner == nil {
		return "players_unknown"
	}
	a, b := snapshot.Self, snapshot.Owner
	if a.Guid != selfGUID || b.Guid != ownerGUID {
		return "player_identity_mismatch"
	}
	if !a.Present || !b.Present {
		return "players_missing"
	}
	if b.PlayerFlags&2 != 0 {
		return "owner_afk_actions_paused"
	}
	if !a.MapKnown || !b.MapKnown || a.MapId != b.MapId {
		return "map_unknown_or_different"
	}
	if !a.PositionKnown || !b.PositionKnown || !finite(playerPosition(a)) || !finite(playerPosition(b)) {
		return "position_unknown_or_invalid"
	}
	if !a.AliveKnown || !b.AliveKnown || !a.Alive || !b.Alive {
		return "player_dead_or_unknown"
	}
	if !a.MovementFlagsKnown || !b.MovementFlagsKnown {
		return "movement_flags_unknown"
	}
	// Only ordinary ground movement and walking are supported. Transports,
	// swimming, falling, flying, spline movement and extra flags stop this slice.
	if (a.MovementFlags|b.MovementFlags)&^uint32(0x1ff) != 0 || a.ExtraMovementFlags != 0 || b.ExtraMovementFlags != 0 {
		return "unsupported_movement_state"
	}
	if b.MovementFlags&0xff != 0 && b.PositionAgeMs > 2000 {
		return "owner_moving_position_stale"
	}
	if !a.UnitFlagsKnown || !b.UnitFlagsKnown {
		return "unit_flags_unknown"
	}
	const unsafe = uint32(0x1 | 0x4 | 0x00040000 | 0x00100000 | 0x00400000 | 0x00800000 | 0x01000000 | 0x02000000 | 0x08000000)
	if (a.UnitFlags|b.UnitFlags)&unsafe != 0 {
		return "unsafe_unit_state"
	}
	return ""
}

func (s *Service) refreshPositionLocked(snapshot *worldv1.SnapshotResponse) bool {
	p := snapshot.Self
	if !s.posKnown {
		s.pos = playerPosition(p)
		s.posKnown = true
		s.positionRevision = p.PositionRevision
		s.observePositionLocked(p)
		return true
	}
	if p.PositionRevision != s.positionRevision {
		s.pos = playerPosition(p)
		s.positionRevision = p.PositionRevision
		s.observePositionLocked(p)
		if p.MovementFlagsKnown && p.MovementFlags&0xff == 0 {
			s.state.MotionUnconfirmed = false
		}
		if s.state.Moving {
			s.state.Reason = "authoritative_position_changed"
			return false
		}
	}
	return true
}

func (s *Service) observePositionLocked(p *worldv1.PlayerState) {
	s.posMap = p.MapId
	s.posEstimated = false
	s.posAt = s.now().Add(-time.Duration(min(p.PositionAgeMs, uint64(24*time.Hour/time.Millisecond))) * time.Millisecond)
	s.posRevision++
}

func (s *Service) sendLocked(ctx context.Context, op uint32, payload []byte) bool {
	if s.session == nil {
		s.state.Reason = "session_unavailable"
		return false
	}
	if _, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: uint32(op), Payload: payload}); err != nil {
		s.state.Following = false
		s.state.Healing = false
		s.state.Assisting = false
		s.state.Reason = "transport_error_no_retry"
		if s.autoTarget != 0 && !s.autoStopping {
			s.stopAutoAttackLocked(ctx)
		}
		return false
	}
	s.state.TransportWrites++
	return true
}

func (s *Service) movementLocked(ctx context.Context, op uint32, p position, moving bool) bool {
	if s.session == nil {
		return false
	}
	clock, err := s.session.GetClock(ctx, &emptypb.Empty{})
	if err != nil || clock == nil {
		s.state.Reason = "session_clock_unavailable"
		return false
	}
	if !s.sendLocked(ctx, op, movementPacket(s.selfGUID, clock.ClientTimeMs, p, moving)) {
		s.state.MotionUnconfirmed = true
		return false
	}
	s.pos = p
	s.state.Moving = moving
	s.posEstimated = true
	s.posAt = s.now()
	s.posRevision++
	return true
}

func (s *Service) stopLocked(ctx context.Context) bool {
	// Corrections can coincide with an unsafe owner/self movement flag. Those
	// paths stop before normal follow position refresh, so rebase here too.
	if s.lastWorld != nil && s.lastWorld.Self != nil {
		p := s.lastWorld.Self
		if p.Guid == s.selfGUID && p.Present && p.MapKnown && p.PositionKnown && finite(playerPosition(p)) && (!s.posKnown || p.PositionRevision != s.positionRevision || p.MapId != s.posMap) {
			s.pos = playerPosition(p)
			s.posKnown = true
			s.positionRevision = p.PositionRevision
			s.observePositionLocked(p)
		}
	}
	if s.state.MotionUnconfirmed {
		return false
	}
	if !s.state.Moving || !s.posKnown {
		return true
	}
	// Consume the movement intent before a possibly ambiguous transport write.
	s.state.Moving = false
	written := s.movementLocked(ctx, opcode.MSGMoveStop, s.pos, false)
	s.state.MotionUnconfirmed = !written
	if !written {
		s.state.Following = false
		s.state.Healing = false
		s.state.Assisting = false
		s.stopAutoAttackLocked(ctx)
	}
	return written
}

func (s *Service) explicitStopLocked(ctx context.Context) bool {
	if s.state.MotionUnconfirmed {
		// A fresh user stop/logout command authorizes one new stop attempt.
		s.state.MotionUnconfirmed = false
		s.state.Moving = true
	}
	return s.stopLocked(ctx)
}

func (s *Service) Tick(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.InteractionOwner != "" && !s.now().Before(s.interactionUntil) {
		s.stopLocked(ctx)
	}
	s.expireInteractionLocked()
	if s.state.InteractionOwner != "" {
		s.lastStep = s.now()
		return
	}
	if s.closed {
		return
	}
	if !s.state.Following && !s.state.Healing && !s.state.Assisting && !s.state.CastPending {
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.lastStep = s.now()
		return
	}
	if s.state.MotionUnconfirmed {
		s.state.Following = false
		s.state.Healing = false
		s.state.Assisting = false
		s.state.Reason = "motion_unconfirmed_explicit_stop_required"
		s.stopAutoAttackLocked(ctx)
		return
	}
	if s.state.AutoAttackUnconfirmed {
		s.stopLocked(ctx)
		s.lastStep = s.now()
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return
	}
	now := s.now()
	if s.state.Following && !now.Before(s.followUntil) {
		s.state.Following = false
		s.stopLocked(ctx)
		s.state.Reason = "follow_lease_expired"
	}
	if s.state.Healing && !now.Before(s.healingUntil) {
		s.state.Healing = false
		s.state.Reason = "healing_lease_expired"
	}
	if s.state.Assisting && !now.Before(s.assistUntil) {
		s.state.Assisting = false
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.state.Reason = "assist_lease_expired"
	}
	if s.state.CastPending && !now.Before(s.castUntil) {
		s.state.CastPending = false
		s.state.Healing = false
		s.state.Following = false
		s.stopLocked(ctx)
		s.state.Assisting = false
		s.stopAutoAttackLocked(ctx)
		s.pendingSpell = 0
		s.pendingTarget = 0
		s.state.Reason = "cast_result_timeout_no_retry"
	}
	if !s.state.Following && !s.state.Healing && !s.state.Assisting && !s.state.CastPending {
		return
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.lastStep = now
		s.state.Reason = "observations_unavailable"
		s.state.AssistReason = s.state.Reason
		return
	}
	if reason := safePlayers(snapshot, s.selfGUID); reason != "" {
		// Unknown or temporarily unsafe observations pause enabled intent. A
		// normal jump, AFK or visibility loss must not silently revoke it.
		// No local motion is attempted until fresh safe context is available.
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		if reason == "player_identity_mismatch" {
			s.state.Following = false
			s.state.Healing = false
			s.state.Assisting = false
		}
		s.lastStep = now
		s.state.Reason = reason
		s.state.AssistReason = reason
		return
	}
	if !s.refreshPositionLocked(snapshot) {
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.lastStep = now
		return
	}
	if s.autoTarget != 0 {
		t := snapshot.OwnerTarget
		if !snapshot.Owner.TargetKnown || snapshot.Owner.TargetGuid != s.autoTarget || t == nil || t.Guid != s.autoTarget || !t.Present || !t.AliveKnown || !t.Alive || !t.AttackVictimKnown || (t.AttackVictimGuid != ownerGUID && t.AttackVictimGuid != s.selfGUID) {
			s.stopLocked(ctx)
			s.stopAutoAttackLocked(ctx)
			s.lastStep = now
			return
		}
	}
	if s.state.MotionUnconfirmed {
		s.state.Assisting = false
		s.state.Reason = "motion_unconfirmed_explicit_stop_required"
		return
	}
	if s.state.CastPending {
		s.stopLocked(ctx)
		s.lastStep = now
		return
	}
	if s.state.Healing && snapshot.Owner.HealthKnown && snapshot.Owner.MaxHealth > 0 && uint64(snapshot.Owner.Health)*100 < uint64(snapshot.Owner.MaxHealth)*85 {
		if s.healLocked(ctx, snapshot) {
			return
		}
	}
	if s.state.Assisting && s.assistLocked(ctx, snapshot) {
		s.lastStep = now
		return
	}
	if s.state.AutoAttackUnconfirmed {
		// A failed assist gate may just have requested ATTACKSTOP. Do not
		// fall through to a new follow movement in that same controller tick.
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return
	}
	if !s.state.Following {
		// The prior movement can belong to a rogue approach even when the
		// separate Follow intent has never been enabled.
		s.stopLocked(ctx)
		s.lastStep = now
		return
	}
	// This first slice is restricted to Eastern Kingdoms outdoor map0 and
	// nearby almost-level ground. It cannot determine walls or ground height.
	if snapshot.Self.MapId != 0 {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "follow_requires_outdoor_map_zero"
		return
	}
	target := playerPosition(snapshot.Owner)
	d := distance(s.pos, target)
	if d > 30 || math.Abs(float64(target.z-s.pos.z)) > 1.5 {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "follow_target_too_far_or_vertical"
		return
	}
	if d <= 3.0 {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "within_follow_distance"
		return
	}
	if !snapshot.Self.SpeedsKnown || math.Abs(float64(snapshot.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(snapshot.Self.RunSpeed)) {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "normal_run_speed_unknown"
		return
	}
	elapsed := now.Sub(s.lastStep).Seconds()
	s.lastStep = now
	if elapsed < 0 || elapsed > 0.6 {
		s.stopLocked(ctx)
		s.state.Reason = "follow_tick_late"
		return
	}
	p := s.pos
	p.o = float32(math.Atan2(float64(target.y-p.y), float64(target.x-p.x)))
	if p.o < 0 {
		p.o += 2 * math.Pi
	}
	op := uint32(opcode.MSGMoveHeartbeat)
	step := 0.0
	if !s.state.Moving {
		op = opcode.MSGMoveStartForward
	} else {
		step = math.Min(math.Min(float64(snapshot.Self.RunSpeed), 7)*elapsed, d-3.0)
		fraction := float32(step / d)
		p.x += (target.x - p.x) * fraction
		p.y += (target.y - p.y) * fraction
		p.z += (target.z - p.z) * fraction
	}
	if s.movementLocked(ctx, op, p, true) {
		s.state.Reason = "following_direct_ground_estimate"
	} else {
		s.state.Following = false
		s.stopLocked(ctx)
	}
}

func (s *Service) healLocked(ctx context.Context, snapshot *worldv1.SnapshotResponse) bool {
	if reason := safePlayers(snapshot, s.selfGUID); reason != "" {
		s.state.Reason = reason
		return false
	}
	if !snapshot.Self.ClassKnown || snapshot.Self.ClassId != 5 {
		s.state.Reason = "healing_unavailable_for_class"
		return false
	}
	if !s.stopAutoAttackLocked(ctx) {
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return false
	}
	if !s.refreshPositionLocked(snapshot) {
		s.stopLocked(ctx)
		return false
	}
	if s.state.MotionUnconfirmed {
		s.state.Reason = "motion_unconfirmed_explicit_stop_required"
		return false
	}
	now := s.now()
	if s.state.CastPending || now.Before(s.nextHeal) {
		s.state.Reason = "heal_throttled_or_pending"
		return false
	}
	if !s.state.KnownLesserHeal {
		s.state.Reason = "lesser_heal_not_in_known_spells"
		return false
	}
	a, b := snapshot.Self, snapshot.Owner
	if a.UnitFlags&0x2000 != 0 {
		s.state.Reason = "caster_silenced"
		return false
	}
	if !b.HealthKnown || b.MaxHealth == 0 || b.Health == 0 || b.Health >= b.MaxHealth {
		s.state.Reason = "target_not_injured_or_health_unknown"
		return false
	}
	// Max mana >= base mana for an ordinary level1 human priest. This
	// conservative budget covers DBC16%baseMana before server cost modifiers.
	budget := manaBudget(a, 16)
	if !a.PowerKnown || a.PowerType != 0 || budget == 0 || uint64(a.Power) < budget {
		s.state.Reason = "mana_unknown_or_insufficient"
		return false
	}
	if distance(s.pos, playerPosition(b)) > 35 {
		s.state.Reason = "heal_outside_conservative_range"
		return false
	}
	if !s.stopLocked(ctx) {
		s.state.Healing = false
		s.state.Reason = "movement_stop_unconfirmed_heal_aborted"
		return false
	}
	s.castCount++
	s.nextHeal = now.Add(3 * time.Second)
	s.castUntil = now.Add(5 * time.Second)
	s.state.CastPending = true
	s.state.HealAttempts++
	s.pendingSpell = lesserHeal
	s.pendingTarget = ownerGUID
	s.nextAssist = now.Add(3 * time.Second)
	s.state.LastCastSpellId = lesserHeal
	s.state.LastCastTargetGuid = ownerGUID
	s.state.LastCastFailure = 0
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, castPacket(s.castCount)) {
		s.state.CastPending = false
		s.pendingSpell = 0
		s.pendingTarget = 0
		return false
	}
	s.state.Reason = "heal_transport_written_waiting_for_server"
	return true
}

func (s *Service) OnPacket(_ context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if packet == nil {
		return &emptypb.Empty{}, nil
	}
	p := packet.Payload
	malformed := false
	switch packet.Opcode {
	case opcode.SMSGItemQuerySingleResponse:
		proof, err := decodeWeaponQuery(p)
		if err != nil {
			malformed = true
		} else if s.weaponQueries[proof.entry] {
			s.weapons[proof.entry] = proof
		}
	case opcode.SMSGAttackerstateupdate:
		attacker, _, damage, err := decodeMeleeDamage(p)
		if err != nil {
			malformed = true
		} else if attacker == s.selfGUID && damage > 0 {
			s.state.MeleeDamageEvents++
			s.state.MeleeDamageTotal += uint64(damage)
			s.state.AssistDamageEvents++
			s.state.AssistDamageTotal += uint64(damage)
		}
	case opcode.SMSGAttackstart:
		if len(p) != 16 {
			malformed = true
		} else if binary.LittleEndian.Uint64(p) == s.selfGUID {
			target := binary.LittleEndian.Uint64(p[8:])
			if target == 0 {
				malformed = true
			} else {
				s.state.AutoAttacking = true
				s.state.AutoAttackTargetGuid = target
				if !s.autoStopping {
					s.autoTarget = target
					s.state.AutoAttackUnconfirmed = false
				}
			}
		}
	case opcode.SMSGAttackstop:
		attacker, n, err := readGUID(p)
		if err != nil {
			malformed = true
			break
		}
		target, m, err := readGUID(p[n:])
		if err != nil || len(p) != n+m+4 {
			malformed = true
			break
		}
		dead := binary.LittleEndian.Uint32(p[n+m:])
		if dead > 1 {
			malformed = true
			break
		}
		if attacker == s.selfGUID {
			if !s.autoStopping && dead == 0 && s.autoTarget != 0 {
				s.failedAssistTarget = s.autoTarget
			}
			_ = target
			s.autoTarget = 0
			s.autoStopping = false
			s.state.AutoAttacking = false
			s.state.AutoAttackTargetGuid = 0
			s.state.AutoAttackRequested = false
			s.state.AutoAttackUnconfirmed = false
		}
	case opcode.SMSGSpellnonmeleedamagelog:
		caster, _, spell, damage, err := decodeSpellDamage(p)
		if err != nil {
			malformed = true
		} else if caster == s.selfGUID {
			s.offensiveDamageLocked(spell, damage)
		}
	case opcode.SMSGInitialSpells:
		_, err := initialSpellKnown(p, lesserHeal)
		if err != nil {
			malformed = true
		} else {
			for _, spell := range []uint32{lesserHeal, smite, fireball, sinisterStrike, autoAttack, 13732, 13863} {
				known, _ := initialSpellKnown(p, spell)
				s.setKnownSpell(spell, known)
			}
		}
	case opcode.SMSGLearnedSpell, opcode.SMSGRemovedSpell:
		if len(p) < 4 {
			malformed = true
		} else {
			spell := binary.LittleEndian.Uint32(p)
			s.setKnownSpell(spell, packet.Opcode == opcode.SMSGLearnedSpell)
		}
	case opcode.SMSGCastFailed:
		if len(p) < 6 {
			malformed = true
		} else if s.state.CastPending && s.pendingSpell != 0 && binary.LittleEndian.Uint32(p[1:]) == s.pendingSpell && p[0] == s.castCount {
			s.castFailedLocked(uint32(p[5]), "server_cast_failed_healing_disabled")
		}
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		guid, count, spell, offset, err := castEvent(p)
		if err != nil {
			malformed = true
		} else if s.state.CastPending && guid == s.selfGUID && spell == s.pendingSpell && count == s.castCount {
			if !validCastTargetBody(p, offset, packet.Opcode == opcode.SMSGSpellGo, s.pendingTarget) {
				malformed = true
			} else if packet.Opcode == opcode.SMSGSpellStart {
				if spell == lesserHeal {
					s.state.ServerCastStarts++
				} else {
					s.state.ServerAssistStarts++
				}
				s.state.Reason = "server_cast_started"
			} else {
				if spell == lesserHeal {
					s.state.ServerCastGo++
					s.state.Reason = "server_spell_go_health_effect_unverified"
				} else {
					s.state.ServerAssistGo++
					if spell == smite {
						s.state.Reason = "server_smite_go_damage_effect_unverified"
					} else {
						s.state.Reason = "server_offensive_spell_go_damage_effect_unverified"
					}
				}
				s.state.CastPending = false
				s.pendingSpell = 0
				s.pendingTarget = 0
			}
		}
	case opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther:
		guid, n, err := readGUID(p)
		if err != nil || len(p) < n+6 {
			malformed = true
		} else if s.state.CastPending && s.pendingSpell != 0 && guid == s.selfGUID && p[n] == s.castCount && binary.LittleEndian.Uint32(p[n+1:]) == s.pendingSpell {
			s.castFailedLocked(uint32(p[n+5]), "server_cast_interrupted_healing_disabled")
		}
	}
	if malformed {
		s.state.MalformedPackets++
	}
	return &emptypb.Empty{}, nil // Packet-handler errors would kill the core session.
}
