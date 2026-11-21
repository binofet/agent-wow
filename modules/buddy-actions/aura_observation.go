package buddyactions

import (
	"sort"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// Present aura facts are shared by the toolkit and persistent Lua feed. Their
// serialization order is stable; no patient, aura or dispel rank is preferred.
func (s *Service) observedPlayerAurasLocked(w *worldv1.SnapshotResponse) []*actionsv1.PlayerAuras {
	ids := make([]uint64, 0, len(s.buffAuras))
	for guid := range s.buffAuras {
		if p := observedHealingPlayer(w, guid); p != nil && p.Present {
			ids = append(ids, guid)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	catalog := s.spellData
	if catalog == nil {
		catalog = spellcatalog.Default()
	}
	var out []*actionsv1.PlayerAuras
	for _, guid := range ids {
		set := s.buffAuras[guid]
		view := &actionsv1.PlayerAuras{TargetGuid: guid, Complete: set.complete, Revision: set.revision}
		for slot, aura := range set.slots {
			remaining := aura.remaining(s.now())
			if aura.spell == 0 || aura.durationKnown && remaining == 0 {
				continue
			}
			row := &actionsv1.ObservedAura{Slot: uint32(slot), SpellId: aura.spell, CasterGuid: aura.caster,
				Positive: aura.positive, Negative: aura.negative, DurationKnown: aura.durationKnown,
				RemainingMs: remaining, Revision: aura.revision}
			if spell, ok := catalog.Lookup(aura.spell); ok {
				row.MetadataKnown, row.DispelType = true, uint32(spell.DispelType)
				seen := map[uint32]bool{}
				for _, effect := range spell.Effects {
					if effect.Aura != 0 && !seen[effect.Aura] {
						row.AuraTypes = append(row.AuraTypes, effect.Aura)
						seen[effect.Aura] = true
					}
				}
			}
			view.Auras = append(view.Auras, row)
		}
		out = append(out, view)
	}
	return out
}
