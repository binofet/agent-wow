// Package spellcast encodes the ordinary Wrath CMSG_CAST_SPELL target DTO.
// It deliberately does not interpret spell effects or choose targets.
package spellcast

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const ObjectMask uint32 = 2 | 0x10000 | 0x800 | 0x200 | 0x8000
const ItemMask uint32 = 0x10 | 0x1000

// AppendPackedGUID appends the Wrath packed GUID used by spell and item-use targets.
func AppendPackedGUID(dst []byte, guid uint64) []byte {
	at := len(dst)
	dst = append(dst, 0)
	for i := 0; i < 8; i++ {
		if b := byte(guid >> (8 * i)); b != 0 {
			dst[at] |= 1 << i
			dst = append(dst, b)
		}
	}
	return dst
}
func finite(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }
func f32(dst []byte, v float32) []byte {
	return binary.LittleEndian.AppendUint32(dst, math.Float32bits(v))
}

// Encode follows SpellCastTargets::Read and HandleClientCastFlags. A zero target
// mask has no GUID byte; each union consumes exactly one packed GUID.
func Encode(count byte, r *actionsv1.CastSpellRequest) ([]byte, error) {
	if r.GetSpellId() == 0 {
		return nil, errors.New("nonzero learned spell ID required")
	}
	t := r.GetTargets()
	mask := t.GetMask()
	if mask&ObjectMask == 0 && t.GetObjectGuid() != 0 || mask&ItemMask == 0 && t.GetItemGuid() != 0 ||
		(mask&0x20 != 0) != (t.GetSource() != nil) || (mask&0x40 != 0) != (t.GetDestination() != nil) ||
		mask&0x2000 == 0 && t.GetStringTarget() != "" {
		return nil, errors.New("target fields must match target mask")
	}
	if mask&ObjectMask != 0 && t.GetObjectGuid() == 0 || mask&0x10 != 0 && t.GetItemGuid() == 0 {
		return nil, errors.New("explicit target mask requires exact nonzero GUID")
	}
	if mask&0x1000 != 0 && (mask&0x10 != 0 || t.GetItemGuid() != 6) {
		return nil, errors.New("trade target must be the non-traded slot6, not an item GUID")
	}
	if mask&0x1000 == 0 && (t.GetTradeRevision() != 0 || t.GetTradePartnerGuid() != 0) {
		return nil, errors.New("trade binding requires trade-item target")
	}
	if !utf8.ValidString(t.GetStringTarget()) || strings.IndexByte(t.GetStringTarget(), 0) >= 0 {
		return nil, errors.New("target string must be UTF-8 without NUL")
	}
	out := []byte{count}
	out = binary.LittleEndian.AppendUint32(out, r.SpellId)
	flags := byte(0)
	if r.Trajectory != nil {
		flags = 2
	}
	out = append(out, flags)
	out = binary.LittleEndian.AppendUint32(out, mask)
	if mask&ObjectMask != 0 {
		out = AppendPackedGUID(out, t.GetObjectGuid())
	}
	if mask&ItemMask != 0 {
		out = AppendPackedGUID(out, t.GetItemGuid())
	}
	for _, loc := range []*actionsv1.SpellCastLocation{t.GetSource(), t.GetDestination()} {
		if loc == nil {
			continue
		}
		if !finite(loc.X) || !finite(loc.Y) || !finite(loc.Z) {
			return nil, errors.New("finite target location required")
		}
		out = AppendPackedGUID(out, loc.TransportGuid)
		out = f32(out, loc.X)
		out = f32(out, loc.Y)
		out = f32(out, loc.Z)
	}
	if mask&0x2000 != 0 {
		out = append(out, t.GetStringTarget()...)
		out = append(out, 0)
	}
	if tr := r.Trajectory; tr != nil {
		if !finite(tr.Elevation) || !finite(tr.Speed) || tr.Speed < 0 {
			return nil, errors.New("finite trajectory with nonnegative speed required")
		}
		out = f32(out, tr.Elevation)
		out = f32(out, tr.Speed)
		if len(tr.Movement) == 0 {
			if tr.MovementOpcode != 0 {
				return nil, errors.New("movement opcode requires movement body")
			}
			out = append(out, 0)
		} else {
			if err := validateMovement(r.SelfGuid, tr.MovementOpcode, tr.Movement); err != nil {
				return nil, err
			}
			out = append(out, 1)
			out = binary.LittleEndian.AppendUint32(out, tr.MovementOpcode)
			out = append(out, tr.Movement...)
		}
	}
	if len(out)+4 >= 10240 {
		return nil, errors.New("cast exceeds normal world sender frame budget")
	}
	return out, nil
}

type cursor struct {
	p  []byte
	ok bool
}

func (c *cursor) take(n int) []byte {
	if !c.ok || n > len(c.p) {
		c.ok = false
		return make([]byte, n)
	}
	p := c.p[:n]
	c.p = c.p[n:]
	return p
}
func (c *cursor) u32() uint32 { return binary.LittleEndian.Uint32(c.take(4)) }
func (c *cursor) floats(n int) {
	for range n {
		if !finite(math.Float32frombits(c.u32())) {
			c.ok = false
		}
	}
}
func (c *cursor) guid() uint64 {
	mask := c.take(1)[0]
	var v uint64
	for i := 0; i < 8; i++ {
		if mask&(1<<i) != 0 {
			v |= uint64(c.take(1)[0]) << (8 * i)
		}
	}
	return v
}
func validateMovement(self uint64, code uint32, p []byte) error {
	// Only movement opcodes whose client body is the normal MovementInfo shape.
	switch code {
	case opcode.MSGMoveStartForward, opcode.MSGMoveStartBackward, opcode.MSGMoveStop,
		opcode.MSGMoveStartStrafeLeft, opcode.MSGMoveStartStrafeRight, opcode.MSGMoveStopStrafe,
		opcode.MSGMoveJump, opcode.MSGMoveStartTurnLeft, opcode.MSGMoveStartTurnRight, opcode.MSGMoveStopTurn,
		opcode.MSGMoveStartPitchUp, opcode.MSGMoveStartPitchDown, opcode.MSGMoveStopPitch,
		opcode.MSGMoveSetRunMode, opcode.MSGMoveSetWalkMode, opcode.MSGMoveFallLand,
		opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim, opcode.MSGMoveSetFacing, opcode.MSGMoveSetPitch, opcode.MSGMoveHeartbeat:
	default:
		return errors.New("trajectory movement requires a normal movement opcode")
	}
	c := cursor{p: p, ok: true}
	if self == 0 || c.guid() != self {
		return errors.New("trajectory movement must bind the exact caster")
	}
	flags := c.u32()
	extra := binary.LittleEndian.Uint16(c.take(2))
	c.u32()
	c.floats(4)
	if flags&0x200 != 0 {
		c.guid()
		c.floats(4)
		c.u32()
		c.take(1)
		if extra&0x400 != 0 {
			c.u32()
		}
	}
	if flags&0x02200000 != 0 || extra&0x20 != 0 {
		c.floats(1)
	}
	c.u32()
	if flags&0x1000 != 0 {
		c.floats(4)
	}
	if flags&0x04000000 != 0 {
		c.floats(1)
	}
	if !c.ok || len(c.p) != 0 {
		return errors.New("complete finite own-body MovementInfo required")
	}
	return nil
}

// TargetPrefixLen validates the ordinary target prefix in a server START/GO.
// Server-only cast flag extensions follow it and do not establish game effects.
func TargetPrefixLen(p []byte) (int, error) {
	c := cursor{p: p, ok: true}
	mask := c.u32()
	if mask&ObjectMask != 0 {
		c.guid()
	}
	if mask&ItemMask != 0 {
		c.guid()
	}
	if mask&0x20 != 0 {
		c.guid()
		c.floats(3)
	}
	if mask&0x40 != 0 {
		c.guid()
		c.floats(3)
	}
	if mask&0x2000 != 0 {
		i := 0
		for i < len(c.p) && c.p[i] != 0 {
			i++
		}
		if i == len(c.p) || !utf8.Valid(c.p[:i]) {
			c.ok = false
		} else {
			c.take(i + 1)
		}
	}
	if !c.ok {
		return 0, errors.New("malformed spell target prefix")
	}
	return len(p) - len(c.p), nil
}
