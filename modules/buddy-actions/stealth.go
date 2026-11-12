package buddyactions

import (
	"context"
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func observedRunSpeed(p *worldv1.PlayerState) float64 {
	if p == nil || !p.SpeedsKnown {
		return 0
	}
	v := float64(p.RunSpeed)
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0.1 || v > 14 {
		return 0
	}
	return v
}
func (s *Service) stealthedLocked() bool {
	set := s.buffAuras[s.selfGUID]
	if set == nil {
		return false
	}
	for _, a := range set.slots {
		if a.spell == 0 || a.durationKnown && a.remaining(s.now()) == 0 {
			continue
		}
		r, ok := s.spellData.Lookup(a.spell)
		if ok && r.Capability().Kind == spellcatalog.Stealth {
			return true
		}
	}
	return false
}

// Stealth is an approach to an already engaged party enemy, never a new pull.
func (s *Service) stealthApproachLocked(ctx context.Context, w *worldv1.SnapshotResponse, target uint64) bool {
	if w.Self.UnitFlags&0x80000 != 0 || s.autoTarget != 0 || s.state.CastPending || s.state.AutoAttackUnconfirmed || s.stealthedLocked() {
		return false
	}
	if op := s.supportOp; op != nil && op.view.Capability == string(spellcatalog.Stealth) && op.owner == target {
		return op.view.Phase == "waiting_server" || s.now().Before(op.until) && op.view.ServerCastGo
	}
	for _, r := range s.candidatesLocked(spellcatalog.Stealth) {
		if s.now().Before(s.spellReady[r.ID]) || !s.stopLocked(ctx) {
			continue
		}
		fresh, err := s.snapshot(ctx)
		if err != nil || safeSelfCombat(fresh, s.selfGUID) != "" || fresh.Self.UnitFlags&0x80000 != 0 || !s.casterRequirementsLocked(r) {
			return false
		}
		return s.castSupportLocked(ctx, r, s.selfGUID, target)
	}
	return false
}

func behindEnemy(self, target position) bool {
	dx, dy := float64(self.x-target.x), float64(self.y-target.y)
	return dx*math.Cos(float64(target.o))+dy*math.Sin(float64(target.o)) < -0.25
}
func (s *Service) weaponStrikeLocked(p *worldv1.PlayerState, target position) spellcatalog.Spell {
	var best spellcatalog.Spell
	score := float64(-1)
	for _, r := range s.candidatesLocked(spellcatalog.WeaponStrike) {
		cost := spellCost(r, p)
		if r.Family == sinisterStrike {
			cost = uint64(s.state.SinisterStrikeEnergyCost)
		}
		if !p.PowerKnown || int32(p.PowerType) != r.Power || uint64(p.Power) < cost || s.now().Before(s.spellReady[r.ID]) || r.Behind && !behindEnemy(s.pos, target) || r.Attrs[0]&0x20000 != 0 && !s.stealthedLocked() {
			continue
		}
		proof := s.weapons[p.GetMainhand().GetEntry()]
		if r.ItemClass >= 0 && (!proof.valid || int32(proof.class) != r.ItemClass || proof.subclass >= 32 || r.SubclassMask != 0 && r.SubclassMask&(1<<proof.subclass) == 0 || proof.inventory >= 32 || r.InventoryMask != 0 && r.InventoryMask&(1<<proof.inventory) == 0) {
			continue
		}
		amount := float64(0)
		for _, e := range r.Effects {
			if e.Kind != 80 {
				amount += float64(e.Points) + float64(e.Dice)/2
			}
		}
		if amount > score {
			best, score = r, amount
		}
	}
	return best
}
