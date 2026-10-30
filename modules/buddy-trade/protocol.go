package buddytrade

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode"
	"unicode/utf8"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
)

var malformed = errors.New("malformed trade packet")

func u32(value uint32) []byte { return binary.LittleEndian.AppendUint32(nil, value) }
func u64(value uint64) []byte { return binary.LittleEndian.AppendUint64(nil, value) }

// AzerothCore TradeHandler.cpp, 3.3.5: status has a fixed status-specific tail.
func parseStatus(data []byte) (uint32, uint64, error) {
	if len(data) < 4 {
		return 0, 0, malformed
	}
	code := binary.LittleEndian.Uint32(data)
	size := 4
	switch code {
	case 1:
		size = 12
	case 2:
		size = 8
	case 12:
		size = 13
	case 22, 23:
		size = 5
	}
	if code > 23 || code == 13 || len(data) != size {
		return 0, 0, malformed
	}
	var guid uint64
	if code == 1 {
		guid = binary.LittleEndian.Uint64(data[4:])
		if guid == 0 || guid>>48 != 0 {
			return 0, 0, malformed
		}
	}
	return code, guid, nil
}

// Extended trade data has seven fixed 73-byte slots, including the nontraded
// enchantment slot. Only the first six slots can transfer items.
func parseExtended(data []byte) (bool, uint32, uint32, []*tradev1.Item, error) {
	if len(data) != 532 || data[0] > 1 || binary.LittleEndian.Uint32(data[5:]) != 7 || binary.LittleEndian.Uint32(data[9:]) != 7 {
		return false, 0, 0, nil, malformed
	}
	copper, spell := binary.LittleEndian.Uint32(data[13:]), binary.LittleEndian.Uint32(data[17:])
	items := make([]*tradev1.Item, 0, 7)
	seen := [7]bool{}
	for at := 21; at < len(data); at += 73 {
		slot := data[at]
		if slot > 6 || seen[slot] {
			return false, 0, 0, nil, malformed
		}
		seen[slot] = true
		entry, count := binary.LittleEndian.Uint32(data[at+1:]), binary.LittleEndian.Uint32(data[at+9:])
		if (entry == 0) != (count == 0) {
			return false, 0, 0, nil, malformed
		}
		if entry != 0 {
			items = append(items, &tradev1.Item{Slot: uint32(slot), Entry: entry, Count: count, MetadataKnown: true})
		}
	}
	return data[0] == 1, copper, spell, items, nil
}

// Names are descriptive only; every item mutation separately binds the live
// server-owned inventory GUID, revision and stack. Never accept WoW markup.
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
