package buddyactions

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const meleeReceiptTarget = uint64(0xf13000022b000001)
const meleeReceiptVisit = 3

func meleeReceiptFixture(t *testing.T, enabled bool) (*Service, func(time.Duration), *meleeAttempt, string) {
	t.Helper()
	s, _, advance := rig(t)
	s.encounterVisit = encounterVisit{revision: meleeReceiptVisit, bound: true}
	token := strings.Repeat("a", 64)
	return s, advance, s.reserveMeleeOperationLocked(token, meleeReceiptTarget, enabled), token
}

func meleeReceiptStart(attacker, target uint64) []byte {
	return binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, attacker), target)
}

func meleeReceiptStop(attacker, target uint64, dead uint32) []byte {
	return binary.LittleEndian.AppendUint32(append(packedGUID(attacker), packedGUID(target)...), dead)
}

type meleeReceiptStream struct{ trailer metadata.MD }

func (*meleeReceiptStream) Method() string               { return "OnPacket" }
func (*meleeReceiptStream) SetHeader(metadata.MD) error  { return nil }
func (*meleeReceiptStream) SendHeader(metadata.MD) error { return nil }
func (stream *meleeReceiptStream) SetTrailer(md metadata.MD) error {
	stream.trailer = metadata.Join(stream.trailer, md)
	return nil
}

func deliverMeleeReceipt(t *testing.T, s *Service, code uint32, payload []byte) (string, *actionsv1.MeleeOperation) {
	t.Helper()
	stream := &meleeReceiptStream{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
	if _, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: code, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	token, body, ok := modcb.PolicyMeleeEffect(stream.trailer)
	if !ok {
		return "", nil
	}
	out := &actionsv1.MeleeOperation{}
	if err := proto.Unmarshal(body, out); err != nil {
		t.Fatal(err)
	}
	return token, out
}

func TestPolicyMeleeReceiptRequiresSameVisitDirectionAndGUIDPair(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		s, _, op, wantToken := meleeReceiptFixture(t, true)
		op.view.TransportWritten = true
		// An existing local bit is insufficient, even after a recorded write.
		s.state.AutoAttacking = true
		s.autoTarget = meleeReceiptTarget
		start := meleeReceiptStart(s.selfGUID, meleeReceiptTarget)
		for _, item := range []struct {
			code    uint32
			payload []byte
		}{
			{opcode.SMSGAttackstart, start[:15]},
			{opcode.SMSGAttackstart, append(append([]byte{}, start...), 0)},
			{opcode.SMSGAttackstart, meleeReceiptStart(7, meleeReceiptTarget)},
			{opcode.SMSGAttackstart, meleeReceiptStart(s.selfGUID, meleeReceiptTarget+1)},
			{opcode.SMSGAttackstart, meleeReceiptStart(s.selfGUID, 0)},
			{opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, meleeReceiptTarget, 0)},
		} {
			if _, got := deliverMeleeReceipt(t, s, item.code, item.payload); got != nil ||
				s.pendingMeleeOperationLocked() != op || s.autoTarget != meleeReceiptTarget || !s.state.AutoAttacking {
				t.Fatalf("unrelated START settled receipt: %v, native %v", got, &op.view)
			}
		}

		token, got := deliverMeleeReceipt(t, s, opcode.SMSGAttackstart, start)
		want := &actionsv1.MeleeOperation{
			SelfGuid: s.selfGUID, TargetGuid: meleeReceiptTarget, Enabled: true,
			Phase: "confirmed_attack_start", TransportWritten: true, VisitId: meleeReceiptVisit,
		}
		if token != wantToken || !proto.Equal(got, want) || s.pendingMeleeOperationLocked() != nil ||
			s.meleeAcknowledgedVisit != meleeReceiptVisit {
			t.Fatalf("START receipt = %v, expected %v", got, want)
		}
		st := state(t, s).GetLastMeleeOperation()
		if !proto.Equal(st, want) {
			t.Fatalf("snapshot = %v, expected %v", st, want)
		}
		st.TargetGuid = 0
		if state(t, s).GetLastMeleeOperation().GetTargetGuid() != meleeReceiptTarget {
			t.Fatal("snapshot aliases retained melee receipt")
		}
		if _, duplicate := deliverMeleeReceipt(t, s, opcode.SMSGAttackstart, start); duplicate != nil {
			t.Fatalf("duplicate START reported another receipt: %v", duplicate)
		}
	})

	t.Run("stop despite ambiguous transport", func(t *testing.T) {
		s, _, op, wantToken := meleeReceiptFixture(t, false)
		op.view.Phase = "transport_unconfirmed_no_retry"
		s.autoTarget, s.autoStopping = meleeReceiptTarget, true
		s.state.AutoAttacking, s.state.AutoAttackUnconfirmed = true, true
		s.meleeAcknowledgedVisit = meleeReceiptVisit
		stop := meleeReceiptStop(s.selfGUID, meleeReceiptTarget, 1)
		for _, item := range []struct {
			code    uint32
			payload []byte
		}{
			{opcode.SMSGAttackstop, stop[:len(stop)-1]},
			{opcode.SMSGAttackstop, append(append([]byte{}, stop...), 0)},
			{opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, meleeReceiptTarget, 2)},
			{opcode.SMSGAttackstop, meleeReceiptStop(7, meleeReceiptTarget, 0)},
			{opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, meleeReceiptTarget+1, 0)},
			{opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, 0, 0)},
			{opcode.SMSGAttackstart, meleeReceiptStart(s.selfGUID, meleeReceiptTarget)},
		} {
			if _, got := deliverMeleeReceipt(t, s, item.code, item.payload); got != nil ||
				s.pendingMeleeOperationLocked() != op || s.autoTarget != meleeReceiptTarget ||
				!s.autoStopping || !s.state.AutoAttackUnconfirmed || s.meleeAcknowledgedVisit != meleeReceiptVisit {
				t.Fatalf("unrelated STOP settled receipt: %v, native %v", got, &op.view)
			}
		}
		token, got := deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, stop)
		want := &actionsv1.MeleeOperation{
			SelfGuid: s.selfGUID, TargetGuid: meleeReceiptTarget, Enabled: false,
			Phase: "confirmed_attack_stop", VisitId: meleeReceiptVisit,
		}
		if token != wantToken || !proto.Equal(got, want) || s.pendingMeleeOperationLocked() != nil ||
			s.autoTarget != 0 || s.autoStopping || s.state.AutoAttacking || s.meleeAcknowledgedVisit != 0 {
			t.Fatalf("STOP receipt = %v, expected %v", got, want)
		}
		if _, duplicate := deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, stop); duplicate != nil {
			t.Fatalf("duplicate STOP reported another receipt: %v", duplicate)
		}
	})
}

func TestLegacyMeleeObservationKeepsLocalSemanticsAndRecordsVisit(t *testing.T) {
	s, _, _ := rig(t)
	s.encounterVisit = encounterVisit{revision: meleeReceiptVisit, bound: true}
	start := meleeReceiptStart(s.selfGUID, meleeReceiptTarget)
	if _, got := deliverMeleeReceipt(t, s, opcode.SMSGAttackstart, start); got != nil ||
		!s.state.AutoAttacking || s.meleeAcknowledgedVisit != meleeReceiptVisit {
		t.Fatalf("legacy START state or visit changed: %v", got)
	}
	if _, got := deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, 0, 0)); got != nil ||
		s.state.AutoAttacking || s.meleeAcknowledgedVisit != 0 {
		t.Fatalf("legacy zero-target STOP state changed: %v", got)
	}
	deliverMeleeReceipt(t, s, opcode.SMSGAttackstart, start)
	deliverMeleeReceipt(t, s, opcode.SMSGTransferPending, make([]byte, 4))
	deliverMeleeReceipt(t, s, opcode.SMSGNewWorld, make([]byte, 20))
	if s.meleeAcknowledgedVisit != 0 || !s.state.AutoAttacking {
		t.Fatal("legacy state changed or retained exact stop authority across visits")
	}
}

func TestLegacySafetyStopRetiresPendingPolicyStart(t *testing.T) {
	s, _, op, wantToken := meleeReceiptFixture(t, true)
	op.view.TransportWritten = true
	s.autoTarget = meleeReceiptTarget
	s.state.AutoAttackRequested = true
	if s.stopAutoAttackLocked(context.Background()) || countPackets(s.session.(*fakeSession), opcode.CMSGAttackstop) != 1 {
		t.Fatal("safety stop did not make its one ordinary stop write")
	}
	token, got := deliverMeleeReceipt(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, meleeReceiptTarget, 0))
	if token != wantToken || got.GetPhase() != "outcome_unconfirmed_no_retry" ||
		s.autoTarget != 0 || s.autoStopping || s.state.AutoAttackUnconfirmed {
		t.Fatalf("safety stop receipt or local reconciliation = %v", got)
	}
}

func TestPolicyMeleeTransferAwaitsValidNewEntryBeforeClearingOldLocals(t *testing.T) {
	s, _, _, wantToken := meleeReceiptFixture(t, false)
	s.autoTarget, s.autoStopping = meleeReceiptTarget, true
	s.state.AutoAttacking, s.state.AutoAttackUnconfirmed = true, true
	s.state.AutoAttackTargetGuid = meleeReceiptTarget

	token, got := deliverMeleeReceipt(t, s, opcode.SMSGTransferPending, make([]byte, 4))
	if token != wantToken || got.GetPhase() != "outcome_unconfirmed_no_retry" ||
		s.autoTarget != meleeReceiptTarget || !s.autoStopping || !s.state.AutoAttackUnconfirmed {
		t.Fatalf("transfer-pending disposition = %v", got)
	}
	deliverMeleeReceipt(t, s, opcode.SMSGNewWorld, make([]byte, 19))
	if s.autoTarget != meleeReceiptTarget || !s.autoStopping {
		t.Fatal("truncated entry cleared attack custody")
	}
	deliverMeleeReceipt(t, s, opcode.SMSGNewWorld, make([]byte, 20))
	if s.encounterVisit.revision != meleeReceiptVisit+1 || s.autoTarget != 0 || s.autoStopping ||
		s.state.AutoAttacking || s.state.AutoAttackTargetGuid != 0 || s.state.AutoAttackUnconfirmed || s.state.AutoAttackRequested {
		t.Fatal("previous typed attack still blocks the new visit")
	}
	if got := state(t, s).GetLastMeleeOperation(); got.GetVisitId() != meleeReceiptVisit ||
		got.GetPhase() != "outcome_unconfirmed_no_retry" {
		t.Fatalf("new entry changed historical receipt: %v", got)
	}
}

func TestPolicyMeleeReceiptRetiresUnverifiedAtDeadlineOrWorldBoundary(t *testing.T) {
	for _, name := range []string{"deadline packet", "deadline tick", "transfer", "fresh world", "visit changed", "logout"} {
		t.Run(name, func(t *testing.T) {
			s, advance, op, wantToken := meleeReceiptFixture(t, true)
			op.view.TransportWritten = true
			code, body := uint32(opcode.SMSGAttackstart), meleeReceiptStart(s.selfGUID, meleeReceiptTarget)
			switch name {
			case "deadline packet":
				advance(meleeReceiptLimit)
			case "deadline tick":
				advance(meleeReceiptLimit)
				s.Tick(context.Background())
				if got := state(t, s).GetLastMeleeOperation().GetPhase(); got != "outcome_unconfirmed_no_retry" {
					t.Fatalf("deadline state = %q", got)
				}
			case "transfer":
				if _, got := deliverMeleeReceipt(t, s, opcode.SMSGTransferPending, []byte{1}); got != nil || s.pendingMeleeOperationLocked() != op {
					t.Fatalf("malformed transfer settled receipt: %v", got)
				}
				code, body = opcode.SMSGTransferPending, []byte{1, 0, 0, 0}
			case "fresh world":
				code, body = opcode.SMSGNewWorld, make([]byte, 20)
			case "visit changed":
				s.encounterVisit.revision++
			case "logout":
				if _, err := s.BeforeLogout(context.Background(), &emptypb.Empty{}); err != nil {
					t.Fatal(err)
				}
			}
			token, got := deliverMeleeReceipt(t, s, code, body)
			if token != wantToken || got.GetPhase() != "outcome_unconfirmed_no_retry" ||
				got.GetVisitId() != meleeReceiptVisit || s.pendingMeleeOperationLocked() != nil {
				t.Fatalf("retirement receipt = %v, token=%q", got, token)
			}
			if _, late := deliverMeleeReceipt(t, s, opcode.SMSGAttackstart, meleeReceiptStart(s.selfGUID, meleeReceiptTarget)); late != nil {
				t.Fatalf("late matching START reported a second outcome: %v", late)
			}
		})
	}
}

func TestPolicyMeleeAndSpellRetirementUseSeparateTrailers(t *testing.T) {
	s, _, _, wantMelee := meleeReceiptFixture(t, true)
	wantSpell := strings.Repeat("b", 64)
	s.supportOp = &supportAttempt{
		view:             actionsv1.SpellOperation{SpellId: 6770, TargetGuid: meleeReceiptTarget, Phase: "outcome_unconfirmed_no_retry"},
		policySpellToken: wantSpell,
	}
	stream := &meleeReceiptStream{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
	_, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGTransferPending, Payload: make([]byte, 4)})
	if err != nil {
		t.Fatal(err)
	}
	meleeToken, _, meleeOK := modcb.PolicyMeleeEffect(stream.trailer)
	spellToken, _, spellOK := modcb.PolicySpellEffect(stream.trailer)
	if !meleeOK || meleeToken != wantMelee || !spellOK || spellToken != wantSpell {
		t.Fatalf("retirement callback lost separate receipts: %v", stream.trailer)
	}
}
