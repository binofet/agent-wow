// Package movementflags classifies actual observed player MovementInfo flags.
package movementflags

// PassiveModifiers change movement status, not the basic ground MovementInfo
// layout: water walking, slow falling (including rogue Safe Fall), and hover.
// Actual falling, flight, swimming, transport, roots and unknown modes remain
// excluded. This classification never proves physical ground or a valid route.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Unit/UnitDefines.h
const PassiveModifiers uint32 = 0x10000000 | 0x20000000 | 0x40000000

const GroundMask uint32 = 0x1ff | PassiveModifiers

// GroundCompatible leaves knowledge, stationary/freshness, map and physical
// navigation checks to the caller. No extra MovementInfo flags are supported.
func GroundCompatible(flags, extra uint32) bool {
	return flags & ^GroundMask == 0 && extra == 0
}
