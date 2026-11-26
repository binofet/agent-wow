package buddyworld

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
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
	unitBytes1       = 0x4a
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
		p.positionInferred = false
		p.positionAt = now
		p.positionRevision++
		p.observedAt = now
		p.trajectory = observedTrajectory{}
		p.receivedPosition = p.isSelf
	}
	return nil
}
func clearOwnExperience(p *observed) {
	p.xpKnown, p.nextLevelXPKnown, p.xp, p.nextLevelXP = false, false, 0, 0
}

func markAbsent(p *observed, now time.Time) {
	if p.guid == 0 {
		return
	}
	p.present = false
	p.positionKnown = false
	p.positionInferred = false
	p.movementKnown = false
	p.mapKnown = false
	p.observedAt = now
	p.attackVictimKnown = false
	p.attackVictim = 0
	p.trajectory = observedTrajectory{}
	clearCastingContext(p)
	clearInventoryContext(p)
	clearOwnExperience(p)
	p.offhandKnown, p.offhand = false, 0
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
		if err := position(p, x, y, z, o, now); err != nil {
			return true, err
		}
		clearCastingContext(p)
		clearInventoryContext(p)
		clearOwnExperience(p)
		p.offhandKnown, p.offhand = false, 0
		markAbsent(&players[1], now)
		state.nearbyPlayers = make(map[uint64]*observed)
		state.creatures = make(map[uint64]*observed)
		state.creatureCoverageLost = false
		state.unreflectedUnits = nil
		state.gameObjects = make(map[uint64]*observed)
		state.groundEffects = nil
		state.groundEffectsOverflow = false
		state.corpses = make(map[uint64]*observed)
		p.mapID, p.mapKnown, p.present = mapID, true, true
		p.movementKnown = false
		state.visitRevision++
		return true, nil
	case opcode.SMSGTransferPending:
		// A transfer has variable transport data. Stop using the old world now.
		if len(payload) < 4 {
			return true, errShort
		}
		markAbsent(&players[0], now)
		markAbsent(&players[1], now)
		state.nearbyPlayers = make(map[uint64]*observed)
		state.creatures = make(map[uint64]*observed)
		state.creatureCoverageLost = false
		state.unreflectedUnits = nil
		state.gameObjects = make(map[uint64]*observed)
		state.groundEffects = nil
		state.groundEffectsOverflow = false
		state.corpses = make(map[uint64]*observed)
		return true, nil
	case opcode.SMSGUpdateObject:
		return true, updates(c, state, now)
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther:
		return state.observeCreatureCast(op, c, now)
	case opcode.SMSGCompressedUpdateObject:
		declared := c.u32()
		if c.err != nil {
			return true, c.err
		}
		if declared > maxPayload || declared < 4 {
			return true, errBounds
		}
		stream := bytes.NewReader(payload[4:])
		r, err := zlib.NewReader(stream)
		if err != nil {
			return true, errCompressed
		}
		data, err := io.ReadAll(io.LimitReader(r, int64(declared)+1))
		closeErr := r.Close()
		if err != nil || closeErr != nil || len(data) != int(declared) || stream.Len() != 0 {
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
	case opcode.SMSGAttackerstateupdate, opcode.SMSGSpellnonmeleedamagelog:
		var attacker, victim uint64
		var damage uint32
		var err error
		if op == opcode.SMSGAttackerstateupdate {
			attacker, victim, damage, err = decodeIncomingMeleeDamage(payload)
		} else {
			attacker, victim, _, damage, err = decodeIncomingSpellDamage(payload)
		}
		if err != nil {
			return true, err
		}
		v := player(players, victim)
		if v == nil {
			v = state.nearbyPlayers[victim]
		}
		if damage > 0 && v != nil && v.present && state.creatures[attacker] != nil {
			p := state.target(attacker)
			if p.present {
				p.attackVictimKnown, p.attackVictim, p.attackAt = true, victim, now
				p.observedAt = now
			}
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
		if p != nil && p.isSelf {
			p.receivedMotionInvalid = true
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
			p.receivedMotionInvalid = p.isSelf
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
			p.receivedMotionInvalid = p.isSelf
		}
		return true, nil
	case opcode.SMSGClientControlUpdate:
		guid, enabled := c.guid(), c.u8()
		if err := c.done(); err != nil {
			return true, err
		}
		if enabled > 1 {
			return true, errLayout
		}
		if p := &players[0]; guid != 0 && guid == p.guid {
			p.receivedMotionInvalid = true
			p.trajectory.controlReturned = enabled == 1
			p.observedAt = now
			settleSelfSpline(p, now)
		}
		return true, nil
	case opcode.SMSGMonsterMove, opcode.SMSGMonsterMoveTransport:
		guid := c.guid()
		p := state.target(guid)
		if p != nil && !p.isSelf && !creatureGUID(guid) {
			// Only the session mover can receive a matching control-return signal.
			if err := monsterMove(c, nil, op == opcode.SMSGMonsterMoveTransport, now); err != nil {
				return true, err
			}
			p.positionKnown = false
			p.positionInferred = false
			p.movementKnown = false
			p.observedAt = now
			p.trajectory = observedTrajectory{}
			return true, nil
		}
		return true, monsterMove(c, p, op == opcode.SMSGMonsterMoveTransport, now)
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
			p.positionInferred = false
			p.movementKnown = false
			p.observedAt = now
			p.trajectory = observedTrajectory{}
			p.receivedMotionInvalid = p.isSelf
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
	if c.err != nil {
		return c.err
	}
	start := c.off
	info, consumed, err := movementinfo.DecodePrefix(c.b[start:])
	if err != nil {
		return errShort
	}
	c.skip(consumed)
	// Preserve the existing World base-pose policy. Optional floats were
	// previously skipped, so their validity qualifies only the new own facts.
	if err := position(p, info.X, info.Y, info.Z, info.Orientation, now); err != nil {
		return err
	}
	if p != nil {
		p.movementKnown = true
		p.movementFlags = info.Flags
		p.extraFlags = uint32(info.ExtraFlags)
		p.movementTime = info.Time
		if p.isSelf {
			p.receivedMovement = true
			p.receivedMovementInfo = info
			clear(p.receivedMovementWire[:])
			copy(p.receivedMovementWire[:], c.b[start:start+consumed])
			p.receivedMovementWireLen = uint8(consumed)
		}
	}
	return nil
}

func updates(c *cursor, state *observations, now time.Time) error {
	players := &state.players
	count := c.u32()
	state.bodyInputBarrier, state.bodyInputFields = "", nil
	if count > maxBlocks {
		return errBounds
	}
	for i := uint32(0); i < count; i++ {
		typ := c.u8()
		switch typ {
		case 0, 1, 2, 3:
			guid := c.guid()
			protected := guid != 0 && (guid == players[0].guid || guid == players[1].guid)
			// Only the selected body and its observed destination can be
			// superseded by these keyed records. Other entities are decoded
			// normally; their movement and lifecycle do not move this body.
			if guid == 0 {
				state.bodyInputBarrier = modcb.BodyUpdateUnknownSubject
			} else if protected && typ != 0 {
				state.blockBodyRecord(guid, typ)
			}
			p := state.target(guid)
			newCreature, newItem, newGameObject, newPlayer, newCorpse := false, false, false, false, false
			newGroundEffect := false
			if typ == 0 || typ == 1 {
				state.noteUnitWithoutCreate(guid)
			}
			if typ == 2 || typ == 3 {
				objectType := c.u8()
				if objectType > 7 || p != nil && p.publicPlayer && objectType != 4 || state.corpses[guid] != nil && objectType != 7 || state.groundEffects[guid] != nil && objectType != 6 {
					return errLayout
				}
				if player(players, guid) != nil {
					if objectType != 4 {
						return errLayout
					}
					revision, questRevision, backpackRevision, inventoryEpoch := p.positionRevision, p.questRevision, p.backpackRevision, p.inventoryEpoch
					selfMap := players[0]
					self := guid == players[0].guid
					*p = observed{guid: guid, positionRevision: revision, present: true, isSelf: self, questLogKnown: self, questRevision: questRevision, backpackKnown: self, backpackRevision: backpackRevision, mainhandKnown: self, offhandKnown: self, copperKnown: self, xpKnown: self, nextLevelXPKnown: self, skillsKnown: self, freePrimaryKnown: self, bagSlotsKnown: self, equipmentKnown: self, inventoryEpoch: inventoryEpoch}
					if self {
						p.receivedOwnCreate = true
						p.ammoKnown = true // Own CREATE omits the selected-ammo zero baseline.
						p.receivedUnitFlags = true
						p.questRevision++
						p.backpackRevision++
						for _, f := range []int{unitChannelObject, unitChannelObject + 1, unitChannelSpell, unitShapeshiftBytes} {
							p.known[f] = true
						}
					}
					setMap(p, selfMap)
					for _, f := range []int{unitBytes0, unitBytes1, unitHealth, unitMaxHealth, unitFlags, unitDynamicFlags, playerFlags, 0x12, 0x13, 0x37, 0x3c, 0x36, 0x78, 0x42, 0x3e} {
						p.known[f] = true
					}
					for f := 0; f < 7; f++ {
						p.known[unitPower1+f] = true
						p.known[unitMaxPower1+f] = true
					}
					p.vitalsAt = now
					p.observedAt = now
				} else if objectType == 4 && publicPlayerGUID(guid) {
					revision := uint64(0)
					if p != nil {
						revision = p.positionRevision
					}
					p = &observed{guid: guid, present: true, publicPlayer: true, positionRevision: revision, observedAt: now, vitalsAt: now}
					setMap(p, players[0])
					// Public player CREATE omits zero-valued fields. Establish
					// only the same public baselines used for the fixed owner.
					for _, f := range []int{unitBytes0, unitHealth, unitMaxHealth, unitFlags, unitDynamicFlags, playerFlags, 0x3c, 0x36, 0x12, 0x13} {
						p.known[f] = true
					}
					for f := 0; f < 7; f++ {
						p.known[unitPower1+f] = true
						p.known[unitMaxPower1+f] = true
					}
					newPlayer = true
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
				} else if objectType == 6 && guid>>48 == 0xf100 && uint32(guid) != 0 {
					firstSeen, revision := now, uint64(0)
					if p != nil {
						firstSeen, revision = p.groundEffectFirstSeen, p.positionRevision
					}
					p = &observed{guid: guid, present: true, observedAt: now, groundEffectFirstSeen: firstSeen, positionRevision: revision}
					setMap(p, players[0])
					newGroundEffect = true
				} else if objectType == 7 && corpseGUID(guid) {
					revision := uint64(0)
					if p != nil {
						revision = p.positionRevision
					}
					p = &observed{guid: guid, present: true, positionRevision: revision, observedAt: now}
					setMap(p, players[0])
					for _, f := range []int{corpseOwner, corpseOwner + 1, corpseFlags, corpseDynamic} {
						p.known[f] = true
					}
					newCorpse = true
				} else if (objectType == 1 || objectType == 2) && itemGUID(guid) {
					p = &observed{guid: guid, present: true, observedAt: now, isContainer: objectType == 2, containerKnown: objectType == 2, inventoryItemKnown: true, inventoryEpoch: players[0].inventoryEpoch}
					if objectType == 2 {
						p.known[containerSlots] = true
						for field := containerFirst; field < containerFirst+maxBagSlots*2; field++ {
							p.known[field] = true
						}
					}
					for _, f := range []int{objectEntry, itemOwner, itemOwner + 1, itemStack, 0x3c, 0x3d} {
						p.known[f] = true
					}
					for f := itemEnhancementFirst; f < itemEnhancementEnd; f++ {
						p.known[f] = true
					}
					newItem = true
				} else {
					if objectType == 3 {
						state.noteUnreflectedUnit(guid)
					}
					p = nil
				}
			}
			if typ != 0 {
				if err := movementUpdate(c, p, now); err != nil {
					return err
				}
			}
			if typ != 1 {
				// Classify each VALUES block before mutation. A later block can
				// restore a field without undoing an intervening death or control change.
				var restricted *observations
				if typ == 0 && protected {
					restricted = state
				}
				if err := values(c, p, now, typ == 2 || typ == 3, restricted); err != nil {
					return err
				}
				if p != nil && p.cast.spell != 0 && observedCreatureDead(p) {
					p.cast = observedCreatureCast{}
				}
			}
			if newPlayer {
				state.putNearbyPlayer(p)
			}
			if newCreature {
				state.putCreature(p)
			}
			if p != nil && itemGUID(guid) && typ != 1 {
				owner := uint64(p.fields[itemOwner]) | uint64(p.fields[itemOwner+1])<<32
				if owner != players[0].guid {
					// Private stack/container observations cannot be renewed by a
					// later owner-only delta after the item left this character.
					p.inventoryItemKnown, p.containerKnown = false, false
				}
			}
			if newItem {
				state.putItem(p)
			} else if p != nil && itemGUID(guid) && typ != 1 {
				state.itemRevision++
			}
			if newGameObject {
				state.putGameObject(p)
			}
			if newGroundEffect {
				state.putGroundEffect(p)
			}
			if newCorpse {
				state.putCorpse(p)
			}
		case 4, 5:
			n := c.u32()
			if n > maxBlocks {
				return errBounds
			}
			for j := uint32(0); j < n; j++ {
				guid := c.guid()
				if guid == 0 {
					state.bodyInputBarrier = modcb.BodyUpdateUnknownSubject
				} else if typ == 4 && (guid == players[0].guid || guid == players[1].guid) {
					state.blockBodyRecord(guid, typ)
				}
				if typ == 4 {
					state.remove(guid, now)
				} else {
					state.noteUnitWithoutCreate(guid)
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
	t.id = c.u32()
	t.idKnown = true
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
		if p.isSelf && t.relative && p.receivedMovement && p.receivedMovementInfo.TransportPresent {
			t.transportKnown = true
			t.transport = p.receivedMovementInfo.Transport.GUID
			t.seat = p.receivedMovementInfo.Transport.Seat
		}
		p.trajectory = t
		if p.isSelf {
			p.receivedSpline = true
			p.receivedSplineHeader = t
		}
	}
	return c.err
}

// Preserve the first physical cause. A later correction of the selected body
// takes precedence over a change to the optional followed destination.
func (s *observations) blockBody(reason string) {
	if s.bodyInputBarrier == "" || modcb.BodyUpdateIsOwnerOnly(s.bodyInputBarrier) && !modcb.BodyUpdateIsOwnerOnly(reason) ||
		s.bodyInputBarrier == modcb.BodyUpdatePosture && !modcb.BodyUpdateIsOwnerOnly(reason) && reason != modcb.BodyUpdatePosture {
		s.bodyInputBarrier = reason
	}
}

func (s *observations) blockBodyRecord(guid uint64, typ uint8) {
	if guid == s.players[0].guid {
		if typ == 1 {
			s.blockBody(modcb.BodyUpdateMovement)
		} else {
			s.blockBody(modcb.BodyUpdateLifecycle)
		}
	} else if typ == 1 {
		s.blockBody(modcb.BodyUpdateOwnerMovement)
	} else {
		s.blockBody(modcb.BodyUpdateOwnerLifecycle)
	}
}

// Compare the actual physical constraint affected by a decoded build-12340
// player field before changing it. Numeric vitals, inventory, appearance and
// other ordinary game state can change without changing the body's movement.
// Walking authority, body identity, collision geometry, life and control cannot.
func bodyInputValueUnchanged(p *observed, field int, value uint32) bool {
	const playerEnd = 0x94 + 0x49a // UNIT_END + player update fields in build 12340
	if p == nil || field < 0 || field >= playerEnd {
		return false
	}
	var mask uint32
	switch field {
	case unitHealth, unitMaxHealth:
		if !p.known[unitHealth] || !p.known[unitMaxHealth] {
			return false
		}
		health, maxHealth := p.fields[unitHealth], p.fields[unitMaxHealth]
		alive := health > 0 && health <= maxHealth
		if field == unitHealth {
			health = value
		} else {
			maxHealth = value
		}
		return alive && health > 0 && health <= maxHealth
	case unitBytes0:
		mask = 0x00ffffff // race, class and gender; the high byte is power type
	case unitFlags:
		// Movement/control flags used by normal Actions, plus ability to swim.
		mask = movementflags.UnsafeUnitFlags | movementflags.CannotSwim
	case 0x3c: // FEIGN_DEATH, FORCE_MOVEMENT, CANNOT_TURN
		mask = movementflags.ForcedOrFeign | movementflags.CannotTurn
	case unitDynamicFlags:
		mask = 0x20 // DEAD
	case playerFlags:
		mask = 0x10 // GHOST
	case unitBytes1:
		if !p.known[field] {
			return false
		}
		old := p.fields[field]
		return old == value || byte(old) <= 6 && byte(value) == 0 && old>>8 == value>>8
	case unitShapeshiftBytes:
		mask = 0xff000000 // the form, independently of sheathing/PvP
	case 0, 1, 2, 3, 4, 6, 7, 0xc, 0xd, unitChannelObject, unitChannelObject + 1, unitChannelSpell,
		0x41, 0x43, 0x45, 0x92: // identity, scale, charm, channel, radius, model, mount, hover height
		mask = ^uint32(0)
	default:
		return true
	}
	return p.known[field] && (p.fields[field]^value)&mask == 0
}

// Field indices are bounded by the packet's one-byte mask count. Public
// non-identity scalars may include their values. Other fields reveal only
// their index, including private fields outside our cached player prefix.
func (s *observations) recordBodyField(p *observed, field int, value uint32) {
	if len(s.bodyInputFields) >= modcb.MaxBodyInputFields {
		if len(s.bodyInputFields) == modcb.MaxBodyInputFields {
			s.bodyInputFields = append(s.bodyInputFields, "more")
		}
		return
	}
	row := fmt.Sprintf("0x%04x", field)
	if modcb.PublicBodyDiagnosticField(field) {
		old := "?"
		if p.known[field] {
			old = fmt.Sprintf("0x%08x", p.fields[field])
		}
		row += fmt.Sprintf("=%s>0x%08x", old, value)
	}
	s.bodyInputFields = append(s.bodyInputFields, row)
}

func values(c *cursor, p *observed, now time.Time, create bool, restricted *observations) error {
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
			if restricted != nil && !bodyInputValueUnchanged(p, field, v) {
				if p != nil && p.isSelf {
					if field == unitBytes1 && p.known[field] && byte(p.fields[field]) <= 6 && byte(v) <= 6 && p.fields[field]>>8 == v>>8 {
						restricted.blockBody(modcb.BodyUpdatePosture)
					} else {
						restricted.blockBody(modcb.BodyUpdateSelfField)
						restricted.recordBodyField(p, field, v)
					}
				} else {
					restricted.blockBody(modcb.BodyUpdateOwnerField)
				}
			}
			if p != nil && p.isContainer && field == containerSlots && v > maxBagSlots {
				return errBounds
			}
			if p != nil && p.isSelf {
				// A level-only delta cannot make old-level XP a current fact.
				// Own CREATE's full zero baseline is already established above.
				if !create && field == 0x36 && (!p.known[field] || p.fields[field] != v) {
					clearOwnExperience(p)
				}
				if field == playerXP {
					p.xpKnown, p.xp = true, v
				}
				if field == playerNextLevelXP {
					p.nextLevelXPKnown, p.nextLevelXP = true, v
				}
				if field == playerAmmo {
					p.ammoKnown = true
					p.ammo = v
				}
				if field == unitFlags {
					p.receivedUnitFlags = true
				}
				if field >= playerEquipmentFirst && field < playerEquipmentFirst+equipmentSlots*2 {
					slot, shift := (field-playerEquipmentFirst)/2, uint((field-playerEquipmentFirst)%2*32)
					p.equipment[slot] = p.equipment[slot]&^(uint64(0xffffffff)<<shift) | uint64(v)<<shift
				}
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
				if field == playerOffhand || field == playerOffhand+1 {
					shift := uint((field - playerOffhand) * 32)
					p.offhand = p.offhand&^(uint64(0xffffffff)<<shift) | uint64(v)<<shift
				}
				if field >= playerBagsFirst && field < playerBagsFirst+8 {
					slot, shift := (field-playerBagsFirst)/2, uint((field-playerBagsFirst)%2*32)
					p.bags[slot] = p.bags[slot]&^(uint64(0xffffffff)<<shift) | uint64(v)<<shift
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
			if p != nil && field < len(p.fields) && (!p.publicPlayer || publicPlayerField(field)) {
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
