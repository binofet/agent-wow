package buddysupplies

import (
	"encoding/binary"
	"errors"

	suppliesv1 "github.com/agent-wow/agent-wow/modules/buddy-supplies/api"
)

var malformedSuppliesPacket = errors.New("malformed supplies packet")

func creatureGUID(g uint64) bool {
	return g>>48 == 0xf130 && g>>24&0xffffff != 0 && g&0xffffff != 0
}

func itemGUID(g uint64) bool   { return g>>48 == 0x4000 && g&0x0000ffffffffffff != 0 }
func itemEntry(id uint32) bool { return id != 0 && id <= 0x7fffffff }

// ItemPackets.cpp/.h: ListInventory is a raw GUID. BuyItem is raw GUID,
// u32 item, u32 one-based slot, u32 bundle count, u8 unknown (zero).
// Player::BuyItemFromVendorSlot narrows the bundle count to uint8.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Server/Packets/ItemPackets.cpp
func listInventoryPacket(npc uint64) []byte {
	if !creatureGUID(npc) {
		return nil
	}
	return binary.LittleEndian.AppendUint64(nil, npc)
}

func buyItemPacket(npc uint64, slot, item, count uint32) []byte {
	if !creatureGUID(npc) || slot == 0 || slot > 255 || !itemEntry(item) || count == 0 || count > 255 {
		return nil
	}
	p := binary.LittleEndian.AppendUint64(nil, npc)
	p = binary.LittleEndian.AppendUint32(p, item)
	p = binary.LittleEndian.AppendUint32(p, slot)
	p = binary.LittleEndian.AppendUint32(p, count)
	return append(p, 0)
}

// AutoEquipItemSlot is raw item GUID and a destination byte. This module's
// only equipment destination is main hand (15); no caller-selected slot.
func autoEquipItemSlotPacket(guid uint64, slot byte) []byte {
	if !itemGUID(guid) || slot != 15 {
		return nil
	}
	return append(binary.LittleEndian.AppendUint64(nil, guid), slot)
}

// ItemHandler::SendListInventory uses a u8 count and eight u32 fields per
// record. Stock is signed: -1 means unlimited. An empty list adds a zero
// error byte; an empty nine-byte header is not the upstream wire form.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ItemHandler.cpp
func parseVendorList(p []byte) (uint64, []*suppliesv1.VendorItem, error) {
	if len(p) < 10 || len(p) > 9+255*32 {
		return 0, nil, malformedSuppliesPacket
	}
	npc, count := binary.LittleEndian.Uint64(p), int(p[8])
	if !creatureGUID(npc) {
		return 0, nil, malformedSuppliesPacket
	}
	if count == 0 {
		if len(p) != 10 || p[9] != 0 {
			return 0, nil, malformedSuppliesPacket
		}
		return npc, nil, nil
	}
	if len(p) != 9+count*32 {
		return 0, nil, malformedSuppliesPacket
	}
	items := make([]*suppliesv1.VendorItem, 0, count)
	seen := [256]bool{}
	for at := 9; at < len(p); at += 32 {
		slot := binary.LittleEndian.Uint32(p[at:])
		item := &suppliesv1.VendorItem{
			Slot: slot, Entry: binary.LittleEndian.Uint32(p[at+4:]),
			AvailableCount: int32(binary.LittleEndian.Uint32(p[at+12:])),
			PriceCopper:    binary.LittleEndian.Uint32(p[at+16:]),
			BuyCount:       binary.LittleEndian.Uint32(p[at+24:]),
			ExtendedCost:   binary.LittleEndian.Uint32(p[at+28:]),
		}
		if slot == 0 || slot > 255 || seen[slot] || !itemEntry(item.Entry) || item.AvailableCount < -1 || item.PriceCopper > 0x7fffffff || item.BuyCount == 0 || item.BuyCount > 0x7fffffff || item.ExtendedCost > 0x7fffffff {
			return 0, nil, malformedSuppliesPacket
		}
		seen[slot] = true
		items = append(items, item)
	}
	return npc, items, nil
}

// A buy acknowledgement contains no item ID. The service must bind its
// vendor, one-based slot and bundle count to a pending request, then prove
// the actual inventory and copper changes independently.
type purchaseReceipt struct {
	kind                         string
	npc                          uint64
	item, slot, count            uint32
	availableCount               int32
	code                         uint8
	itemGUID1, itemGUID2         uint64
	requiredLevel, limitCategory uint32
	param                        uint32
}

// Player::_StoreOrEquipNewItem: GUID, one-based slot, signed stock, bundles.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
func parseBuyItem(p []byte) (purchaseReceipt, error) {
	if len(p) != 20 {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	r := purchaseReceipt{kind: "buy_item", npc: binary.LittleEndian.Uint64(p), slot: binary.LittleEndian.Uint32(p[8:]), availableCount: int32(binary.LittleEndian.Uint32(p[12:])), count: binary.LittleEndian.Uint32(p[16:])}
	if !creatureGUID(r.npc) || r.slot == 0 || r.slot > 255 || r.availableCount < -1 || r.count == 0 || r.count > 255 {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	return r, nil
}

// SendBuyError can omit the vendor or item, and inserts a u32 parameter only
// when nonzero. Both documented frames are failures, even code zero.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/PlayerStorage.cpp
func parseBuyFailed(p []byte) (purchaseReceipt, error) {
	if len(p) != 13 && len(p) != 17 {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	r := purchaseReceipt{kind: "buy_failed", npc: binary.LittleEndian.Uint64(p), item: binary.LittleEndian.Uint32(p[8:]), code: p[len(p)-1]}
	if r.npc != 0 && !creatureGUID(r.npc) || r.item > 0x7fffffff {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	if len(p) == 17 {
		r.param = binary.LittleEndian.Uint32(p[12:])
		if r.param == 0 {
			return purchaseReceipt{}, malformedSuppliesPacket
		}
	}
	switch r.code {
	case 0, 1, 2, 4, 5, 7, 8, 11, 12:
		return r, nil
	default:
		return purchaseReceipt{}, malformedSuppliesPacket
	}
}

// SendEquipError / InventoryResult in Item.h define exact code-specific tails.
// Code zero is an informational one-byte frame, never equipment proof.
func parseInventoryChangeFailure(p []byte) (purchaseReceipt, error) {
	if len(p) == 0 {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	r := purchaseReceipt{kind: "inventory_change_failure", code: p[0]}
	if r.code == 0 {
		if len(p) == 1 {
			return r, nil
		}
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	if r.code > 89 || r.code == 74 || r.code == 83 {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	size := 18
	switch r.code {
	case 1, 87, 84, 85, 89:
		size = 22
	case 81:
		size = 38
	}
	if len(p) != size || p[17] != 0 {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	r.itemGUID1, r.itemGUID2 = binary.LittleEndian.Uint64(p[1:]), binary.LittleEndian.Uint64(p[9:])
	if r.itemGUID1 != 0 && !itemGUID(r.itemGUID1) || r.itemGUID2 != 0 && !itemGUID(r.itemGUID2) {
		return purchaseReceipt{}, malformedSuppliesPacket
	}
	switch r.code {
	case 1, 87:
		r.requiredLevel = binary.LittleEndian.Uint32(p[18:])
		if r.requiredLevel > 80 {
			return purchaseReceipt{}, malformedSuppliesPacket
		}
	case 84, 85, 89:
		r.limitCategory = binary.LittleEndian.Uint32(p[18:])
	case 81:
		for _, b := range p[18:] {
			if b != 0 {
				return purchaseReceipt{}, malformedSuppliesPacket
			}
		}
	}
	return r, nil
}
