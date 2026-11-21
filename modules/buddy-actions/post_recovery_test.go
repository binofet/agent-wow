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
		tickDeathLua(t, s)
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
	s, f := postRecoveryMotionRig(t)
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
		s.state.InteractionOwner != "" || countOpcode(f, opcode.MSGMoveStop) != 1 || s.recovery.state.MovementUnconfirmed ||
		s.recovery.state.Phase != "alive_observed" || s.recovery.state.Rested {
		t.Fatal("actual fresh pose did not reconcile only owned STOP", s.stateLocked(), f.packets)
	}
}

func TestPostRecoveryPredispatchStopRefusalRetainsCustodyForFreshRetry(t *testing.T) {
	s, f := postRecoveryMotionRig(t)
	f.world.Self.Alive, f.world.Self.Health = true, 10
	s.state.Moving, f.failClock = true, true
	s.Tick(context.Background())
	if !s.recovery.state.Active || !s.state.Moving || s.state.MotionUnconfirmed ||
		s.recovery.state.MovementUnconfirmed || s.state.InteractionOwner != "recovery" ||
		countOpcode(f, opcode.MSGMoveStop) != 0 {
		t.Fatal("known prewrite refusal retired custody or invented uncertainty", s.stateLocked(), f.packets)
	}
	f.failClock = false
	s.Tick(context.Background())
	if s.recovery.state.Active || s.state.Moving || s.state.MotionUnconfirmed ||
		s.state.InteractionOwner != "" || countOpcode(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("fresh admissible STOP did not retire physical recovery", s.stateLocked(), f.packets)
	}
}

func TestPostRecoveryControlReturnReconcilesBeforeOwnedStop(t *testing.T) {
	s, f := postRecoveryMotionRig(t)
	f.world.Self.Alive, f.world.Self.Health = true, 10
	s.clientControlLost = true
	s.Tick(context.Background())
	if !s.recovery.state.Active || s.state.InteractionOwner != "recovery" || countOpcode(f, opcode.MSGMoveStop) != 0 {
		t.Fatal("lost movement control retired physical recovery", s.stateLocked())
	}
	packet := append(packedGUID(s.selfGUID), 1)
	if !s.clientControlPacketLocked(packet) || !s.clientControlRestoring {
		t.Fatal("actual control return was not retained")
	}
	f.world.Self.PositionRevision++
	s.Tick(context.Background())
	if s.clientControlRestoring || s.recovery.state.Active || s.state.InteractionOwner != "" || s.state.MotionUnconfirmed {
		t.Fatal("recovery prevented normal fresh control reconciliation", s.stateLocked())
	}
}

// These checks own real physical ghost movement, not an obsolete native
// release-controller lease. The shared Lua policy starts the exact route.
func postRecoveryMotionRig(t *testing.T) (*Service, *fakeSession) {
	t.Helper()
	s, f, _ := recoveryRig(t)
	tickDeathLua(t, s)
	ghostAt(f, 0)
	tickDeathLua(t, s)
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	tickDeathLua(t, s)
	if !s.recoveryOwnsLocked() || !s.state.Moving {
		t.Fatal("no actual recovery motion", s.stateLocked())
	}
	f.packets = nil
	return s, f
}
