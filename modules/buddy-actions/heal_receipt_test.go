package buddyactions

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/metadata"
)

const healReceiptSpell = uint32(2050)

func startHealReceipt(t *testing.T, ambiguous bool) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	learn(s)
	f.failSend = ambiguous
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("agentwow-policy-spell", strings.Repeat("a", 64)))
	result, err := s.CastHeal(ctx, &actionsv1.CastHealRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: healReceiptSpell,
	})
	op := result.GetLastSpellOperation()
	if err != nil || !result.GetCastPending() || result.GetHealAttempts() != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 ||
		op.GetCapability() != "heal" || op.GetSpellId() != healReceiptSpell || op.GetTargetGuid() != ownerGUID ||
		op.GetTransportWritten() == ambiguous || op.GetServerCastGo() || op.GetEffectObserved() {
		t.Fatalf("exact heal reservation = %v, %v with %d cast writes", result, err, countPackets(f, opcode.CMSGCastSpell))
	}
	return s, f, advance
}

func deliverHealReceiptPacket(t *testing.T, s *Service, code uint32, payload []byte) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: code, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func healReceiptCast(goEvent bool, count byte, target uint64) []byte {
	p := smiteEvent(goEvent, count, target)
	_, _, _, off, _ := castEvent(p)
	binary.LittleEndian.PutUint32(p[off-4:], healReceiptSpell)
	return p
}

func healReceiptLog(target, caster uint64, spell, heal, overheal, absorb uint32) []byte {
	p := append(packedGUID(target), packedGUID(caster)...)
	for _, field := range []uint32{spell, heal, overheal, absorb} {
		p = binary.LittleEndian.AppendUint32(p, field)
	}
	return append(p, 1, 0)
}

func TestExactHealReceiptRequiresCountBoundGoAndOwnEffectiveLog(t *testing.T) {
	s, f, _ := startHealReceipt(t, false)
	log := healReceiptLog(ownerGUID, s.selfGUID, healReceiptSpell, 40, 20, 30)
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellheallog, log)
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, healReceiptCast(true, s.castCount+1, ownerGUID))
	foreignSource := healReceiptCast(true, s.castCount, ownerGUID)
	foreignSource[1] = 9
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, foreignSource)
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, healReceiptCast(true, s.castCount, 9))
	before := state(t, s)
	if !before.CastPending || before.ServerCastGo != 0 || before.GetLastSpellOperation().GetEffectObserved() {
		t.Fatal("bad cast or standalone log settled heal", before)
	}
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellStart, healReceiptCast(false, s.castCount, ownerGUID))
	for range 2 {
		deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, healReceiptCast(true, s.castCount, ownerGUID))
	}
	for _, p := range [][]byte{
		healReceiptLog(ownerGUID, 9, healReceiptSpell, 40, 0, 0),
		healReceiptLog(9, s.selfGUID, healReceiptSpell, 40, 0, 0),
		healReceiptLog(ownerGUID, s.selfGUID, healReceiptSpell+1, 40, 0, 0),
		healReceiptLog(ownerGUID, s.selfGUID, healReceiptSpell, 39, 40, 0),
		log[:len(log)-1],
	} {
		deliverHealReceiptPacket(t, s, opcode.SMSGSpellheallog, p)
	}
	before = state(t, s)
	if !before.CastPending || before.ServerCastStarts != 1 || before.ServerCastGo != 1 ||
		!before.GetLastSpellOperation().GetServerCastGo() || before.GetLastSpellOperation().GetEffectObserved() {
		t.Fatal("GO or unrelated/malformed log settled heal", before)
	}
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellheallog, log)
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellheallog, log)
	after := state(t, s)
	op := after.GetLastSpellOperation()
	if after.CastPending || !op.GetEffectObserved() || op.GetPhase() != "confirmed_heal" ||
		after.ServerCastGo != 1 || f.world.Owner.Health != 50 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("effective heal did not settle exactly once", after)
	}
}

func TestExactHealReceiptDoesNotConfirmZeroEffectLateOrWrongResult(t *testing.T) {
	for _, name := range []string{"zero effective", "late", "transfer", "no target hit", "rejected", "post-GO rejection"} {
		t.Run(name, func(t *testing.T) {
			s, f, advance := startHealReceipt(t, false)
			log := healReceiptLog(ownerGUID, s.selfGUID, healReceiptSpell, 40, 20, 0)
			want := "server_cast_go_effect_unverified"
			if name != "rejected" && name != "no target hit" {
				deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, healReceiptCast(true, s.castCount, ownerGUID))
			}
			switch name {
			case "zero effective":
				deliverHealReceiptPacket(t, s, opcode.SMSGSpellheallog, healReceiptLog(ownerGUID, s.selfGUID, healReceiptSpell, 40, 40, 0))
			case "late":
				advance(5 * time.Second)
				want = "outcome_unconfirmed_no_retry"
			case "transfer":
				deliverHealReceiptPacket(t, s, opcode.SMSGTransferPending, make([]byte, 4))
				want = "world_transfer_outcome_unconfirmed"
			case "no target hit":
				start := healReceiptCast(false, s.castCount, ownerGUID)
				_, _, _, offset, _ := castEvent(start)
				miss := append(append([]byte{}, start[:offset+8]...), 0, 1)
				miss = binary.LittleEndian.AppendUint64(miss, ownerGUID)
				miss = append(miss, 1)
				miss = append(miss, start[offset+8:]...)
				deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, miss)
			case "rejected", "post-GO rejection":
				fail := healingRejectionPacket(opcode.SMSGCastFailed, s.selfGUID, s.castCount, healReceiptSpell)
				deliverHealReceiptPacket(t, s, fail.Opcode, fail.Payload)
				if name == "rejected" {
					want = "server_rejected_no_replay"
				}
			}
			deliverHealReceiptPacket(t, s, opcode.SMSGSpellheallog, log)
			st := state(t, s)
			op := st.GetLastSpellOperation()
			if st.CastPending || op.GetEffectObserved() || op.GetPhase() != want || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatalf("unconfirmed/rejected cast marked as effect or replayed: %v", st)
			}
		})
	}
}

func TestExactHealReceiptCanObserveEffectAfterAmbiguousWrite(t *testing.T) {
	s, f, _ := startHealReceipt(t, true)
	if op := state(t, s).GetLastSpellOperation(); op.GetPhase() != "transport_unconfirmed_no_retry" {
		t.Fatal(op)
	}
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, healReceiptCast(true, s.castCount, ownerGUID))
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellheallog, healReceiptLog(ownerGUID, s.selfGUID, healReceiptSpell, 10, 0, 0))
	st := state(t, s)
	op := st.GetLastSpellOperation()
	if st.CastPending || op.GetTransportWritten() || !op.GetServerCastGo() || !op.GetEffectObserved() ||
		op.GetPhase() != "confirmed_heal" || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("later native evidence did not settle the effect separately from transport", st)
	}
}
