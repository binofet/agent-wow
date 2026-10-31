package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPassivePlayerModifiersKeepNormalGuardsAndRejectActualAirborneModes(t *testing.T) {
	for _, flags := range []uint32{0x10000000, 0x20000000, 0x40000000, 0x70000000} {
		s, f, _ := rig(t)
		f.world.Self.MovementFlags, f.world.Owner.MovementFlags = flags, flags
		if safePlayers(f.world, 1) != "" || safeSelfCombat(f.world, 1) != "" || safeQuestCharacter(f.world, 1) != "" || !safeHealingPlayer(f.world.Owner, 0) {
			t.Fatal("passive status blocked ordinary players", flags)
		}
		for _, bad := range []uint32{0x200, 0x400, 0x800, 0x1000, 0x2000, 0x200000, 0x400000, 0x800000, 0x1000000, 0x2000000, 0x8000000, 0x80000000} {
			f.world.Self.MovementFlags = flags | bad
			if safePlayers(f.world, 1) == "" || safeSelfCombat(f.world, 1) == "" || safeQuestCharacter(f.world, 1) == "" || safeHealingPlayer(f.world.Self, 0) {
				t.Fatal("passive modifier hid actual unsupported own mode", bad)
			}
		}
		f.world.Self.MovementFlags = flags
		f.world.Owner.MovementFlags = flags | 1
		f.world.Owner.PositionAgeMs = 2001
		if safePlayers(f.world, 1) != "owner_moving_position_stale" || safeHealingPlayer(f.world.Owner, 0) {
			t.Fatal("passive modifier hid stale real motion")
		}
		f.world.Owner.MovementFlags = flags
		f.world.Owner.ExtraMovementFlags = 1
		if safePlayers(f.world, 1) == "" || safeHealingPlayer(f.world.Owner, 0) {
			t.Fatal("unknown extra flags permitted")
		}
		_ = s
	}
}

func TestPassiveOwnerStillGetsActualSupportedBuffAndHealing(t *testing.T) {
	s, f, advance := buffRig(t, 5, 1243)
	f.world.Owner.MovementFlags = 0x70000000
	f.world.Owner.PositionAgeMs = 300000 // Stationary passive modifiers are not motion.
	for i := 0; i < 2; i++ {
		s.Tick(context.Background())
		if s.buffOp == nil || s.buffOp.view.Phase != "waiting_server" {
			t.Fatal("known passive owner blocked buff", s.state.BuffReason)
		}
		confirmBuff(t, s, false)
		advance(3 * time.Second)
	}
	if s.state.BuffsConfirmed != 2 || s.buffOp.view.TargetGuid != 3 {
		t.Fatal("no own GO+actual owner aura proof", s.buffOp)
	}
	s2, f2, _ := rig(t)
	f2.world.Owner.MovementFlags = 0x70000000
	f2.world.Owner.Health = 30
	enableTriage(t, s2)
	s2.Tick(context.Background())
	if healingCastTarget(f2) != 3 || countPackets(f2, opcode.CMSGCastSpell) != 1 {
		t.Fatal("normal injured owner did not get actual heal cast", s2.stateLocked())
	}
}

func TestPassiveOwnerFollowStillRequiresVerifiedNavigation(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			s, f, advance := formationRig(t, 1, 5)
			f.world.Owner.X += 12
			f.world.Owner.MovementFlags = 0x70000000
			calls := 0
			original := s.planPath
			s.planPath = func(ctx context.Context, root string, a, b position) ([]position, error) {
				calls++
				if blocked {
					return nil, fmt.Errorf("actual physical route unavailable")
				}
				return original(ctx, root, a, b)
			}
			toggle(t, s, true)
			for i := 0; i < 5; i++ {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if calls == 0 || (!blocked && !s.state.Moving) || (blocked && countPackets(f, opcode.MSGMoveStartForward) != 0) {
				t.Fatal("passive flags bypassed or blocked normal NAV", calls, s.stateLocked())
			}
		})
	}
}

func TestPassiveOwnMovementRetainsObservedStatusAndRejectsPostClockFlight(t *testing.T) {
	for _, changed := range []bool{false, true} {
		s, f, _ := rig(t)
		f.world.Self.MovementFlags = 0x70000000
		s.refreshPositionLocked(f.world)
		if changed {
			f.onClock = func() { f.world.Self.MovementFlags |= 0x02000000 }
		}
		written := s.movementLocked(context.Background(), opcode.MSGMoveStartForward, s.pos, true)
		if written == changed || countPackets(f, opcode.MSGMoveStartForward) != boolInt(!changed) {
			t.Fatal("postclock mode did not gate movement", changed, written)
		}
		if !changed {
			payload := f.packets[0].Payload
			at := len(packedGUID(1))
			flags := binary.LittleEndian.Uint32(payload[at:])
			if flags&0x70000000 != 0x70000000 || flags&0x02000000 != 0 {
				t.Fatal("actual passive bits lost or flight invented", flags)
			}
		}
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
