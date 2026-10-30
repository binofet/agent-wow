package buddyloot

import (
	"encoding/binary"
	"errors"
)

// Primary protocol: AzerothCore Player.cpp SendLoot/SendLootError,
// LootMgr.cpp operator<<(LootView), PlayerStorage.cpp SendNewItem,
// LootHandler.cpp HandleLootOpcode/HandleAutostoreLootItemOpcode.
// https://github.com/azerothcore/azerothcore-wotlk/tree/master/src/server/game
type lootItem struct {
	slot      byte
	id, count uint32
	slotType  byte
}
type lootWindow struct {
	guid      uint64
	errorCode byte
	failed    bool
	gold      uint32
	items     []lootItem
}

func guidPacket(guid uint64) []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint64(p, guid)
	return p
}
func decodeWindow(p []byte) (lootWindow, error) {
	var result lootWindow
	if len(p) < 9 {
		return result, errors.New("short loot response")
	}
	result.guid = binary.LittleEndian.Uint64(p)
	if p[8] == 0 {
		if len(p) != 10 {
			return result, errors.New("invalid loot error")
		}
		result.failed = true
		result.errorCode = p[9]
		return result, nil
	}
	if p[8] != 1 || len(p) < 14 {
		return result, errors.New("unsupported loot response")
	}
	result.gold = binary.LittleEndian.Uint32(p[9:])
	count := int(p[13])
	if count > 64 || len(p) != 14+count*22 {
		return result, errors.New("invalid loot item count")
	}
	seen := map[byte]bool{}
	for i := 0; i < count; i++ {
		row := p[14+i*22:]
		item := lootItem{row[0], binary.LittleEndian.Uint32(row[1:]), binary.LittleEndian.Uint32(row[5:]), row[21]}
		if seen[item.slot] || item.id == 0 || item.count == 0 || item.slotType > 4 {
			return result, errors.New("invalid loot item")
		}
		seen[item.slot] = true
		result.items = append(result.items, item)
	}
	return result, nil
}

type itemPush struct {
	player            uint64
	received, created uint32
	id, count, total  uint32
}

func decodePush(p []byte) (itemPush, error) {
	if len(p) != 45 {
		return itemPush{}, errors.New("invalid item push size")
	}
	r := itemPush{binary.LittleEndian.Uint64(p), binary.LittleEndian.Uint32(p[8:]), binary.LittleEndian.Uint32(p[12:]), binary.LittleEndian.Uint32(p[25:]), binary.LittleEndian.Uint32(p[37:]), binary.LittleEndian.Uint32(p[41:])}
	if r.received > 1 || r.created > 1 || r.id == 0 || r.count == 0 || r.total < r.count {
		return itemPush{}, errors.New("invalid item push")
	}
	return r, nil
}
