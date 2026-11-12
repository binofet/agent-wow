package buddyquestitems

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

var malformed = errors.New("malformed quest-item packet")

type reader struct {
	data []byte
	off  int
	err  error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.data)-r.off {
		r.err = malformed
		return nil
	}
	p := r.data[r.off : r.off+n]
	r.off += n
	return p
}
func (r *reader) u8() byte {
	if p := r.take(1); p != nil {
		return p[0]
	}
	return 0
}
func (r *reader) u32() uint32 {
	if p := r.take(4); p != nil {
		return binary.LittleEndian.Uint32(p)
	}
	return 0
}
func (r *reader) u64() uint64 {
	if p := r.take(8); p != nil {
		return binary.LittleEndian.Uint64(p)
	}
	return 0
}
func (r *reader) guid() uint64 {
	m := r.u8()
	var v uint64
	for i := uint(0); i < 8; i++ {
		if m&(1<<i) != 0 {
			v |= uint64(r.u8()) << (i * 8)
		}
	}
	return v
}
func (r *reader) str(limit int) {
	if r.err != nil {
		return
	}
	p := r.data[r.off:]
	if len(p) > limit+1 {
		p = p[:limit+1]
	}
	n := bytes.IndexByte(p, 0)
	if n < 0 || !utf8.Valid(p[:n]) {
		r.err = malformed
		return
	}
	r.take(n + 1)
}
func (r *reader) done() error {
	if r.err != nil {
		return r.err
	}
	if r.off != len(r.data) {
		return malformed
	}
	return nil
}
func finite(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) && math.Abs(float64(v)) < 40000
}

type point struct{ x, y, z float32 }

func (r *reader) point() point {
	p := point{math.Float32frombits(r.u32()), math.Float32frombits(r.u32()), math.Float32frombits(r.u32())}
	if !finite(p.x) || !finite(p.y) || !finite(p.z) {
		r.err = malformed
	}
	return p
}
func dist(a, b point) float64 {
	dx, dy, dz := float64(a.x-b.x), float64(a.y-b.y), float64(a.z-b.z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
func u32(v uint32) []byte { p := make([]byte, 4); binary.LittleEndian.PutUint32(p, v); return p }
func goUse(guid uint64) []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint64(p, guid)
	return p
}
func goQuery(guid uint64) []byte {
	p := make([]byte, 12)
	binary.LittleEndian.PutUint32(p, uint32(guid>>24&0xffffff))
	binary.LittleEndian.PutUint64(p[4:], guid)
	return p
}

// HandleUseItemOpcode reads the full item GUID. SpellCastTargets::Read uses
// packed transport0 followed by XYZ for DEST_LOCATION. Client castFlags0 has
// no trajectory/movement extension; spell42340 itself has missile speed18.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/SpellHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Spell.cpp
func useBucket(bag, slot uint32, item uint64, count byte, dst point) ([]byte, error) {
	if bag > 4 || bag == 0 && slot >= 16 || bag > 0 && slot >= 36 || item>>48 != 0x4000 || item&0xffffffffffff == 0 || !finite(dst.x) || !finite(dst.y) || !finite(dst.z) {
		return nil, malformed
	}
	p := make([]byte, 37)
	p[0] = byte(18 + bag)
	p[1] = byte(slot)
	if bag == 0 {
		p[0] = 255
		p[1] = byte(23 + slot)
	}
	p[2] = count
	binary.LittleEndian.PutUint32(p[3:], throwSpell)
	binary.LittleEndian.PutUint64(p[7:], item)
	binary.LittleEndian.PutUint32(p[20:], 0x40)
	for i, v := range []float32{dst.x, dst.y, dst.z} {
		binary.LittleEndian.PutUint32(p[25+i*4:], math.Float32bits(v))
	}
	return p, nil
}

// Full normal query framing is required before static metadata authorizes use.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/QueryHandler.cpp
func barrelTemplate(p []byte) (bool, error) {
	if len(p) > 20000 {
		return false, malformed
	}
	r := reader{data: p}
	entry := r.u32()
	if entry&0x80000000 != 0 {
		if len(p) == 4 && entry&0x7fffffff == barrelEntry {
			return false, nil
		}
		return false, malformed
	}
	kind := r.u32()
	r.u32()
	r.str(4096)
	if z := r.take(3); len(z) != 3 || z[0]|z[1]|z[2] != 0 {
		return false, malformed
	}
	for range 3 {
		r.str(4096)
	}
	var data [24]uint32
	for i := range data {
		data[i] = r.u32()
	}
	scale := math.Float32frombits(r.u32())
	r.take(24)
	if err := r.done(); err != nil {
		return false, err
	}
	if entry != barrelEntry || kind != 10 || !finite(scale) || scale <= 0 {
		return false, malformed
	}
	return data[10] == createSpell, nil
}

// Item query layout: four names,22 fixed prefix words, variable stats,
// scaling/damage/resistance/weapon words, five six-word spell records, bonding,
// description and29 fixed trailing words. Signed charges are retained.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ItemHandler.cpp
func bucketTemplate(p []byte) (bool, error) {
	if len(p) > 20000 {
		return false, malformed
	}
	r := reader{data: p}
	entry := r.u32()
	if entry&0x80000000 != 0 {
		if len(p) == 4 && entry&0x7fffffff == bucketEntry {
			return false, nil
		}
		return false, malformed
	}
	r.take(12)
	r.str(255)
	if z := r.take(3); len(z) != 3 || z[0]|z[1]|z[2] != 0 {
		return false, malformed
	}
	var prefix [22]uint32
	for i := range prefix {
		prefix[i] = r.u32()
	}
	if prefix[21] > 10 {
		return false, malformed
	}
	r.take(int(prefix[21])*8 + 8 + 24 + 28 + 12)
	onUse := 0
	matching := false
	for range 5 {
		spell, trigger, charges := r.u32(), r.u32(), int32(r.u32())
		r.take(12)
		if spell != 0 && trigger == 0 {
			onUse++
			matching = spell == throwSpell && charges == -1
		}
	}
	r.u32()
	r.str(4096)
	r.take(29 * 4)
	if err := r.done(); err != nil {
		return false, err
	}
	if entry != bucketEntry {
		return false, malformed
	}
	return onUse == 1 && matching, nil
}

type castEvent struct {
	kind         string
	item, caster uint64
	count        byte
	spell        uint32
	failure      byte
	mask         uint32
	dst          point
}

func decodeCast(op uint32, p []byte) (castEvent, error) {
	r := reader{data: p}
	e := castEvent{}
	switch op {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		e.item, e.caster, e.count, e.spell = r.guid(), r.guid(), r.u8(), r.u32()
		flags := r.u32()
		r.u32()
		if flags&^uint32(1|2|0x100|0x800|0x40000) != 0 {
			return e, malformed
		}
		e.kind = "start"
		if op == opcode.SMSGSpellGo {
			e.kind = "go"
			n := int(r.u8())
			r.take(n * 8)
			n = int(r.u8())
			for i := 0; i < n && r.err == nil; i++ {
				r.take(8)
				reason := r.u8()
				if reason == 0 || reason > 11 {
					return e, malformed
				}
				if reason == 11 && r.u8() > 11 {
					return e, malformed
				}
			}
		}
		e.mask = r.u32()
		if e.mask&^uint32(0x42) != 0 {
			return e, malformed
		}
		if e.mask&2 != 0 {
			target := r.guid()
			if target != 0 && target != e.caster {
				return e, malformed
			}
		}
		if e.mask&0x40 != 0 {
			if r.guid() != 0 {
				return e, malformed
			}
			e.dst = r.point()
		}
		if flags&0x800 != 0 {
			r.u32()
		}
		if op == opcode.SMSGSpellGo && e.mask&0x40 != 0 && r.u8() != 0 {
			return e, malformed
		}
	case opcode.SMSGCastFailed:
		e.kind = "failed"
		e.count, e.spell, e.failure = r.u8(), r.u32(), r.u8()
		if e.failure == 0 || e.failure > 187 {
			return e, malformed
		}
		n := 0
		switch e.failure {
		case 29, 30, 31, 55, 150:
			n = 8
		case 54, 100, 101, 102, 147, 172, 181:
			n = 4
		}
		r.take(n)
	case opcode.SMSGSpellFailure:
		e.kind = "failed"
		e.caster, e.count, e.spell, e.failure = r.guid(), r.u8(), r.u32(), r.u8()
		if e.failure == 0 || e.failure > 187 {
			return e, malformed
		}
	default:
		return e, malformed
	}
	return e, r.done()
}

func itemPush(p []byte) (uint64, uint32, error) {
	if len(p) != 45 {
		return 0, 0, malformed
	}
	for _, i := range []int{8, 12, 16} {
		if binary.LittleEndian.Uint32(p[i:]) > 1 {
			return 0, 0, malformed
		}
	}
	entry, n, total := binary.LittleEndian.Uint32(p[25:]), binary.LittleEndian.Uint32(p[37:]), binary.LittleEndian.Uint32(p[41:])
	if entry == 0 || n == 0 || total < n {
		return 0, 0, malformed
	}
	return binary.LittleEndian.Uint64(p), entry, nil
}
func inventoryFailure(p []byte) (byte, uint64, uint64, error) {
	r := reader{data: p}
	code := r.u8()
	if code == 0 {
		return 0, 0, 0, r.done()
	}
	if code == 1 {
		r.u32()
	}
	a, b := r.u64(), r.u64()
	r.u8()
	return code, a, b, r.done()
}
