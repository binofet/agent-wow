package buddyactions

import (
	"encoding/binary"
	"errors"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"time"
)

// AuraApplication::BuildUpdatePacket and Player::GetAurasForTarget, not a
// guessed spell effect: packed target, slot/spell, flags/level/charges,
// optional packed caster and uint32 maximum/current duration.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Auras/SpellAuras.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
// AFLAG_ANY_EFFECT_AMOUNT_SENT is not produced by that current writer.
type observedBuffAura struct {
	spell                   uint32
	caster                  uint64
	positive, durationKnown bool
	duration                uint32
	at                      time.Time
	revision                uint64
}

func (a observedBuffAura) remaining(now time.Time) uint32 {
	if !a.durationKnown {
		return 0
	}
	age := now.Sub(a.at)
	if age < 0 || age >= time.Duration(a.duration)*time.Millisecond {
		return 0
	}
	return a.duration - uint32(age/time.Millisecond)
}

type buffAuraSet struct {
	slots        [255]observedBuffAura
	complete     bool
	revision     uint64
	fullRevision uint64
	at           time.Time
}
type auraDelta struct {
	slot uint8
	aura observedBuffAura
}

func decodeBuffAuras(p []byte, all bool) (uint64, []auraDelta, error) {
	bad := errors.New("malformed complete aura update")
	guid, n, err := readGUID(p)
	if err != nil || guid == 0 {
		return 0, nil, bad
	}
	p = p[n:]
	var out []auraDelta
	seen := map[byte]bool{}
	for len(p) > 0 {
		if len(out) >= 255 || len(p) < 5 || p[0] == 255 || seen[p[0]] {
			return 0, nil, bad
		}
		slot := p[0]
		seen[slot] = true
		spell := binary.LittleEndian.Uint32(p[1:])
		p = p[5:]
		a := observedBuffAura{spell: spell}
		if spell != 0 {
			if spell > 0x7fffffff || len(p) < 3 {
				return 0, nil, bad
			}
			flags := p[0]
			p = p[3:]
			if flags&0x40 != 0 || flags&0x90 == 0x90 {
				return 0, nil, bad
			}
			a.positive = flags&0x10 != 0 && flags&7 != 0
			if flags&8 != 0 {
				a.caster = guid
			} else {
				caster, m, e := readGUID(p)
				if e != nil {
					return 0, nil, bad
				}
				a.caster = caster
				p = p[m:]
			}
			if flags&0x20 != 0 {
				if len(p) < 8 {
					return 0, nil, bad
				}
				max := binary.LittleEndian.Uint32(p)
				duration := binary.LittleEndian.Uint32(p[4:])
				if max == 0 || max > 0x7fffffff || duration > max {
					return 0, nil, bad
				}
				a.durationKnown = true
				a.duration = duration
				p = p[8:]
			}
		} else if all {
			return 0, nil, bad
		}
		out = append(out, auraDelta{slot: slot, aura: a})
	}
	if !all && len(out) != 1 {
		return 0, nil, bad
	}
	return guid, out, nil
}
func (s *Service) applyBuffAurasLocked(guid uint64, updates []auraDelta, all bool) {
	if s.buffAuras == nil {
		s.buffAuras = make(map[uint64]*buffAuraSet)
	}
	set := s.buffAuras[guid]
	if set == nil && len(s.buffAuras) >= 64 {
		var oldest uint64
		var at time.Time
		for g, v := range s.buffAuras {
			if g == s.selfGUID || s.buffOp != nil && g == s.buffOp.view.TargetGuid {
				continue
			}
			if oldest == 0 || v.at.Before(at) {
				oldest, at = g, v.at
			}
		}
		if oldest == 0 {
			return
		}
		delete(s.buffAuras, oldest)
	}
	if set == nil || all {
		set = &buffAuraSet{complete: all}
		s.buffAuras[guid] = set
	}
	s.buffAuraRevision++
	set.revision = s.buffAuraRevision
	if all {
		set.fullRevision = s.buffAuraRevision
	}
	set.at = s.now()
	for _, v := range updates {
		a := v.aura
		prior := set.slots[v.slot]
		if prior.spell != 0 && prior.spell != a.spell {
			family := buffMetadata(prior.spell).family
			if family == 0 {
				for _, f := range []uint32{168, 687} {
					if strongerArmorAura(prior.spell, f) {
						family = f
					}
				}
			}
			if b := s.buffBindings[buffKey{guid, family}]; b != nil && b.bouncedSpell != 0 {
				b.blocked = false
				b.bouncedSpell = 0
			}
		}
		a.at = s.now()
		a.revision = s.buffAuraRevision
		set.slots[v.slot] = a
		if op := s.buffOp; op != nil && op.view.Phase == "waiting_server" && op.view.TargetGuid == guid && op.view.SpellId == a.spell && a.positive && a.caster == s.selfGUID && a.revision > op.baseline && (!a.durationKnown || a.duration > 0) {
			op.view.AuraConfirmed = true
			op.view.AuraRevision = a.revision
		}
	}
	s.confirmBuffLocked()
}
func (s *Service) pruneBuffAurasLocked(w *worldv1.SnapshotResponse) {
	if w == nil || w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present {
		s.buffAuras = nil
		return
	}
	for guid := range s.buffAuras {
		p := observedHealingPlayer(w, guid)
		if p == nil || !p.Present || !p.MapKnown || !w.Self.MapKnown || p.MapId != w.Self.MapId {
			delete(s.buffAuras, guid)
		}
	}
}
func (s *Service) auraPacketLocked(op uint32, p []byte) (bool, error) {
	switch op {
	case opcode.SMSGTransferPending, opcode.SMSGNewWorld:
		if op == opcode.SMSGNewWorld && len(p) != 20 || op == opcode.SMSGTransferPending && len(p) != 4 && len(p) != 12 {
			return true, errors.New("malformed aura world lifecycle frame")
		}
		s.buffAuras = nil
		if s.buffOp != nil && s.buffOp.view.Phase == "waiting_server" {
			s.buffUnconfirmedLocked("world_transfer_outcome_unconfirmed")
		}
		return false, nil
	case opcode.SMSGAuraUpdate, opcode.SMSGAuraUpdateAll:
		guid, updates, err := decodeBuffAuras(p, op == opcode.SMSGAuraUpdateAll)
		if err == nil && guid>>48 == 0 {
			s.applyBuffAurasLocked(guid, updates, op == opcode.SMSGAuraUpdateAll)
		}
		return true, err
	}
	return false, nil
}
