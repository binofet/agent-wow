package buddyactions

import (
	"context"
	"sort"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
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
	if reason := safeSelfCombat(w, s.selfGUID); reason != "" {
		return reason
	}
	self := w.Self
	if self.MaxHealth == 0 || self.Health > self.MaxHealth || !self.PowerKnown || self.PowerType != 0 {
		return "healing_caster_vitals_unknown"
	}
	if !s.spellbookKnown {
		return "healing_spellbook_unknown"
	}
	if s.clientControlLost || s.clientControlRestoring || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed ||
		s.state.InteractionOwner != "" || s.state.CastPending || s.now().Before(s.nextHeal) ||
		self.ChannelSpellId != 0 || self.UnitFlags&0x2000 != 0 {
		return "healing_caster_busy"
	}
	return ""
}

func (s *Service) healRankAvailableLocked(self *worldv1.PlayerState, r spellcatalog.Spell) bool {
	return r.ID != 0 && r.Capability().Kind == spellcatalog.Heal && r.Power == 0 && self.PowerKnown && self.PowerType == 0 &&
		uint64(self.Power) >= spellCost(r, self) && !s.now().Before(s.spellReady[r.ID]) && casterRequirementsObserved(r, self)
}

func (s *Service) healRankEligibleLocked(self, patient *worldv1.PlayerState, r spellcatalog.Spell, caster position) bool {
	if !s.healRankAvailableLocked(self, r) {
		return false
	}
	if patient.Guid == s.selfGUID {
		return true
	}
	rangeYards := distance(caster, playerPosition(patient))
	return r.Capability().Target != "self" && rangeYards >= r.MinFriendly && rangeYards <= r.MaxFriendly
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
	return s.healingOptionsLocked(w, func() map[uint64]bool { return s.healingPartyLocked(ctx) }), nil
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
	caster := playerPosition(self)
	if s.posKnown && s.posMap == self.MapId && s.positionRevision == self.PositionRevision {
		caster = s.pos
	}
	var members map[uint64]bool
	if !s.independent && (w.Owner != nil || len(w.NearbyPlayers) != 0) {
		members = onlineMembers()
	}
	for _, players := range [][]*worldv1.PlayerState{{self, w.Owner}, w.NearbyPlayers} {
		for _, p := range players {
			if !healablePlayer(p, self.MapId) || !injuredForHealing(p) ||
				p != self && (p.Guid == s.selfGUID || !s.healingAllyLocked(w, p, members) || observedHealingPlayer(w, p.Guid) != p) {
				continue
			}
			rangeYards := float64(0)
			if p != self {
				rangeYards = distance(caster, playerPosition(p))
			}
			out.Patients = append(out.Patients, &actionsv1.HealingPatient{Guid: p.Guid, Health: p.Health, MaxHealth: p.MaxHealth,
				Distance: proto.Float64(rangeYards), IsOwner: p.Guid == ownerGUID, UnderAttack: observedHealingAttack(w, p)})
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current owned body required")
	}
	s.expireInteractionLocked()
	w, err := s.snapshot(ctx)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "current caster observation required")
	}
	if reason := s.healCasterReadyLocked(w); reason != "" {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	if !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "caster position changed before heal")
	}
	self, target := w.Self, observedHealingPlayer(w, r.TargetGuid)
	heal, learned := s.learnedContractsLocked()[r.SpellId]
	if !healablePlayer(target, self.MapId) || !injuredForHealing(target) ||
		!s.healingAllyLocked(w, target, s.healingPartyLocked(ctx)) ||
		!s.spellbookKnown || !learned || !s.healRankEligibleLocked(self, target, heal, s.pos) {
		return nil, status.Error(codes.FailedPrecondition, "exact learned heal and current injured in-range patient required")
	}
	attempts := s.state.HealAttempts
	if !s.castHealLocked(ctx, target, heal) && s.state.HealAttempts == attempts {
		return nil, status.Error(codes.FailedPrecondition, s.state.Reason)
	}
	return s.stateLocked(), nil
}
