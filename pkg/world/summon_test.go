package world

import (
	"encoding/binary"
	"testing"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestSummonTransportReservesOnlyLatestLiveOffer(t *testing.T) {
	for _, reason := range []string{"current", "superseded_same_caster", "other_caster", "expired", "canceled", "malformed_replacement", "new_visit", "unscoped", "wrong_choice"} {
		t.Run(reason, func(t *testing.T) {
			s := transportScopeState{scope: &modv1.TransportScope{ConnectionId: "one", SelfGuid: 7, VisitBound: true, VisitRevision: 2}}
			offer := binary.LittleEndian.AppendUint64(nil, 42)
			offer = binary.LittleEndian.AppendUint32(offer, 10)
			offer = binary.LittleEndian.AppendUint32(offer, 60_000)
			s.observe(opcode.SMSGSummonRequest, offer)
			scope := s.snapshot()
			scope.SummonOfferSequence = s.summon.sequence
			body := append(binary.LittleEndian.AppendUint64(nil, 42), 1)
			switch reason {
			case "superseded_same_caster":
				s.observe(opcode.SMSGSummonRequest, offer)
			case "other_caster":
				body[0] = 43
			case "expired":
				s.summon.expires = time.Now().Add(-time.Millisecond)
			case "canceled":
				s.observe(opcode.SMSGSummonCancel, nil)
			case "malformed_replacement":
				s.observe(opcode.SMSGSummonRequest, offer[:8])
			case "new_visit":
				s.observe(opcode.SMSGTransferPending, make([]byte, 4))
			case "unscoped":
				scope = nil
			case "wrong_choice":
				body[8] = 2
			}
			err := s.reserve(opcode.CMSGSummonResponse, body, scope)
			if reason == "current" {
				if err != nil || !modcb.PacketWasNotDispatched(s.reserve(opcode.CMSGSummonResponse, body, scope)) {
					t.Fatal("response did not reserve exact offer", err)
				}
			} else if !modcb.PacketWasNotDispatched(err) {
				t.Fatal("invalid response was admitted", err)
			}
			if s.snapshot().SummonOfferSequence != 0 {
				t.Fatal("offer-only token leaked into unrelated motion scope")
			}
		})
	}
}
