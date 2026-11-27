package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The current policy continues to renew throughout the old START's receipt
// window. Lease expiry must not supply an unrelated STOP to these cases.
func followAfterUnknownSwing(t *testing.T) (*Service, *fakeSession, *actionsv1.MeleeRequest, func(time.Duration)) {
	t.Helper()
	s, f, request, ctx, advance := leasedMeleeFixture(t)
	seedSyntheticPortalWorldScope(f, 9)
	if _, err := s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		advance(500 * time.Millisecond)
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, request.VisitId), wrapperspb.UInt64(request.VisitId)); err != nil {
			t.Fatal(err)
		}
	}
	op := state(t, s).GetLastMeleeOperation()
	if op.GetPhase() != "outcome_unconfirmed_no_retry" || !op.GetTransportWritten() || !s.state.AutoAttackRequested || s.state.AutoAttacking || countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGAttackstop) != 0 {
		t.Fatalf("fixture lost the original written, unacknowledged request: %v", op)
	}
	return s, f, request, advance
}

func TestFollowAfterUnknownMeleeRequiresObservedTerminalEvidence(t *testing.T) {
	for _, mode := range []string{"exact dead target", "still alive", "missing target", "wrong corpse", "health unknown", "feign death", "foreign visit", "body still fighting", "unordered realm cancel", "other own attack", "other own swing", "old victim stop", "other attack stopped", "unknown cleanup stop"} {
		t.Run(mode, func(t *testing.T) {
			s, f, request, advance := followAfterUnknownSwing(t)
			original := proto.Clone(state(t, s).LastMeleeOperation)
			c := proto.Clone(f.world.RequestedCreature).(*worldv1.CreatureState)
			switch mode {
			case "exact dead target", "wrong corpse", "health unknown", "foreign visit", "body still fighting", "other own attack", "other own swing", "old victim stop", "other attack stopped", "unknown cleanup stop":
				c.Health, c.Alive = 0, false
			case "feign death":
				c.Alive = false
				c.DynamicFlags |= 0x20
			}
			switch mode {
			case "missing target":
				c = nil
			case "wrong corpse":
				c.Guid++
			case "health unknown":
				c.HealthKnown = false
			case "foreign visit":
				f.world.VisitRevision++
			case "body still fighting":
				f.world.Self.UnitFlags |= 0x80000
			}
			f.lookupCreatures = map[uint64]*worldv1.CreatureState{}
			if c != nil {
				f.lookupCreatures[request.TargetGuid] = c
			}
			switch mode {
			case "unordered realm cancel":
				_, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCancelCombat, TransportScope: clonePortalScope(f.portalCoreScope)})
				if err != nil {
					t.Fatal(err)
				}
			case "other own attack", "old victim stop", "other attack stopped":
				other := request.TargetGuid + 1
				deliverMeleeReceipt(t, s, opcode.SMSGAttackstart, meleeReceiptStart(s.selfGUID, other))
				if mode == "old victim stop" || mode == "other attack stopped" {
					deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, request.TargetGuid, 0))
				}
				if mode == "other attack stopped" {
					deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, other, 0))
				}
			case "other own swing":
				deliverMeleeReceipt(t, s, opcode.SMSGAttackerstateupdate, meleeDamageFixture(s.selfGUID, request.TargetGuid+1, 3, 0))
			case "unknown cleanup stop":
				f.failSend, f.failSendOpcode = true, opcode.CMSGAttackstop
				if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, request.VisitId), wrapperspb.UInt64(0)); err != nil {
					t.Fatal(err)
				}
				f.failSend = false
			}
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			want := mode == "exact dead target" || mode == "other attack stopped"
			if (countPackets(f, opcode.MSGMoveStartForward) > 0) != want || (s.encounterSwing.target == 0) != want || !s.state.Following || !proto.Equal(original, state(t, s).LastMeleeOperation) || countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGAttackstop) > 1 || mode == "unknown cleanup stop" && (!s.state.AutoAttackUnconfirmed || countPackets(f, opcode.CMSGAttackstop) != 1) {
				t.Fatalf("follow=%v moving=%v swing=%d attacks=%d stops=%d reason=%s receipt=%v", s.state.Following, s.state.Moving, s.encounterSwing.target, countPackets(f, opcode.CMSGAttackswing), countPackets(f, opcode.CMSGAttackstop), s.state.Reason, s.meleeOp.view.Phase)
			}
		})
	}
}

func TestMeleeCustodyKeepsTheOwnerPortalSighting(t *testing.T) {
	s, f, request, ctx, advance := leasedMeleeFixture(t)
	portal := dungeonEntrances[2]
	near := portal.center
	near.x -= 8
	putAtPortal(f.world.Owner, portal.source, portal.center)
	putAtPortal(f.world.Self, portal.source, near)
	f.world.Owner.Health = f.world.Owner.MaxHealth
	seedSyntheticPortalWorldScope(f, 9)
	newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	if _, err := s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal(err)
	}
	advance(250 * time.Millisecond)
	s.Tick(ctx)
	if s.followPortal.witness == 0 || s.followPortal.portal.trigger != portal.trigger || s.meleeOp.view.Phase != "waiting_server" ||
		countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGAreatrigger) != 0 {
		t.Fatal("outstanding START lost the passive sighting or authorized physical movement", s.stateLocked(), f.packets)
	}
}
