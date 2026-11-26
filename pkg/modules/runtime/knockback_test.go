package modrt

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestKnockbackReportIsBoundToNativeCallerAndActualOrder(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	scope := &modv1.TransportScope{ConnectionId: "actual", SelfGuid: 4, VisitBound: true, VisitRevision: 8, MotionRevision: 22}
	body := make([]byte, 22)
	body[0], body[1] = 1, 4
	binary.LittleEndian.PutUint32(body[6:], math.Float32bits(1))
	received := &modv1.WorldPacket{Opcode: opcode.SMSGMoveKnockBack, Payload: body, TransportScope: scope}
	m.session.KnockbackReport = func(p *modv1.WorldPacket, land bool) *modv1.WorldPacket {
		if !proto.Equal(p, received) {
			return nil
		}
		op := uint32(opcode.CMSGMoveKnockBackAck)
		if land {
			op = opcode.MSGMoveFallLand
		}
		return &modv1.WorldPacket{Opcode: op, TransportScope: scope, Payload: []byte{1}}
	}
	for _, mode := range []string{"actions", "world", "trade", "wrong_guid", "stale", "unbound", "truncated", "closing"} {
		p := proto.Clone(received).(*modv1.WorldPacket)
		caller := actionsModule
		switch mode {
		case "world":
			caller = "buddy-world"
		case "trade":
			caller = "buddy-trade"
		case "wrong_guid":
			p.Payload[1] = 5
		case "stale":
			p.TransportScope.MotionRevision--
		case "unbound":
			p.TransportScope.VisitBound = false
		case "truncated":
			p.Payload = p.Payload[:21]
		case "closing":
			m.closing = true
		}
		request, _ := anypb.New(p)
		out, err := m.invokeModule(context.Background(), caller, &modv1.InvokeModuleRequest{Module: "session", Method: modcb.KnockbackReportMethod, Request: request})
		if mode == "actions" || mode == "world" {
			result := &modv1.WorldPacket{}
			if err != nil || out.Result.UnmarshalTo(result) != nil || (result.Opcode == opcode.MSGMoveFallLand) != (mode == "world") {
				t.Fatal(mode, err, result)
			}
		} else if err == nil || mode == "trade" && status.Code(err) != codes.PermissionDenied {
			t.Fatal(mode, err)
		}
	}
}
