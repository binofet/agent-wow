package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// Physical caster observations and ordinary packet/effect mechanics only.
// Ordinary target and spell selection lives in the attached Lua policy.
func safeSelfCombat(w *worldv1.SnapshotResponse, selfGUID uint64) string {
	if w == nil || w.Self == nil || w.Self.Guid != selfGUID {
		return "player_identity_mismatch"
	}
	p := w.Self
	if !p.Present || !p.MapKnown || !p.PositionKnown || !finite(playerPosition(p)) || !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 {
		return "self_defense_character_unknown_or_dead"
	}
	if !p.MovementFlagsKnown || !movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) {
		return "unsupported_movement_state"
	}
	const unsafe = uint32(0x1 | 0x4 | 0x00040000 | 0x00100000 | 0x00400000 | 0x00800000 | 0x01000000 | 0x02000000 | 0x08000000)
	if !p.UnitFlagsKnown || p.UnitFlags&unsafe != 0 {
		return "unsafe_unit_state"
	}
	return ""
}

func (s *Service) faceEnemyLocked(ctx context.Context, target position) bool {
	facing := s.pos
	facing.o = float32(math.Atan2(float64(target.y-facing.y), float64(target.x-facing.x)))
	if facing.o < 0 {
		facing.o += 2 * math.Pi
	}
	if !s.movementLocked(ctx, opcode.MSGMoveSetFacing, facing, false) {
		s.state.Assisting = false
		s.stopAutoAttackLocked(ctx)
		return false
	}
	return true
}
func creaturePosition(c *worldv1.CreatureState) (position, bool, bool) {
	if c == nil {
		return position{}, false, false
	}
	p := position{c.X, c.Y, c.Z, c.Orientation}
	t := c.Trajectory
	if t != nil && t.Known {
		if t.TransportRelative {
			return p, false, false
		}
		if t.Stopped {
			return p, c.PositionKnown && finite(p), false
		}
		if t.Cyclic || !t.DestinationKnown || t.DurationMs == 0 {
			return p, false, false
		}
		destination := position{t.DestinationX, t.DestinationY, t.DestinationZ, c.Orientation}
		if !finite(destination) {
			return p, false, false
		}
		if t.AgeMs >= uint64(t.DurationMs) {
			return destination, true, true
		}
		if !c.PositionKnown || !finite(p) {
			return p, false, false
		}
		// The world feed identifies a server-declared spline start, endpoint,
		// duration and elapsed time. Its linear estimate is sufficient to
		// attempt a normal target cast/swing; the server still checks actual
		// range and LoS. Motion is not a reason to refuse a legal action.
		fraction := math.Min(1, float64(t.AgeMs)/float64(t.DurationMs))
		p.x += float32(float64(destination.x-p.x) * fraction)
		p.y += float32(float64(destination.y-p.y) * fraction)
		p.z += float32(float64(destination.z-p.z) * fraction)
		if fraction < 1 && (destination.x != c.X || destination.y != c.Y) {
			p.o = followFacing(position{x: c.X, y: c.Y, z: c.Z}, destination)
		}
		return p, finite(p), true
	}
	if c.MovementFlags&0x08000000 != 0 || c.MovementFlags&0xff != 0 && c.PositionAgeMs > 1500 {
		return p, false, false
	}
	return p, c.PositionKnown && finite(p), false
}

const (
	spellFailedInterrupted       = 40
	spellFailedInterruptedCombat = 41
)

func (s *Service) castFailedLocked(code uint32, reason string) {
	s.state.LastCastFailure = code
	s.state.CastPending = false
	kind := s.spellKindLocked(s.pendingSpell)
	if code == spellFailedConfused || code == spellFailedFleeing ||
		(s.clientControlLost || s.clientControlRestoring) && (code == spellFailedInterrupted || code == spellFailedInterruptedCombat) {
		// The server rejected this cast because of our own temporary condition.
		// Keep the normal GCD and reselect healing/offense once it wears off.
		s.state.Reason = "server_cast_interrupted_by_control_loss"
	} else if kind == spellcatalog.Interrupt {
		s.state.Reason = "interrupt_cast_rejected"
	} else if kind != spellcatalog.Heal && (s.state.Practicing || s.fargodeep.active) {
		s.failedAssistTarget = s.pendingTarget
		s.failedAssistCastTarget = s.pendingTarget
		s.nextAssist = s.now().Add(5 * time.Second)
		if spellFamily(s.pendingSpell, smite) {
			s.state.Reason = "server_smite_failed_target_paused"
		} else {
			s.state.Reason = "server_offensive_spell_failed_target_paused"
		}
	} else {
		// A definite server rejection settles only this cast. Keep the real
		// GCD/cooldown reservation; fresh observations may select another heal
		// or patient without imposing a global target-failure retry policy.
		s.state.Reason = reason
	}
	s.pendingSpell = 0
	s.pendingTarget = 0
}

// Positive attack evidence for out-of-combat legality, without selecting a target.
func (s *Service) hasObservedSelfAttackerLocked(w *worldv1.SnapshotResponse) bool {
	if len(w.NearbyCreatures) > 32 {
		return false
	}
	for _, view := range [][]*worldv1.CreatureState{w.NearbyCreatures, {w.OwnerTarget, w.RequestedCreature}} {
		for _, c := range view {
			if c == nil || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.HealthKnown || !c.AliveKnown || !c.Alive || c.Health == 0 || !c.AttackVictimKnown || c.AttackVictimGuid != s.selfGUID || !ongoingAttack(c, w.Self) {
				continue
			}
			if kind := c.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
				continue
			}
			return true
		}
	}
	return false
}
