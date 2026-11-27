// Package auraupdate decodes the build 12340 visible aura slot records.
package auraupdate

import (
	"encoding/binary"
	"errors"
)

var errLayout = errors.New("malformed visible aura update")

// Slot holds the fields carried by a single visible aura application. SpellID
// zero clears the slot. StacksOrCharges is the one count displayed by the client.
type Slot struct {
	Index, Flags, CasterLevel, StacksOrCharges uint8
	SpellID, MaxDurationMS, RemainingMS        uint32
	CasterGUID                                 uint64
}

func (s Slot) Positive() bool    { return s.Flags&0x10 != 0 }
func (s Slot) Negative() bool    { return s.Flags&0x80 != 0 }
func (s Slot) HasDuration() bool { return s.Flags&0x20 != 0 }

// Decode consumes the complete SMSG_AURA_UPDATE or SMSG_AURA_UPDATE_ALL payload.
// See AuraApplication::BuildUpdatePacket and Player::GetAurasForTarget in AzerothCore 49bdb66.
func Decode(p []byte, all bool) (uint64, []Slot, error) {
	target, n, ok := packedGUID(p)
	if !ok || target == 0 {
		return 0, nil, errLayout
	}
	p = p[n:]
	var slots []Slot
	var seen [255]bool
	for len(p) > 0 {
		if len(p) < 5 || p[0] == 255 || seen[p[0]] || !all && len(slots) != 0 {
			return 0, nil, errLayout
		}
		slot := Slot{Index: p[0], SpellID: binary.LittleEndian.Uint32(p[1:])}
		seen[slot.Index] = true
		p = p[5:]
		if slot.SpellID == 0 {
			if all {
				return 0, nil, errLayout
			}
		} else {
			if slot.SpellID > 0x7fffffff || len(p) < 3 {
				return 0, nil, errLayout
			}
			slot.Flags, slot.CasterLevel, slot.StacksOrCharges = p[0], p[1], p[2]
			p = p[3:]
			// The pinned writer sends no effect amounts. Opposed disposition
			// flags cannot describe one application.
			if slot.Flags&0x40 != 0 || slot.Flags&0x90 == 0x90 {
				return 0, nil, errLayout
			}
			if slot.Flags&8 != 0 {
				slot.CasterGUID = target
			} else {
				slot.CasterGUID, n, ok = packedGUID(p)
				if !ok {
					return 0, nil, errLayout
				}
				p = p[n:]
			}
			if slot.HasDuration() {
				if len(p) < 8 {
					return 0, nil, errLayout
				}
				slot.MaxDurationMS = binary.LittleEndian.Uint32(p)
				slot.RemainingMS = binary.LittleEndian.Uint32(p[4:])
				if slot.MaxDurationMS == 0 || slot.MaxDurationMS > 0x7fffffff || slot.RemainingMS > slot.MaxDurationMS {
					return 0, nil, errLayout
				}
				p = p[8:]
			}
		}
		slots = append(slots, slot)
	}
	if !all && len(slots) != 1 {
		return 0, nil, errLayout
	}
	return target, slots, nil
}

func packedGUID(p []byte) (uint64, int, bool) {
	if len(p) == 0 {
		return 0, 0, false
	}
	var guid uint64
	n := 1
	for i := uint(0); i < 8; i++ {
		if p[0]&(1<<i) == 0 {
			continue
		}
		if n >= len(p) {
			return 0, 0, false
		}
		guid |= uint64(p[n]) << (8 * i)
		n++
	}
	return guid, n, true
}
