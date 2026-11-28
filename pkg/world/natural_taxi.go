package world

import (
	"context"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

type naturalTaxiMenu struct {
	requested *modv1.TransportScope
	npc       uint64
	writtenAt time.Time
}

func (e *engine) reserveNaturalTaxiMenu(ctx context.Context, op uint32, requested *modv1.TransportScope) error {
	current := cloneTransportScope(requested)
	current.TaxiArrivalQueryRevision = 0
	t := &e.transport
	if op != opcode.CMSGTaxiqueryavailablenodes || !proto.Equal(t.scope, current) || t.exhausted ||
		!current.VisitBound || !current.SplineActive || !current.SplineIdKnown || current.SplineRevision == 0 ||
		current.SplineSourceOpcode != opcode.SMSGMonsterMove || current.MotionRevision <= current.SplineSourceRevision ||
		!t.currentHeaderKnown || sameTransferSource(t.uncertainTaxiMotion, current) || t.attemptedVisit == current.VisitRevision && t.attemptedID == current.SplineRevision ||
		e.impulse != nil && !e.impulse.finished || !modsession.FlightpathWorldCurrent(ctx) {
		return modcb.PacketNotDispatchedError("natural taxi landing source or flightmaster changed before menu query")
	}
	return nil
}

func (m *naturalTaxiMenu) markReply(op uint16, body []byte, scope *modv1.TransportScope, received time.Time) {
	if op != opcode.SMSGShowtaxinodes || m.requested == nil || !received.After(m.writtenAt) ||
		!sameTransferSource(m.requested, scope) || scope.MotionRevision < m.requested.MotionRevision {
		return
	}
	npc, _, err := modcb.FlightpathMenuHeader(body)
	if err != nil || npc != m.npc {
		return
	}
	scope.TaxiArrivalQueryRevision = m.requested.TaxiArrivalQueryRevision
	*m = naturalTaxiMenu{}
}
