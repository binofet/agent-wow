package buddyactions

import (
	"context"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPostRecoveryActualLifeRetiresOnlyPhysicalCustody(t *testing.T) {
	for _, unknownPower := range []bool{false, true} {
		s, f, _ := recoveryRig(t)
		s.Tick(context.Background())
		p := f.world.Self
		p.Alive, p.Health, p.Power, p.PlayerFlags = true, 1, 0, 0
		p.PowerKnown = !unknownPower
		// This is another operation's unresolved cast, not the corpse task.
		s.state.CastPending, s.pendingSpell, s.pendingTarget = true, 6063, 1
		s.castUntil = s.now().Add(time.Hour)
		s.Tick(context.Background())
		if !s.recovery.state.AliveObserved || s.recovery.state.Active || s.recovery.state.Rested ||
			s.recovery.state.Phase != "alive_observed" || s.state.InteractionOwner != "" ||
			!s.state.CastPending || s.pendingSpell != 6063 || s.pendingTarget != 1 ||
			countOpcode(f, opcode.CMSGRepopRequest) != 1 || countOpcode(f, opcode.CMSGCastSpell) != 0 {
			t.Fatal("life retained a native rest gate or erased unrelated cast custody", unknownPower, s.stateLocked())
		}
	}
}

func TestPostRecoveryUnknownStopWaitsForActualFreshPose(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.Tick(context.Background())
	f.world.Self.Alive, f.world.Self.Health = true, 10
	f.world.Self.Power = 0
	s.state.Moving = true
	f.failSend = true
	s.Tick(context.Background())
	if !s.recovery.state.Active || !s.recovery.state.AliveObserved || !s.state.MotionUnconfirmed ||
		s.state.InteractionOwner != "recovery" || countOpcode(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("unknown STOP released physical custody", s.stateLocked(), f.packets)
	}
	s.Tick(context.Background())
	if countOpcode(f, opcode.MSGMoveStop) != 1 || !s.recovery.state.Active {
		t.Fatal("unchanged unknown STOP replayed", s.stateLocked(), f.packets)
	}
	f.failSend = false
	f.world.Self.PositionRevision++
	s.Tick(context.Background())
	if s.recovery.state.Active || s.state.MotionUnconfirmed || s.state.Moving ||
		s.state.InteractionOwner != "" || countOpcode(f, opcode.MSGMoveStop) != 2 ||
		s.recovery.state.Phase != "alive_observed" || s.recovery.state.Rested {
		t.Fatal("actual fresh pose did not reconcile only owned STOP", s.stateLocked(), f.packets)
	}
}
