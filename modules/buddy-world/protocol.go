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
	if guid == 0 {
		return nil
	}
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
		p.trajectory = observedTrajectory{}
	}
	return nil
}
func markAbsent(p *observed, now time.Time) {
	if p.guid == 0 {
		return
	}
	p.present = false
	p.positionKnown = false
	p.movementKnown = false
	p.mapKnown = false
	p.observedAt = now
	p.attackVictimKnown = false
	p.attackVictim = 0
	p.trajectory = observedTrajectory{}
	clearCastingContext(p)
}
func setMap(p *observed, self observed) { p.mapID, p.mapKnown = self.mapID, self.mapKnown }

func parsePacket(op uint32, payload []byte, state *observations, now time.Time) (bool, error) {
	players := &state.players
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
		clearCastingContext(p)
		if op == opcode.SMSGNewWorld || !p.mapKnown || p.mapID != mapID {
			markAbsent(&players[1], now)
			state.creatures = make(map[uint64]*observed)
			state.gameObjects = make(map[uint64]*observed)
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
		state.creatures = make(map[uint64]*observed)
		state.gameObjects = make(map[uint64]*observed)
		return true, nil
	case opcode.SMSGUpdateObject:
		return true, updates(c, state, now)
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
		return true, updates(&cursor{b: data}, state, now)
	case opcode.SMSGDestroyObject:
		guid := c.u64()
		c.u8()
		if err := c.done(); err != nil {
			return true, err
		}
		state.remove(guid, now)
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
		if p := state.target(guid); p != nil {
			p.fields[unitPower1+powerType] = value
			p.known[unitPower1+powerType] = true
			p.vitalsAt = now
			p.observedAt = now
		}
		return true, nil
	case opcode.SMSGAttackstart:
		attacker, victim := c.u64(), c.u64()
		if err := c.done(); err != nil {
			return true, err
		}
		if p := state.target(attacker); p != nil {
			p.attackVictimKnown = true
			p.attackVictim = victim
			p.attackAt = now
			p.observedAt = now
		}
		return true, nil
	case opcode.SMSGAttackstop:
		attacker := c.guid()
		c.guid()
		dead := c.u32()
		if err := c.done(); err != nil {
			return true, err
		}
		if dead > 1 {
			return true, errLayout
		}
		if p := state.target(attacker); p != nil {
			p.attackVictimKnown = false
			p.attackVictim = 0
			p.attackAt = now
			p.observedAt = now
		}
		return true, nil
	case opcode.MSGMoveTeleportAck:
		// Server's near-teleport correction has an order counter before info.
		guid := c.guid()
		c.skip(4)
		p := state.target(guid)
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
		if p := state.target(guid); p != nil {
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
		if p := state.target(guid); p != nil {
			if op == opcode.SMSGForceRunSpeedChange {
				p.runSpeed = speed
			} else {
				p.walkSpeed = speed
			}
			p.observedAt = now
		}
		return true, nil
	case opcode.SMSGMonsterMove, opcode.SMSGMonsterMoveTransport:
		guid := c.guid()
		p := state.target(guid)
		if !creatureGUID(guid) {
			p = nil
		}
		if err := monsterMove(c, p, op == opcode.SMSGMonsterMoveTransport, now); err != nil {
			return true, err
		}
		// Server spline motion of controlled players is outside this controller.
		if !creatureGUID(guid) {
			if player := state.target(guid); player != nil {
				player.positionKnown = false
				player.movementKnown = false
				player.observedAt = now
			}
		}
		return true, nil
	case opcode.SMSGMoveKnockBack:
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
		if p := state.target(guid); p != nil {
			p.positionKnown = false
			p.movementKnown = false
			p.observedAt = now
			p.trajectory = observedTrajectory{}
		}
		return true, nil
	}
	if movementOpcode(op) {
		guid := c.guid()
		p := state.target(guid)
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

func updates(c *cursor, state *observations, now time.Time) error {
	players := &state.players
	count := c.u32()
	if count > maxBlocks {
		return errBounds
	}
	for i := uint32(0); i < count; i++ {
		typ := c.u8()
		switch typ {
		case 0, 1, 2, 3:
			guid := c.guid()
			p := state.target(guid)
			newCreature, newItem, newGameObject := false, false, false
			if typ == 2 || typ == 3 {
				objectType := c.u8()
				if objectType > 7 {
					return errLayout
				}
				if player(players, guid) != nil {
					if objectType != 4 {
						return errLayout
					}
					revision, questRevision, backpackRevision := p.positionRevision, p.questRevision, p.backpackRevision
					selfMap := players[0]
					self := guid == players[0].guid
					*p = observed{guid: guid, positionRevision: revision, present: true, isSelf: self, questLogKnown: self, questRevision: questRevision, backpackKnown: self, backpackRevision: backpackRevision, mainhandKnown: self, copperKnown: self, skillsKnown: self, freePrimaryKnown: self}
					if self {
						p.questRevision++
						p.backpackRevision++
						for _, f := range []int{unitChannelObject, unitChannelObject + 1, unitChannelSpell, unitShapeshiftBytes} {
							p.known[f] = true
						}
					}
					setMap(p, selfMap)
					for _, f := range []int{unitBytes0, unitHealth, unitMaxHealth, unitFlags, unitDynamicFlags, playerFlags, 0x12, 0x13, 0x37, 0x3c, 0x36, 0x78, 0x42, 0x3e} {
						p.known[f] = true
					}
					for f := 0; f < 7; f++ {
						p.known[unitPower1+f] = true
						p.known[unitMaxPower1+f] = true
					}
					p.vitalsAt = now
					p.observedAt = now
				} else if objectType == 3 && creatureGUID(guid) {
					revision := uint64(0)
					if p != nil {
						revision = p.positionRevision
					}
					p = &observed{guid: guid, present: true, positionRevision: revision, observedAt: now, vitalsAt: now}
					setMap(p, players[0])
					for _, f := range []int{objectEntry, unitNPCFlags, unitHealth, unitMaxHealth, unitFlags, unitDynamicFlags, 0x12, 0x13, 0x36, 0x37, 0x3c, 0x42} {
						p.known[f] = true
					}
					newCreature = true
				} else if objectType == 5 && gameObjectGUID(guid) {
					revision := uint64(0)
					if p != nil {
						revision = p.positionRevision
					}
					p = &observed{guid: guid, present: true, positionRevision: revision, observedAt: now}
					setMap(p, players[0])
					p.known[objectEntry] = true
					p.known[gameObjectBytes] = true
					p.known[gameObjectFlags] = true
					p.known[gameObjectDynamic] = true
					p.known[gameObjectCreatedBy] = true
					p.known[gameObjectCreatedBy+1] = true
					newGameObject = true
				} else if (objectType == 1 || objectType == 2) && itemGUID(guid) {
					p = &observed{guid: guid, present: true, observedAt: now}
					for _, f := range []int{objectEntry, itemOwner, itemOwner + 1, itemStack, 0x3c, 0x3d} {
						p.known[f] = true
					}
					newItem = true
				} else {
					p = nil
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
			if newCreature {
				state.putCreature(p)
			}
			if newItem {
				state.putItem(p)
			} else if p != nil && itemGUID(guid) && typ != 1 {
				state.itemRevision++
			}
			if newGameObject {
				state.putGameObject(p)
			}
		case 4, 5:
			n := c.u32()
			if n > maxBlocks {
				return errBounds
			}
			for j := uint32(0); j < n; j++ {
				guid := c.guid()
				if typ == 4 {
					state.remove(guid, now)
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
			if err := splineCreate(c, p, now); err != nil {
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

func splineCreate(c *cursor, p *observed, now time.Time) error {
	t := observedTrajectory{known: true, at: now}
	t.flags = c.u32()
	t.cyclic = t.flags&0x80000 != 0
	if t.flags&0x20000 != 0 {
		if angle := c.f32(); !validFloat(angle) {
			return errFloat
		}
	} else if t.flags&0x10000 != 0 {
		c.u64()
	} else if t.flags&0x8000 != 0 {
		if _, err := point(c); err != nil {
			return err
		}
	}
	t.elapsed = c.u32()
	t.duration = c.u32()
	c.u32()
	for i := 0; i < 3; i++ {
		if f := c.f32(); !validFloat(f) {
			return errFloat
		}
	}
	c.u32()
	nodes := c.u32()
	if c.err != nil {
		return c.err
	}
	if nodes > maxBlocks || uint64(nodes)*12 > uint64(len(c.b)-c.off) {
		return errBounds
	}
	for i := uint32(0); i < nodes; i++ {
		if _, err := point(c); err != nil {
			return err
		}
	}
	mode := c.u8()
	destination, err := point(c)
	if err != nil {
		return err
	}
	t.destination = destination
	t.destinationKnown = nodes > 0 && t.duration > 0 && !t.cyclic && t.flags&unsafeEndpointFlags == 0 && mode <= 1
	if p != nil {
		t.relative = p.movementFlags&0x200 != 0
		p.trajectory = t
	}
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
			if p != nil && p.isSelf {
				if field >= playerSkillsFirst && field < playerSkillsFirst+128*3 {
					slot, part := (field-playerSkillsFirst)/3, (field-playerSkillsFirst)%3
					p.skills[slot][part] = v
				}
				if field == playerFreePrimary {
					p.freePrimaryKnown = true
					p.freePrimary = v
				}
				if field == playerCoinage {
					p.copperKnown = true
					p.copper = v
				}
				if field >= questLogFirst && field < questLogFirst+25*5 {
					slot := (field - questLogFirst) / 5
					part := (field - questLogFirst) % 5
					if p.questLog[slot][part] != v {
						p.questLog[slot][part] = v
						p.questRevision++
					}
				}
				if field == 0x162 || field == 0x163 {
					shift := uint((field - 0x162) * 32)
					p.mainhand = p.mainhand&^(uint64(0xffffffff)<<shift) | uint64(v)<<shift
				}
				if field >= backpackFirst && field < backpackFirst+32 {
					slot, half := (field-backpackFirst)/2, (field-backpackFirst)%2
					old := p.backpack[slot]
					shift := uint(half * 32)
					p.backpack[slot] = (old &^ (uint64(0xffffffff) << shift)) | uint64(v)<<shift
					if p.backpack[slot] != old {
						p.backpackRevision++
					}
				}
			}
			if p != nil && field < len(p.fields) {
				p.fields[field] = v
				p.known[field] = true
				p.observedAt = now
				if field >= unitBytes0 && field <= unitMaxPower1+6 || field == unitFlags || field == unitDynamicFlags || field == playerFlags {
					p.vitalsAt = now
				}
				if (field == unitHealth && v == 0) || (field == unitDynamicFlags && v&0x20 != 0) {
					p.attackVictimKnown = false
					p.attackVictim = 0
					p.attackAt = now
				}
			}
		}
	}
	return c.err
}
