package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func leasedMeleeFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.MeleeRequest, context.Context, func(time.Duration)) {
	t.Helper()
	s, f, req, advance := exactMeleeFixture(t)
	activateEncounterWriter(t, s, f)
	ctx := modcb.WithPolicySpell(context.Background(), strings.Repeat("a", 64))
	ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, req.VisitId)
	md, _ := metadata.FromOutgoingContext(ctx)
	return s, f, req, metadata.NewIncomingContext(context.Background(), md), advance
}

func TestLeaseMeleeCustodyRejectsUnrelatedStopAndOrdinaryMovement(t *testing.T) {
	s, f, request, ctx, _ := leasedMeleeFixture(t)
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal(err)
	}
	acknowledgeExactMeleeStart(t, s, request.TargetGuid)
	if _, err := s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, snapshotFailure := range []bool{false, true} {
		f.failSnapshot = snapshotFailure
		if _, err := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "loot"}); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("ordinary interaction acquired live melee custody", err)
		}
		deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, request.TargetGuid+1, 0))
		s.Tick(ctx)
		if len(f.packets) != 1 || s.encounterSwing.target != request.TargetGuid || !s.state.AutoAttacking || s.state.AutoAttackUnconfirmed || s.state.Moving {
			t.Fatal("unrelated ack, interaction, or ordinary follow interfered with live swing", s.stateLocked(), f.packets)
		}
	}
}

func TestLeaseMeleeLossStopsOnceAndWaitsForExactAck(t *testing.T) {
	for _, name := range []string{"release acknowledged start", "expiry with ambiguous start", "expiry with explicit stop in flight"} {
		t.Run(name, func(t *testing.T) {
			s, f, request, ctx, advance := leasedMeleeFixture(t)
			ambiguous := name == "expiry with ambiguous start"
			f.failSend = ambiguous
			if _, err := s.Melee(ctx, request); err != nil {
				t.Fatal(err)
			}
			if !ambiguous {
				acknowledgeExactMeleeStart(t, s, request.TargetGuid)
			}
			if name == "expiry with explicit stop in flight" {
				request.Stop = true
				if _, err := s.Melee(ctx, request); err != nil {
					t.Fatal(err)
				}
			}
			learnSpells(s, autoAttack, smite)
			attacker := f.world.RequestedCreature
			attacker.AttackVictimGuid = s.selfGUID
			f.world.Self.UnitFlags = 0x80000
			f.world.NearbyCreatures = []*worldv1.CreatureState{attacker}
			if name == "release acknowledged start" {
				if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(0)); err != nil {
					t.Fatal(err)
				}
			} else {
				advance(encounterLeaseDuration)
			}
			s.Tick(context.Background())
			f.failSend = false
			deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, request.TargetGuid+1, 0))
			acknowledgeExactMeleeStart(t, s, request.TargetGuid)
			advance(meleeReceiptLimit)
			s.Tick(context.Background())
			if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(request.VisitId)); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("Lua regained the body with a stop unconfirmed", err)
			}
			if len(f.packets) != 2 || countPackets(f, opcode.CMSGAttackstop) != 1 || !s.state.AutoAttackUnconfirmed || s.encounterSwing.target == 0 {
				t.Fatal("retirement replayed a stop or released on the wrong evidence", s.stateLocked(), f.packets)
			}
			deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, request.TargetGuid, 0))
			s.Tick(context.Background())
			if s.encounterSwing.target != 0 || s.state.AutoAttackUnconfirmed || lastCast(f) != smite || countPackets(f, opcode.CMSGAttackstop) != 1 {
				t.Fatal("exact stop did not release the body to native fallback", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestLeaseMeleeWaitsForWorldEntryOrExactStopAcrossRetirement(t *testing.T) {
	s, f, request, ctx, advance := leasedMeleeFixture(t)
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal(err)
	}
	advance(encounterLeaseDuration)
	s.Tick(context.Background())
	deliverMeleeReceipt(t, s, opcode.SMSGTransferPending, make([]byte, 4))
	deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, request.TargetGuid, 0))
	deliverMeleeReceipt(t, s, opcode.SMSGNewWorld, make([]byte, 19))
	s.Tick(context.Background())
	if s.encounterSwing.target == 0 || !s.state.AutoAttackUnconfirmed || len(f.packets) != 2 {
		t.Fatal("an unbound stop or malformed entry cleared movement/attack custody", s.stateLocked(), f.packets)
	}
	deliverMeleeReceipt(t, s, opcode.SMSGNewWorld, make([]byte, 20))
	if s.encounterSwing.target != 0 || s.state.AutoAttackUnconfirmed || s.state.AutoAttackRequested || s.autoTarget != 0 || len(f.packets) != 2 {
		t.Fatal("verified new visit did not abandon old swing custody", s.stateLocked(), f.packets)
	}
}

func TestLeaseMeleeSafetyStopAndLogoutDoNotRepeatTransport(t *testing.T) {
	s, f, request, ctx, _ := leasedMeleeFixture(t)
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal(err)
	}
	deliverMeleeReceipt(t, s, opcode.SMSGAuraUpdate, sheepAura(request.TargetGuid, s.selfGUID, 118, 20000))
	s.Tick(context.Background())
	if _, err := s.BeforeLogout(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	if countPackets(f, opcode.CMSGAttackstop) != 1 || !s.state.AutoAttackUnconfirmed || s.encounterSwing.target != request.TargetGuid {
		t.Fatal("verified crowd control did not stop or logout repeated an unconfirmed stop", s.stateLocked(), f.packets)
	}
}
