package buddyactions

import (
	"context"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Learned spell capability is a fact; the attached script chooses its role.
func (s *Service) hasLearnedHealingLocked() bool {
	return s.hasLearnedSupportSpellLocked(spellcatalog.Heal)
}

func (s *Service) hasLearnedSupportSpellLocked(kind spellcatalog.Kind) bool {
	for _, spell := range s.learnedContractsLocked() {
		if spell.Capability().Kind == kind {
			return true
		}
	}
	return false
}

func injuredForHealing(p *worldv1.PlayerState) bool {
	if p == nil || !p.HealthKnown || p.MaxHealth == 0 || p.Health == 0 || p.Health >= p.MaxHealth {
		return false
	}
	return true
}

// Health and positions are observed update-on-change values. An unchanged
// stationary player stays known; a moving player's old position cannot grant range.
func safeHealingPlayer(p *worldv1.PlayerState, mapID uint32) bool {
	const incapacitated = uint32(0x00040000 | 0x00400000 | 0x00800000) // stunned, confused, fleeing
	return healablePlayer(p, mapID) && p.UnitFlags&incapacitated == 0
}

func healablePlayer(p *worldv1.PlayerState, mapID uint32) bool {
	if p == nil || p.Guid == 0 || p.Guid>>48 != 0 || !p.Present || !p.MapKnown || p.MapId != mapID || !p.PositionKnown || !finite(playerPosition(p)) || !p.AliveKnown || !p.Alive || !p.UnitFlagsKnown || p.PlayerFlags&0x10 != 0 || p.DynamicFlags&0x20 != 0 {
		return false
	}
	if !p.MovementFlagsKnown || !movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) || p.MovementFlags&0xff != 0 && p.PositionAgeMs > 2000 {
		return false
	}
	const unsafe = uint32(0x1 | 0x4 | 0x00100000 | 0x01000000 | 0x02000000 | 0x08000000)
	return p.UnitFlags&unsafe == 0
}

// A module invocation is routed through this body's session. Only a complete
// current server group with this exact online self authorizes another player.
// Joel is subject to the same membership rule; self-healing needs no group.
func (s *Service) healingPartyLocked(ctx context.Context) map[uint64]bool {
	return s.onlineHealingGroup(s.readPartyStateLocked(ctx).GetGroup())
}

func (s *Service) readPartyStateLocked(ctx context.Context) *partyv1.StateResponse {
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
	return state
}

func (s *Service) onlineHealingGroup(g *partyv1.Group) map[uint64]bool {
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

// healingAllyLocked validates an explicitly observed friendly body. Membership
// proves party eligibility; known same-faction players can also receive heals.
// This never ranks patients or chooses a spell. The server remains authoritative
// for dynamic faction restrictions such as duels and spell-specific targets.
func (s *Service) healingAllyLocked(w *worldv1.SnapshotResponse, patient *worldv1.PlayerState, members map[uint64]bool) bool {
	if patient == nil || w.GetSelf().GetGuid() != s.selfGUID {
		return false
	}
	if patient.Guid == s.selfGUID {
		return true
	}
	if online, member := members[patient.Guid]; member {
		return online
	}
	team := observedPlayerTeam(w.Self)
	return team != 0 && observedPlayerTeam(patient) == team
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

func (s *Service) castHealLocked(ctx context.Context, target *worldv1.PlayerState, heal spellcatalog.Spell) bool {
	if !s.stopAutoAttackLocked(ctx) {
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return false
	}
	if !s.stopLocked(ctx) {
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
	if s.healCasterReadyLocked(fresh) != "" {
		s.state.Reason = "healing_caster_changed_before_cast"
		return false
	}
	freshBudget := spellCost(heal, fresh.Self)
	if fresh.Self.MaxHealth == 0 || fresh.Self.Health > fresh.Self.MaxHealth || fresh.Self.UnitFlags&0x2000 != 0 || !fresh.Self.PowerKnown || fresh.Self.PowerType != 0 || uint64(fresh.Self.Power) < freshBudget {
		s.state.Reason = "healing_caster_changed_before_cast"
		return false
	}
	target = observedHealingPlayer(fresh, target.Guid)
	if !healablePlayer(target, fresh.Self.MapId) || !injuredForHealing(target) ||
		!s.healingAllyLocked(fresh, target, s.healingPartyLocked(ctx)) {
		s.state.Reason = "healing_target_changed_before_cast"
		return false
	}
	if heal.ID == 0 || !s.healRankEligibleLocked(fresh.Self, target, heal, s.pos) {
		s.state.Reason = "healing_spell_or_resources_changed_before_cast"
		return false
	}
	if ctx.Err() != nil {
		s.state.Reason = "healing_request_cancelled_before_cast"
		return false
	}
	now := s.now()
	s.castCount++
	s.reserveSpellCooldownLocked(heal)
	s.nextHeal = now.Add(time.Duration(heal.GcdMs) * time.Millisecond)
	s.castUntil = now.Add(max(5*time.Second, (time.Duration(heal.CastMs)*time.Millisecond)+1500*time.Millisecond))
	s.state.CastPending = true
	s.state.HealAttempts++
	s.pendingSpell, s.pendingTarget = heal.ID, target.Guid
	s.nextAssist = s.nextHeal
	s.state.LastCastSpellId, s.state.LastCastTargetGuid = heal.ID, target.Guid
	s.state.LastCastFailure = 0
	var receipt *supportAttempt
	if token := modcb.PolicySpellToken(ctx); token != "" {
		receipt = s.reserveHealReceiptLocked(token, heal, target.Guid)
	}
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, heal.ID, target.Guid)) {
		// The packet may have reached the server. Reassess after the cast window.
		s.nextHeal = s.castUntil
		if receipt != nil {
			receipt.view.Phase = "transport_unconfirmed_no_retry"
		} else {
			s.state.CastPending = false
			s.pendingSpell, s.pendingTarget = 0, 0
		}
		return false
	}
	if receipt != nil {
		receipt.view.TransportWritten = true
	}
	s.state.Reason = "heal_transport_written_waiting_for_server"
	return true
}
