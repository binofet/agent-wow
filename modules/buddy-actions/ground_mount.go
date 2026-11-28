package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/channelstate"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// A speed buff or mounted flag alone does not identify a ground mount.
func (s *Service) groundMountAuraLocked(guid uint64) (uint32, uint64) {
	set := s.buffAuras[guid]
	if set == nil {
		return 0, 0
	}
	for _, a := range set.slots {
		if a.spell != 0 && a.caster == guid && a.positive && !a.negative &&
			(!a.durationKnown || a.remaining(s.now()) > 0) && s.spellCatalogLocked().GroundMountSpeed(a.spell) > 0 {
			return a.spell, a.revision
		}
	}
	return 0, 0
}

func (s *Service) groundTravelUnitFlags(p *worldv1.PlayerState) uint32 {
	flags := p.GetUnitFlags()
	if flags&movementflags.Mounted == 0 || !p.GetUnitFlagsKnown() || !p.GetMovementFlagsKnown() ||
		!movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) ||
		p.UnitFlags_2&movementflags.ForcedOrFeign != 0 || observedRunSpeed(p) == 0 {
		return flags
	}
	if id, _ := s.groundMountAuraLocked(p.Guid); id != 0 {
		return flags &^ movementflags.Mounted
	}
	return flags
}

// A revision represents an observed change of the owner's actual mount, not an
// unchanged aura refresh. Each follower retains its last physical attempt.
type followMountState struct {
	visit, follow, revision  uint64
	ownerSpell, releaseSpell uint32
	ownerKnown, dismounted   bool
	last                     *followMountAttempt
}
type followMountAttempt struct {
	visit, revision, baseline uint64
	spell                     uint32
	dismount, written, effect bool
	phase                     string
	cast                      *supportAttempt
}

func (s *Service) observedMountLocked(p *worldv1.PlayerState) (*bool, uint32, uint64) {
	if !p.GetPresent() || !p.GetUnitFlagsKnown() {
		return nil, 0, 0
	}
	id, rev := s.groundMountAuraLocked(p.Guid)
	return proto.Bool(p.UnitFlags&movementflags.Mounted != 0), id, rev
}

func (s *Service) followMountObservationLocked(ctx context.Context, w *worldv1.SnapshotResponse) *actionsv1.FollowMountObservation {
	me, owner := w.GetSelf(), w.GetOwner()
	v := &actionsv1.FollowMountObservation{}
	st := &s.followMount
	if !s.matchesCurrentEncounterVisitLocked(w) || !me.GetPresent() || me.Guid != s.selfGUID {
		return v
	}
	own, ownSpell, ownRevision := s.observedMountLocked(me)
	v.SelfMounted, v.SelfGroundSpell = own, ownSpell
	if speed := observedRunSpeed(me); speed > 0 {
		v.SelfRunSpeed = proto.Float32(float32(speed))
	}
	if st.visit != w.VisitRevision || st.follow != s.followRevision {
		st.visit, st.follow, st.ownerKnown, st.dismounted = w.VisitRevision, s.followRevision, false, false
		st.releaseSpell = 0
	}
	if op := st.last; op != nil {
		if op.visit == w.VisitRevision && me.GetAliveKnown() && me.GetAlive() && own != nil && !op.effect {
			if op.cast != nil && op.cast.view.Phase == "server_rejected_no_replay" {
				op.phase = "server_rejected_no_replay"
			}
			set := s.buffAuras[me.Guid]
			changed := !op.dismount && *own && ownSpell == op.spell && ownRevision > op.baseline ||
				op.dismount && !*own && ownSpell == 0 && set != nil && set.revision > op.baseline
			if changed && (op.phase == "waiting_for_aura" && op.written || op.phase == "transport_unknown") {
				// An ambiguous write followed by a compatible state permits a fresh
				// opposite choice but does not establish that our write produced it.
				op.effect = op.written
				op.phase = "observed_ground_mount_state"
				if op.dismount && op.cast != nil {
					op.cast.view.Phase = op.phase
					op.cast.view.EffectObserved = op.effect
				}
			}
		}
		v.LastPhase, v.TransportWritten, v.EffectObserved = op.phase, op.written, op.effect
	}
	if s.independent || (!s.state.Following || !s.now().Before(s.followUntil)) && (s.followRevision != 0 || !s.currentFollowFormationGoalLocked(s.followGoal, w)) ||
		owner.GetGuid() != ownerGUID || !owner.GetPresent() || !owner.GetMapKnown() || !me.GetMapKnown() || owner.MapId != me.MapId {
		return v
	}
	theirs, ownerSpell, _ := s.observedMountLocked(owner)
	v.OwnerMounted, v.OwnerGroundSpell = theirs, ownerSpell
	if speed := observedRunSpeed(owner); speed > 0 {
		v.OwnerRunSpeed = proto.Float32(float32(speed))
	}
	if theirs == nil || *theirs != (ownerSpell != 0) || owner.GetPositionInferred() ||
		!owner.GetAliveKnown() || !owner.GetAlive() || !owner.GetMovementFlagsKnown() ||
		!movementflags.GroundCompatible(owner.MovementFlags, owner.ExtraMovementFlags) ||
		owner.UnitFlags_2&movementflags.ForcedOrFeign != 0 || s.groundTravelUnitFlags(owner)&movementflags.UnsafeUnitFlags != 0 {
		return v
	}
	if own == nil || !*own || ownSpell != st.releaseSpell {
		st.releaseSpell = 0
	}
	if !st.ownerKnown || st.ownerSpell != ownerSpell {
		st.dismounted = st.ownerKnown && st.ownerSpell != 0 && ownerSpell == 0
		st.releaseSpell = 0
		if st.dismounted && own != nil && *own {
			st.releaseSpell = ownSpell
		}
		st.ownerSpell, st.ownerKnown = ownerSpell, true
		st.revision++
	}
	v.OwnerMountRevision, v.OwnerDismounted = st.revision, st.dismounted
	if own == nil || *own != (ownSpell != 0) || ownerSpell == 0 && (st.releaseSpell == 0 || st.releaseSpell != ownSpell) {
		return v
	}
	if op := st.last; op != nil {
		v.AttemptedForOwnerState = op.visit == st.visit && op.revision == st.revision
		if op.phase == "transport_unknown" {
			return v
		}
		if op.phase == "waiting_for_aura" {
			// A completed, known write may still receive its aura update.
			// Only a separate witnessed owner transition can select a new attempt.
			completed := op.written && (op.dismount || op.cast != nil && op.cast.view.Phase == "server_cast_go_effect_unverified")
			if v.AttemptedForOwnerState || !completed {
				return v
			}
		}
	}
	if len(w.NearbyCreatures) > 32 || s.hasObservedSelfAttackerLocked(w) || me.GetPositionInferred() || s.state.MotionUnconfirmed || s.traversalOccupiedForMountLocked() || s.clientControlLost || s.clientControlRestoring ||
		(me.UnitFlags|owner.UnitFlags)&0x80000 != 0 || (me.UnitFlags_2|owner.UnitFlags_2)&movementflags.ForcedOrFeign != 0 ||
		safePlayerMovementFlags(w, s.selfGUID, movementflags.GroundCompatible, movementflags.GroundCompatible, s.groundTravelUnitFlags) != "" ||
		v.SelfRunSpeed == nil || v.OwnerRunSpeed == nil || !me.HealthKnown || me.Health == 0 || !channelstate.Idle(me) {
		return v
	}
	s.expireMovementOwnerLocked()
	if s.movementOwner.owner != "" && s.movementOwner.owner != "ground_mount_follow" {
		return v
	}
	v.FollowMoving = s.state.Moving
	// Raw movement on a stationary client requires a genuinely accepted STOP,
	// not merely passage of time or an origin inferred by another player.
	if me.MovementFlags&0xff != 0 && !s.state.Moving {
		stop, err := modcb.ReadSettledBodySource(ctx, s.session, s.sessionID, me.Guid)
		if err != nil || stop.GetOpcode() != opcode.MSGMoveStop || !samePortalCoreVisit(stop.GetTransportScope(), portalObservedWorldScope(w, me.Guid)) {
			return v
		}
		info, ok := movementinfo.OwnFrame(stop.Payload, me.Guid)
		if !ok || info.Flags != 0 || info.ExtraFlags != 0 || info.FallTime != 0 || !s.posKnown || s.posMap != me.MapId ||
			s.positionRevision != me.PositionRevision || (position{info.X, info.Y, info.Z, info.Orientation}) != s.pos {
			return v
		}
	}
	v.Ready = true
	return v
}

func (s *Service) traversalOccupiedForMountLocked() bool {
	return s.state.CastPending || s.state.Traveling || s.bodyMoving || s.questApproaching || s.state.InteractionOwner != "" ||
		s.state.AutoAttacking || s.state.AutoAttackRequested || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 ||
		s.recovery.state.Active || s.targetMovementActiveLocked() || s.state.Practicing || s.fargodeep.active ||
		s.flightpathActivation.operation.GetPending() || s.flightpath.operation.GetPending()
}
func (s *Service) admitFollowMountLocked(ctx context.Context, w *worldv1.SnapshotResponse, guard *actionsv1.FollowMountGuard, spell uint32, dismount, preparing bool) error {
	// The physical mount, dismount and preparatory STOP use the same current
	// own movement evidence as the observation that selected this transition.
	w = s.surfaceMovementWorldLocked(ctx, w)
	v := s.followMountObservationLocked(ctx, w)
	if guard == nil || guard.OwnerGuid != w.GetOwner().GetGuid() || guard.FollowRevision != s.followRevision || guard.OwnerMountRevision == 0 ||
		guard.OwnerMountRevision != v.OwnerMountRevision || !v.Ready || v.FollowMoving != preparing || v.AttemptedForOwnerState ||
		s.spellCatalogLocked().GroundMountSpeed(spell) == 0 {
		return status.Error(codes.FailedPrecondition, "current free stationary ground body and followed mount transition required")
	}
	if dismount {
		if v.GetOwnerMounted() || !v.OwnerDismounted || !v.GetSelfMounted() || v.SelfGroundSpell != spell {
			return status.Error(codes.FailedPrecondition, "observed owner dismount and exact own ground aura required")
		}
	} else if !s.spellbookKnown || !s.knownSpells[spell] || v.GetSelfMounted() || !v.GetOwnerMounted() || v.OwnerGroundSpell == 0 {
		return status.Error(codes.FailedPrecondition, "observed owner's ground mount and unmounted self required")
	}
	return nil
}
func (s *Service) startFollowMountLocked(spell uint32, dismount bool) *followMountAttempt {
	op := &followMountAttempt{visit: s.followMount.visit, revision: s.followMount.revision, baseline: s.buffAuraRevision, spell: spell, dismount: dismount, phase: "waiting_for_aura"}
	s.followMount.last = op
	return op
}

func (s *Service) DismountGround(ctx context.Context, r *actionsv1.DismountGroundRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetSpellId() == 0 || r.GetFollowMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "exact body, visit, ground mount and follow revision required")
	}
	r = proto.Clone(r).(*actionsv1.DismountGroundRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	w, err := s.readWorldSnapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId || modcb.PolicySpellToken(ctx) == "" || !s.admitsPolicyEncounterLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "current body, visit and Lua writer required")
	}
	if err = s.admitFollowMountLocked(ctx, w, r.FollowMount, r.SpellId, true, false); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	op := s.startFollowMountLocked(r.SpellId, true)
	op.cast = &supportAttempt{
		view:             actionsv1.SpellOperation{SpellId: r.SpellId, VisitId: r.VisitId, Capability: "dismount_ground", Phase: op.phase},
		policySpellToken: modcb.PolicySpellToken(ctx),
	}
	s.supportOp = op.cast
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGCancelMountAura, nil)
	op.written = err == nil
	op.cast.view.TransportWritten = op.written
	if modcb.PacketWasNotDispatched(err) {
		op.phase = "not_dispatched"
	} else if err != nil {
		op.phase = "transport_unknown"
	}
	return s.stateLocked(), err
}
