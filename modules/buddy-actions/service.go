// Package buddyactions implements an explicitly enabled, bounded controller.
// It has no language model, arbitrary target/spell selector or teleport.
package buddyactions

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/dungeontrace"
	"github.com/agent-wow/agent-wow/pkg/harvestguard"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	lease      = 4 * time.Hour
	limitation = "Navigation uses installed client map, terrain and collision data for the currently observed map; unavailable or unsafe routes pause movement. Spell transport and server spell-go are not health/damage confirmation."
)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	GetClock(context.Context, *emptypb.Empty, ...grpc.CallOption) (*modv1.ClockResponse, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}

type Service struct {
	ordinaryMovement *ordinaryMovementAttempt
	movementPoints   movementPointInput

	clientControlLost, clientControlRestoring bool

	actionsv1.UnimplementedBuddyActionsServer
	mu                                         sync.Mutex
	session                                    sessionAPI
	dungeonTrace                               *dungeontrace.Recorder
	traceAutoDirectory                         string
	traceVisitPending                          string
	traceDraining                              []*dungeontrace.Recorder
	traceLastWorld, traceLastParty             time.Time
	tracePreviousWorld                         *worldv1.SnapshotResponse
	traceBoundary                              dungeonTraceBoundary
	sessionID                                  string
	selfGUID                                   uint64
	independent                                bool
	hostStandingHealing                        bool
	hostSelfMana                               bool
	hostEncounterKey                           string
	hostEncounterVisit                         uint64
	hostEncounterUntil                         time.Time
	hostEncounterEpoch                         uint64
	encounterVisit                             encounterVisit
	now                                        func() time.Time
	state                                      actionsv1.State
	closed                                     bool
	followUntil, lastStep, nextHeal, castUntil time.Time
	pos                                        position
	posKnown                                   bool
	positionRevision                           uint64
	castCount, cancelCastCount                 byte
	spellOperationSequence                     uint64
	spellOperationEpoch                        uint64
	spellOperationEntropy                      io.Reader
	talents                                    talentObservation
	protectedCastCancelAttempted               bool
	lastWorld                                  *worldv1.SnapshotResponse
	posMap                                     uint32
	posEstimated                               bool
	posAt                                      time.Time
	posRevision                                uint64
	movementSequence                           uint64
	engage                                     engageIntent
	crowdControlSight                          func(context.Context, string, uint32, position, position) (bool, error)
	controlTemplates                           map[uint32]controlTemplate
	targetProtection                           targetProtection
	targetMovement                             targetMovementIntent
	targetMovementExecuting                    bool
	bodyMoving                                 bool
	bodyMotionPlan                             func(context.Context, string, uint32, buddynav.Point, float64, float64, time.Duration, bool) ([]buddynav.BodyMotionSample, error)
	bodyMotionWait                             func(context.Context, time.Duration) error
	navigationGoal                             position
	navigationGoalKnown                        bool
	thermaplugg                                thermapluggButtons
	movementOwner                              movementOwner
	interactionToken                           string
	interactionUntil                           time.Time
	idleInteraction                            idleInteractionBinding
	harvestGuardAt                             time.Time
	assistUntil, nextAssist                    time.Time
	pendingSpell                               uint32
	lastInterrupt                              enemyCastIdentity
	pendingTarget, failedAssistTarget          uint64
	failedAssistCastTarget                     uint64
	weaponQueries                              map[uint32]bool
	weapons                                    map[uint32]weaponProof
	weaponOrder                                []uint32
	knownSpells                                map[uint32]bool
	spellData, capabilityCatalog               *spellcatalog.Catalog
	capabilities                               map[uint32]spellcatalog.Spell
	capabilityRevision                         uint64
	spellReady                                 map[uint32]time.Time
	spellCooldownBaselineKnown                 bool
	weaponCombo                                comboObservation
	categoryReady                              map[uint32]time.Time
	serverSpellReady, serverCategoryReady      map[uint32]time.Time
	controlBreakBindings                       map[uint32]observedBuffAura
	supportOp                                  *supportAttempt
	meleeOp                                    *meleeAttempt
	encounterSwing                             encounterMeleeSwing
	meleeAcknowledgedVisit                     uint64
	hostResurrectionOwned, hostInterruptOwned  bool
	resurrectionBindings                       map[uint64]bool
	resurrectionOffer                          *resurrectionOffer
	resurrectionOfferSequence                  uint64
	behavior                                   *behavior.Runtime
	behaviorRejection                          string

	improvedSinister1, improvedSinister2 bool
	autoTarget                           uint64
	autoStopping                         bool
	questApproaching                     bool
	lootApproachGuard                    func(context.Context, *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool)
	approachDelay                        func(context.Context, time.Duration) error
	navDataDir                           string
	planPath                             func(context.Context, string, position, position) ([]position, error)
	planMapPath                          func(context.Context, string, uint32, position, position) ([]position, error)
	planRecoveryPath                     func(context.Context, string, uint32, position, position, float64) ([]position, error)
	planLegPath                          func(context.Context, string, uint32, position, position) ([]position, error)
	planColdridgePath                    func(context.Context, string, position, position) ([]position, error)
	planColdridge182Path                 func(context.Context, string, position, position) ([]position, error)
	practiceUntil                        time.Time
	practiceRoute                        []position
	practiceRouteIndex                   int
	practiceTarget                       uint64
	practicePullStarted                  bool
	practiceRouteTarget                  position
	practiceReplans                      int
	practiceKillCredited                 bool
	practiceQuestGoal                    bool
	coldridgeGoal                        coldridgePracticeGoal
	formation                            followFormation
	starterSpells                        starterSpellPolicy
	followGoal                           followFormationGoal
	followGoalExecuting                  *followFormationGoal
	followRevision                       uint64
	followNav                            followNavigation
	followPortal                         dungeonPortalFollow
	portalGoal                           portalCatchupGoal
	portalGoalRevision                   uint64
	portalWitnessRevision                uint64
	swimFollow                           swimFollowRoute
	swimTrail                            swimTrail
	planSwimPath                         func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.SwimPoint, error)
	swimming                             bool
	meleeNav                             followNavigation
	recovery                             deathRecovery
	fargodeep                            fargodeepEncounter
	fargodeepSeen                        map[uint64]bool
	spellbookKnown                       bool
	spellbookRevision                    uint64
	buffUntil                            time.Time
	buffAuras                            map[uint64]*buffAuraSet
	ownDamageAuras                       map[damageAuraSlot]observedBuffAura
	buffAuraRevision, buffSequence       uint64
	buffOp                               *buffAttempt
	transportSequence                    uint64
	lastTransportError                   error
	flightpath                           flightpathTracker
	flightpathActivation                 flightpathActivationTracker
	areaTrigger                          areaTriggerTracker
	idleMotion                           *idleMotionScope
}

func NewService(session sessionAPI, selfGUID uint64, sessionID ...string) *Service {
	s := &Service{session: session, selfGUID: selfGUID, now: time.Now, state: actionsv1.State{Reason: "disabled", Limitation: limitation}, weaponQueries: make(map[uint32]bool), weapons: make(map[uint32]weaponProof), approachDelay: waitApproach, planPath: planNavigation, planMapPath: planMapNavigation, planRecoveryPath: planRecoveryNavigation, planLegPath: planLegNavigation, planColdridgePath: planColdridgeNavigation, planColdridge182Path: planColdridge182Navigation}
	if len(sessionID) == 1 && len(sessionID[0]) <= 128 {
		s.sessionID = sessionID[0]
	}
	return s
}

// NewIndependentService keeps self-scoped actions available without granting
// authority to the companion controllers or a nearby owner's observations.
func NewIndependentService(session sessionAPI, selfGUID uint64, sessionID ...string) *Service {
	s := NewService(session, selfGUID, sessionID...)
	s.independent = true
	s.state.Limitation += " Independent mode disables owner follow, healing and assistance; own quest travel and practice retain their existing safety bounds."
	return s
}

func (s *Service) stateLocked() *actionsv1.State {
	s.expireInteractionLocked()
	s.expireMeleeOperationLocked()
	s.updateClassLocked()
	s.buffTimeoutLocked()
	out := proto.Clone(&s.state).(*actionsv1.State)
	out.SessionId, out.SelfGuid = s.sessionID, s.selfGUID
	if s.formation.lastFailure != nil {
		out.LastFollowPolicyFailure = proto.Clone(s.formation.lastFailure).(*actionsv1.PolicyDecisionFailure)
	}
	out.Recovery = s.recoveryStateLocked()
	out.Recovery.MotionPending = s.state.Moving || s.state.MotionUnconfirmed
	out.Fargodeep = s.fargodeep.snapshot(s.now())
	out.EngageNpc = s.engage.snapshot(s.now())
	out.CrowdControlApproachSupported = true
	out.TargetMovement = s.targetMovementSnapshotLocked()
	out.MovementOwner = s.movementOwnerStateLocked()
	out.MovementPoints = s.movementPointsStateLocked()
	s.targetProtection.snapshot(out, s.now())
	out.Buffing = s.state.Buffing
	out.BuffLeaseRemainingMs = s.buffLeaseRemainingLocked()
	if s.supportOp != nil {
		out.LastSpellOperation = proto.Clone(&s.supportOp.view).(*actionsv1.SpellOperation)
	}
	if s.meleeOp != nil {
		out.LastMeleeOperation = proto.Clone(&s.meleeOp.view).(*actionsv1.MeleeOperation)
	}
	out.BuffReason = s.state.BuffReason
	if s.buffOp != nil {
		out.LastBuff = proto.Clone(&s.buffOp.view).(*actionsv1.BuffOperation)
	}
	now := s.now()
	if s.state.Following && now.Before(s.followUntil) {
		out.FollowLeaseRemainingMs = uint64(s.followUntil.Sub(now).Milliseconds())
	}
	if s.state.Assisting && now.Before(s.assistUntil) {
		out.AssistLeaseRemainingMs = uint64(s.assistUntil.Sub(now).Milliseconds())
	}
	if s.state.Practicing && now.Before(s.practiceUntil) {
		out.PracticeLeaseRemainingMs = uint64(s.practiceUntil.Sub(now).Milliseconds())
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
	return owner == "loot" || owner == "quests" || owner == "trade" || owner == "training" || owner == "mail" || owner == "professions" || owner == "exploration"
}
func (s *Service) expireInteractionLocked() {
	if s.state.InteractionOwner != "" && !s.now().Before(s.interactionUntil) {
		// An approach must stop before its expired lease can be discarded.
		// Tick owns the bounded transport write; status remains read-only.
		if s.state.Moving || s.state.MotionUnconfirmed && (s.state.InteractionHarvest || s.idleInteraction.token != "" && s.idleInteraction.token == s.interactionToken) {
			return
		}
		s.state.InteractionOwner = ""
		s.state.InteractionHarvest = false
		s.state.HarvestGuardGuids = nil
		s.interactionToken = ""
		s.lastStep = s.now()
	}
}

func (s *Service) AcquireInteraction(ctx context.Context, r *actionsv1.InteractionRequest) (*actionsv1.InteractionLease, error) {
	if r == nil || !validInteractionOwner(r.Owner) {
		return nil, status.Error(codes.InvalidArgument, "interaction owner must be loot, quests, trade, training, mail, professions, or exploration")
	}
	if r.Harvest && r.Owner != "exploration" && r.Owner != "professions" {
		return nil, status.Error(codes.InvalidArgument, "harvest guard only supports exploration and professions")
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
	if s.bodyMoving {
		return nil, status.Error(codes.FailedPrecondition, "bounded body input still owns movement")
	}
	s.expireInteractionLocked()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if s.encounterSwing.target != 0 {
		return nil, status.Error(codes.FailedPrecondition, "encounter melee owns the body until the server stops its swing")
	}
	if s.state.InteractionOwner != "" {
		if !s.now().Before(s.interactionUntil) {
			return nil, status.Error(codes.FailedPrecondition, "expired interaction awaits movement stop")
		}
		if r.Owner != s.state.InteractionOwner || r.Token == "" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 {
			return nil, status.Error(codes.FailedPrecondition, "another interaction is active")
		}
		if r.Harvest != s.state.InteractionHarvest {
			return nil, status.Error(codes.FailedPrecondition, "interaction harvest guard cannot change for this token")
		}
		if r.Harvest {
			_, guids, reason := s.harvestContextLocked(ctx)
			if reason != "" {
				s.revokeHarvestLocked(ctx, reason)
				return nil, status.Error(codes.FailedPrecondition, reason)
			}
			s.state.HarvestGuardGuids, s.harvestGuardAt = guids, s.now()
		}
		s.interactionUntil = s.now().Add(time.Duration(duration) * time.Millisecond)
		return &actionsv1.InteractionLease{Token: s.interactionToken, State: s.stateLocked()}, nil
	}
	if r.Token != "" {
		return nil, status.Error(codes.FailedPrecondition, "interaction lease expired; acquire a new lease")
	}
	if s.fargodeep.active {
		return nil, status.Error(codes.FailedPrecondition, "coordinated encounter is active")
	}
	if s.state.CastPending || s.state.MotionUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "casting or movement state is unconfirmed")
	}
	var harvestWorld *worldv1.SnapshotResponse
	var harvestGuids []uint64
	// Only this guarded route may yield confirmed ordinary following. It must
	// complete the same normal STOP before a token can authorize any detour.
	harvestFollowHandoff := r.Harvest && r.Owner == "exploration" && s.state.Moving && s.state.Following && s.now().Before(s.followUntil)
	if r.Harvest {
		if s.state.Moving && !harvestFollowHandoff || s.state.AutoAttacking || s.state.AutoAttackRequested || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.state.Practicing || s.state.Traveling || s.recovery.state.Active {
			return nil, status.Error(codes.FailedPrecondition, "harvest_defense_or_other_action_pending")
		}
		var reason string
		harvestWorld, harvestGuids, reason = s.harvestContextLocked(ctx)
		if reason != "" {
			return nil, status.Error(codes.FailedPrecondition, reason)
		}
		if harvestWorld.Self.MovementFlags&0xff != 0 && !harvestFollowHandoff {
			return nil, status.Error(codes.FailedPrecondition, "harvest_initial_stationary_observation_required")
		}
	}
	if !s.stopAutoAttackLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "auto attack stop awaits server confirmation")
	}
	w, err := harvestWorld, error(nil)
	if !r.Harvest {
		w, err = s.snapshot(ctx)
	}
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "world observations unavailable")
	}
	if !s.clientPositionLocked().Known {
		return nil, status.Error(codes.FailedPrecondition, "current client position is unknown")
	}
	// A server correction can arrive between controller ticks. Rebase before
	// writing STOP so an interaction never writes the superseded estimate.
	s.refreshPositionLocked(w)
	stopped := false
	if r.Harvest {
		stopped = s.stopHarvestMovementLocked(ctx)
	} else {
		stopped = s.stopLocked(ctx)
	}
	if !stopped {
		return nil, status.Error(codes.FailedPrecondition, "movement stop could not be written")
	}
	if r.Harvest {
		if harvestFollowHandoff {
			// STOP itself crosses clock/transport callbacks. Rebind the party
			// before the LAST world read so a changed roster cannot inherit
			// custody from the earlier following context.
			fresh, guids, reason := s.harvestContextLocked(ctx)
			if reason != "" {
				return nil, status.Error(codes.FailedPrecondition, reason)
			}
			if len(guids) != len(harvestGuids) {
				return nil, status.Error(codes.FailedPrecondition, "harvest_follow_handoff_party_changed")
			}
			for i := range guids {
				if guids[i] != harvestGuids[i] {
					return nil, status.Error(codes.FailedPrecondition, "harvest_follow_handoff_party_changed")
				}
			}
			if fresh.Self.MapId != harvestWorld.Self.MapId || fresh.Self.PositionRevision != harvestWorld.Self.PositionRevision {
				return nil, status.Error(codes.FailedPrecondition, "harvest_follow_handoff_baseline_changed")
			}
		}
		// STOP obtains its own final world snapshot. A fresh attacker arriving
		// there must not be hidden by granting the lease from the earlier view.
		if reason := harvestguard.WorldReason(s.lastWorld, s.selfGUID, harvestGuids); reason != "" {
			return nil, status.Error(codes.FailedPrecondition, reason)
		}
		if harvestFollowHandoff && (s.lastWorld.Self.MapId != harvestWorld.Self.MapId || s.lastWorld.Self.PositionRevision != harvestWorld.Self.PositionRevision) {
			return nil, status.Error(codes.FailedPrecondition, "harvest_follow_handoff_baseline_changed")
		}
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, status.Error(codes.Internal, "interaction token generation failed")
	}
	// This is a fresh legacy lease, not a renewal of the previous idle-only
	// token. Its new authority must not inherit that token's body/visit fence.
	s.idleInteraction = idleInteractionBinding{}
	s.interactionToken = hex.EncodeToString(token[:])
	s.interactionUntil = s.now().Add(time.Duration(duration) * time.Millisecond)
	s.state.InteractionOwner = r.Owner
	s.state.InteractionHarvest = r.Harvest
	if r.Harvest {
		s.state.HarvestGuardGuids, s.harvestGuardAt = harvestGuids, s.now()
	}
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
	if r.SessionId != "" || r.SelfGuid != 0 || r.VisitId != 0 {
		if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || r.VisitId == 0 {
			return nil, status.Error(codes.FailedPrecondition, "current interaction body and visit required")
		}
		w, err := s.snapshot(ctx)
		if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId {
			return nil, status.Error(codes.FailedPrecondition, "current observed interaction visit required")
		}
	}
	if s.state.InteractionOwner != r.Owner || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 {
		return nil, status.Error(codes.FailedPrecondition, "interaction lease does not match")
	}
	ctx = modcb.ForwardPolicySpell(ctx)
	stopped := false
	if s.state.InteractionHarvest {
		stopped = s.stopHarvestMovementLocked(ctx)
	} else {
		stopped = s.stopLocked(ctx)
	}
	if !stopped {
		return nil, status.Error(codes.FailedPrecondition, "interaction movement stop unconfirmed")
	}
	s.state.InteractionOwner = ""
	s.state.Traveling = false
	s.state.InteractionHarvest = false
	s.state.HarvestGuardGuids = nil
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
	if r.Enabled && s.independent {
		return nil, status.Error(codes.FailedPrecondition, "owner follow is unavailable in independent mode")
	}
	if r.Enabled {
		if s.state.MotionUnconfirmed {
			s.state.Reason = "motion_unconfirmed_explicit_stop_required"
			return s.stateLocked(), nil
		}
		if !s.state.Following {
			s.formation.Reset()
			s.followGoal = followFormationGoal{}
			s.followRevision++
			s.followNav.Reset()
			if !s.followPortal.notified {
				s.followPortal = dungeonPortalFollow{}
			}
		}
		s.state.Following = true
		s.followUntil = s.now().Add(lease)
		s.lastStep = s.now()
		s.state.Reason = "follow_enabled_waiting_for_safe_observations"
	} else {
		s.state.Following = false
		if s.fargodeep.active {
			s.stopFargodeepLocked(ctx, "explicit_follow_stop")
		}
		s.formation.Reset()
		s.followGoal = followFormationGoal{}
		s.followRevision++
		s.followNav.Reset()
		if !s.followPortal.notified {
			s.followPortal = dungeonPortalFollow{}
		}
		s.swimFollow = swimFollowRoute{}
		s.swimTrail = swimTrail{}
		if s.explicitStopLocked(ctx) {
			s.state.Reason = "follow_disabled"
		}
	}
	return s.stateLocked(), nil
}

// Assist retains only the historical intent bit used to arm explicitly named
// starter missions. Ordinary defense/targeting always belongs to Lua; changing
// this compatibility value never stops an attack or disables that policy.
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
	s.state.Assisting = r.Enabled
	if r.Enabled {
		s.assistUntil = s.now().Add(lease)
	} else if s.fargodeep.active {
		s.stopFargodeepLocked(ctx, "explicit_starter_mission_stop")
	}
	s.state.Reason = "legacy_assist_intent_only_lua_owns_combat"
	return s.stateLocked(), nil
}

// HealOnce is preserved as a public compatibility input routed by the core
// to the current attached Lua policy. This module never chooses its patient.
func (s *Service) HealOnce(ctx context.Context, _ *emptypb.Empty) (*actionsv1.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return nil, status.Error(codes.FailedPrecondition, "HealOnce requires the session Lua policy input route; exact callers use CastHeal")
}

func (s *Service) Status(ctx context.Context, _ *emptypb.Empty) (*actionsv1.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read-only refresh: active movement is not echoed back to its sender, so
	// report the client's estimate only while its authoritative base is valid.
	w, err := s.snapshot(ctx)
	if err != nil {
		s.lastWorld = nil
	}
	s.expireCrowdControlLocked()
	s.expireSelfChannelLocked()
	s.expireExplicitCastLocked()
	s.observeItemCreationLocked(w)
	s.supportSnapshotLocked(w)
	return s.stateLocked(), nil
}

func (s *Service) BeforeLogout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishDungeonTraceLocked(s.now(), "observer_logout")
	s.closed = true
	s.state.Buffing = false
	s.buffUnconfirmedLocked("logout_buff_outcome_unconfirmed")
	s.targetProtection.reset()
	s.finishCrowdControlLocked("logout_outcome_unconfirmed")
	s.finishItemCreationLocked("logout_outcome_unconfirmed")
	s.finishSelfChannelLocked("logout_outcome_unconfirmed")
	s.endExplicitCastLocked("outcome_unconfirmed_no_retry")
	s.endMeleeOperationLocked()
	s.meleeAcknowledgedVisit = 0
	if s.encounterSwing.target != 0 {
		s.stopEncounterMeleeLocked(ctx)
	} else {
		s.autoStopping = false
		s.state.AutoAttackUnconfirmed = false
		s.stopAutoAttackLocked(ctx)
	}
	s.state.InteractionOwner = ""
	s.state.InteractionHarvest = false
	s.state.HarvestGuardGuids = nil
	s.interactionToken = ""
	s.movementOwner.owner, s.movementOwner.token = "", ""
	s.movementOwner.until = time.Time{}
	s.state.Following = false
	s.state.Assisting = false
	s.state.Practicing = false
	s.fargodeep.active = false
	s.fargodeep.phase = "session_closing"
	s.engage.phase = "session_closing"
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

func (s *Service) snapshot(ctx context.Context, creatureGUID ...uint64) (*worldv1.SnapshotResponse, error) {
	out, err := s.readWorldSnapshot(ctx, creatureGUID...)
	if err != nil {
		s.observeDungeonWorld(nil)
		return nil, err
	}
	s.observeDungeonWorld(out)
	if s.state.Following && s.navDataDir != "" {
		s.swimTrail.observe(out, s.selfGUID)
	}
	s.lastWorld = out
	s.observeRecoveryLifeLocked(out)
	s.updateClassLocked()
	return out, nil
}

func (s *Service) readWorldSnapshot(ctx context.Context, creatureGUID ...uint64) (*worldv1.SnapshotResponse, error) {
	query := &worldv1.SnapshotRequest{}
	// The exact World lookup is NPC-only. Player and corpse targets already
	// belong to the normal observed rows and must not become creature_guid.
	if len(creatureGUID) != 0 && creatureGUID[0]>>48 == 0xf130 && creatureGUID[0]&0xffffff != 0 {
		query.CreatureGuid = creatureGUID[0]
	}
	return s.readWorldSnapshotRequest(ctx, query)
}

func (s *Service) readWorldSnapshotRequest(ctx context.Context, query *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	if s.session == nil {
		return nil, errors.New("session callback unavailable")
	}
	request, err := anypb.New(query)
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
	if s.independent {
		// A mixed-version world module must not restore owner authority.
		out.Owner, out.OwnerTarget = nil, nil
	}
	return out, nil
}

func playerPosition(p *worldv1.PlayerState) position { return position{p.X, p.Y, p.Z, p.Orientation} }
func distance(a, b position) float64 {
	return math.Sqrt(math.Pow(float64(a.x-b.x), 2) + math.Pow(float64(a.y-b.y), 2) + math.Pow(float64(a.z-b.z), 2))
}

func safePlayers(snapshot *worldv1.SnapshotResponse, selfGUID uint64) string {
	return safePlayerMovement(snapshot, selfGUID, false)
}

func safePlayerMovement(snapshot *worldv1.SnapshotResponse, selfGUID uint64, swimFollow bool) string {
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
	// Passive water-walk/slow-fall/hover status is compatible with ground
	// movement. Actual transports, swimming, falling and flight remain excluded.
	compatible := movementflags.GroundCompatible
	if swimFollow {
		compatible = swimFollowCompatible
	}
	if !compatible(a.MovementFlags, a.ExtraMovementFlags) || !compatible(b.MovementFlags, b.ExtraMovementFlags) {
		return "unsupported_movement_state"
	}
	if b.MovementFlags&(0xff|swimAscendingFlag|swimDescendingFlag) != 0 && b.PositionAgeMs > 2000 {
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
	if p.PositionRevision != s.positionRevision || !p.MapKnown || p.MapId != s.posMap {
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
	s.swimming = p.MovementFlagsKnown && p.MovementFlags&swimMovementFlag != 0
	s.swimFollow = swimFollowRoute{}
	s.posEstimated = false
	s.posAt = s.now().Add(-time.Duration(min(p.PositionAgeMs, uint64(24*time.Hour/time.Millisecond))) * time.Millisecond)
	s.posRevision++
}

func (s *Service) sendLocked(ctx context.Context, op uint32, payload []byte) bool {
	return s.sendPacketLocked(ctx, op, payload) == nil
}

// Keep the typed pre-transport refusal available to exact operations. A plain
// RPC error cannot prove that a packet did not reach the game transport.
func (s *Service) sendPacketLocked(ctx context.Context, op uint32, payload []byte, scopes ...*modv1.TransportScope) error {
	if s.session == nil {
		s.state.Reason = "session_unavailable"
		return errors.New("session unavailable")
	}
	if s.fargodeep.active && op == opcode.CMSGAttackswing && (len(payload) != 8 || !s.fargodeepWriteAllowedLocked(ctx, binary.LittleEndian.Uint64(payload), 0)) {
		return errors.New("packet outside current Fargodeep binding")
	}
	sendCtx := ctx
	if op == opcode.CMSGCastSpell {
		sendCtx = modcb.ForwardPolicySpell(ctx)
	}
	s.transportSequence++
	s.lastTransportError = nil
	request := &modv1.SendPacketRequest{Opcode: op, Payload: payload}
	if len(scopes) != 0 && scopes[0] != nil {
		request.TransportScope = proto.Clone(scopes[0]).(*modv1.TransportScope)
	}
	motion, _ := ctx.Value(idleMotionScopeKey{}).(*idleMotionScope)
	if motion != nil {
		scope, err := s.idleMotionBeforeWriteLocked(ctx, motion, op, payload)
		if err != nil {
			if motion.writes > 0 && errors.Is(err, errIdleMotionReadUnavailable) {
				// The unsent checkpoint cannot be retried through this captured scope.
				// Only fresh fully validated cleanup of its prior writes remains.
				motion.cleanupOnly = true
			}
			if motion.writes > 0 && !errors.Is(err, errNavigationSpeedChanged) && !errors.Is(err, errIdleMotionReadUnavailable) && !errors.Is(err, errIdleMotionStopOnly) {
				motion.broken = true
				s.state.MotionUnconfirmed = true
			}
			s.lastTransportError = err
			s.state.LastTransportFailure = &actionsv1.TransportFailure{Sequence: s.transportSequence, Opcode: op, Error: err.Error(), OutcomeUnknown: false}
			return err
		}
		request.TransportScope = scope
		s.idleMotion = motion
		motion.lastOpcode, motion.lastPayload = op, append([]byte(nil), payload...)
	}
	if _, err := s.session.SendPacket(sendCtx, request); err != nil {
		if motion != nil && (!modcb.PacketWasNotDispatched(err) || motion.writes > 0) {
			motion.broken = true
			s.state.MotionUnconfirmed = true
		}
		s.lastTransportError = err
		s.state.LastTransportFailure = &actionsv1.TransportFailure{Sequence: s.transportSequence, Opcode: op, Error: err.Error(), OutcomeUnknown: !modcb.PacketWasNotDispatched(err)}
		s.state.Reason = "transport_error_outcome_unconfirmed"
		// Only the exact operation's caller can fence its uncertain physical
		// state. A failed packet cannot revoke goals or send unrelated packets.
		return err
	}

	s.state.TransportWrites++
	if motion != nil {
		if err := s.idleMotionAfterWriteLocked(ctx, motion, op, payload); err != nil {
			s.lastTransportError = err
			s.state.NavReason = boundedPolicyDiagnostic("packet written; source report unavailable: "+err.Error(), 240)
			return err
		}
	}
	return nil
}

func (s *Service) movementLocked(ctx context.Context, op uint32, p position, moving bool) bool {
	return s.writeMovementLocked(ctx, op, p, moving, nil)
}

func (s *Service) writeMovementLocked(ctx context.Context, op uint32, p position, moving bool, swim *swimMovement) bool {
	if s.bodyMoving || s.session == nil || s.clientControlLost || s.clientControlRestoring {
		return false
	}
	clock, err := s.session.GetClock(ctx, &emptypb.Empty{})
	if err != nil || clock == nil {
		s.state.Reason = "session_clock_unavailable"
		return false
	}
	// Clock/planning RPCs may race a transfer or authoritative correction. Bind
	// every ordinary movement write to the actual current map and position
	// revision. A STOP is rebased to the fresh observation instead of replaying
	// an old map's coordinates.
	var w *worldv1.SnapshotResponse
	var snapshotErr error
	w, snapshotErr = s.snapshot(ctx)
	if !moving && op == opcode.MSGMoveStop {
		if cleanup, ok := ctx.Value(lootApproachCleanupKey{}).(*lootApproachCleanup); ok && !cleanup.current(s, w, snapshotErr != nil) {
			s.state.Reason = "loot_approach_cleanup_owner_changed"
			return false
		}
	}
	// An owned corpse approach rechecks party and corpse authority after the
	// clock callback. STOP remains available after that authority is revoked.
	if moving && s.lootApproachGuard != nil {
		var allowed bool
		w, allowed = s.lootApproachGuard(ctx, w)
		if !allowed {
			s.state.Reason = "loot_approach_authority_changed_before_movement"
			return false
		}
	}
	if moving {
		if guard, ok := ctx.Value(movementPrewriteGuardKey{}).(movementPrewriteGuard); ok {
			var allowed bool
			w, allowed = guard(ctx, w)
			if !allowed {
				return false
			}
		}
	}
	if snapshotErr != nil {
		// Visibility/RPC loss must still permit one bounded STOP of movement
		// already written at the last known position. It never permits travel.
		if moving || !s.posKnown {
			s.state.Reason = "movement_current_map_observation_unavailable"
			return false
		}
	} else if w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.MapKnown || !w.Self.PositionKnown || !finite(playerPosition(w.Self)) {
		s.state.Reason = "movement_current_map_observation_unavailable"
		return false
	}
	changed := snapshotErr == nil && s.posKnown && (w.Self.MapId != s.posMap || w.Self.PositionRevision != s.positionRevision)
	if changed {
		s.refreshPositionLocked(w)
		s.followNav.Reset()
		s.meleeNav.Reset()
		if moving {
			s.state.Reason = "movement_map_or_authoritative_position_changed"
			return false
		}
		p = s.pos
	}
	missionMovement := moving && s.fargodeep.active
	if missionMovement && !s.fargodeepMovementAllowedLocked(ctx) {
		s.state.Reason = "fargodeep_movement_observations_changed"
		return false
	}
	if moving && s.thermaplugg.approaching && !s.thermapluggMovementAllowedLocked(ctx, w) {
		s.state.Reason = "thermaplugg_button_approach_changed"
		return false
	}
	if moving && s.targetMovementExecuting && (!s.matchesCurrentEncounterVisitLocked(w) || !s.admitsPolicyEncounterLocked(ctx, w)) {
		s.state.Reason = "target_movement_visit_changed"
		return false
	}
	if moving && s.followGoalExecuting != nil &&
		(!s.currentFollowFormationGoalLocked(*s.followGoalExecuting, w) || safeFollowPlayers(w, s.selfGUID, s.navDataDir != "") != "") {
		s.state.Reason = "follow_formation_goal_changed_before_movement"
		return false
	}
	if moving && s.followPortal.movementInProgress {
		if !s.portalMovementAllowedLocked(w) {
			s.state.Reason = "follow_instance_source_changed"
			return false
		}
		// Policy is read through the host. Recheck physical authority after
		// that callback without making another policy call before the write.
		latest, err := s.snapshot(ctx)
		if err != nil || !s.portalMovementCurrentLocked(latest) ||
			latest.GetSelf().GetPositionRevision() != w.GetSelf().GetPositionRevision() || latest.GetSelf().GetMapId() != w.GetSelf().GetMapId() ||
			playerPosition(latest.GetSelf()) != playerPosition(w.GetSelf()) {
			s.state.Reason = "follow_instance_source_changed"
			return false
		}
		w = latest
	}
	if moving && !s.targetMovementExecuting && !missionMovement && !s.independent && !s.state.Practicing && s.state.InteractionOwner == "" && (s.state.Following || s.state.Assisting) && (w.Owner == nil || !w.Owner.Present || !w.Owner.MapKnown || w.Owner.MapId != w.Self.MapId) && !s.followPortal.movementInProgress {
		s.state.Reason = "movement_owner_map_changed"
		return false
	}
	// Clock and observation callbacks may consume the remainder of a lease.
	// Expiry revokes travel writes immediately; the old movement still owes STOP.
	if moving && s.state.InteractionOwner != "" && !s.now().Before(s.interactionUntil) {
		s.state.Reason = "movement_interaction_lease_expired"
		return false
	}
	if moving && s.recoveryOwnsLocked() && (snapshotErr != nil || !s.recovery.state.Active || !ghostSelf(w, s.selfGUID)) {
		s.state.Reason = "recovery_ghost_changed_before_movement"
		return false
	}
	if moving && s.state.InteractionHarvest {
		reason := ""
		if s.now().Sub(s.harvestGuardAt) < 0 || s.now().Sub(s.harvestGuardAt) > time.Second {
			reason = "harvest_party_proof_expired_before_movement"
		} else {
			reason = harvestguard.WorldReason(w, s.selfGUID, s.state.HarvestGuardGuids)
		}
		if reason != "" {
			s.revokeHarvestLocked(ctx, reason)
			return false
		}
	}
	if moving && swim != nil && (!s.state.Following || !s.now().Before(s.followUntil) || safePlayerMovement(w, s.selfGUID, true) != "") {
		s.state.Reason = "swim_follow_observation_changed"
		return false
	}
	// Refuse a stale faster checkpoint after clock/World callbacks. STOP remains
	// available; no packet was dispatched by this failed mechanical check.
	if moving && !navigationSpeedAllowed(ctx, w, p) {
		s.state.Reason = "exploration_observed_run_speed_changed"
		return false
	}
	if moving && swim == nil && !s.recoveryOwnsLocked() && (s.swimming || !w.Self.MovementFlagsKnown || !movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags)) {
		s.state.Reason = "movement_current_ground_mode_unsupported"
		return false
	}
	if moving && swim == nil && !s.recoveryOwnsLocked() && w.GetSelf().GetAliveKnown() && w.GetSelf().GetAlive() {
		if points := s.currentMovementPointsLocked(); len(points) != 0 {
			leg := p
			if distance(s.pos, leg) < .001 {
				leg.x += float32(math.Cos(float64(p.o)) * .25)
				leg.y += float32(math.Sin(float64(p.o)) * .25)
			}
			proof, _ := ctx.Value(movementPointsEscapePathKey{}).(*movementPointsEscapePath)
			if !pointPathClear([]position{s.pos, leg}, points) && !s.movementPointEscapePrefix(proof, leg) {
				s.movementPoints.reason = "next_leg_blocked"
				s.state.Reason = "movement_points_next_leg_blocked"
				return false
			}
		}
	}
	var passive uint32
	if snapshotErr == nil && w.Self.AliveKnown && w.Self.Alive && w.Self.MovementFlagsKnown {
		passive = w.Self.MovementFlags & movementflags.PassiveModifiers
	}
	if !moving && op == opcode.MSGMoveStop && s.state.InteractionOwner == "exploration" && s.idleInteraction.token != "" && s.idleInteraction.token == s.interactionToken {
		_, portalStop := ctx.Value(portalStopGuardKey{}).(portalStopAdmission)
		if !portalStop {
			if scope, ok := ctx.Value(idleMotionScopeKey{}).(*idleMotionScope); ok && scope != nil {
				cleanup := *scope
				cleanup.cleanupOnly = true
				ctx = context.WithValue(ctx, idleMotionScopeKey{}, &cleanup)
			} else {
				if snapshotErr != nil || p != s.pos {
					return false
				}
				scope, err := s.newIdleMotionScopeLocked(ctx, s.interactionToken, w, true)
				if err != nil {
					s.state.Reason = "idle_cleanup_source_unavailable"
					return false
				}
				ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
			}
		}
	}
	var payload []byte
	isSwimming := swim != nil && swim.wet
	if !moving && op == opcode.MSGMoveStop {
		// STOP uses the actual medium at the rebased position after a correction.
		isSwimming = s.swimming
	}
	if isSwimming {
		flags := passive | swimMovementFlag
		var pitch float32
		if moving {
			flags |= swim.flags
			pitch = swim.pitch
		}
		payload = swimMovementPacket(s.selfGUID, clock.ClientTimeMs, p, pitch, flags)
		if payload == nil {
			s.state.Reason = "swim_movement_packet_invalid"
			return false
		}
	} else {
		payload = movementPacket(s.selfGUID, clock.ClientTimeMs, p, moving)
		at := len(packedGUID(s.selfGUID))
		flags := binary.LittleEndian.Uint32(payload[at:]) | passive
		binary.LittleEndian.PutUint32(payload[at:], flags)
	}
	if snapshotErr == nil && w.Self.AliveKnown && !w.Self.Alive && w.Self.PlayerFlags&ghostFlag != 0 {
		if moving && (!s.recoveryOwnsLocked() || !ghostSelf(w, s.selfGUID)) {
			s.state.Reason = "ghost_movement_requires_recovery_custody"
			return false
		}
		// Water-walking has no optional MovementInfo tail. Preserve only this
		// actually observed ghost flag; never invent swimming/flying flags.
		at := len(packedGUID(s.selfGUID))
		flags := binary.LittleEndian.Uint32(payload[at:]) | w.Self.MovementFlags&ghostWaterWalking
		binary.LittleEndian.PutUint32(payload[at:], flags)
	}
	// A portal entry may owe a fresh normal STOP even while already idle. Its
	// immutable proposal must survive the clock and World callbacks above.
	var scopes []*modv1.TransportScope
	if admission, ok := ctx.Value(portalStopGuardKey{}).(portalStopAdmission); ok {
		if moving || op != opcode.MSGMoveStop || admission.scope == nil || admission.guard == nil || !admission.guard(w, p) {
			s.state.Reason = "follow_instance_source_changed_before_stop"
			return false
		}
		scopes = []*modv1.TransportScope{admission.scope}
	}
	var attempt *ordinaryMovementAttempt
	// Only ordinary follow uses this recovery; leased/finite operations retain
	// their existing receipt and cleanup custody.
	if ctx.Value(ordinaryFollowMovementKey{}) == true && len(scopes) == 0 && !s.bodyMoving && swim == nil && ctx.Value(idleMotionScopeKey{}) == nil && s.state.Following && s.state.InteractionOwner == "" {
		attempt = s.newOrdinaryMovementAttemptLocked(w, op, payload, p, moving, isSwimming)
		if attempt != nil {
			ctx = modcb.WithMovementAttempt(ctx, attempt.id)
		}
	}
	// Refuse before callback entry while retaining the prior physical state and
	// receipt. Errors from a callback already entered keep UNKNOWN handling.
	if ctx.Value(ordinaryFollowMovementKey{}) == true && ctx.Err() != nil {
		s.state.Reason = "ordinary_follow_context_ended_before_packet"
		return false
	}
	before := s.transportSequence
	if s.sendPacketLocked(ctx, op, payload, scopes...) != nil {
		if failure := s.state.LastTransportFailure; failure != nil && failure.Sequence > before && !failure.OutcomeUnknown {
			s.state.Reason = "movement_not_dispatched"
			return false
		}
		s.state.MotionUnconfirmed = true
		if attempt != nil {
			attempt.sequence = s.transportSequence
			s.ordinaryMovement = attempt
		}
		s.publishClientMovementLocked(ctx, nil, p, false)
		return false
	}
	s.pos = p
	s.swimming = isSwimming
	s.state.Moving = moving
	s.posEstimated = true
	s.posAt = s.now()
	s.posRevision++
	s.publishClientMovementLocked(ctx, w, p, snapshotErr == nil)
	if snapshotErr != nil {
		s.state.MotionUnconfirmed = true
		return false
	}
	return true
}

func (s *Service) stopLocked(ctx context.Context) bool {
	if s.bodyMoving {
		// The finite input already owns its landing and terminal STOP. A
		// competing request has not written anything and cannot make it unknown.
		s.state.Reason = "bounded_body_input_active"
		return false
	}
	if s.idleInteraction.token != "" && s.state.MotionUnconfirmed {
		s.observeIdleMotionStoppedLocked(ctx)
	}
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
	if !written && !s.state.MotionUnconfirmed {
		// Validation/clock rejection occurred before SendPacket. The previous
		// motion remains known and still owes a STOP; preserve standing intent
		// so a fresh ordinary tick can retry that undispatched stop.
		s.state.Moving = true
	}
	// An actual failed write is already fenced by MotionUnconfirmed. Preserve
	// standing goals and the separately observed attack; do not issue another
	// packet merely because the stop's outcome is unknown.
	return written
}

func (s *Service) explicitStopLocked(ctx context.Context) bool {
	if s.bodyMoving {
		return s.stopLocked(ctx)
	}
	priorUnknown := s.state.MotionUnconfirmed
	if priorUnknown {
		// A fresh user stop/logout command authorizes one new stop attempt.
		s.state.MotionUnconfirmed = false
		s.state.Moving = true
	}
	stopped := s.stopLocked(ctx)
	if !stopped && priorUnknown {
		s.state.MotionUnconfirmed = true
	}
	return stopped
}

func (s *Service) Tick(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireMovementOwnerLocked()
	s.observeHostWeaponMetadataLocked(ctx)
	if s.bodyMoving {
		return
	}
	if s.dungeonTraceEnabled() {
		defer s.sampleDungeonTraceLocked(ctx)
	}
	s.buffTimeoutLocked()
	s.cancelProtectedCastLocked(ctx)
	s.expireSelfChannelLocked()
	s.expireExplicitCastLocked()
	s.expireMeleeOperationLocked()
	if !s.closed && !s.hostEncounterActiveLocked() {
		s.stopEncounterMeleeLocked(ctx)
	}
	if !s.closed && s.autoTarget != 0 {
		s.blockProtectedOffenseLocked(ctx, s.autoTarget)
	}
	s.expireCrowdControlLocked()
	if s.pendingCrowdControlLocked() != nil {
		return
	}
	if op := s.itemCreationPendingLocked(); op != nil {
		var w *worldv1.SnapshotResponse
		if op.view.ServerCastGo && op.view.ItemPushObserved {
			w, _ = s.snapshot(ctx)
		}
		s.observeItemCreationLocked(w)
	}
	if op := s.supportOp; op != nil && (op.view.Phase == "waiting_server" || op.view.Phase == "waiting_cleanse_effect") && !s.now().Before(op.until) {
		op.view.Phase = "outcome_unconfirmed_no_retry"
		if s.pendingSpell == op.view.SpellId && s.castCount == op.count {
			s.state.CastPending = false
			s.pendingSpell = 0
			s.pendingTarget = 0
		}
	}
	if op := s.supportOp; op != nil && op.owner != 0 && op.view.Capability == string(spellcatalog.Stealth) &&
		!s.now().Before(op.until) && op.view.Phase != "server_rejected_no_replay" && (!op.view.ServerCastGo || !op.view.EffectObserved) {
		s.starterSpells.uncertainStrike = op
	}
	if !s.closed && s.recoveryTickLocked(ctx) {
		if s.fargodeep.active {
			s.fargodeep.active, s.fargodeep.phase, s.fargodeep.route = false, "death_recovery_has_priority", nil
		}
		s.lastStep = s.now()
		return
	}
	formationUsed := false
	defer func() {
		// Combat, casts, interactions and corrections can change our position.
		// A later follow must plan from that new position rather than reuse an
		// earlier formation route through an unrelated action.
		if !formationUsed {
			s.followNav.Reset()
		}
	}()
	if s.controlRecoveryLocked(ctx) {
		return
	}
	s.expirePendingCastLocked(ctx)
	if s.encounterSwing.target != 0 {
		s.lastStep = s.now()
		return
	}
	if s.state.InteractionHarvest && s.state.InteractionOwner != "" {
		_, guids, reason := s.harvestContextLocked(ctx)
		if !s.now().Before(s.interactionUntil) {
			reason = "harvest_interaction_lease_expired"
		}
		if reason != "" {
			if !s.revokeHarvestLocked(ctx, reason) {
				return
			}
		} else {
			s.state.HarvestGuardGuids, s.harvestGuardAt = guids, s.now()
		}
	}
	if s.state.InteractionOwner != "" && !s.now().Before(s.interactionUntil) {
		s.stopLocked(ctx)
	}
	s.expireInteractionLocked()
	if s.state.InteractionOwner != "" {
		if s.state.Practicing && !s.now().Before(s.practiceUntil) {
			s.state.Practicing = false
			s.state.PracticePhase = "goal_expired"
		}
		s.lastStep = s.now()
		return
	}
	if s.closed {
		return
	}
	// Lua remains the target policy across short stalls, reloads and lease
	// expiry. Expiry fences its writes; it must not install a different target
	// selector. Support and movement retain their separate ownership contracts.
	nativeTargetPolicy := s.hostEncounterKey == ""
	if s.fargodeep.active && (!s.state.Assisting || !s.now().Before(s.assistUntil) || !s.now().Before(s.fargodeep.until)) {
		s.stopFargodeepLocked(ctx, "authorization_expired")
	}
	if nativeTargetPolicy && s.state.Practicing {
		s.practiceTickLocked(ctx)
		return
	}
	if s.state.MotionUnconfirmed {
		// A new authoritative stationary pose can settle an ambiguous stop.
		if world, err := s.snapshot(ctx); err == nil && safeSelfCombat(world, s.selfGUID) == "" {
			s.refreshPositionLocked(world)
			if s.state.MotionUnconfirmed {
				s.reconcileOrdinaryMovementLocked(ctx, world)
			}
		}
	}
	if s.state.MotionUnconfirmed {
		// Uncertain motion pauses physical writes, not the Lua encounter or
		// standing intents. A fresh stopped pose above resumes the same plan.
		s.state.Reason = "motion_unconfirmed_waiting_for_fresh_pose"
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
	if s.state.Assisting && !now.Before(s.assistUntil) {
		s.state.Assisting = false
		s.state.Reason = "legacy_assist_intent_expired_lua_defense_unchanged"
	}

	snapshot, err := s.snapshot(ctx)
	if err != nil {
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "observations_unavailable"
		s.state.AssistReason = s.state.Reason
		return
	}
	// A missing owner cannot block this body's already-authorized healing or
	// defense. In-flight casts and exclusive interactions retain priority.
	s.observeDungeonPortalLocked(snapshot, now)
	if s.state.CastPending {
		s.stopLocked(ctx)
		s.lastStep = now
		return
	}

	if nativeTargetPolicy && s.fargodeepTickLocked(ctx, snapshot) {
		s.lastStep = now
		return
	}
	if s.thermapluggButtonsTickLocked(ctx, snapshot) {
		s.lastStep = now
		return
	}
	// A prepared approach yields to standing support and actual defense. An
	// already dispatched CC cast is settled above before any new action.
	// A fresh exact Lua movement request owns its route. Standing support above
	// may still act; native formation must not steer back toward the owner.
	if s.targetMovementActiveLocked() {
		s.lastStep = now
		return
	}
	if s.movementOwner.owner != "" {
		// Explicit Lua movement and standing support/defense have already had
		// their turn above. Only the competing idle/follow writer yields.
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "finite_movement_owner_active"
		return
	}
	if s.followDungeonPortalLocked(ctx, snapshot, now) {
		formationUsed = true
		return
	}
	ownedFollow := s.currentFollowFormationGoalLocked(s.followGoal, snapshot)
	following := s.state.Following || ownedFollow
	swimFollow := s.state.Following && s.navDataDir != "" && (s.swimming ||
		snapshot.GetSelf().GetMovementFlags()&swimMovementFlag != 0 ||
		snapshot.GetOwner().GetMovementFlags()&swimMovementFlag != 0 ||
		len(s.swimFollow.path) != 0)
	reason := ""
	if swimFollow {
		reason = safePlayerMovement(snapshot, s.selfGUID, true)
	} else {
		reason = safeFollowPlayers(snapshot, s.selfGUID, following && s.navDataDir != "")
	}
	if reason != "" {
		// Unknown or temporarily unsafe observations pause enabled intent. A
		// normal jump, AFK or visibility loss must not silently revoke it.
		// No local motion is attempted until fresh safe context is available.
		s.stopLocked(ctx)
		if reason == "player_identity_mismatch" {
			s.state.Following = false
			s.state.Assisting = false
		}
		s.lastStep = now
		s.state.Reason = reason
		s.state.AssistReason = reason
		return
	}
	if !s.refreshPositionLocked(snapshot) {
		s.stopLocked(ctx)
		s.lastStep = now
		return
	}
	if s.state.MotionUnconfirmed {
		s.state.Reason = "motion_unconfirmed_waiting_for_fresh_pose"
		return
	}
	if s.state.Following && !ownedFollow && s.navDataDir != "" {
		// Observe travel heading even while this actor is casting or assisting,
		// so each role keeps the same formation frame after combat ends.
		classID := uint32(0)
		if snapshot.Self.ClassKnown {
			classID = snapshot.Self.ClassId
		}
		if !s.formation.Target(now, s.selfGUID, classID, playerPosition(snapshot.Owner), snapshot.Owner.MovementFlags&0x3f != 0, snapshot.Owner.PlayerFlags).Valid {
			s.stopLocked(ctx)
			s.lastStep = now
			s.state.Reason = "formation_position_invalid"
			return
		}
	}
	if s.state.AutoAttackUnconfirmed {
		// A failed assist gate may just have requested ATTACKSTOP. Do not
		// fall through to a new follow movement in that same controller tick.
		s.stopLocked(ctx)
		s.lastStep = now
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return
	}
	if !following {
		// The prior movement can belong to a rogue approach even when the
		// separate Follow intent has never been enabled.
		s.stopLocked(ctx)
		s.lastStep = now
		return
	}
	if ownedFollow {
		goal := s.followGoal
		s.followGoalExecuting = &goal
		defer func() { s.followGoalExecuting = nil }()
	}
	if !ownedFollow && !swimFollow {
		ctx = context.WithValue(ctx, ordinaryFollowMovementKey{}, true)
	}
	if s.navDataDir != "" {
		formationUsed = true
		if swimFollow {
			s.swimFollowTickLocked(ctx, playerPosition(snapshot.Owner), now)
		} else {
			s.followFormationLocked(ctx, snapshot, now)
		}
		return
	}
	s.nearOwnerFollowLocked(ctx, snapshot, now)

}

func (s *Service) OnPacket(ctx context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if packet == nil {
		return &emptypb.Empty{}, nil
	}
	if op := s.supportOp; op != nil && op.policySpellToken != "" {
		defer func() {
			op.reportPolicySpellEffect(ctx, s.supportOp == op && s.state.CastPending)
		}()
	}
	if op := s.buffOp; op != nil && op.policySpellToken != "" {
		defer op.reportPolicySpellEffect(ctx)
	}
	if op := s.meleeOp; op != nil && op.token != "" {
		defer op.reportPolicyMeleeEffect(ctx)
	}
	if s.dungeonTraceEnabled() {
		s.traceOwnerPacket(packet.Opcode, packet.Payload, s.now())
	}
	p := packet.Payload
	if handled, err := s.observeComboPacket(packet.Opcode, p); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	s.expireMeleeOperationLocked()
	s.meleeWorldBoundaryLocked(packet.Opcode, p)
	s.observeEncounterVisitLocked(packet.Opcode, p)
	if handled, err := s.flightpathActivationPacketLocked(packet.Opcode, p, packet.TransportScope); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	if handled, err := s.flightpathPacketLocked(packet.Opcode, p, packet.TransportScope); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	s.expireCrowdControlLocked()
	if packet.Opcode == opcode.SMSGCreatureQueryResponse {
		s.observeControlTemplateLocked(p)
		return &emptypb.Empty{}, nil
	}
	malformed := false
	if handled, err := s.explicitCastPacketLocked(packet.Opcode, p); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	if handled, err := s.supportPacketLocked(packet.Opcode, p); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	if handled, err := s.auraPacketLocked(packet.Opcode, p); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	if handled, err := s.buffCastPacketLocked(packet.Opcode, p); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	if handled, err := s.recoveryPacketLocked(packet.Opcode, p); handled {
		if err != nil {
			s.state.MalformedPackets++
		}
		return &emptypb.Empty{}, nil
	}
	switch packet.Opcode {
	case opcode.SMSGTalentsInfo:
		if err := s.observePlayerTalentsLocked(p); err != nil {
			s.state.MalformedPackets++
		}
	case opcode.SMSGClientControlUpdate:
		malformed = !s.clientControlPacketLocked(p)
	case opcode.SMSGPartykilllog:
		if len(p) != 16 {
			malformed = true
		} else if s.state.Practicing && s.practicePullStarted && !s.practiceKillCredited && binary.LittleEndian.Uint64(p) == s.selfGUID && binary.LittleEndian.Uint64(p[8:]) == s.practiceTarget {
			s.state.PracticeKills++
			s.practiceKillCredited = true
		}
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
			} else if s.acceptOwnMeleeAttackLocked(target, true) {
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
		leasedSwing := s.encounterSwing.target != 0
		if attacker == s.selfGUID && s.acceptOwnMeleeAttackLocked(target, false) {
			if !leasedSwing && !s.autoStopping && dead == 0 && s.autoTarget != 0 && !s.clientControlLost && !s.clientControlRestoring {
				s.failedAssistTarget = s.autoTarget
				s.failedAssistCastTarget = 0
			}
			s.autoTarget = 0
			s.autoStopping = false
			s.state.AutoAttacking = false
			s.state.AutoAttackTargetGuid = 0
			s.state.AutoAttackRequested = false
			s.state.AutoAttackUnconfirmed = false
		}
	case opcode.SMSGSpellnonmeleedamagelog:
		caster, target, spell, damage, err := decodeSpellDamage(p)
		if err != nil {
			malformed = true
		} else if caster == s.selfGUID {
			s.offensiveDamageLocked(spell, damage)
			s.observeOffenseDamageLocked(target, spell, damage)
		}
	case opcode.SMSGInitialSpells:
		known, err := completeSpellbook(p)
		if err != nil {
			malformed = true
		} else {
			s.applySpellbookLocked(known)
			s.observeInitialCooldownsLocked(p)
		}
	case opcode.SMSGLearnedSpell, opcode.SMSGRemovedSpell, opcode.SMSGSupercededSpell:
		remove, add, err := spellKnowledgePacket(packet.Opcode, p)
		if err != nil {
			malformed = true
		} else if add != 0 && !s.knownSpells[add] && len(s.knownSpells) >= 4096 && (remove == 0 || !s.knownSpells[remove]) {
			malformed = true
		} else {
			if remove != 0 {
				s.setKnownSpell(remove, false)
			}
			if add != 0 {
				s.setKnownSpell(add, true)
			}
		}
	case opcode.SMSGCastFailed:
		if len(p) < 6 {
			malformed = true
		} else if s.state.CastPending && s.pendingSpell != 0 && binary.LittleEndian.Uint32(p[1:]) == s.pendingSpell && p[0] == s.castCount {
			s.castFailedLocked(uint32(p[5]), "server_cast_failed_healing_paused")
		}
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		guid, count, spell, offset, err := castEvent(p)
		if err != nil {
			malformed = true
		} else if s.state.CastPending && guid == s.selfGUID && spell == s.pendingSpell && count == s.castCount {
			if !validCastTargetBody(p, offset, packet.Opcode == opcode.SMSGSpellGo, s.pendingTarget, s.implicitSelfSpellLocked(spell, s.pendingTarget)) {
				malformed = true
			} else if packet.Opcode == opcode.SMSGSpellStart {
				if s.spellKindLocked(spell) == spellcatalog.Heal {
					s.state.ServerCastStarts++
				} else {
					s.state.ServerAssistStarts++
				}
				s.state.Reason = "server_cast_started"
			} else {
				if s.spellKindLocked(spell) == spellcatalog.Heal {
					s.state.ServerCastGo++
					s.state.Reason = "server_spell_go_health_effect_unverified"
				} else if s.spellKindLocked(spell) == spellcatalog.Interrupt {
					s.state.Reason = "interrupt_cast_confirmed"
				} else {
					s.state.ServerAssistGo++
					if spellFamily(spell, smite) {
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
			s.castFailedLocked(uint32(p[n+5]), "server_cast_interrupted_healing_paused")
		}
	}
	if malformed {
		s.state.MalformedPackets++
	}
	return &emptypb.Empty{}, nil // Packet-handler errors would kill the core session.
}
