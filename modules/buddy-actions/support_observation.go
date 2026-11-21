package buddyactions

import (
	"context"
	"math"
	"sort"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// This projection enumerates mechanically possible spell/target pairs. It
// deliberately does not choose a patient, an enemy cast, or an interrupt rank.
func (s *Service) encounterSupportLocked(ctx context.Context, w *worldv1.SnapshotResponse, online map[uint64]bool) *actionsv1.EncounterSupport {
	out := &actionsv1.EncounterSupport{CasterInCombat: w.Self.UnitFlags&0x80000 != 0,
		PlayerAuras: s.observedPlayerAurasLocked(w)}
	catalog := s.spellData
	if catalog == nil {
		catalog = spellcatalog.Default()
	}
	out.AuraSpells = make(map[uint32]*actionsv1.EncounterSpell)
	for _, player := range out.PlayerAuras {
		for _, aura := range player.Auras {
			if spell, ok := catalog.Lookup(aura.SpellId); ok {
				metadata := encounterSpellMechanics(spell, w.Self)
				metadata.Known = s.knownSpells[spell.ID]
				out.AuraSpells[aura.SpellId] = metadata
			}
		}
	}
	s.supportSnapshotLocked(w)
	if !s.spellbookKnown || safeSelfCombat(w, s.selfGUID) != "" {
		return out
	}
	self := w.Self
	owners := map[uint64]bool{}
	for _, rows := range [][]*worldv1.PlayerState{{w.Owner}, w.NearbyPlayers} {
		for _, p := range rows {
			if p != nil && p.Guid != s.selfGUID && p.Present {
				if on, member := online[p.Guid]; !member || on {
					owners[p.Guid] = true
				}
			}
		}
	}
	for _, c := range w.PlayerCorpses {
		if c != nil && c.OwnerGuid != s.selfGUID {
			if on, member := online[c.OwnerGuid]; !member || on {
				owners[c.OwnerGuid] = true
			}
		}
	}
	team := observedPlayerTeam(self)
	resCaster := team != 0 && s.resurrectionCasterAvailableLocked(w)
	for _, rank := range s.candidatesLocked(spellcatalog.Resurrect) {
		if !resCaster || !s.resurrectionRankAvailableLocked(rank, self) {
			continue
		}
		for owner := range owners {
			if s.resurrectionBindings[owner] {
				continue
			}
			target, pos, ok := s.resurrectionBodyLocked(w, owner, team)
			if !ok {
				continue
			}
			d := distance(playerPosition(self), pos)
			out.Resurrections = append(out.Resurrections, &actionsv1.ResurrectionOption{
				PatientGuid: owner, CastTargetGuid: target, SpellId: rank.ID, RangeYards: float32(rank.MaxFriendly),
				CanApproach: true, Ready: d >= rank.MinFriendly && d <= rank.MaxFriendly &&
					!s.state.Moving && self.MovementFlags&0xff == 0 && !s.state.AutoAttacking &&
					!s.state.AutoAttackRequested && s.autoTarget == 0 && !s.autoStopping,
			})
		}
	}
	if s.interruptCasterAvailableLocked(w) {
		seen := map[uint64]bool{}
		for _, rows := range [][]*worldv1.CreatureState{w.NearbyCreatures, {w.OwnerTarget, w.RequestedCreature}} {
			for _, enemy := range rows {
				if enemy == nil || seen[enemy.Guid] || !s.interruptTargetObservedLocked(w, enemy) {
					continue
				}
				seen[enemy.Guid] = true
				cast := enemy.ActiveCast
				healing := false
				if metadata, ok := s.spellData.Lookup(cast.SpellId); ok {
					healing = metadata.IsHealing()
				}
				for _, rank := range s.candidatesLocked(spellcatalog.Interrupt) {
					if !s.interruptRankAvailableLocked(rank, self) {
						continue
					}
					reach := interruptRange(rank, self, enemy)
					d := distance(playerPosition(self), position{enemy.X, enemy.Y, enemy.Z, enemy.Orientation})
					out.Interrupts = append(out.Interrupts, &actionsv1.InterruptOption{
						TargetGuid: enemy.Guid, SpellId: rank.ID, EnemySpellId: cast.SpellId, CastRevision: cast.Revision,
						RemainingMs: uint32(min(cast.RemainingMs, math.MaxUint32)), Healing: healing,
						CanApproach: true, Ready: d >= rank.MinHostile && d <= reach, RangeYards: float32(reach),
					})
				}
			}
		}
	}
	// Stable serialization is useful for hot reloads and observation diffing;
	// this order is not the gameplay priority, which belongs to Lua.
	sort.Slice(out.Resurrections, func(i, j int) bool {
		a, b := out.Resurrections[i], out.Resurrections[j]
		return a.PatientGuid < b.PatientGuid || a.PatientGuid == b.PatientGuid && a.SpellId < b.SpellId
	})
	sort.Slice(out.Interrupts, func(i, j int) bool {
		a, b := out.Interrupts[i], out.Interrupts[j]
		return a.TargetGuid < b.TargetGuid || a.TargetGuid == b.TargetGuid && a.SpellId < b.SpellId
	})
	return out
}

func (s *Service) resurrectionCasterAvailableLocked(w *worldv1.SnapshotResponse) bool {
	self := w.GetSelf()
	return !s.closed && safeSelfCombat(w, s.selfGUID) == "" && self.PositionKnown && finite(playerPosition(self)) &&
		self.UnitFlags&(0x80000|0x2000) == 0 && self.ChannelSpellId == 0 && !s.hasObservedSelfAttackerLocked(w) &&
		!s.clientControlLost && !s.clientControlRestoring && !s.state.CastPending && !s.state.MotionUnconfirmed &&
		!s.state.AutoAttackUnconfirmed && s.state.InteractionOwner == "" && !s.recovery.state.Active
}

func (s *Service) resurrectionRankAvailableLocked(r spellcatalog.Spell, self *worldv1.PlayerState) bool {
	return r.Capability().Kind == spellcatalog.Resurrect && r.Power == 0 && self.PowerKnown && self.PowerType == 0 &&
		uint64(self.Power) >= spellCost(r, self) && !s.spellOnCooldownLocked(r) && casterRequirementsObserved(r, self)
}

func (s *Service) interruptCasterAvailableLocked(w *worldv1.SnapshotResponse) bool {
	self := w.GetSelf()
	return !s.closed && safeSelfCombat(w, s.selfGUID) == "" && self.PositionKnown && finite(playerPosition(self)) &&
		self.UnitFlags&0x2000 == 0 && self.ChannelSpellId == 0 && !s.clientControlLost && !s.clientControlRestoring &&
		!s.state.CastPending && !s.state.MotionUnconfirmed && s.state.InteractionOwner == "" && !s.recovery.state.Active
}

func (s *Service) interruptTargetObservedLocked(w *worldv1.SnapshotResponse, enemy *worldv1.CreatureState) bool {
	cast := enemy.GetActiveCast()
	return cast != nil && cast.Revision != 0 && cast.RemainingMs > 0 &&
		s.lastInterrupt != (enemyCastIdentity{guid: enemy.Guid, revision: cast.Revision}) &&
		!s.targetProtection.blocks(enemy.Guid, s.now()) && enemy.Present && enemy.Guid>>48 == 0xf130 &&
		enemy.MapKnown && enemy.MapId == w.Self.MapId && enemy.AliveKnown && enemy.Alive && enemy.HealthKnown && enemy.Health > 0 &&
		enemy.UnitFlagsKnown && enemy.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 &&
		enemy.PositionKnown && finite(position{enemy.X, enemy.Y, enemy.Z, enemy.Orientation})
}

func (s *Service) interruptRankAvailableLocked(r spellcatalog.Spell, self *worldv1.PlayerState) bool {
	return r.Capability().Kind == spellcatalog.Interrupt && r.RangeKnown && r.MaxHostile > 0 && r.CastMs == 0 && self.PowerKnown && int32(self.PowerType) == r.Power &&
		uint64(self.Power) >= spellCost(r, self) && !s.spellOnCooldownLocked(r) &&
		(r.GcdMs == 0 || !s.now().Before(s.nextAssist) && !s.now().Before(s.nextHeal)) && casterRequirementsObserved(r, self)
}

func interruptRange(r spellcatalog.Spell, self *worldv1.PlayerState, enemy *worldv1.CreatureState) float64 {
	reach := r.MaxHostile
	if self.CombatReachKnown && enemy.CombatReachKnown {
		reach = math.Max(reach, float64(self.CombatReach+enemy.CombatReach)+4.0/3.0)
	}
	return reach
}
