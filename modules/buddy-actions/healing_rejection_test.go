package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func healingRejectionPacket(op uint32, caster uint64, count byte, spell uint32) *modv1.WorldPacket {
	p := []byte{count}
	p = binary.LittleEndian.AppendUint32(p, spell)
	p = append(p, 47) // Definite server line-of-sight rejection.
	if op != opcode.SMSGCastFailed {
		p = append(packedGUID(caster), p...)
	}
	return &modv1.WorldPacket{Opcode: op, Payload: p}
}

func TestHealingRejectionKeepsOptInAndSelectsFreshPartyTargetAfterCooldown(t *testing.T) {
	for _, op := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther} {
		t.Run(opcode.WorldName(op), func(t *testing.T) {
			s, f, advance := rig(t)
			mira := triagePlayer(f, 4, 75)
			f.world.NearbyPlayers = []*worldv1.PlayerState{mira}
			triageMember(f, 4)
			f.world.Owner.Health = 20
			enableTriage(t, s)
			until := s.healingUntil
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 1 || healingCastTarget(f) != 3 || !s.state.CastPending {
				t.Fatal("initial actual heal missing", state(t, s))
			}
			oldCount, oldSpell := s.castCount, s.pendingSpell
			s.OnPacket(context.Background(), healingRejectionPacket(op, 1, oldCount, oldSpell))
			got := state(t, s)
			if !got.Healing || got.CastPending || got.LastCastFailure != 47 || got.ServerCastGo != 0 || s.pendingSpell != 0 || s.pendingTarget != 0 || s.healingUntil != until {
				t.Fatal("definite rejection lost intent or invented success", got)
			}
			if got.Reason != "server_cast_failed_healing_paused" && got.Reason != "server_cast_interrupted_healing_paused" {
				t.Fatal("misleading rejection reason", got)
			}
			// The original target has left the current party/observation. Only the newly
			// injured, currently online companion may be selected by the next triage.
			f.party.Group.Members[1].Online = false
			f.world.Owner = nil
			mira.Health = 10
			advance(5*time.Second - time.Millisecond)
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 1 || !s.state.Healing {
				t.Fatal("cooldown bypass", state(t, s))
			}
			advance(time.Millisecond)
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 2 || healingCastTarget(f) != 4 || !s.state.CastPending || s.castCount != oldCount+1 || !s.state.Healing || f.world.Self.Health != 100 || mira.Health != 10 {
				t.Fatal("fresh triage did not use current party/target", state(t, s), f.packets)
			}
		})
	}
}

func TestHealingRejectionNeverReenablesExplicitDisable(t *testing.T) {
	for _, when := range []string{"before_receipt", "after_receipt"} {
		t.Run(when, func(t *testing.T) {
			s, f, advance := rig(t)
			enableTriage(t, s)
			s.Tick(context.Background())
			failure := healingRejectionPacket(opcode.SMSGCastFailed, 1, s.castCount, s.pendingSpell)
			if when == "before_receipt" {
				s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			}
			s.OnPacket(context.Background(), failure)
			if when == "after_receipt" {
				s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			}
			advance(6 * time.Second)
			s.Tick(context.Background())
			if s.state.Healing || s.state.CastPending || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("explicit disable was overridden", state(t, s))
			}
		})
	}
}

func TestHealingRejectionRequiresActualPendingCastCorrelation(t *testing.T) {
	for _, mode := range []string{"count", "spell", "caster", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			enableTriage(t, s)
			s.Tick(context.Background())
			count, spell := s.castCount, s.pendingSpell
			next := s.nextHeal
			p := healingRejectionPacket(opcode.SMSGSpellFailure, 1, count, spell)
			switch mode {
			case "count":
				p = healingRejectionPacket(opcode.SMSGCastFailed, 1, count+1, spell)
			case "spell":
				p = healingRejectionPacket(opcode.SMSGCastFailed, 1, count, spell+1)
			case "caster":
				p = healingRejectionPacket(opcode.SMSGSpellFailure, 4, count, spell)
			case "truncated":
				p.Payload = p.Payload[:1]
			}
			s.OnPacket(context.Background(), p)
			if !s.state.Healing || !s.state.CastPending || s.pendingSpell != spell || s.nextHeal != next || s.state.LastCastFailure != 0 || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("unrelated rejection consumed cast", state(t, s))
			}
			s.OnPacket(context.Background(), healingRejectionPacket(opcode.SMSGCastFailed, 1, count, spell))
			if !s.state.Healing || s.state.CastPending || s.state.LastCastFailure != 47 {
				t.Fatal("matching rejection was not settled", state(t, s))
			}
		})
	}
}

func TestHealingRejectionDoesNotRelaxAmbiguityOrLeaseStops(t *testing.T) {
	for _, mode := range []string{"timeout", "transport", "motion", "lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			enableTriage(t, s)
			switch mode {
			case "transport":
				f.failSend = true
			case "lease":
				f.world.Owner.Health = 100
			}
			s.Tick(context.Background())
			before := countPackets(f, opcode.CMSGCastSpell)
			switch mode {
			case "timeout":
				advance(6 * time.Second)
			case "motion":
				s.state.MotionUnconfirmed = true
			case "lease":
				advance(4 * time.Hour)
			}
			s.Tick(context.Background())
			f.failSend = false
			advance(6 * time.Second)
			s.Tick(context.Background())
			if s.state.Healing || countPackets(f, opcode.CMSGCastSpell) != before {
				t.Fatal("ambiguous/expired authority resumed healing", mode, state(t, s))
			}
		})
	}
}
