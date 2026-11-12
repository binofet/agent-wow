package buddyinventory

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode"
	"unicode/utf8"
)

var malformed = errors.New("malformed inventory packet")

func itemGUID(g uint64) bool     { return g>>48 == 0x4000 && g&0x0000ffffffffffff != 0 }
func creatureGUID(g uint64) bool { return g>>48 == 0xf130 && g>>24&0xffffff != 0 && g&0xffffff != 0 }
func wireSlot(bag, slot uint32) (byte, byte, bool) {
	if bag == 0 && slot < 16 {
		return 255, byte(23 + slot), true
	}
	if bag >= 1 && bag <= 4 && slot < 36 {
		return byte(18 + bag), byte(slot), true
	}
	return 0, 0, false
}

// Verified ItemPackets.cpp SwapItem: destination bag/slot, then source bag/slot.
func movePacket(sb, ss, db, ds uint32) []byte {
	a, b, ok := wireSlot(sb, ss)
	c, d, yes := wireSlot(db, ds)
	if !ok || !yes || (sb == db && ss == ds) {
		return nil
	}
	return []byte{c, d, a, b}
}
func equipPacket(g uint64, slot uint32) []byte {
	if !itemGUID(g) || slot < 19 || slot > 22 {
		return nil
	}
	return append(binary.LittleEndian.AppendUint64(nil, g), byte(slot))
}

// Verified ItemPackets.cpp SellItem: raw vendor GUID, item GUID and uint32 count.
func sellPacket(v, g uint64, count uint32) []byte {
	if !creatureGUID(v) || !itemGUID(g) || count == 0 {
		return nil
	}
	p := binary.LittleEndian.AppendUint64(nil, v)
	p = binary.LittleEndian.AppendUint64(p, g)
	return binary.LittleEndian.AppendUint32(p, count)
}

type inventoryFailure struct {
	kind                         string
	code                         uint8
	itemGUID1, itemGUID2         uint64
	requiredLevel, limitCategory uint32
}

// SellItem server messages are errors, never positive success acknowledgements.
func sellFailure(p []byte) (uint64, uint64, uint8, error) {
	if len(p) != 17 {
		return 0, 0, 0, malformed
	}
	v, g, c := binary.LittleEndian.Uint64(p), binary.LittleEndian.Uint64(p[8:]), p[16]
	if (v != 0 && !creatureGUID(v)) || (g != 0 && !itemGUID(g)) || c > 7 {
		return 0, 0, 0, malformed
	}
	return v, g, c, nil
}
func parseItemName(data []byte) (uint32, string, error) {
	if len(data) < 4 || len(data) > 65536 {
		return 0, "", malformed
	}
	entry := binary.LittleEndian.Uint32(data)
	if entry&0x80000000 != 0 {
		if len(data) == 4 {
			return entry & 0x7fffffff, "", nil
		}
		return 0, "", malformed
	}
	if entry == 0 || len(data) < 16+1+3+84 {
		return 0, "", malformed
	}
	end := bytes.IndexByte(data[16:], 0)
	if end < 1 || end > 255 || len(data) < 16+end+1+3+84 {
		return 0, "", malformed
	}
	name := string(data[16 : 16+end])
	if !utf8.ValidString(name) {
		return 0, "", malformed
	}
	for _, c := range name {
		if unicode.IsControl(c) || c == '|' {
			return 0, "", malformed
		}
	}
	return entry, name, nil
}
func parseInventoryChangeFailure(p []byte) (inventoryFailure, error) {
	if len(p) == 0 {
		return inventoryFailure{}, malformed
	}
	r := inventoryFailure{kind: "inventory_change_failure", code: p[0]}
	if r.code == 0 {
		if len(p) == 1 {
			return r, nil
		}
		return inventoryFailure{}, malformed
	}
	if r.code > 89 || r.code == 74 || r.code == 83 {
		return inventoryFailure{}, malformed
	}
	size := 18
	switch r.code {
	case 1, 87, 84, 85, 89:
		size = 22
	case 81:
		size = 38
	}
	if len(p) != size || p[17] != 0 {
		return inventoryFailure{}, malformed
	}
	r.itemGUID1, r.itemGUID2 = binary.LittleEndian.Uint64(p[1:]), binary.LittleEndian.Uint64(p[9:])
	if r.itemGUID1 != 0 && !itemGUID(r.itemGUID1) || r.itemGUID2 != 0 && !itemGUID(r.itemGUID2) {
		return inventoryFailure{}, malformed
	}
	switch r.code {
	case 1, 87:
		r.requiredLevel = binary.LittleEndian.Uint32(p[18:])
		if r.requiredLevel > 80 {
			return inventoryFailure{}, malformed
		}
	case 84, 85, 89:
		r.limitCategory = binary.LittleEndian.Uint32(p[18:])
	case 81:
		for _, b := range p[18:] {
			if b != 0 {
				return inventoryFailure{}, malformed
			}
		}
	}
	return r, nil
}
