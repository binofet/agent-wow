package buddyactions

import (
	"context"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func sendClientControl(s *Service, guid uint64, allowed byte) {
	p := append(packedGUID(guid), allowed)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGClientControlUpdate, Payload: p})
}

func TestFearResumesCurrentCombatWithoutReenablingAssist(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class uint32
	}{
		{"caster", 8},
		{"melee", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, target, advance := selfDefenseRig(t, tc.class)
			ctx := context.Background()
			s.Tick(ctx)
			if !s.state.CastPending {
				t.Fatal("wanted active combat before fear", s.stateLocked())
			}
			sendClientControl(s, s.selfGUID, 0)
			p := healingRejectionPacket(opcode.SMSGCastFailed, s.selfGUID, s.castCount, s.pendingSpell)
			p.Payload[5] = spellFailedFleeing
			s.OnPacket(ctx, p)
			if tc.class == 4 {
				acknowledgeDefensiveAttackStop(s, target.Guid)
			}
			s.state.MotionUnconfirmed = true
			f.world.Self.PositionKnown = false
			f.world.Self.UnitFlags |= 0x800000
			before := len(f.packets)
			advance(2 * time.Second)
			sendClientControl(s, 99, 1)
			s.Tick(ctx)
			sendClientControl(s, s.selfGUID, 1)
			s.Tick(ctx)
			if len(f.packets) != before || !s.state.Assisting || s.state.CastPending {
				t.Fatal("fear must suspend combat writes and keep combat intent", s.stateLocked())
			}
			f.world.Self.UnitFlags &^= 0x800000
			f.world.Self.PositionKnown = true
			f.world.Self.PositionRevision++
			f.world.Self.X = 0.5
			s.Tick(ctx)
			if countPackets(f, opcode.CMSGCastSpell) != 2 || s.pendingTarget != target.Guid || s.state.MotionUnconfirmed {
				t.Fatal("did not resume against current attacker when movement returned", s.stateLocked(), f.packets)
			}
			if tc.class == 4 && countPackets(f, opcode.CMSGAttackswing) != 2 {
				t.Fatal("did not restart melee after server's stop", f.packets)
			}
		})
	}
}

func TestFearRecoveryDoesNotPullAnEnemyThatStoppedAttacking(t *testing.T) {
	s, f, target, advance := selfDefenseRig(t, 8)
	ctx := context.Background()
	s.Tick(ctx)
	sendClientControl(s, s.selfGUID, 0)
	p := healingRejectionPacket(opcode.SMSGCastFailed, s.selfGUID, s.castCount, s.pendingSpell)
	p.Payload[5] = spellFailedFleeing
	s.OnPacket(ctx, p)
	target.AttackVictimKnown = false
	f.world.Self.PositionRevision++
	sendClientControl(s, s.selfGUID, 1)
	advance(2 * time.Second)
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.CastPending || s.clientControlRestoring {
		t.Fatal("new attack was not backed by a live threat", s.stateLocked(), f.packets)
	}
}
