package buddyactions

import (
	"context"
	"encoding/binary"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

const (
	fearBreakCapability = "fear_break"
	fearAura            = 7
	fleeingUnitFlag     = 0x00800000
	pvpBreakSpell       = 42292
	humanBreakSpell     = 59752
)

func (s *Service) hasObservedFearLocked() bool {
	if set := s.buffAuras[s.selfGUID]; set != nil {
		for _, aura := range set.slots {
			if !aura.negative || aura.durationKnown && aura.remaining(s.now()) == 0 {
				continue
			}
			if s.spellData == nil {
				s.spellData = spellcatalog.Default()
			}
			spell, ok := s.spellData.Lookup(aura.spell)
			if !ok {
				continue
			}
			for _, effect := range spell.Effects {
				if effect.Kind == 6 && effect.Aura == fearAura {
					return true
				}
			}
		}
	}
	s.fearBreakAttempted = false
	return false
}

func fearBreakSpell(spell spellcatalog.Spell) bool {
	const purgesImmuneAuras = 0x8000
	if !spell.TimingKnown || spell.CastMs != 0 || spell.Cost != 0 || spell.CostPerLevel != 0 || spell.CostPercent != 0 || spell.PerSecond != 0 ||
		spell.Stances != 0 || spell.ItemClass != -1 || spell.Attrs[0]&0x40 != 0 ||
		spell.Attrs[1]&purgesImmuneAuras == 0 {
		return false
	}
	for _, effect := range spell.Effects {
		if effect.Kind == 6 && effect.A == 1 && effect.B == 0 && effect.Aura == 77 &&
			(effect.Misc == 5 || (spell.ID == humanBreakSpell || spell.ID == pvpBreakSpell) && effect.Misc == 1) {
			return true
		}
	}
	return false
}

func fearBreakTrinket(w *worldv1.SnapshotResponse) *worldv1.EquipmentSlotState {
	if !w.GetSelfEquipmentKnown() {
		return nil
	}
	for _, item := range w.SelfEquipment {
		if item.GetSlot() != 13 && item.GetSlot() != 14 || !item.GetSlotKnown() || !item.GetMetadataKnown() ||
			item.GetItemGuid()>>48 != 0x4000 || item.GetStackCount() != 1 {
			continue
		}
		if gamecatalog.TrinketHasUseSpell(item.GetEntry(), pvpBreakSpell) {
			return item
		}
	}
	return nil
}

// An instant self-targeted fear break is legal even during server-controlled
// movement. Never send a stop, facing or movement packet to prepare it.
func (s *Service) breakControlLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if !s.hasObservedFearLocked() || s.closed || s.fearBreakAttempted || s.state.CastPending {
		return false
	}
	self := w.GetSelf()
	if self.GetGuid() != s.selfGUID || !self.GetPresent() || !self.GetAliveKnown() || !self.GetAlive() ||
		!self.GetHealthKnown() || self.GetHealth() == 0 || !self.GetUnitFlagsKnown() || self.GetUnitFlags()&fleeingUnitFlag == 0 {
		return false
	}
	now := s.now()
	var best spellcatalog.Spell
	for _, spell := range s.learnedContractsLocked() {
		if spell.ID == pvpBreakSpell || now.Before(s.spellReady[spell.ID]) || !fearBreakSpell(spell) ||
			spell.Category != 0 && now.Before(s.fearBreakReady) {
			continue
		}
		if best.ID == 0 || spell.CooldownMs < best.CooldownMs && spell.Category == 0 {
			best = spell
		}
	}
	var item *worldv1.EquipmentSlotState
	if best.ID == 0 && !now.Before(s.fearBreakReady) {
		item = fearBreakTrinket(w)
		if item != nil {
			best, _ = s.spellData.Lookup(pvpBreakSpell)
		}
	}
	if best.ID == 0 {
		return false
	}
	if item != nil || best.ID == humanBreakSpell {
		s.fearBreakReady = now.Add(2 * time.Minute)
	} else if best.CategoryCooldownMs > 0 {
		s.fearBreakReady = now.Add(time.Duration(best.CategoryCooldownMs) * time.Millisecond)
	}
	s.fearBreakAttempted = true
	return s.castSupportPacketLocked(ctx, best, s.selfGUID, s.selfGUID, fearBreakCapability, item)
}

func fearBreakItemPacket(item *worldv1.EquipmentSlotState, count byte, spell uint32) []byte {
	// Concrete equipped inventory position, followed by the normal item use
	// header. A zero spell target mask uses the item spell's implicit self.
	p := make([]byte, 24)
	p[0], p[1], p[2] = 255, byte(item.Slot), count
	binary.LittleEndian.PutUint32(p[3:], spell)
	binary.LittleEndian.PutUint64(p[7:], item.ItemGuid)
	return p
}
