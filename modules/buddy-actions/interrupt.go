package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type enemyCastIdentity struct {
	guid     uint64
	revision uint64
}

func (s *Service) enemyHealInOurFight(w *worldv1.SnapshotResponse, enemy *worldv1.CreatureState, online map[uint64]bool) bool {
	cast := enemy.GetActiveCast()
	if cast.GetRevision() == 0 || cast.GetRemainingMs() == 0 ||
		(s.lastInterrupt == enemyCastIdentity{enemy.GetGuid(), cast.GetRevision()}) {
		return false
	}
	spell, known := spellcatalog.Default().Lookup(cast.GetSpellId())
	self := w.Self
	kind := enemy.Guid >> 48
	if !known || !spell.IsHealing() || kind != 0xf130 && kind != 0xf150 ||
		!enemy.Present || !enemy.MapKnown || enemy.MapId != self.MapId ||
		!enemy.AliveKnown || !enemy.Alive || !enemy.HealthKnown || enemy.Health == 0 ||
		!enemy.UnitFlagsKnown || enemy.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 ||
		enemy.UnitFlags_2&1 != 0 || enemy.DynamicFlags&0x20 != 0 {
		return false
	}
	if enemy.AttackVictimKnown && enemy.AttackAgeMs <= 30000 {
		if enemy.AttackVictimGuid == self.Guid {
			return true
		}
		if online[enemy.AttackVictimGuid] {
			p := observedHealingPlayer(w, enemy.AttackVictimGuid)
			if p.GetAlive() && p.GetPresent() && p.GetMapKnown() && p.GetMapId() == self.MapId {
				return true
			}
		}
	}
	if enemy.UnitFlags&0x80000 == 0 {
		return false
	}
	if self.UnitFlags&0x80000 != 0 && s.autoTarget == enemy.Guid {
		return true
	}
	// A mob can start healing before it lands an attack. An online party
	// member already fighting it can identify it without pulling a new pack.
	for guid, on := range online {
		if !on {
			continue
		}
		p := observedHealingPlayer(w, guid)
		if p.GetPresent() && p.GetAlive() && p.GetMapKnown() && p.GetMapId() == self.MapId &&
			p.GetUnitFlagsKnown() && p.GetUnitFlags()&0x80000 != 0 &&
			p.GetTargetKnown() && p.GetTargetGuid() == enemy.Guid {
			return true
		}
	}
	return false
}

func (s *Service) interruptAvailableLocked(r spellcatalog.Spell, self *worldv1.PlayerState, enemy *worldv1.CreatureState) bool {
	p, known, _ := creaturePosition(enemy)
	if !known || !r.RangeKnown || r.MaxHostile <= 0 || !self.PowerKnown || int32(self.PowerType) != r.Power ||
		r.CastMs != 0 || s.now().Before(s.spellReady[r.ID]) ||
		r.GcdMs > 0 && (s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal)) ||
		uint64(self.Power) < spellCost(r, self) || !s.casterRequirementsLocked(r) {
		return false
	}
	reach := r.MaxHostile
	if self.CombatReachKnown && enemy.CombatReachKnown {
		reach = math.Max(reach, float64(self.CombatReach+enemy.CombatReach)+4.0/3.0)
	}
	dist := distance(s.pos, p)
	return dist >= r.MinHostile && dist <= reach
}

func (s *Service) interruptHealLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if s.closed || s.state.CastPending || s.state.InteractionOwner != "" || s.state.MotionUnconfirmed ||
		s.state.Practicing || s.fargodeep.active || safeSelfCombat(w, s.selfGUID) != "" ||
		w.Self.UnitFlags&0x2000 != 0 {
		return false
	}
	enemies := append(append([]*worldv1.CreatureState(nil), w.NearbyCreatures...), w.OwnerTarget, w.RequestedCreature)
	hasCast := false
	for _, enemy := range enemies {
		if enemy.GetActiveCast().GetRemainingMs() != 0 {
			hasCast = true
			break
		}
	}
	if !hasCast {
		return false
	}
	interrupts := s.candidatesLocked(spellcatalog.Interrupt)
	if len(interrupts) == 0 || !s.refreshPositionLocked(w) {
		return false
	}
	var online map[uint64]bool
	if !s.independent {
		online = s.healingPartyLocked(ctx)
	}
	var target *worldv1.CreatureState
	var chosen spellcatalog.Spell
	for _, enemy := range enemies {
		if !s.enemyHealInOurFight(w, enemy, online) {
			continue
		}
		for _, spell := range interrupts {
			if !s.interruptAvailableLocked(spell, w.Self, enemy) {
				continue
			}
			if target == nil || enemy.ActiveCast.RemainingMs < target.ActiveCast.RemainingMs {
				target, chosen = enemy, spell
			}
			break
		}
	}
	if target == nil {
		return false
	}

	if !s.stopLocked(ctx) {
		return false
	}
	p, _, _ := creaturePosition(target)
	if chosen.Facing != 0 && math.Abs(math.Remainder(float64(s.pos.o-followFacing(s.pos, p)), 2*math.Pi)) > math.Pi/2 &&
		!s.faceEnemyLocked(ctx, p) {
		return false
	}
	// The cast can finish while we turn or check range. Re-read the exact
	// caster and its unique cast revision before spending the interrupt.
	fresh, err := s.snapshot(ctx, target.Guid)
	if err != nil || safeSelfCombat(fresh, s.selfGUID) != "" ||
		fresh.Self.UnitFlags&0x2000 != 0 || fresh.Self.MapId != s.posMap || fresh.Self.PositionRevision != s.positionRevision {
		return false
	}
	enemy := partyDefenseCreature(fresh, target.Guid)
	if enemy.GetActiveCast().GetRevision() != target.ActiveCast.Revision ||
		!s.enemyHealInOurFight(fresh, enemy, online) || !s.interruptAvailableLocked(chosen, fresh.Self, enemy) {
		return false
	}
	s.lastInterrupt = enemyCastIdentity{enemy.Guid, enemy.ActiveCast.Revision}
	s.castCount++
	s.castUntil = s.now().Add(2 * time.Second)
	s.pendingSpell, s.pendingTarget = chosen.ID, enemy.Guid
	s.state.CastPending = true
	s.state.LastCastSpellId, s.state.LastCastTargetGuid = chosen.ID, enemy.Guid
	s.state.LastCastFailure = 0
	s.reserveSpellCooldownLocked(chosen)
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, chosen.ID, enemy.Guid)) {
		s.state.CastPending = false
		s.pendingSpell, s.pendingTarget = 0, 0
		return false
	}
	s.state.Reason = "interrupting_enemy_heal"
	return true
}
