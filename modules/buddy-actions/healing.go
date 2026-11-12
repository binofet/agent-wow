package buddyactions

import (
	"context"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const urgentSelfHealPercent = 50

func injuredForHealing(p *worldv1.PlayerState, automatic bool) bool {
	if p == nil || !p.HealthKnown || p.MaxHealth == 0 || p.Health == 0 || p.Health >= p.MaxHealth {
		return false
	}
	return !automatic || uint64(p.Health)*100 < uint64(p.MaxHealth)*85
}

// Health and positions are observed update-on-change values. An unchanged
// stationary player stays known; a moving player's old position cannot grant range.
func safeHealingPlayer(p *worldv1.PlayerState, mapID uint32) bool {
	if p == nil || p.Guid == 0 || p.Guid>>48 != 0 || !p.Present || !p.MapKnown || p.MapId != mapID || !p.PositionKnown || !finite(playerPosition(p)) || !p.AliveKnown || !p.Alive || !p.UnitFlagsKnown || p.PlayerFlags&0x10 != 0 || p.DynamicFlags&0x20 != 0 {
		return false
	}
	if !p.MovementFlagsKnown || !movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) || p.MovementFlags&0xff != 0 && p.PositionAgeMs > 2000 {
		return false
	}
	const unsafe = uint32(0x1 | 0x4 | 0x00040000 | 0x00100000 | 0x00400000 | 0x00800000 | 0x01000000 | 0x02000000 | 0x08000000)
	return p.UnitFlags&unsafe == 0
}

// A module invocation is routed through this body's session. Only a complete
// current server group with this exact online self authorizes another player.
// Joel is subject to the same membership rule; self-healing needs no group.
func (s *Service) healingPartyLocked(ctx context.Context) map[uint64]bool {
	if s.session == nil {
		return nil
	}
	call, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	request, _ := anypb.New(&emptypb.Empty{})
	result, err := s.session.InvokeModule(call, &modv1.InvokeModuleRequest{Module: "buddy-party", Method: "state", Request: request})
	if err != nil || result == nil || result.Result == nil {
		return nil
	}
	state := &partyv1.StateResponse{}
	if result.Result.UnmarshalTo(state) != nil || len(state.SessionId) == 0 || len(state.SessionId) > 128 {
		return nil
	}
	g := state.Group
	if g == nil || !g.Known || !g.InGroup || len(g.Members) < 2 || len(g.Members) > 40 {
		return nil
	}
	seen, online, self := map[uint64]bool{}, map[uint64]bool{}, false
	for _, m := range g.Members {
		if m == nil || m.Guid == 0 || m.Guid>>48 != 0 || seen[m.Guid] || m.Self != (m.Guid == s.selfGUID) {
			return nil
		}
		seen[m.Guid] = true
		online[m.Guid] = m.Online
		if m.Guid == s.selfGUID && m.Online {
			self = true
		}
	}
	if !self {
		return nil
	}
	return online
}

func observedHealingPlayer(w *worldv1.SnapshotResponse, guid uint64) *worldv1.PlayerState {
	if w == nil || w.Self == nil {
		return nil
	}
	if guid == w.Self.Guid {
		return w.Self
	}
	if guid == ownerGUID {
		if w.Owner != nil && w.Owner.Guid == ownerGUID {
			return w.Owner
		}
		return nil
	}
	if len(w.NearbyPlayers) > 64 {
		return nil
	}
	var found *worldv1.PlayerState
	for _, p := range w.NearbyPlayers {
		if p != nil && p.Guid == guid {
			if found != nil {
				return nil
			}
			found = p
		}
	}
	return found
}

func healingTiePriority(guid, self uint64) int {
	if guid == ownerGUID {
		return 0
	}
	if guid == self {
		return 1
	}
	return 2
}

func (s *Service) healingTargetLocked(ctx context.Context, w *worldv1.SnapshotResponse, automatic, urgentOnly bool) *worldv1.PlayerState {
	self := w.Self
	eligible := func(p *worldv1.PlayerState) bool {
		return safeHealingPlayer(p, self.MapId) && injuredForHealing(p, automatic) && (p.Guid == s.selfGUID || p.PlayerFlags&2 == 0 && distance(s.pos, playerPosition(p)) <= 35)
	}
	urgent := eligible(self) && uint64(self.Health)*100 < uint64(self.MaxHealth)*urgentSelfHealPercent
	if urgent {
		return self
	}
	if urgentOnly {
		return nil
	}
	var best *worldv1.PlayerState
	var scriptTargets []*worldv1.PlayerState
	if eligible(self) {
		best = self
		scriptTargets = append(scriptTargets, self)
	}
	if len(w.NearbyPlayers) > 64 {
		return best
	}
	players := append([]*worldv1.PlayerState{w.Owner}, w.NearbyPlayers...)
	potential := false
	for _, p := range players {
		if eligible(p) && p.Guid != s.selfGUID && observedHealingPlayer(w, p.Guid) == p {
			potential = true
			break
		}
	}
	if !potential {
		return best
	}
	members := s.healingPartyLocked(ctx)
	for _, p := range players {
		if !eligible(p) || p.Guid == s.selfGUID || !members[p.Guid] || observedHealingPlayer(w, p.Guid) != p {
			continue
		}
		scriptTargets = append(scriptTargets, p)
		better := best == nil
		if best != nil {
			left, right := uint64(p.Health)*uint64(best.MaxHealth), uint64(best.Health)*uint64(p.MaxHealth)
			pt, bt := healingTiePriority(p.Guid, s.selfGUID), healingTiePriority(best.Guid, s.selfGUID)
			better = left < right || left == right && (pt < bt || pt == bt && p.Guid < best.Guid)
		}
		if better {
			best = p
		}
	}
	return s.behaviorHealTargetLocked(self, scriptTargets, best)
}

func (s *Service) urgentSelfHealLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	return s.healTriageLocked(ctx, w, true, true)
}

func (s *Service) triageHealLocked(ctx context.Context, w *worldv1.SnapshotResponse, automatic bool) bool {
	return s.healTriageLocked(ctx, w, automatic, false)
}

func (s *Service) healTriageLocked(ctx context.Context, w *worldv1.SnapshotResponse, automatic, urgentOnly bool) bool {
	if reason := safeSelfCombat(w, s.selfGUID); reason != "" {
		s.state.Reason = reason
		return false
	}
	if w.Self.MaxHealth == 0 || w.Self.Health > w.Self.MaxHealth {
		s.state.Reason = "healing_caster_health_unconfirmed"
		return false
	}
	if !s.refreshPositionLocked(w) || s.state.MotionUnconfirmed {
		s.stopLocked(ctx)
		return false
	}
	if s.state.CastPending || s.now().Before(s.nextHeal) {
		s.state.Reason = "heal_throttled_or_pending"
		return false
	}
	if len(s.candidatesLocked(spellcatalog.Heal)) == 0 {
		s.state.Reason = "no_known_healing_effect"
		return false
	}
	if w.Self.UnitFlags&0x2000 != 0 {
		s.state.Reason = "caster_silenced"
		return false
	}
	target := s.healingTargetLocked(ctx, w, automatic, urgentOnly)
	if target == nil {
		s.state.Reason = "no_observed_injured_party_member_in_range"
		return false
	}
	heal := s.bestHealLocked(w.Self, target)
	budget := spellCost(heal, w.Self)
	if heal.ID == 0 || !w.Self.PowerKnown || w.Self.PowerType != 0 || uint64(w.Self.Power) < budget {
		s.state.Reason = "mana_unknown_or_insufficient"
		return false
	}
	if !s.stopAutoAttackLocked(ctx) {
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return false
	}
	if !s.stopLocked(ctx) {
		s.state.Healing = false
		s.state.Reason = "movement_stop_unconfirmed_heal_aborted"
		return false
	}
	// STOP may read a server correction; bind the actual cast to a new own
	// snapshot and current membership rather than replaying the selection.
	fresh, err := s.snapshot(ctx)
	if err != nil || safeSelfCombat(fresh, s.selfGUID) != "" || !s.refreshPositionLocked(fresh) || s.state.MotionUnconfirmed {
		s.state.Reason = "healing_observations_changed_before_cast"
		return false
	}
	freshBudget := spellCost(heal, fresh.Self)
	if fresh.Self.MaxHealth == 0 || fresh.Self.Health > fresh.Self.MaxHealth || fresh.Self.UnitFlags&0x2000 != 0 || !fresh.Self.PowerKnown || fresh.Self.PowerType != 0 || freshBudget == 0 || uint64(fresh.Self.Power) < freshBudget {
		s.state.Reason = "healing_caster_changed_before_cast"
		return false
	}
	// A newly urgent own injury takes priority even if an ally was selected
	// before the movement-stop/observation round trip.
	if injuredForHealing(fresh.Self, true) && uint64(fresh.Self.Health)*100 < uint64(fresh.Self.MaxHealth)*urgentSelfHealPercent {
		target = fresh.Self
	} else {
		target = observedHealingPlayer(fresh, target.Guid)
	}
	if !safeHealingPlayer(target, fresh.Self.MapId) || !injuredForHealing(target, automatic) || target.Guid != s.selfGUID && (target.PlayerFlags&2 != 0 || distance(s.pos, playerPosition(target)) > 35) || urgentOnly && (target.Guid != s.selfGUID || uint64(target.Health)*100 >= uint64(target.MaxHealth)*urgentSelfHealPercent) {
		s.state.Reason = "healing_target_changed_before_cast"
		return false
	}
	if target.Guid != s.selfGUID && !s.healingPartyLocked(ctx)[target.Guid] {
		s.state.Reason = "healing_target_no_longer_in_current_party"
		return false
	}
	heal = s.bestHealLocked(fresh.Self, target)
	if heal.ID == 0 {
		s.state.Reason = "healing_spell_or_resources_changed_before_cast"
		return false
	}
	now := s.now()
	s.castCount++
	s.reserveSpellCooldownLocked(heal)
	s.nextHeal = now.Add(3 * time.Second)
	s.castUntil = now.Add(max(5*time.Second, (time.Duration(heal.CastMs)*time.Millisecond)+1500*time.Millisecond))
	s.state.CastPending = true
	s.state.HealAttempts++
	s.pendingSpell, s.pendingTarget = heal.ID, target.Guid
	s.nextAssist = now.Add(3 * time.Second)
	s.state.LastCastSpellId, s.state.LastCastTargetGuid = heal.ID, target.Guid
	s.state.LastCastFailure = 0
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, heal.ID, target.Guid)) {
		s.state.CastPending = false
		s.pendingSpell, s.pendingTarget = 0, 0
		return false
	}
	s.state.Reason = "heal_transport_written_waiting_for_server"
	return true
}
