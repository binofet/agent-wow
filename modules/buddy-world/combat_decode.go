package buddyworld

import (
	"encoding/binary"
	"errors"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// combatPackedGUID also checks canonical encoding: a mask bit must carry a
// nonzero byte. Zero is valid only for optional cast-item/explicit targets.
func combatPackedGUID(data []byte) (uint64, int, error) {
	c := cursor{b: data}
	mask := c.u8()
	var guid uint64
	for i := uint(0); i < 8; i++ {
		if mask&(1<<i) != 0 {
			value := c.u8()
			if value == 0 {
				c.err = errLayout
			}
			guid |= uint64(value) << (8 * i)
		}
	}
	return guid, c.off, c.err
}

func combatReadGUID(c *cursor) uint64 {
	if c.err != nil {
		return 0
	}
	guid, n, err := combatPackedGUID(c.b[c.off:])
	c.off += n
	if err != nil {
		c.err = err
	}
	return guid
}

// Only ordinary server combat packets are admitted. No chat, credentials,
// outgoing request, client prediction, or sampled health delta enters this log.
func decodeCombatEvent(op uint32, payload []byte, self uint64) *worldv1.CombatEvent {
	event := &worldv1.CombatEvent{Kind: "unknown"}
	var err error
	switch op {
	case opcode.SMSGAttackerstateupdate:
		event.SourceGuid, event.TargetGuid, event.Amount, err = combatMeleeDamage(payload)
		event.Kind, event.AmountKnown = "melee_damage", true
	case opcode.SMSGSpellnonmeleedamagelog:
		event.SourceGuid, event.TargetGuid, event.SpellId, event.Amount, err = combatSpellDamage(payload)
		event.Kind, event.AmountKnown = "spell_damage", true
	case opcode.SMSGSpellheallog:
		event.TargetGuid, event.SourceGuid, event.SpellId, event.Amount, err = combatHeal(payload)
		event.Kind, event.AmountKnown = "heal", true
	case opcode.SMSGPartykilllog:
		if len(payload) != 16 {
			err = errLayout
			break
		}
		event.SourceGuid, event.TargetGuid = binary.LittleEndian.Uint64(payload), binary.LittleEndian.Uint64(payload[8:])
		if event.SourceGuid == 0 || event.TargetGuid == 0 {
			err = errLayout
			break
		}
		event.Kind, event.Detail = "death", "party_kill_log"
	case opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther:
		c := cursor{b: payload}
		event.SourceGuid = self
		if op != opcode.SMSGCastFailed {
			event.SourceGuid = combatReadGUID(&c)
		}
		c.u8() // cast count, retained in raw payload
		event.SpellId = c.u32()
		event.Result = uint32(c.u8())
		// CAST_FAILED may append result-specific uint32 parameters. Prefix facts
		// remain explicit; do not reinterpret those parameters as targets/effects.
		if c.err != nil || event.SourceGuid == 0 || event.SpellId == 0 {
			err = errLayout
			break
		}
		event.Kind, event.ResultKnown = "cast_failure", true
		event.Detail = "server_cast_rejection"
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		c := cursor{b: payload}
		combatReadGUID(&c) // item or unit that supplied the spell
		event.SourceGuid = combatReadGUID(&c)
		c.u8() // cast count
		event.SpellId = c.u32()
		c.u32() // cast flags
		c.u32() // START duration, or GO server clock
		if op == opcode.SMSGSpellGo {
			for hits := c.u8(); hits != 0 && c.err == nil; hits-- {
				c.u64()
			}
			for misses := c.u8(); misses != 0 && c.err == nil; misses-- {
				c.u64()
				if c.u8() == 11 {
					c.u8()
				} // reflected result
			}
		}
		mask := c.u32()
		const objectTargets = 0x2 | 0x200 | 0x800 | 0x8000 | 0x10000
		if mask&objectTargets != 0 {
			event.TargetGuid = combatReadGUID(&c)
		}
		if c.err != nil || event.SourceGuid == 0 || event.SpellId == 0 {
			err = errLayout
			break
		}
		event.Kind, event.Detail = "cast_start", "server_cast_start; explicit_target_prefix"
		if op == opcode.SMSGSpellGo {
			event.Kind, event.Detail = "cast_go", "server_launch_only; explicit_target_prefix; not_effect_confirmation"
		}
	case opcode.SMSGAttackstart, opcode.SMSGAttackstop,
		opcode.SMSGPeriodicauralog, opcode.SMSGSpelldamageshield,
		opcode.SMSGSpellinstakilllog, opcode.SMSGCombatEventFailed:
		event.Detail = "unsupported_combat_packet"
	default:
		return nil
	}
	if err != nil {
		// Partial decoder fields must never masquerade as positive evidence.
		return &worldv1.CombatEvent{Kind: "unknown", Malformed: true, Detail: err.Error()}
	}
	return event
}

// Unit::SendHealSpellLog serializes target, caster, spell, heal, overheal,
// absorbed, critical, debug. Effective healing is heal minus overheal after
// absorption. This shares the tested layout used by the action receipt harness.
func combatHeal(data []byte) (target, caster uint64, spell, effective uint32, err error) {
	bad := errors.New("invalid ordinary heal log")
	target, n, e := combatPackedGUID(data)
	if e != nil || target == 0 {
		return 0, 0, 0, 0, bad
	}
	caster, m, e := combatPackedGUID(data[n:])
	if e != nil || caster == 0 {
		return 0, 0, 0, 0, bad
	}
	tail := data[n+m:]
	if len(tail) != 18 || tail[16] > 1 || tail[17] != 0 {
		return 0, 0, 0, 0, bad
	}
	spell = binary.LittleEndian.Uint32(tail)
	heal, overheal := binary.LittleEndian.Uint32(tail[4:]), binary.LittleEndian.Uint32(tail[8:])
	if spell == 0 || overheal > heal {
		return 0, 0, 0, 0, bad
	}
	return target, caster, spell, heal - overheal, nil
}
