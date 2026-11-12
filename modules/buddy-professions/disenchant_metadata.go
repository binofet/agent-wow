package buddyprofessions

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

type disenchantTemplate struct {
	known                                 bool
	entry, class, quality, requiredSkill  uint32
	containerSlots                        uint32
	flags, flags2, inventoryType, bonding uint32
	hasSpell                              bool
	scaling                               bool
	name                                  string
	tail                                  [29]uint32
}

// A complete ordinary ItemHandler::HandleItemQuerySingleOpcode response is
// required before the selected item can be mutated. Tail[24] is the actual
// RequiredDisenchantSkill, including its -1 sentinel. DisenchantID is not sent:
// the server remains authoritative about whether a loot template exists.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ItemHandler.cpp
func decodeDisenchantTemplate(p []byte) (disenchantTemplate, error) {
	bad := errors.New("invalid_bounded_complete_disenchant_item_template")
	m := disenchantTemplate{}
	if len(p) < 4 || len(p) > 5500 {
		return m, bad
	}
	entry := binary.LittleEndian.Uint32(p)
	m.entry = entry & 0x7fffffff
	if m.entry == 0 {
		return m, bad
	}
	if entry&0x80000000 != 0 {
		if len(p) != 4 {
			return m, bad
		}
		return m, nil
	}
	r := packetReader{data: p}
	r.u32()
	m.class = r.u32()
	r.u32() // SubClass.
	r.u32() // SoundOverrideSubclass.
	str := func(limit int) (string, bool) {
		if r.err != nil || r.off >= len(p) {
			return "", false
		}
		remaining := p[r.off:]
		if len(remaining) > limit+1 {
			remaining = remaining[:limit+1]
		}
		n := bytes.IndexByte(remaining, 0)
		if n < 0 || !utf8.Valid(remaining[:n]) {
			return "", false
		}
		v := string(remaining[:n])
		r.take(n + 1)
		return v, true
	}
	var ok bool
	if m.name, ok = str(255); !ok || m.name == "" {
		return m, bad
	}
	for range 3 {
		if r.u8() != 0 {
			return m, bad
		}
	}
	var prefix [22]uint32
	for i := range prefix {
		prefix[i] = r.u32()
	}
	m.quality, m.flags, m.flags2, m.inventoryType, m.containerSlots = prefix[1], prefix[2], prefix[3], prefix[6], prefix[20]
	stats := prefix[21]
	if m.class > 16 || m.quality > 7 || stats > 10 {
		return m, bad
	}
	r.take(int(stats) * 8)
	distribution, scaling := r.u32(), r.u32()
	m.scaling = distribution != 0 || scaling != 0
	for range 2 {
		minimum, maximum, school := math.Float32frombits(r.u32()), math.Float32frombits(r.u32()), r.u32()
		if math.IsNaN(float64(minimum)) || math.IsInf(float64(minimum), 0) || math.IsNaN(float64(maximum)) || math.IsInf(float64(maximum), 0) || minimum < 0 || maximum < minimum || school > 6 {
			return m, bad
		}
	}
	r.take(7 * 4) // Armor and six additional resistances.
	r.take(2 * 4) // Delay/ammo type.
	rangeModifier := math.Float32frombits(r.u32())
	if math.IsNaN(float64(rangeModifier)) || math.IsInf(float64(rangeModifier), 0) {
		return m, bad
	}
	for range 5 {
		spell := r.u32()
		m.hasSpell = m.hasSpell || spell != 0
		r.take(20) // Complete remaining fields, including signed cooldowns.
	}
	m.bonding = r.u32()
	if m.bonding > 5 {
		return m, bad
	}
	if _, ok = str(4096); !ok {
		return m, bad
	}
	for i := range m.tail {
		m.tail[i] = r.u32()
	}
	if err := r.finish(); err != nil {
		return m, err
	}
	armorModifier := math.Float32frombits(m.tail[25])
	if math.IsNaN(float64(armorModifier)) || math.IsInf(float64(armorModifier), 0) {
		return m, bad
	}
	m.requiredSkill = m.tail[24]
	m.known = true
	return m, nil
}

// This is client protection policy for selected item loss, separate from the
// server's DisenchantID/rank decision. Ordinary BOE/BOP gear may be unused;
// quest, account-bound, functional and special items stay protected.
func (m disenchantTemplate) protected() bool {
	if !m.known || m.flags != 0 || m.flags2 != 0 || m.bonding > 3 || m.scaling ||
		m.inventoryType == 0 || m.inventoryType > 28 || m.inventoryType == 18 || m.inventoryType == 24 || m.inventoryType == 27 ||
		m.containerSlots != 0 || m.hasSpell || m.tail[0] != 0 || m.tail[3] != 0 ||
		m.tail[4] != 0 || m.tail[14] != 0 || m.tail[26] != 0 {
		return true
	}
	for _, field := range []int{7, 8, 10, 12, 13, 15, 16, 17, 18, 19, 20, 21, 22, 23, 27, 28} {
		if m.tail[field] != 0 {
			return true
		}
	}
	return math.Float32frombits(m.tail[25]) != 0
}
