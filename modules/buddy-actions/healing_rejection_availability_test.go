package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestExactHealLOSRejectionAllowsFreshSelfHealAfterActualGCD(t *testing.T) {
	s, f, advance := startHealReceipt(t, false)
	s.UseHostStandingHealing()
	failedCount := s.castCount
	gcd := time.Duration(s.learnedContractsLocked()[healReceiptSpell].GcdMs) * time.Millisecond
	if gcd <= 0 || gcd >= 3*time.Second || s.nextHeal != s.now().Add(gcd) {
		t.Fatal("heal dispatch did not retain its learned spell's real GCD", gcd, s.nextHeal)
	}
	deliverHealReceiptPacket(t, s, opcode.SMSGCastFailed,
		healingRejectionPacket(opcode.SMSGCastFailed, s.selfGUID, failedCount, healReceiptSpell).Payload)
	if s.state.CastPending || s.supportOp.view.Phase != "server_rejected_no_replay" || s.nextHeal != s.now().Add(gcd) {
		t.Fatal("definite rejected cast was not released or extended a global retry policy", s.stateLocked())
	}
	f.world.Self.Health = 20
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("agentwow-policy-spell", strings.Repeat("a", 64)))
	request := &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: s.selfGUID, SpellId: healReceiptSpell}
	advance(gcd - time.Millisecond)
	if _, err := s.CastHeal(ctx, request); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("self heal bypassed the actual GCD", err, f.packets)
	}
	advance(time.Millisecond)
	options, err := s.HealingOptions(ctx, &emptypb.Empty{})
	if err != nil || !options.Caster.Ready || len(options.Spells) == 0 {
		t.Fatal("Joel's rejected heal hid all ready self-healing options beyond the GCD", options, err)
	}
	selfObserved := false
	for _, patient := range options.Patients {
		selfObserved = selfObserved || patient.Guid == s.selfGUID && patient.Health == 20
	}
	if !selfObserved {
		t.Fatal("injured self omitted from fresh healing observations", options)
	}
	result, err := s.CastHeal(ctx, request)
	if err != nil || !result.CastPending || result.HealAttempts != 2 || result.LastCastTargetGuid != s.selfGUID || s.castCount != failedCount+1 || countPackets(f, opcode.CMSGCastSpell) != 2 {
		t.Fatal("fresh exact self heal waited for the old target's five-second retry policy", result, err, f.packets)
	}
	if f.world.Self.Health != 20 || result.LastSpellOperation.EffectObserved {
		t.Fatal("new self cast invented a healing effect", result)
	}
}

func TestExactHealUnknownTransportKeepsCastWindowDespiteElapsedGCD(t *testing.T) {
	s, f, advance := startHealReceipt(t, true)
	s.UseHostStandingHealing()
	f.world.Self.Health = 20
	advance(2 * time.Second)
	options, err := s.HealingOptions(context.Background(), &emptypb.Empty{})
	if err != nil || options.Caster.Ready || !s.state.CastPending || s.supportOp.view.Phase != "transport_unconfirmed_no_retry" || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("an ambiguous first write was replayed or cleared by the shorter GCD", options, err, s.stateLocked())
	}
}
