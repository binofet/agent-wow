package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRejectedResurrectionPreservesActualGCDWithoutDelayingHealing(t *testing.T) {
	for _, code := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther} {
		t.Run(opcode.WorldName(code), func(t *testing.T) {
			s, f, advance := resRig(t)
			learnSpells(s, 2006, 2050)
			s.UseHostStandingHealing()
			if _, err := requestResurrection(s); err != nil {
				t.Fatal(err)
			}
			gcd := s.nextHeal.Sub(s.now())
			if gcd <= 0 || gcd != time.Duration(s.learnedContractsLocked()[2006].GcdMs)*time.Millisecond {
				t.Fatal("resurrection must retain its real GCD", gcd)
			}
			ready := s.nextHeal
			s.OnPacket(context.Background(), healingRejectionPacket(code, s.selfGUID, s.castCount, 2006))
			if s.resurrectionBindings[3] || s.state.CastPending || s.nextHeal != ready {
				t.Fatal("definite rejection introduced a retry delay or retained death custody", s.stateLocked())
			}
			f.world.Self.Health = 20
			request := &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: s.selfGUID, SpellId: 2050}
			advance(gcd - time.Millisecond)
			if _, err := s.CastHeal(context.Background(), request); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("bypassed real GCD", err)
			}
			advance(time.Millisecond)
			options, err := s.HealingOptions(context.Background(), &emptypb.Empty{})
			if err != nil || !options.Caster.Ready {
				t.Fatal("rejected resurrection suppressed unrelated healing", options, err)
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("native replayed rejected resurrection")
			}
			if _, err := s.CastHeal(context.Background(), request); err != nil || lastCast(f) != 2050 {
				t.Fatal("fresh Lua-selected self heal unavailable after actual GCD", err, s.stateLocked())
			}
		})
	}
}

func TestResurrectionDeathCustodyRequiresDefiniteRejectionOrObservedLife(t *testing.T) {
	for _, phase := range []string{"offered", "unknown_transport", "timeout"} {
		t.Run(phase, func(t *testing.T) {
			s, f, advance := resRig(t)
			if phase == "unknown_transport" {
				f.failSend = true
			}
			_, _ = requestResurrection(s)
			if phase == "offered" {
				p := smiteEvent(true, s.castCount, 3)
				_, _, _, offset, _ := castEvent(p)
				// Spell-go launch is not proof that the player accepted the offer.
				p[offset-4], p[offset-3], p[offset-2], p[offset-1] = 0xd6, 0x07, 0, 0
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: p})
			}
			advance(time.Minute)
			w, _ := s.snapshot(context.Background())
			options := s.encounterSupportLocked(context.Background(), w, s.onlineHealingGroup(f.party.Group))
			if !s.resurrectionBindings[3] || len(options.Resurrections) != 0 {
				t.Fatal("ambiguous or offered resurrection rearmed the same death", options, s.supportOp)
			}
			if _, err := requestResurrection(s); err == nil || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("same death repeated", err, f.packets)
			}
			// An actual living update settles this death; a later observed death is a
			// new decision, not a replay or a permanent blacklist by player GUID.
			f.world.Owner.Alive, f.world.Owner.Health = true, 100
			state(t, s)
			if s.resurrectionBindings[3] {
				t.Fatal("observed resurrection did not release death custody")
			}
			f.world.Owner.Alive, f.world.Owner.Health = false, 0
			f.failSend = false
			if _, err := requestResurrection(s); err != nil || countPackets(f, opcode.CMSGCastSpell) != 2 {
				t.Fatal("new observed death remained hidden", err, s.stateLocked())
			}
		})
	}
}
