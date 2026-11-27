package buddyactions

import (
	"encoding/binary"
	"errors"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/auraupdate"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// AuraApplication::BuildUpdatePacket and Player::GetAurasForTarget, not a
// guessed spell effect: packed target, slot/spell, flags/level/charges,
// optional packed caster and uint32 maximum/current duration.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Auras/SpellAuras.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
// AFLAG_ANY_EFFECT_AMOUNT_SENT is not produced by that current writer.
type observedBuffAura struct {
	spell                             uint32
	caster                            uint64
	positive, negative, durationKnown bool
	duration                          uint32
	at                                time.Time
	revision                          uint64
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
	guid, slots, err := auraupdate.Decode(p, all)
	if err != nil {
		return 0, nil, err
	}
	out := make([]auraDelta, 0, len(slots))
	for _, slot := range slots {
		// Existing cast/dispellability evidence needs a visible effect on the application.
		effects := slot.Flags&7 != 0
		out = append(out, auraDelta{slot: slot.Index, aura: observedBuffAura{
			spell: slot.SpellID, caster: slot.CasterGUID, positive: effects && slot.Positive(), negative: effects && slot.Negative(),
			durationKnown: slot.HasDuration(), duration: slot.RemainingMS,
		}})
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
		a.at = s.now()
		a.revision = s.buffAuraRevision
		set.slots[v.slot] = a
		for i, op := range [2]*supportAttempt{s.supportOp, s.starterSpells.uncertainStrike} {
			if op == nil || i == 1 && op == s.supportOp || op.view.Capability != "stealth" || op.view.TargetGuid != guid ||
				op.view.SpellId != a.spell || a.caster != s.selfGUID || !a.positive {
				continue
			}
			b := op.starterBinding
			if b == nil || s.starterSelfCastBodyCurrentLocked(op) {
				op.view.EffectObserved = true
				if op.view.ServerCastGo {
					op.view.Phase = "confirmed_aura"
					s.confirmStarterSelfCastLocked(op)
				}
			}
		}
		if op := s.buffOp; op != nil && op.view.Phase == "waiting_server" && op.view.TargetGuid == guid && op.view.SpellId == a.spell && a.positive && a.caster == s.selfGUID && a.revision > op.baseline && (!a.durationKnown || a.duration > 0) {
			op.view.AuraConfirmed = true
			op.view.AuraRevision = a.revision
		}
	}
	s.confirmBuffLocked()
	s.confirmCleanseLocked()
	if guid == s.selfGUID {
		if release := s.followMount.releaseSpell; release != 0 {
			if current, _ := s.groundMountAuraLocked(guid); current != release {
				s.followMount.releaseSpell = 0
			}
		}
		s.retireControlBreaksLocked()
	}
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
		if op == opcode.SMSGNewWorld && !validEncounterWorldEntry(p) || op == opcode.SMSGTransferPending && len(p) != 4 && len(p) != 12 {
			return true, errors.New("malformed aura world lifecycle frame")
		}
		s.clientControlLost, s.clientControlRestoring = false, false
		s.buffAuras = nil
		s.ownDamageAuras = nil
		s.targetProtection.reset()
		s.finishSpellLifecycleLocked("world_transfer_outcome_unconfirmed")
		s.finishCrowdControlLocked("world_transfer_outcome_unconfirmed")
		s.finishItemCreationLocked("map_changed_outcome_unconfirmed")
		if s.buffOp != nil && s.buffOp.view.Phase == "waiting_server" {
			s.buffUnconfirmedLocked("world_transfer_outcome_unconfirmed")
		}
		return false, nil
	case opcode.SMSGDestroyObject:
		if len(p) != 9 {
			return true, errors.New("malformed object destruction frame")
		}
		s.clearOwnDamageAurasLocked(binary.LittleEndian.Uint64(p))
		return true, s.protectedDestroyLocked(p)
	case opcode.SMSGAuraUpdate, opcode.SMSGAuraUpdateAll:
		guid, updates, err := decodeBuffAuras(p, op == opcode.SMSGAuraUpdateAll)
		if err == nil && guid>>48 == 0 {
			s.applyBuffAurasLocked(guid, updates, op == opcode.SMSGAuraUpdateAll)
		}
		if err == nil && guid>>48 == 0xf130 {
			s.observeTargetProtectionLocked(guid, updates, op == opcode.SMSGAuraUpdateAll)
			s.crowdControlAuraLocked(guid, updates, op == opcode.SMSGAuraUpdateAll)
		}
		if err == nil && (guid>>48 == 0xf130 || guid>>48 == 0xf150) {
			s.observeOwnDamageAurasLocked(guid, updates, op == opcode.SMSGAuraUpdateAll)
		}
		return true, err
	}
	return false, nil
}
