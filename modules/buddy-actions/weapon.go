package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"unicode/utf8"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type weaponProof struct {
	entry, class, subclass, inventory uint32
	valid                             bool
}

// Full normal ItemHandler.cpp HandleItemQuerySingleOpcode response framing.
// Names are discarded; weapon proof comes from server class/subclass/type.
func decodeWeaponQuery(data []byte) (weaponProof, error) {
	bad := errors.New("invalid item weapon query")
	if len(data) < 4 || len(data) > 32768 {
		return weaponProof{}, bad
	}
	p := weaponProof{entry: binary.LittleEndian.Uint32(data)}
	if p.entry&0x80000000 != 0 {
		if len(data) != 4 {
			return weaponProof{}, bad
		}
		p.entry &= 0x7fffffff
		return p, nil
	}
	if p.entry == 0 || len(data) < 16 {
		return weaponProof{}, bad
	}
	p.class, p.subclass = binary.LittleEndian.Uint32(data[4:]), binary.LittleEndian.Uint32(data[8:])
	at := 16
	skip := func(n int) bool {
		if n < 0 || n > len(data)-at {
			return false
		}
		at += n
		return true
	}
	text := func(limit int) bool {
		if at > len(data) {
			return false
		}
		end := bytes.IndexByte(data[at:min(len(data), at+limit+1)], 0)
		if end < 0 || !utf8.Valid(data[at:at+end]) {
			return false
		}
		at += end + 1
		return true
	}
	for i := 0; i < 4; i++ {
		if !text(255) {
			return weaponProof{}, bad
		}
	}
	if len(data)-at < 88 {
		return weaponProof{}, bad
	}
	p.inventory = binary.LittleEndian.Uint32(data[at+24:])
	at += 84
	stats := binary.LittleEndian.Uint32(data[at:])
	at += 4
	if stats > 10 || !skip(int(stats)*8+8+24+28+12+120+4) || !text(16384) || !skip(116) || at != len(data) {
		return weaponProof{}, bad
	}
	p.valid = true
	return p, nil
}

func (s *Service) weaponReady(p *worldv1.PlayerState) bool {
	if p == nil || p.Mainhand == nil || !p.Mainhand.SlotKnown || p.Mainhand.ItemGuid == 0 || !p.Mainhand.MetadataKnown || !p.Mainhand.DurabilityKnown || (p.Mainhand.MaxDurability > 0 && p.Mainhand.Durability == 0) {
		return false
	}
	proof, ok := s.weapons[p.Mainhand.Entry]
	return ok && proof.valid && proof.class == 2 && proof.subclass < 32 && uint32(173555)&(1<<proof.subclass) != 0 && (proof.inventory == 13 || proof.inventory == 17 || proof.inventory == 21)
}

func (s *Service) requestWeaponLocked(ctx context.Context, p *worldv1.PlayerState) {
	if p.Mainhand == nil || !p.Mainhand.MetadataKnown || p.Mainhand.Entry == 0 || s.weaponQueries[p.Mainhand.Entry] {
		return
	}
	entry := p.Mainhand.Entry
	if len(s.weaponOrder) >= 16 {
		old := s.weaponOrder[0]
		s.weaponOrder = s.weaponOrder[1:]
		delete(s.weaponQueries, old)
		delete(s.weapons, old)
	}
	s.weaponOrder = append(s.weaponOrder, entry)
	s.weaponQueries[entry] = true
	// Reserve before writing; ambiguous query outcomes never cause a retry loop.
	s.sendLocked(ctx, opcode.CMSGItemQuerySingle, binary.LittleEndian.AppendUint32(nil, entry))
}
