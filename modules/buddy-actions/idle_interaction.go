package buddyactions

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/channelstate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// This binding recognizes only leases created through AcquireIdleInteraction.
// It adds no separate ownership or action history to the existing lease.
type idleInteractionBinding struct {
	sessionID       string
	selfGUID, visit uint64
	token           string
}

// AcquireIdleInteraction acquires the existing finite interaction lease only
// when the exact current body is already idle. It never stops, reconciles or
// changes an outstanding action in order to make that body available.
func (s *Service) AcquireIdleInteraction(ctx context.Context, request *actionsv1.IdleInteractionRequest) (*actionsv1.InteractionLease, error) {
	if request == nil || request.SessionId == "" || len(request.SessionId) > 128 || request.SelfGuid == 0 || request.VisitId == 0 ||
		!validInteractionOwner(request.Owner) || len(request.Token) > 128 {
		return nil, status.Error(codes.InvalidArgument, "exact session body visit and interaction owner required")
	}
	r := proto.Clone(request).(*actionsv1.IdleInteractionRequest)
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
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	started := s.now()
	if reason := s.idleInteractionGuardLocked(r, started); reason != "" {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	token := r.Token
	if token == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, status.Error(codes.Internal, "interaction token generation failed")
		}
		token = hex.EncodeToString(random[:])
	}
	// Unlike snapshot(), these factual reads do not update
	// recovery, navigation, class, receipt or movement bookkeeping on refusal.
	w, err := s.readWorldSnapshot(ctx)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		return nil, status.Error(codes.FailedPrecondition, "current world observations unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId {
		return nil, status.Error(codes.FailedPrecondition, "current observed body and visit required")
	}
	// Pending taxi arrivals are admitted only by their own authoritative landing path.
	if !s.flightpathActivation.operation.GetPending() && s.hasSurfaceBodyAt(w) {
		stop, err := s.currentSurfaceReportLocked(ctx, w)
		if err != nil || !s.surfaceInteractionStopMatches(w, stop) {
			return nil, status.Error(codes.FailedPrecondition, "current written stationary ground body required")
		}
		w = surfaceMovementView(w, s.surfaceFollow.body.info)
	}
	flags := w.GetSelf().GetUnitFlags()
	if r.Owner == "exploration" {
		flags = s.groundTravelUnitFlags(w.Self)
	}
	natural := r.Owner == "exploration" && s.naturalTaxiLandingLocked(s.flightpathActivation.revision, w.GetSelfMotionAuthority().GetNaturalTaxiLandingRevision(), w)
	if reason := safeQuestCharacterFlags(w, r.SelfGuid, movementflags.GroundCompatible, flags); reason != "" && !natural {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	self := w.Self
	if !self.HealthKnown || self.Health == 0 || self.MaxHealth == 0 || self.Health > self.MaxHealth ||
		!natural && (self.PositionRevision == 0 || self.MovementFlags&0xff != 0) || !channelstate.Idle(self) {
		return nil, status.Error(codes.FailedPrecondition, "known stationary living unchanneled body required")
	}
	now := s.now()
	until := started.Add(time.Duration(duration) * time.Millisecond)
	if now.Before(started) || !now.Before(until) {
		return nil, status.Error(codes.FailedPrecondition, "interaction admission deadline expired")
	}
	if reason := s.idleInteractionGuardLocked(r, now); reason != "" {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	if s.flightpathActivation.operation.GetPending() && !natural {
		// An accepted booking stays pending until actual arrival settlement.
		// Only the parent may obtain new current-visit custody after a qualified
		// actual ground state; this path never carries or clears an old token.
		if !s.acceptedFlightpathIdleCandidateLocked(r) || safeQuestCharacter(w, r.SelfGuid) != "" ||
			!w.Self.HealthKnown || w.Self.Health == 0 || w.Self.MaxHealth == 0 || w.Self.Health > w.Self.MaxHealth ||
			w.Self.MovementFlags&0xff != 0 || !channelstate.Idle(w.Self) ||
			s.flightpathRetiredWorldReasonLocked(s.flightpathActivation.revision, w.GetSplineCompletion().GetRevision(), w) != "" {
			return nil, status.Error(codes.FailedPrecondition, "accepted flight requires exact current retired ground proof before idle custody")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	// Nothing below reads a peer or reconciles unrelated state. A renewal keeps
	// its exact token; an expired token cannot enter this commit path.
	s.interactionToken, s.interactionUntil = token, until
	s.state.InteractionOwner = r.Owner
	s.idleInteraction = idleInteractionBinding{sessionID: r.SessionId, selfGUID: r.SelfGuid, visit: r.VisitId, token: token}
	s.lastStep = now
	result := s.idleInteractionLeaseLocked(w, now)
	if natural {
		// Possession of this finite token conveys no destination coordinates.
		s.flightpathActivation.naturalMenuToken = token
		result.State.Position = nil
	}
	return result, nil
}

func (s *Service) idleInteractionGuardLocked(r *actionsv1.IdleInteractionRequest, now time.Time) string {
	if s.closed || s.session == nil || s.sessionID != r.SessionId || s.selfGUID != r.SelfGuid ||
		!s.encounterVisit.bound || s.encounterVisit.revision != r.VisitId {
		return "current connected body and bound visit required"
	}
	if s.state.InteractionHarvest || len(s.state.HarvestGuardGuids) != 0 {
		return "non-harvest idle custody required"
	}
	if s.state.InteractionOwner != "" {
		binding := s.idleInteraction
		if !now.Before(s.interactionUntil) || s.state.InteractionHarvest || r.Owner != s.state.InteractionOwner || r.Token == "" ||
			subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 ||
			binding.token != s.interactionToken || binding.sessionID != r.SessionId || binding.selfGUID != r.SelfGuid || binding.visit != r.VisitId {
			return "exact unexpired idle interaction binding required"
		}
	} else if r.Token != "" || s.interactionToken != "" {
		return "idle interaction token expired or no longer owns the lease"
	}
	if s.bodyMoving || s.state.Moving || s.state.MotionUnconfirmed || s.clientControlLost || s.clientControlRestoring ||
		s.state.CastPending || s.pendingSpell != 0 || s.pendingTarget != 0 || s.pendingCrowdControlLocked() != nil || s.itemCreationPendingLocked() != nil ||
		s.pendingMeleeOperationLocked() != nil || s.state.AutoAttackRequested || s.state.AutoAttacking || s.state.AutoAttackUnconfirmed ||
		s.autoTarget != 0 || s.autoStopping || s.encounterSwing.target != 0 || s.state.Traveling || s.questApproaching ||
		s.state.Practicing || s.fargodeep.active || s.recovery.state.Active || s.recovery.token != "" ||
		s.targetMovementExecuting || s.targetMovement.view.TargetGuid != 0 && s.targetMovement.view.VisitId == r.VisitId && now.Before(s.targetMovement.until) || s.followGoalExecuting != nil ||
		s.movementOwner.owner != "" && now.Before(s.movementOwner.until) ||
		s.flightpath.operation.GetPending() || s.flightpathActivation.operation.GetPending() && !s.acceptedFlightpathIdleCandidateLocked(r) {
		return "body action or foreign custody must already be idle"
	}
	if op := s.supportOp; op != nil {
		switch op.view.Phase {
		case "outcome_unconfirmed_no_retry":
			// A validated later world entry invalidates the old visit's
			// physical cast, not its UNKNOWN outcome. Admission still needs
			// fresh current living/stationary/channel-idle World below.
			if op.view.OperationRevision != 0 && op.view.VisitId != 0 && op.view.VisitId < r.VisitId &&
				op.lifecycleSession == r.SessionId && op.lifecycleSelf == r.SelfGuid {
				break
			}
			// A count-matched GO launched this non-channel offense. Its
			// remaining receipt window was for damage, which may never be
			// observed. Exploration still checks the current idle body.
			if r.Owner == "exploration" && op.view.Capability == offenseCapability && op.view.TransportWritten &&
				op.view.ServerCastGo && op.effectLogEligible && op.count == s.castCount && !op.selfChannel && !op.view.ChannelStarted &&
				!now.Before(op.until) && !now.Before(s.castUntil) {
				if spell, known := s.spellData.Lookup(op.view.SpellId); known && !spell.Channeled() {
					break
				}
			}
			return "previous cast or channel is unsettled"
		case "waiting_server", "waiting_channel_observation", "channeling", "waiting_cleanse_effect", "waiting_created_item", "transport_unconfirmed_no_retry":
			return "previous cast or channel is unsettled"
		}
	}
	if op := s.buffOp; op != nil && (op.view.Phase == "waiting_server" || op.view.Phase == "outcome_unconfirmed") {
		return "previous buff cast is unsettled"
	}
	return ""
}

func (s *Service) acceptedFlightpathIdleCandidateLocked(r *actionsv1.IdleInteractionRequest) bool {
	return r.Owner == "exploration" && s.flightpathAcceptedCurrentLocked(s.flightpathActivation.revision)
}

// Return a detached observation without stateLocked's expiry/reconciliation
// side effects. Raw World position remains a baseline, not a movement ACK.
func (s *Service) idleInteractionLeaseLocked(w *worldv1.SnapshotResponse, now time.Time) *actionsv1.InteractionLease {
	out := proto.Clone(&s.state).(*actionsv1.State)
	out.SessionId, out.SelfGuid = s.sessionID, s.selfGUID
	out.InteractionLeaseRemainingMs = uint64(s.interactionUntil.Sub(now).Milliseconds())
	self := w.Self
	out.Position = &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: self.MapId,
		X: self.X, Y: self.Y, Z: self.Z, Orientation: self.Orientation, AgeMs: self.PositionAgeMs, Revision: self.PositionRevision}
	if s.posKnown && s.posMap == self.MapId && s.positionRevision == self.PositionRevision && finite(s.pos) {
		out.Position.X, out.Position.Y, out.Position.Z, out.Position.Orientation = s.pos.x, s.pos.y, s.pos.z, s.pos.o
		out.Position.Estimated, out.Position.Revision = s.posEstimated, s.posRevision
		if !s.posAt.IsZero() && !now.Before(s.posAt) {
			out.Position.AgeMs = uint64(now.Sub(s.posAt).Milliseconds())
		}
	}
	return &actionsv1.InteractionLease{Token: s.interactionToken, State: out}
}
