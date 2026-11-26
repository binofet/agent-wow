package modcb

import (
	"context"
	"errors"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
)

// Both callbacks are reads of the exact received order on the caller's own
// socket. Actions gets the acknowledged launch, World gets the written contact.
const KnockbackReportMethod = "knockback_movement_report"
const KnockbackPoseTrailer = "agentwow-observed-own-pose-bin"
const KnockbackContactTrailer = "agentwow-measured-knockback-contact-bin"

func ReadKnockbackReport(ctx context.Context, client FollowPolicyClient, order *modv1.WorldPacket) (*modv1.WorldPacket, error) {
	if client == nil || order == nil {
		return nil, errors.New("knockback source unavailable")
	}
	input, err := anypb.New(order)
	if err != nil {
		return nil, err
	}
	response, err := client.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "session", Method: KnockbackReportMethod, Request: input})
	if err != nil {
		return nil, err
	}
	out := &modv1.WorldPacket{}
	if response.GetResult() == nil || response.Result.UnmarshalTo(out) != nil || len(out.Payload) > 128 || out.TransportScope == nil {
		return nil, errors.New("knockback movement report unavailable")
	}
	return out, nil
}
