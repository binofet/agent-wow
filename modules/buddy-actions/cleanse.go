package buddyactions

import (
	"encoding/binary"
	"errors"

	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

func (s *Service) observedDispelMask(a observedBuffAura) uint32 {
	if a.spell == 0 || !a.negative || a.durationKnown && a.remaining(s.now()) == 0 {
		return 0
	}
	r, ok := s.spellData.Lookup(a.spell)
	if !ok || r.DispelType < spellcatalog.DispelMagic || r.DispelType > spellcatalog.DispelPoison {
		return 0
	}
	return 1 << r.DispelType
}

// Spell::EffectDispel sends packed victim and caster GUIDs, the dispel spell,
// one zero byte, a uint32 count, then five bytes per affected aura.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/SpellEffects.cpp
func (s *Service) observeCleanseLocked(data []byte) error {
	op := s.supportOp
	if op == nil || op.cleanseAura == 0 || !s.now().Before(op.until) ||
		(op.view.Phase != "waiting_server" && op.view.Phase != "waiting_cleanse_effect" && op.view.Phase != "transport_unconfirmed_no_retry") {
		return nil
	}
	bad := errors.New("malformed spell dispel log")
	target, n, err := readGUID(data)
	if err != nil {
		return bad
	}
	caster, m, err := readGUID(data[n:])
	if err != nil {
		return bad
	}
	data = data[n+m:]
	if len(data) < 9 {
		return bad
	}
	if caster != s.selfGUID || target != op.view.TargetGuid || binary.LittleEndian.Uint32(data) != op.view.SpellId {
		return nil
	}
	count := binary.LittleEndian.Uint32(data[5:])
	rows := data[9:]
	if data[4] != 0 || count == 0 || len(rows)%5 != 0 || uint64(count) != uint64(len(rows)/5) {
		return bad
	}
	matched := false
	observed := map[uint32]bool{}
	for off := 0; off < len(rows); off += 5 {
		entry := binary.LittleEndian.Uint32(rows[off:])
		if entry == 0 || entry >= 1<<31 || rows[off+4] > 1 {
			return bad
		}
		if op.cleanseEligible[entry] {
			matched, observed[entry] = true, true
		}
	}
	if matched {
		op.cleanseLogObserved = true
		for spell := range observed {
			op.cleanseLogs[spell] = true
		}
		s.confirmCleanseLocked()
	}
	return nil
}

func (s *Service) confirmCleanseLocked() {
	op := s.supportOp
	if op == nil || op.cleanseAura == 0 || !op.cleanseLogObserved || !s.now().Before(op.until) {
		return
	}
	set := s.buffAuras[op.view.TargetGuid]
	if set == nil {
		return // Missing observations never prove removal.
	}
	for slot, before := range op.cleanseBaseline {
		current := set.slots[slot]
		changed := current.revision > before.revision || set.complete && set.fullRevision > before.revision
		if op.cleanseLogs[before.spell] && changed && (current.spell != before.spell || current.caster != before.caster) {
			op.view.EffectObserved = true
			if op.view.ServerCastGo {
				op.view.Phase = "confirmed_effect"
			}
			return
		}
	}
}
