package buddyinventory

import (
	"bytes"
	"encoding/binary"
	"math"
)

type itemMetadata struct {
	name                                                                                                  string
	known                                                                                                 bool
	sellPrice                                                                                             uint32
	entry, class, subclass, quality, flags, flags2, inventoryType, containerSlots                         uint32
	bonding, startQuest, lockID, pageText, bagFamily, duration, maxDurability                             uint32
	hasSpell                                                                                              bool
	hasRequirement                                                                                        bool
	allowableClass, allowableRace, requiredLevel, requiredSkill, requiredSkillRank                        uint32
	requiredSpell, requiredHonorRank, requiredCityRank, requiredReputationFaction, requiredReputationRank uint32
	maxCount, stackable, scalingDistribution, scalingValue, armor, itemSet, itemLimitCategory             uint32
	stats                                                                                                 [10]itemStat
	statCount                                                                                             uint32
	resistances                                                                                           [6]uint32
	specialArmor                                                                                          bool
}
type itemStat struct {
	kind  uint32
	value int32
}

// This predicate only authorizes vendor sales. Carried slot/GUID binding in the
// service excludes equipped items; destruction has its separate unchanged
// predicate in buddy-trade. Primary ItemTemplate.h defines class4, armor
// subclasses1..4 and these ordinary armor inventory types.
func (m itemMetadata) disposable() bool {
	ordinary := m.known && m.entry != 0 && m.quality == 0 &&
		m.flags == 0 && m.flags2 == 0 && m.containerSlots == 0 && !m.hasSpell &&
		m.bonding == 0 && m.startQuest == 0 && m.lockID == 0 && m.pageText == 0 &&
		m.bagFamily == 0 && m.duration == 0
	if !ordinary {
		return false
	}
	if m.class == 15 {
		return m.subclass == 0 && m.inventoryType == 0 && m.maxDurability == 0
	}
	if m.class != 4 || m.subclass < 1 || m.subclass > 4 || m.hasRequirement {
		return false
	}
	switch m.inventoryType {
	case 1, 3, 5, 6, 7, 8, 9, 10, 16, 20: // head, shoulder, chest, waist, legs, feet, wrist, hands, cloak, robe
		return true // Ordinary armor durability is expected and does not make it a usable tool.
	}
	return false
}

// Complete 3.3.5 ItemHandler::HandleItemQuerySingleOpcode framing. A short name
// prefix can describe an item, but cannot authorize an irreversible mutation.
func parseItemMetadata(data []byte) (itemMetadata, error) {
	var m itemMetadata
	entry, name, err := parseItemName(data)
	if err != nil {
		return m, err
	}
	if len(data) == 4 {
		return m, nil
	}
	m.entry, m.class, m.subclass = entry, binary.LittleEndian.Uint32(data[4:]), binary.LittleEndian.Uint32(data[8:])
	m.name = name
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
	m.allowableClass, m.allowableRace, m.requiredLevel = prefix[7], prefix[8], prefix[10]
	m.requiredSkill, m.requiredSkillRank, m.requiredSpell = prefix[11], prefix[12], prefix[13]
	m.requiredHonorRank, m.requiredCityRank, m.requiredReputationFaction, m.requiredReputationRank = prefix[14], prefix[15], prefix[16], prefix[17]
	m.maxCount, m.stackable = prefix[18], prefix[19]
	// Required skill/rank/spell, honor/city rank and reputation faction/rank.
	// Required level alone is normal for armor and is not a special use.
	for _, requirement := range prefix[11:18] {
		m.hasRequirement = m.hasRequirement || requirement != 0
	}
	stats := prefix[21]
	if m.class > 16 || m.quality > 7 || stats > 10 {
		return m, malformed
	}
	// Exact bounded stats and finite damage/range fields. These are static
	// template facts, not effective enchanted or character-adjusted statistics.
	if int(stats)*8+8+24+28+12 > len(data)-at {
		return m, malformed
	}
	m.statCount = stats
	seenStats := map[uint32]bool{}
	for i := uint32(0); i < stats; i++ {
		kind, _ := read()
		value, _ := read()
		if kind > 48 || seenStats[kind] {
			return m, malformed
		}
		seenStats[kind] = true
		m.stats[i] = itemStat{kind, int32(value)}
	}
	m.scalingDistribution, _ = read()
	m.scalingValue, _ = read()
	for range 2 {
		a, _ := read()
		b, _ := read()
		school, _ := read()
		minimum, maximum := math.Float32frombits(a), math.Float32frombits(b)
		if math.IsNaN(float64(minimum)) || math.IsInf(float64(minimum), 0) || math.IsNaN(float64(maximum)) || math.IsInf(float64(maximum), 0) || minimum < 0 || maximum < minimum || school > 6 {
			return m, malformed
		}
	}
	m.armor, _ = read()
	for i := range m.resistances {
		m.resistances[i], _ = read()
	}
	_, _ = read()
	_, _ = read()
	rangeBits, _ := read()
	rangeModifier := math.Float32frombits(rangeBits)
	if math.IsNaN(float64(rangeModifier)) || math.IsInf(float64(rangeModifier), 0) || rangeModifier < 0 {
		return m, malformed
	}
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
	m.itemSet, m.itemLimitCategory = tail[10], tail[27]
	for _, index := range []int{7, 8, 12, 13, 15, 16, 17, 18, 19, 20, 21, 22, 23, 28} {
		m.specialArmor = m.specialArmor || tail[index] != 0
	}
	modifier := math.Float32frombits(tail[25])
	if math.IsNaN(float64(modifier)) || math.IsInf(float64(modifier), 0) {
		return m, malformed
	}
	m.specialArmor = m.specialArmor || modifier != 0
	m.known = true
	return m, nil
}
