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

func TestLeaseMeleeResumesAfterOwnControlLossStopsTheSwing(t *testing.T) {
	s, f, request, ctx, _ := leasedMeleeFixture(t)
	if _, err := s.Melee(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, receipt := deliverMeleeReceipt(t, s, opcode.SMSGAttackstart, meleeReceiptStart(s.selfGUID, request.TargetGuid)); receipt.GetPhase() != "confirmed_attack_start" {
		t.Fatal("missing original START receipt", receipt)
	}
	if _, receipt := deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, 0, 0)); receipt != nil {
		t.Fatal("unsolicited stop produced an operation receipt", receipt)
	}
	sendClientControl(s, s.selfGUID, 0)
	if s.state.AutoAttacking || s.encounterSwing.target != 0 || s.meleeOp.view.Phase != "confirmed_attack_start" {
		t.Fatal("server control left the acknowledged swing active or changed its receipt", s.stateLocked())
	}
	if _, err := s.Melee(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("melee admitted during loss of control", err)
	}
	sendClientControl(s, s.selfGUID, 1)
	f.world.Self.UnitFlags |= unitFlagsForcedMovement
	s.Tick(ctx)
	if _, err := s.Melee(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("melee admitted before the body recovered", err)
	}
	f.world.Self.UnitFlags &^= unitFlagsForcedMovement
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGAttackstop) != 0 {
		t.Fatal("the native controller issued an unsolicited attack", f.packets)
	}
	if _, err := s.Melee(ctx, request); err != nil || countPackets(f, opcode.CMSGAttackswing) != 2 {
		t.Fatal("new exact policy attack after recovery", err, f.packets)
	}
	if _, err := s.Melee(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("replayed the newly written attack before its own acknowledgement", err)
	}
	acknowledgeExactMeleeStart(t, s, request.TargetGuid)
	if !s.state.AutoAttacking || s.state.AutoAttackTargetGuid != request.TargetGuid {
		t.Fatal("new exact START was not confirmed", s.stateLocked())
	}
}

func TestLeaseMeleeControlStopRequiresCurrentOwnEvidence(t *testing.T) {
	for _, name := range []string{
		"stop alone", "control alone", "already under control", "foreign stop", "foreign control",
		"noncanonical victim", "noncanonical attacker", "noncanonical mover", "expired stop",
		"new start", "different target start", "new swing", "control granted", "transfer",
	} {
		t.Run(name, func(t *testing.T) {
			s, f, request, ctx, advance := leasedMeleeFixture(t)
			if _, err := s.Melee(ctx, request); err != nil {
				t.Fatal(err)
			}
			acknowledgeExactMeleeStart(t, s, request.TargetGuid)
			attacker, mover := s.selfGUID, s.selfGUID
			if name == "foreign stop" {
				attacker++
			}
			if name == "foreign control" {
				mover++
			}
			if name == "already under control" {
				sendClientControl(s, mover, 0)
			}
			if name != "control alone" {
				stop := meleeReceiptStop(attacker, 0, 0)
				switch name {
				case "noncanonical victim":
					stop = append(packedGUID(attacker), 1, 0, 0, 0, 0, 0)
				case "noncanonical attacker":
					stop = []byte{3, byte(attacker), 0, 0, 0, 0, 0, 0}
				}
				deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, stop)
			}
			switch name {
			case "expired stop":
				advance(meleeReceiptLimit)
			case "new start":
				acknowledgeExactMeleeStart(t, s, request.TargetGuid)
			case "different target start":
				acknowledgeExactMeleeStart(t, s, request.TargetGuid+1)
			case "new swing":
				deliverMeleeReceipt(t, s, opcode.SMSGAttackerstateupdate, meleeDamageFixture(s.selfGUID, request.TargetGuid, 5, 0))
			case "control granted":
				sendClientControl(s, s.selfGUID, 1)
			case "transfer":
				deliverMeleeReceipt(t, s, opcode.SMSGTransferPending, make([]byte, 4))
			}
			if name == "noncanonical mover" {
				deliverMeleeReceipt(t, s, opcode.SMSGClientControlUpdate, []byte{3, byte(mover), 0, 0})
			} else if name != "stop alone" {
				sendClientControl(s, mover, 0)
			}
			if !s.state.AutoAttacking || s.encounterSwing.target != request.TargetGuid || countPackets(f, opcode.CMSGAttackswing) != 1 {
				t.Fatal("unrelated or stale events retired the original swing", s.stateLocked())
			}
		})
	}
}

func TestLeaseMeleeControlStopNeverSettlesOutstandingWrites(t *testing.T) {
	for _, name := range []string{"pending start", "unknown start", "pending stop", "unknown stop"} {
		t.Run(name, func(t *testing.T) {
			s, f, request, ctx, _ := leasedMeleeFixture(t)
			f.failSend = name == "unknown start"
			if _, err := s.Melee(ctx, request); err != nil {
				t.Fatal(err)
			}
			if name == "pending stop" || name == "unknown stop" {
				acknowledgeExactMeleeStart(t, s, request.TargetGuid)
				request.Stop = true
				f.failSend = name == "unknown stop"
				if _, err := s.Melee(ctx, request); err != nil {
					t.Fatal(err)
				}
			}
			phase := s.meleeOp.view.Phase
			deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, 0, 0))
			sendClientControl(s, s.selfGUID, 0)
			if s.meleeOp.view.Phase != phase || s.encounterSwing.target != request.TargetGuid {
				t.Fatal("control settled a directed operation without its exact receipt", s.stateLocked())
			}
		})
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
			if s.encounterSwing.target != 0 || s.state.AutoAttackUnconfirmed || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackstop) != 1 {
				t.Fatal("exact stop did not settle custody or restarted Go target selection", s.stateLocked(), f.packets)
			}
			if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(request.VisitId)); err != nil {
				t.Fatal("same Lua writer could not reacquire the settled body", err)
			}
			request.Stop = false
			if _, err := s.Melee(ctx, request); err != nil || countPackets(f, opcode.CMSGAttackswing) != 2 {
				t.Fatal("settled body lost exact Lua-selected attack capability", err, s.stateLocked())
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
