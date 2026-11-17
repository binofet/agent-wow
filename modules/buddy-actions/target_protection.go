package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

const maxProtectedAuras = 256

type protectedSlot struct {
	target uint64
	slot   uint8
}

// Separate from the last cast receipt: this is the local client's current
// positive observation, including effects cast by other players.
type targetProtection struct {
	auras    map[protectedSlot]observedBuffAura
	revision uint64
	overflow bool
}

func (p *targetProtection) expire(now time.Time) {
	for key, aura := range p.auras {
		if aura.durationKnown && !now.Before(aura.at.Add(time.Duration(aura.duration)*time.Millisecond)) {
			delete(p.auras, key)
			p.revision++
		}
	}
}

func (p *targetProtection) forget(target uint64) bool {
	changed := false
	for key := range p.auras {
		if key.target == target {
			delete(p.auras, key)
			changed = true
		}
	}
	return changed
}

func (p *targetProtection) reset() {
	p.auras, p.overflow = nil, false
	p.revision++
}

func (p *targetProtection) blocks(target uint64, now time.Time) bool {
	p.expire(now)
	if p.overflow {
		return true
	}
	for key := range p.auras {
		if key.target == target {
			return true
		}
	}
	return false
}

func (p *targetProtection) snapshot(out *actionsv1.State, now time.Time) {
	p.expire(now)
	out.ProtectedTargetRevision = p.revision + 1 // Zero means the older API.
	out.TargetProtectionOverflow = p.overflow
	for key, aura := range p.auras {
		spell, _ := spellcatalog.Default().Lookup(aura.spell)
		remaining := aura.remaining(now)
		if now.Before(aura.at) {
			remaining = aura.duration
		}
		out.ProtectedTargets = append(out.ProtectedTargets, &actionsv1.ProtectedTarget{
			TargetGuid: key.target, CasterGuid: aura.caster, SpellId: aura.spell, Slot: uint32(key.slot),
			DurationKnown: aura.durationKnown, RemainingMs: remaining, Revision: aura.revision,
			Capability: string(spell.Capability().Kind),
		})
	}
	sort.Slice(out.ProtectedTargets, func(i, j int) bool {
		a, b := out.ProtectedTargets[i], out.ProtectedTargets[j]
		if a.TargetGuid != b.TargetGuid {
			return a.TargetGuid < b.TargetGuid
		}
		return a.Slot < b.Slot
	})
}

func (s *Service) observeTargetProtectionLocked(target uint64, updates []auraDelta, all bool) {
	if s.closed || target&0xffffff == 0 {
		return
	}
	p, now := &s.targetProtection, s.now()
	p.expire(now)
	changed := all && p.forget(target)
	for _, update := range updates {
		key, aura := protectedSlot{target, update.slot}, update.aura
		if _, exists := p.auras[key]; exists {
			delete(p.auras, key) // A same-slot replacement/removal is evidence.
			changed = true
		}
		if !aura.negative || aura.caster == 0 || aura.caster>>48 != 0 || aura.durationKnown && aura.duration == 0 {
			continue
		}
		spell, ok := spellcatalog.Default().Lookup(aura.spell)
		if !ok || !spell.Capability().Kind.BreaksOnDamage() {
			continue
		}
		if len(p.auras) >= maxProtectedAuras {
			// Don't silently evict a still-controlled target and permit damage.
			changed = changed || !p.overflow
			p.overflow = true
			continue
		}
		if p.auras == nil {
			p.auras = make(map[protectedSlot]observedBuffAura)
		}
		aura.at, aura.revision = now, p.revision+2
		p.auras[key] = aura
		changed = true
	}
	if changed {
		p.revision++
	}
}

func (s *Service) protectedDestroyLocked(payload []byte) error {
	if len(payload) != 9 {
		return errors.New("malformed destroy-object frame")
	}
	guid := binary.LittleEndian.Uint64(payload)
	if guid == s.selfGUID {
		s.targetProtection.reset()
	}
	if s.targetProtection.forget(guid) {
		s.targetProtection.revision++
	}
	if op := s.pendingCrowdControlLocked(); op != nil && (guid == s.selfGUID || guid == op.view.TargetGuid) {
		s.finishCrowdControlLocked("target_lost_outcome_unconfirmed")
	}
	return nil
}

func (s *Service) blockProtectedOffenseLocked(ctx context.Context, target uint64) bool {
	if !s.targetProtection.blocks(target, s.now()) {
		return false
	}
	s.state.Reason = "observed_target_protection_blocks_offense"
	if s.autoTarget != 0 && (s.autoTarget == target || s.targetProtection.overflow) {
		if s.encounterSwing.target != 0 {
			s.stopEncounterMeleeLocked(ctx)
		} else {
			s.stopAutoAttackLocked(ctx)
		}
	}
	return true
}
