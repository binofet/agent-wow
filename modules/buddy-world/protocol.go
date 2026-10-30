package buddyworld

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/bits"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Layouts follow AzerothCore 3.3.5a UpdateData.cpp, Object.cpp,
// Unit::BuildMovementPacket and WorldSession::WriteMovementInfo:
// https://github.com/azerothcore/azerothcore-wotlk/tree/master/src/server/game
const (
	maxPayload       = 4 << 20
	maxBlocks        = 4096
	unitBytes0       = 0x17
	unitHealth       = 0x18
	unitPower1       = 0x19
	unitMaxHealth    = 0x20
	unitMaxPower1    = 0x21
	unitFlags        = 0x3b
	unitDynamicFlags = 0x4f
	playerFlags      = 0x96
)

var (
	errShort      = errors.New("truncated observation packet")
	errLayout     = errors.New("unsupported observation layout")
	errBounds     = errors.New("observation bounds exceeded")
	errFloat      = errors.New("invalid position or speed")
	errCompressed = errors.New("invalid compressed object update")
)

type cursor struct {
	b   []byte
	off int
	err error
}

func (c *cursor) take(n int) []byte {
	if c.err != nil {
		return nil
	}
	if n < 0 || n > len(c.b)-c.off {
		c.err = errShort
		return nil
	}
	b := c.b[c.off : c.off+n]
	c.off += n
	return b
}
func (c *cursor) skip(n int) { c.take(n) }
func (c *cursor) u8() uint8 {
	b := c.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}
func (c *cursor) u16() uint16 {
	b := c.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}
func (c *cursor) u32() uint32 {
	b := c.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}
func (c *cursor) u64() uint64 {
	b := c.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}
func (c *cursor) f32() float32 { return math.Float32frombits(c.u32()) }
func (c *cursor) guid() uint64 {
	mask := c.u8()
	var guid uint64
	for i := uint(0); i < 8; i++ {
		if mask&(1<<i) != 0 {
			guid |= uint64(c.u8()) << (8 * i)
		}
	}
	return guid
}
func (c *cursor) done() error {
	if c.err != nil {
		return c.err
	}
	if c.off != len(c.b) {
		return errLayout
	}
	return nil
}
func player(players *[2]observed, guid uint64) *observed {
	for i := range players {
		if players[i].guid == guid {
			return &players[i]
		}
	}
	return nil
}
func validFloat(f float32) bool {
	return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) && math.Abs(float64(f)) < 1e6
}
func position(p *observed, x, y, z, o float32, now time.Time) error {
	if !validFloat(x) || !validFloat(y) || !validFloat(z) || !validFloat(o) {
		return errFloat
	}
	if p != nil {
		p.x, p.y, p.z, p.orientation = x, y, z, o
		p.positionKnown = true
		p.positionAt = now
		p.positionRevision++
		p.observedAt = now
	}
	return nil
}
func markAbsent(p *observed, now time.Time) {
	p.present = false
	p.positionKnown = false
	p.movementKnown = false
	p.mapKnown = false
	p.observedAt = now
}
func setMap(p *observed, self observed) { p.mapID, p.mapKnown = self.mapID, self.mapKnown }

func parsePacket(op uint32, payload []byte, players *[2]observed, now time.Time) (bool, error) {
	if len(payload) > maxPayload {
		return true, errBounds
	}
	c := &cursor{b: payload}
	switch op {
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld:
		mapID := c.u32()
		x, y, z, o := c.f32(), c.f32(), c.f32(), c.f32()
		if err := c.done(); err != nil {
			return true, err
		}
		p := &players[0]
		if op == opcode.SMSGNewWorld || !p.mapKnown || p.mapID != mapID {
			markAbsent(&players[1], now)
		}
		p.mapID, p.mapKnown, p.present = mapID, true, true
		p.movementKnown = false
		return true, position(p, x, y, z, o, now)
	case opcode.SMSGTransferPending:
		// A transfer has variable transport data. Stop using the old world now.
		if len(payload) < 4 {
			return true, errShort
		}
		markAbsent(&players[0], now)
		markAbsent(&players[1], now)
		return true, nil
	case opcode.SMSGUpdateObject:
		return true, updates(c, players, now)
	case opcode.SMSGCompressedUpdateObject:
		declared := c.u32()
		if c.err != nil {
			return true, c.err
		}
		if declared > maxPayload || declared < 4 {
			return true, errBounds
		}
		r, err := zlib.NewReader(bytes.NewReader(payload[4:]))
		if err != nil {
			return true, errCompressed
		}
		data, err := io.ReadAll(io.LimitReader(r, int64(declared)+1))
		closeErr := r.Close()
		if err != nil || closeErr != nil || len(data) != int(declared) {
			return true, errCompressed
		}
		return true, updates(&cursor{b: data}, players, now)
	case opcode.SMSGDestroyObject:
		guid := c.u64()
		c.u8()
		if err := c.done(); err != nil {
			return true, err
		}
		if p := player(players, guid); p != nil {
			markAbsent(p, now)
		}
		return true, nil
	case opcode.SMSGPowerUpdate:
		guid := c.guid()
		powerType := int(c.u8())
		value := c.u32()
		if err := c.done(); err != nil {
			return true, err
		}
		if powerType >= 7 {
			return true, errLayout
		}
		if p := player(players, guid); p != nil {
			p.fields[unitPower1+powerType] = value
			p.known[unitPower1+powerType] = true
			p.vitalsAt = now
			p.observedAt = now
		}
		return true, nil
	case opcode.MSGMoveTeleportAck:
		// Server's near-teleport correction has an order counter before info.
		guid := c.guid()
		c.skip(4)
		p := player(players, guid)
		if err := movement(c, p, now); err != nil {
			return true, err
		}
		return true, c.done()
	case opcode.SMSGForceMoveRoot, opcode.SMSGForceMoveUnroot:
		guid := c.guid()
		c.skip(4)
		if err := c.done(); err != nil {
			return true, err
		}
		if p := player(players, guid); p != nil {
			if op == opcode.SMSGForceMoveRoot {
				p.movementFlags |= 0x800
			} else {
				p.movementFlags &^= 0x800
			}
			p.observedAt = now
		}
		return true, nil
	case opcode.SMSGForceRunSpeedChange, opcode.SMSGForceWalkSpeedChange:
		guid := c.guid()
		c.skip(4)
		if op == opcode.SMSGForceRunSpeedChange {
			c.skip(1)
		}
		speed := c.f32()
		if err := c.done(); err != nil {
			return true, err
		}
		if !validFloat(speed) || speed < 0 {
			return true, errFloat
		}
		if p := player(players, guid); p != nil {
			if op == opcode.SMSGForceRunSpeedChange {
				p.runSpeed = speed
			} else {
				p.walkSpeed = speed
			}
			p.observedAt = now
		}
		return true, nil
	case opcode.SMSGMoveKnockBack, opcode.SMSGMonsterMove, opcode.SMSGMonsterMoveTransport:
		// Spline and knockback trajectories are deliberately not predicted.
		// Invalidate a tracked player's position until the next actual position.
		guid := c.guid()
		if op == opcode.SMSGMoveKnockBack {
			c.skip(20)
		} else {
			c.skip(1)
		}
		if c.err != nil {
			return true, c.err
		}
		if p := player(players, guid); p != nil {
			p.positionKnown = false
			p.movementKnown = false
			p.observedAt = now
		}
		return true, nil
	}
	if movementOpcode(op) {
		guid := c.guid()
		p := player(players, guid)
		if err := movement(c, p, now); err != nil {
			return true, err
		}
		if err := c.done(); err != nil {
			return true, err
		}
		if p != nil {
			p.present = true
			setMap(p, players[0])
		}
		return true, nil
	}
	return false, nil
}

func movementOpcode(op uint32) bool {
	switch op {
	case opcode.MSGMoveStartForward, opcode.MSGMoveStartBackward, opcode.MSGMoveStop,
		opcode.MSGMoveStartStrafeLeft, opcode.MSGMoveStartStrafeRight, opcode.MSGMoveStopStrafe,
		opcode.MSGMoveJump, opcode.MSGMoveStartTurnLeft, opcode.MSGMoveStartTurnRight, opcode.MSGMoveStopTurn,
		opcode.MSGMoveStartPitchUp, opcode.MSGMoveStartPitchDown, opcode.MSGMoveStopPitch,
		opcode.MSGMoveSetRunMode, opcode.MSGMoveSetWalkMode, opcode.MSGMoveTeleport,
		opcode.MSGMoveFallLand, opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim,
		opcode.MSGMoveSetFacing, opcode.MSGMoveSetPitch, opcode.MSGMoveHeartbeat,
		opcode.MSGMoveStartAscend, opcode.MSGMoveStopAscend, opcode.MSGMoveStartDescend:
		return true
	}
	return false
}

func movement(c *cursor, p *observed, now time.Time) error {
	flags, extra, timestamp := c.u32(), c.u16(), c.u32()
	x, y, z, o := c.f32(), c.f32(), c.f32(), c.f32()
	if flags&0x200 != 0 {
		c.guid()
		c.skip(16 + 4 + 1)
		if extra&0x400 != 0 {
			c.skip(4)
		}
	}
	if flags&0x02200000 != 0 || extra&0x20 != 0 {
		c.skip(4)
	}
	c.skip(4) // fallTime is present even while standing.
	if flags&0x1000 != 0 {
		c.skip(16)
	}
	if flags&0x04000000 != 0 {
		c.skip(4)
	}
	if c.err != nil {
		return c.err
	}
	if err := position(p, x, y, z, o, now); err != nil {
		return err
	}
	if p != nil {
		p.movementKnown = true
		p.movementFlags = flags
		p.extraFlags = uint32(extra)
		p.movementTime = timestamp
	}
	return nil
}

func updates(c *cursor, players *[2]observed, now time.Time) error {
	count := c.u32()
	if count > maxBlocks {
		return errBounds
	}
	for i := uint32(0); i < count; i++ {
		typ := c.u8()
		switch typ {
		case 0, 1, 2, 3:
			guid := c.guid()
			p := player(players, guid)
			if typ == 2 || typ == 3 {
				objectType := c.u8()
				if objectType > 7 {
					return errLayout
				}
				if p != nil {
					if objectType != 4 {
						return errLayout
					}
					revision := p.positionRevision
					selfMap := players[0]
					*p = observed{guid: guid, positionRevision: revision, present: true}
					setMap(p, selfMap)
					// CREATE omits zero-valued public fields. Initialize only the
					// public unit/player fields consumed by this module.
					for _, f := range []int{unitBytes0, unitHealth, unitMaxHealth, unitFlags, unitDynamicFlags, playerFlags} {
						p.known[f] = true
					}
					for f := 0; f < 7; f++ {
						p.known[unitPower1+f] = true
						p.known[unitMaxPower1+f] = true
					}
					p.vitalsAt = now
					p.observedAt = now
				}
			}
			if typ != 0 {
				if err := movementUpdate(c, p, now); err != nil {
					return err
				}
			}
			if typ != 1 {
				if err := values(c, p, now); err != nil {
					return err
				}
			}
		case 4, 5:
			n := c.u32()
			if n > maxBlocks {
				return errBounds
			}
			for j := uint32(0); j < n; j++ {
				guid := c.guid()
				if typ == 4 {
					if p := player(players, guid); p != nil {
						markAbsent(p, now)
					}
				}
			}
		default:
			return errLayout
		}
		if c.err != nil {
			return c.err
		}
	}
	return c.done()
}

func movementUpdate(c *cursor, p *observed, now time.Time) error {
	flags := c.u16()
	if flags&^uint16(0x3ff) != 0 {
		return errLayout
	}
	if flags&0x20 != 0 {
		if len(c.b)-c.off < 4 {
			return errShort
		}
		moveFlags := binary.LittleEndian.Uint32(c.b[c.off:])
		if err := movement(c, p, now); err != nil {
			return err
		}
		speeds := [9]float32{}
		for i := range speeds {
			speeds[i] = c.f32()
			if !validFloat(speeds[i]) || speeds[i] < 0 {
				return errFloat
			}
		}
		if p != nil {
			p.walkSpeed, p.runSpeed = speeds[0], speeds[1]
			p.speedsKnown = true
		}
		if moveFlags&0x08000000 != 0 {
			if err := splineCreate(c); err != nil {
				return err
			}
		}
	} else if flags&0x100 != 0 {
		c.guid()
		x, y, z := c.f32(), c.f32(), c.f32()
		c.skip(12)
		o := c.f32()
		c.skip(4)
		if err := position(p, x, y, z, o, now); err != nil {
			return err
		}
	} else if flags&0x40 != 0 {
		if err := position(p, c.f32(), c.f32(), c.f32(), c.f32(), now); err != nil {
			return err
		}
	}
	if flags&0x8 != 0 {
		c.skip(4)
	}
	if flags&0x10 != 0 {
		c.skip(4)
	}
	if flags&0x4 != 0 {
		c.guid()
	}
	if flags&0x2 != 0 {
		c.skip(4)
	}
	if flags&0x80 != 0 {
		c.skip(8)
	}
	if flags&0x200 != 0 {
		c.skip(8)
	}
	return c.err
}

func splineCreate(c *cursor) error {
	flags := c.u32()
	if flags&0x20000 != 0 {
		c.skip(4)
	} else if flags&0x10000 != 0 {
		c.skip(8)
	} else if flags&0x8000 != 0 {
		c.skip(12)
	}
	c.skip(28) // elapsed, duration, id, two modifiers, acceleration, effect start
	nodes := c.u32()
	if nodes > maxBlocks || uint64(nodes)*12 > uint64(len(c.b)-c.off) {
		return errBounds
	}
	c.skip(int(nodes)*12 + 1 + 12)
	return c.err
}

func values(c *cursor, p *observed, now time.Time) error {
	n := int(c.u8())
	masks := make([]uint32, n)
	total := 0
	for i := range masks {
		masks[i] = c.u32()
		total += bits.OnesCount32(masks[i])
	}
	if total > (len(c.b)-c.off)/4 {
		return errShort
	}
	for block, mask := range masks {
		for bit := 0; bit < 32; bit++ {
			if mask&(uint32(1)<<bit) == 0 {
				continue
			}
			v := c.u32()
			field := block*32 + bit
			if p != nil && field < len(p.fields) {
				p.fields[field] = v
				p.known[field] = true
				p.observedAt = now
				if field >= unitBytes0 && field <= unitMaxPower1+6 || field == unitFlags || field == unitDynamicFlags || field == playerFlags {
					p.vitalsAt = now
				}
			}
		}
	}
	return c.err
}
