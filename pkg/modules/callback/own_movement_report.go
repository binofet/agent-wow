package modcb

import (
	"context"
	"errors"
	"strconv"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const OwnMovementReportMethod = "own_movement_report"

// ReadOwnMovementReport reads the module's own session socket, not a public
// JSON method or a gameplay mutation. The bytes and their original transport
// correlation are inseparable; this must never freshen an old World image.
func ReadOwnMovementReport(ctx context.Context, client FollowPolicyClient, sid string, self uint64) (*modv1.WorldPacket, error) {
	if sid == "" || len(sid) > 128 || self == 0 {
		return nil, errors.New("current movement-report body required")
	}
	value, err := structpb.NewStruct(map[string]any{"sessionId": sid, "selfGuid": strconv.FormatUint(self, 10)})
	if err != nil {
		return nil, err
	}
	packed, err := anypb.New(value)
	if err != nil {
		return nil, err
	}
	response, err := client.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "session", Method: OwnMovementReportMethod, Request: packed})
	if err != nil {
		return nil, err
	}
	packet := &modv1.WorldPacket{}
	if response.GetResult() == nil || response.Result.UnmarshalTo(packet) != nil || len(packet.Payload) == 0 || len(packet.Payload) > 256 ||
		packet.TransportScope == nil || packet.TransportScope.SelfGuid != self || packet.TransportScope.ConnectionId == "" ||
		!packet.TransportScope.VisitBound || packet.TransportScope.VisitRevision == 0 {
		return nil, errors.New("current own movement report unavailable")
	}
	return packet, nil
}
