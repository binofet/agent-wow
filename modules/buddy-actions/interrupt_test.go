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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExactKickIgnoresDamageGCDAndDoesNotReplayRejectedCast(t *testing.T) {
	s, f, enemy, _ := supportPolicyInterruptRig(t)
	s.nextAssist = s.now().Add(time.Second) // Kick is mechanically off the damage GCD.
	req := &actionsv1.InterruptCastRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemy.Guid, SpellId: 1766, CastRevision: 7}
	result, err := s.InterruptCast(context.Background(), req)
	if err != nil || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(f.packets[0].Payload[1:]) != 1766 || result.LastCastTargetGuid != enemy.Guid {
		t.Fatal("exact learned Kick unavailable while damage GCD is active", result, err, f.packets)
	}
	p := []byte{s.castCount}
	p = binary.LittleEndian.AppendUint32(p, 1766)
	p = append(p, 47)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: p})
	if s.failedAssistTarget != 0 || s.failedAssistCastTarget != 0 {
		t.Fatal("failed interrupt blocked ordinary damage")
	}
	if _, err = s.InterruptCast(context.Background(), req); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("replayed exact enemy cast", err, f.packets)
	}
}

func TestExactInterruptRejectsDistantEndedStaleOrUnlearnedWithoutSubstitution(t *testing.T) {
	for name, change := range map[string]func(*worldv1.CreatureState, *actionsv1.InterruptCastRequest){
		"distant":        func(c *worldv1.CreatureState, r *actionsv1.InterruptCastRequest) { c.X = 30 },
		"ended":          func(c *worldv1.CreatureState, r *actionsv1.InterruptCastRequest) { c.ActiveCast.RemainingMs = 0 },
		"stale revision": func(c *worldv1.CreatureState, r *actionsv1.InterruptCastRequest) { r.CastRevision = 6 },
		"unlearned":      func(c *worldv1.CreatureState, r *actionsv1.InterruptCastRequest) { r.SpellId = 2139 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, enemy, _ := supportPolicyInterruptRig(t)
			req := &actionsv1.InterruptCastRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemy.Guid, SpellId: 1766, CastRevision: 7}
			change(enemy, req)
			if _, err := s.InterruptCast(context.Background(), req); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatal("exact interrupt substituted an invalid choice", err, f.packets)
			}
		})
	}
}

func TestNativeTickDoesNotSelectInterruptEnemyOrRank(t *testing.T) {
	for _, mode := range []string{"idle", "following", "interaction_emergency", "expired_host_lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f, enemy, advance := supportPolicyInterruptRig(t)
			f.world.Owner.Alive, f.world.Owner.Health = true, 100
			enemy.AttackVictimKnown, enemy.AttackVictimGuid = true, s.selfGUID
			enemy.X = 3
			switch mode {
			case "following":
				toggle(t, s, true)
			case "interaction_emergency":
				s.state.InteractionOwner = "fixture"
				s.interactionUntil = s.now().Add(time.Minute)
			case "expired_host_lease":
				activateEncounterWriter(t, s, f)
				advance(encounterLeaseDuration)
			}
			for i := 0; i < 3; i++ {
				s.Tick(context.Background())
				advance(100 * time.Millisecond)
			}
			for _, packet := range f.packets {
				if packet.Opcode == opcode.CMSGCastSpell && binary.LittleEndian.Uint32(packet.Payload[1:]) == 1766 {
					t.Fatal("native scheduler independently selected Kick", s.stateLocked(), f.packets)
				}
			}
			w, _ := s.snapshot(context.Background())
			view := s.encounterSupportLocked(context.Background(), w, s.onlineHealingGroup(f.party.Group))
			if len(view.Interrupts) != 1 || !view.Interrupts[0].Ready || view.Interrupts[0].SpellId != 1766 || view.Interrupts[0].CastRevision != 7 {
				t.Fatal("removing native selection hid the exact learned interrupt", view)
			}
		})
	}
}
