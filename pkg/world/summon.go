package world

import (
	"encoding/binary"
	"math"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// The realm uses its current stored summon point when it handles the response.
// Reserve the precise offer at the socket, even if Actions' incoming queue lags.
type pendingSummon struct {
	sequence uint64
	caster   uint64
	expires  time.Time
}

func (t *transportScopeState) observeSummon(op uint16, payload []byte) {
	t.summon.caster = 0
	if op != opcode.SMSGSummonRequest || len(payload) != 16 || t.scope == nil || !t.scope.VisitBound || t.summon.sequence == math.MaxUint64 {
		return
	}
	caster := binary.LittleEndian.Uint64(payload)
	timeout := binary.LittleEndian.Uint32(payload[12:])
	if caster == 0 || caster>>48 != 0 || caster == t.scope.SelfGuid || timeout == 0 {
		return
	}
	t.summon = pendingSummon{sequence: t.summon.sequence + 1, caster: caster, expires: time.Now().Add(time.Duration(timeout) * time.Millisecond)}
}

func (t *transportScopeState) reserveSummonResponse(payload []byte, scope *modv1.TransportScope) error {
	offer := &t.summon
	if !sameTransferVisit(t.scope, scope) || t.exhausted || offer.caster == 0 || scope.SummonOfferSequence != offer.sequence ||
		!time.Now().Before(offer.expires) || len(payload) != 9 || payload[8] > 1 || binary.LittleEndian.Uint64(payload) != offer.caster {
		return modcb.PacketNotDispatchedError("current unexpired summon offer and exact response required")
	}
	offer.caster = 0
	return nil
}
