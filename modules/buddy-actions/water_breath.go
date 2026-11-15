package buddyactions

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/proto"
)

const waterBreathRefresh = time.Minute

func (s *Service) dueWaterBreathLocked() buffSpell {
	if !s.spellbookKnown || s.independent {
		return buffSpell{}
	}
	if s.waterBreathSpellRevision != s.spellbookRevision {
		s.waterBreathSpellRevision = s.spellbookRevision
		var best buffSpell
		for id := range s.knownSpells {
			r := buffMetadata(id)
			if r.waterBreathing && (r.rank > best.rank || r.rank == best.rank && r.id < best.id) {
				best = r
			}
		}
		s.waterBreathSpell = best
	}
	if s.waterBreathSpell.id == 0 || s.now().Before(s.nextBuff) || s.now().Before(s.nextWaterBreathCheck) {
		return buffSpell{}
	}
	return s.waterBreathSpell
}

func waterBreathPlayer(p *worldv1.PlayerState, mapID uint32) bool {
	if p.GetMovementFlags()&swimMovementFlag != 0 {
		if !swimFollowCompatible(p.GetMovementFlags(), p.GetExtraMovementFlags()) ||
			p.GetMovementFlags()&(swimAscendingFlag|swimDescendingFlag) != 0 && p.GetPositionAgeMs() > 2000 {
			return false
		}
		p = proto.Clone(p).(*worldv1.PlayerState)
		p.MovementFlags &^= swimMovementFlag | swimAscendingFlag | swimDescendingFlag
	}
	return safeHealingPlayer(p, mapID) && p.HealthKnown && p.Health > 0
}

func (s *Service) waterBreathContext(w *worldv1.SnapshotResponse, r buffSpell) bool {
	self := w.GetSelf()
	if self.GetGuid() != s.selfGUID || !waterBreathPlayer(self, self.GetMapId()) || self.GetUnitFlags()&0x80000 != 0 ||
		!self.GetPowerKnown() || self.GetPowerType() != 0 || self.GetMaxPower() == 0 || self.GetPower() > self.GetMaxPower() ||
		s.closed || s.state.InteractionOwner != "" || s.recovery.state.Active || s.state.Practicing || s.fargodeep.active ||
		s.state.Traveling || s.state.Moving && !s.state.Following || s.state.CastPending || s.state.MotionUnconfirmed ||
		s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.state.AutoAttackRequested || s.autoTarget != 0 ||
		s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist) || !s.knownSpells[r.id] {
		return false
	}
	cost := manaBudget(self, uint64(r.manaPercent))
	return cost > 0 && uint64(self.GetPower()) >= cost
}

func (s *Service) hasFreshWaterBreathLocked(guid uint64) bool {
	set := s.buffAuras[guid]
	if set == nil {
		return false
	}
	for _, aura := range set.slots {
		if aura.spell == 0 || !aura.positive || aura.durationKnown && time.Duration(aura.remaining(s.now()))*time.Millisecond < waterBreathRefresh {
			continue
		}
		if spell, known := spellcatalog.Default().Lookup(aura.spell); known && spell.ProvidesWaterBreathing() {
			return true
		}
	}
	return false
}

func (s *Service) waterBreathTargetLocked(w *worldv1.SnapshotResponse, members map[uint64]bool, r buffSpell, guid uint64) bool {
	if !members[s.selfGUID] || !members[guid] || guid != s.selfGUID && !r.party {
		return false
	}
	p := observedHealingPlayer(w, guid)
	return waterBreathPlayer(p, w.GetSelf().GetMapId()) && (guid == s.selfGUID || distance(s.pos, playerPosition(p)) <= r.rangeYards) &&
		!s.hasFreshWaterBreathLocked(guid) && s.buffBindingAllowsLocked(guid, r)
}

func (s *Service) waterBreathTickLocked(ctx context.Context, w *worldv1.SnapshotResponse, r buffSpell) bool {
	if r.id == 0 {
		return false
	}
	// Ordinary movement packets arrive more often than buff eligibility can
	// change, and a check can read group state. Bound idle observation traffic.
	s.nextWaterBreathCheck = s.now().Add(time.Second)
	if s.buffOp != nil && s.buffOp.view.Phase == "waiting_server" || !s.waterBreathContext(w, r) || !s.refreshPositionLocked(w) {
		return false
	}
	s.pruneBuffAurasLocked(w)
	members := s.healingPartyLocked(ctx)
	if !members[s.selfGUID] {
		return false
	}
	for _, guid := range s.buffTargets(members) {
		if !s.waterBreathTargetLocked(w, members, r, guid) {
			continue
		}
		// Only stop our own follow step. Cast, verify the aura, and leave the
		// follow intent active, including while the companion is in water.
		if s.state.Moving && !s.stopLocked(ctx) {
			return true
		}
		freshMembers := s.healingPartyLocked(ctx)
		fresh, err := s.snapshot(ctx)
		if err != nil || !s.waterBreathContext(fresh, r) || !s.refreshPositionLocked(fresh) || !s.waterBreathTargetLocked(fresh, freshMembers, r, guid) {
			return false
		}
		return s.startBuffLocked(ctx, r, guid)
	}
	return false
}
