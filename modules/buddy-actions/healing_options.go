package buddyactions

import (
	"context"
	"sort"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// UseHostStandingHealing is selected at launch for a body whose native
// standing healer is replaced by the attached Lua session host.
func (s *Service) UseHostStandingHealing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostStandingHealing = true
}

func (s *Service) healCasterReadyLocked(w *worldv1.SnapshotResponse) string {
	if s.closed {
		return "session_closing"
	}
	self := w.GetSelf()
	if self.GetGuid() != s.selfGUID || s.selfGUID == 0 {
		return "player_identity_mismatch"
	}
	if !livingHealingPlayer(self, self.MapId) || self.UnitFlags&movementflags.UnsafeUnitFlags != 0 || self.UnitFlags_2&movementflags.ForcedOrFeign != 0 {
		return "unsafe_unit_state"
	}
	if !self.HealthKnown || self.Health == 0 || self.MaxHealth == 0 || self.Health > self.MaxHealth || !self.PowerKnown || self.PowerType != 0 {
		return "healing_caster_vitals_unknown"
	}
	if !s.spellbookKnown {
		return "healing_spellbook_unknown"
	}
	if s.clientControlLost || s.clientControlRestoring || s.bodyMoving || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed ||
		s.state.Moving && !observedHealPosition(self) ||
		s.state.InteractionOwner != "" || s.state.CastPending || s.now().Before(s.nextHeal) ||
		self.ChannelSpellId != 0 || self.ChannelObjectGuid != 0 || self.UnitFlags&0x2000 != 0 {
		return "healing_caster_busy"
	}
	return ""
}

func (s *Service) healRankAvailableLocked(self *worldv1.PlayerState, r spellcatalog.Spell) bool {
	// The ordinary movement writer can stop its own run before a cast. A
	// fresh external translation or fall still precludes a cast-time rank.
	const moving = 0xf | 0x1000 | 0x2000 | swimAscendingFlag | swimDescendingFlag
	if r.CastMs > 0 && !s.state.Moving && self.GetMovementFlagsKnown() && self.MovementFlags&moving != 0 &&
		self.PositionAgeMs <= 2000 && !(s.posEstimated && s.ownHealPositionLocked(self)) {
		return false
	}
	return r.ID != 0 && r.Capability().Kind == spellcatalog.Heal && r.Power == 0 && self.PowerKnown && self.PowerType == 0 &&
		uint64(self.Power) >= spellCost(r, self) && !s.spellOnCooldownLocked(r) && casterRequirementsObserved(r, self)
}

func observedHealPosition(p *worldv1.PlayerState) bool {
	return p.GetPositionKnown() && !p.PositionInferred && finite(playerPosition(p)) &&
		(p.PositionAgeMs <= 2000 || p.MovementFlagsKnown && swimFollowCompatible(p.MovementFlags, p.ExtraMovementFlags) &&
			p.MovementFlags&(0xff|swimAscendingFlag|swimDescendingFlag) == 0)
}

func (s *Service) ownHealPositionLocked(self *worldv1.PlayerState) bool {
	return self.GetPositionKnown() && !self.PositionInferred && finite(playerPosition(self)) && s.posKnown && finite(s.pos) &&
		s.posMap == self.MapId && s.positionRevision == self.PositionRevision && !s.state.MotionUnconfirmed
}

func (s *Service) healDistanceLocked(self, patient *worldv1.PlayerState) *float64 {
	if patient.Guid == self.Guid {
		return proto.Float64(0)
	}
	if !observedHealPosition(patient) || s.state.MotionUnconfirmed {
		return nil
	}
	caster := playerPosition(self)
	if s.ownHealPositionLocked(self) && (s.posEstimated || observedHealPosition(self)) {
		caster = s.pos
	} else if !observedHealPosition(self) {
		return nil
	}
	return proto.Float64(distance(caster, playerPosition(patient)))
}

func (s *Service) healingVisitCurrentLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if s.encounterVisit.revision != 0 && !s.matchesCurrentEncounterVisitLocked(w) {
		return false
	}
	return modcb.IncomingPolicyEncounterVisit(ctx) == 0 || s.hasPolicyEncounterAccessLocked(ctx) && s.matchesCurrentEncounterVisitLocked(w)
}

func (s *Service) healRankEligibleLocked(self, patient *worldv1.PlayerState, r spellcatalog.Spell) bool {
	if !s.healRankAvailableLocked(self, r) {
		return false
	}
	if patient.Guid == s.selfGUID {
		return true
	}
	rangeYards := s.healDistanceLocked(self, patient)
	return r.Capability().Target == "ally" && r.MaxFriendly > 0 &&
		(rangeYards == nil || *rangeYards >= r.MinFriendly && *rangeYards <= r.MaxFriendly)
}

func observedHealingAttack(w *worldv1.SnapshotResponse, patient *worldv1.PlayerState) bool {
	for _, creatures := range [][]*worldv1.CreatureState{w.NearbyCreatures, {w.OwnerTarget}} {
		for _, creature := range creatures {
			if creature != nil && creature.Present && creature.MapKnown && creature.MapId == patient.MapId &&
				creature.AliveKnown && creature.Alive && creature.AttackVictimKnown && creature.AttackVictimGuid == patient.Guid {
				return true
			}
		}
	}
	return false
}

// HealingOptions reports observations and spell mechanics without selecting a
// patient or a learned rank. Lua owns urgency, target ordering and rank choice.
func (s *Service) HealingOptions(ctx context.Context, _ *emptypb.Empty) (*actionsv1.HealingOptionsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.snapshot(ctx)
	if err != nil {
		return &actionsv1.HealingOptionsResponse{SessionId: s.sessionID, HostOwned: s.hostStandingHealing,
			Caster: &actionsv1.HealingCaster{Guid: s.selfGUID}, Reason: "healing_observations_unavailable"}, nil
	}
	return s.healingOptionsLocked(w, func() map[uint64]bool { return s.currentHealPartyLocked(ctx) }), nil
}

func (s *Service) healingOptionsLocked(w *worldv1.SnapshotResponse, onlineMembers func() map[uint64]bool) *actionsv1.HealingOptionsResponse {
	out := &actionsv1.HealingOptionsResponse{SessionId: s.sessionID, HostOwned: s.hostStandingHealing,
		Caster: &actionsv1.HealingCaster{Guid: s.selfGUID}}
	self := w.GetSelf()
	if self.GetGuid() == s.selfGUID && self.GetPowerKnown() && self.GetPowerType() == 0 {
		out.Caster.Mana = proto.Uint32(self.Power)
	}
	out.Reason = s.healCasterReadyLocked(w)
	if out.Reason != "" && out.Reason != "healing_caster_busy" {
		return out
	}
	// Custody/readiness may prevent a cast without hiding real available ranks
	// or injured allies from the Lua policy that can release that custody.
	out.Caster.Ready = out.Reason == ""
	for _, r := range s.learnedContractsLocked() {
		if s.healRankAvailableLocked(self, r) {
			out.Spells = append(out.Spells, &actionsv1.HealingRank{SpellId: r.ID, Rank: proto.Uint32(r.Rank),
				Level: proto.Uint32(r.Level), Cost: proto.Uint32(uint32(spellCost(r, self))), Amount: directHealing(r),
				CastMs: proto.Uint32(r.CastMs), MinRange: proto.Float64(r.MinFriendly), MaxRange: proto.Float64(r.MaxFriendly),
				SelfOnly: r.Capability().Target == "self"})
		}
	}
	// Preserve the legacy observation order for existing readers.
	sort.Slice(out.Spells, func(i, j int) bool {
		a, b := out.Spells[i], out.Spells[j]
		if a.GetLevel() != b.GetLevel() {
			return a.GetLevel() > b.GetLevel()
		}
		if a.GetRank() != b.GetRank() {
			return a.GetRank() > b.GetRank()
		}
		return a.SpellId < b.SpellId
	})
	var members map[uint64]bool
	if !s.independent && (w.Owner != nil || len(w.NearbyPlayers) != 0) {
		members = onlineMembers()
	}
	for _, players := range [][]*worldv1.PlayerState{{self, w.Owner}, w.NearbyPlayers} {
		for _, p := range players {
			if !livingHealingPlayer(p, self.MapId) || !injuredForHealing(p) ||
				p != self && (p.Guid == s.selfGUID || !s.healingAllyLocked(w, p, members) || observedHealingPlayer(w, p.Guid) != p) {
				continue
			}
			out.Patients = append(out.Patients, &actionsv1.HealingPatient{Guid: p.Guid, Health: p.Health, MaxHealth: p.MaxHealth,
				Distance: s.healDistanceLocked(self, p), IsOwner: p.Guid == ownerGUID, UnderAttack: observedHealingAttack(w, p)})
		}
	}
	return out
}

// CastHeal does not reread the policy or choose a substitute when a fact has
// changed. The next fresh observation belongs to the caller's scheduler.
func (s *Service) CastHeal(ctx context.Context, r *actionsv1.CastHealRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetTargetGuid() == 0 || r.GetSpellId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "session, exact caster, patient and learned rank required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	token, supplied := modcb.IncomingPolicySpell(ctx)
	if supplied && token == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid policy spell token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current owned body required")
	}
	if prior := s.supportOp; supplied && prior != nil && prior.policySpellToken == token &&
		prior.view.Capability == string(spellcatalog.Heal) && prior.view.SpellId == r.SpellId && prior.view.TargetGuid == r.TargetGuid {
		return nil, status.Error(codes.FailedPrecondition, "this exact heal attempt already entered transport")
	}
	s.expireInteractionLocked()
	w, err := s.snapshot(ctx)
	if err != nil || !s.healingVisitCurrentLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "current caster observation required")
	}
	if reason := s.healCasterReadyLocked(w); reason != "" {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	if observedHealPosition(w.Self) && !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "caster position changed before heal")
	}
	self, target := w.Self, observedHealingPlayer(w, r.TargetGuid)
	heal, learned := s.learnedContractsLocked()[r.SpellId]
	if !livingHealingPlayer(target, self.MapId) || !injuredForHealing(target) ||
		!s.healingAllyLocked(w, target, s.currentHealPartyLocked(ctx)) ||
		!learned || !s.healRankEligibleLocked(self, target, heal) {
		return nil, status.Error(codes.FailedPrecondition, "exact learned heal and eligible injured patient required")
	}
	attempts := s.state.HealAttempts
	if !s.castHealLocked(ctx, w, target.Guid, heal.ID) && s.state.HealAttempts == attempts {
		return nil, status.Error(codes.FailedPrecondition, s.state.Reason)
	}
	return s.stateLocked(), nil
}
