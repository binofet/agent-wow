package buddyinventory

import (
	"bytes"
	"encoding/binary"
)

type itemMetadata struct {
	known                                                                         bool
	sellPrice                                                                     uint32
	entry, class, subclass, quality, flags, flags2, inventoryType, containerSlots uint32
	bonding, startQuest, lockID, pageText, bagFamily, duration, maxDurability     uint32
	hasSpell                                                                      bool
}

func (m itemMetadata) disposable() bool {
	return m.known && m.entry != 0 && m.class == 15 && m.subclass == 0 && m.quality == 0 &&
		m.flags == 0 && m.flags2 == 0 && m.inventoryType == 0 && m.containerSlots == 0 && !m.hasSpell &&
		m.bonding == 0 && m.startQuest == 0 && m.lockID == 0 && m.pageText == 0 &&
		m.bagFamily == 0 && m.duration == 0 && m.maxDurability == 0
}

// Complete 3.3.5 ItemHandler::HandleItemQuerySingleOpcode framing. A short name
// prefix can describe an item, but cannot authorize an irreversible mutation.
func parseItemMetadata(data []byte) (itemMetadata, error) {
	var m itemMetadata
	entry, _, err := parseItemName(data)
	if err != nil {
		return m, err
	}
	if len(data) == 4 {
		return m, nil
	}
	m.entry, m.class, m.subclass = entry, binary.LittleEndian.Uint32(data[4:]), binary.LittleEndian.Uint32(data[8:])
	at := 16
	str := func(max int) bool {
		if at >= len(data) {
			return false
		}
		end := bytes.IndexByte(data[at:], 0)
		if end < 0 || end > max {
			return false
		}
		at += end + 1
		return true
	}
	if !str(255) {
		return m, malformed
	}
	for range 3 {
		if at >= len(data) || data[at] != 0 {
			return m, malformed
		}
		at++
	}
	read := func() (uint32, bool) {
		if at > len(data)-4 {
			return 0, false
		}
		v := binary.LittleEndian.Uint32(data[at:])
		at += 4
		return v, true
	}
	var prefix [22]uint32
	for i := range prefix {
		var ok bool
		prefix[i], ok = read()
		if !ok {
			return m, malformed
		}
	}
	m.quality, m.flags, m.flags2, m.inventoryType, m.containerSlots = prefix[1], prefix[2], prefix[3], prefix[6], prefix[20]
	m.sellPrice = prefix[5]
	stats := prefix[21]
	if m.class > 16 || m.quality > 7 || stats > 10 {
		return m, malformed
	}
	// Statistics, scaling, two damage records, seven resistances, delay/ammo/range.
	skip := int(stats)*8 + 8 + 24 + 28 + 12
	if skip > len(data)-at {
		return m, malformed
	}
	at += skip
	for range 5 {
		spell, ok := read()
		if !ok {
			return m, malformed
		}
		m.hasSpell = m.hasSpell || spell != 0
		if len(data)-at < 20 {
			return m, malformed
		}
		at += 20
	}
	var ok bool
	if m.bonding, ok = read(); !ok || m.bonding > 5 || !str(4096) {
		return m, malformed
	}
	var tail [29]uint32
	for i := range tail {
		tail[i], ok = read()
		if !ok {
			return m, malformed
		}
	}
	if at != len(data) {
		return m, malformed
	}
	m.pageText, m.startQuest, m.lockID = tail[0], tail[3], tail[4]
	m.maxDurability, m.bagFamily, m.duration = tail[11], tail[14], tail[26]
	m.known = true
	return m, nil
}
