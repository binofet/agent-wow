package modcb

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const packetNotDispatchedReason = "POLICY_PACKET_NOT_DISPATCHED"

// PacketNotDispatchedError is used only before the core's game transport is
// called. A plain RPC status, including PermissionDenied, does not prove that.
func PacketNotDispatchedError(message string) error {
	s, err := status.New(codes.PermissionDenied, message).WithDetails(&errdetails.ErrorInfo{Domain: "agentwow.policy", Reason: packetNotDispatchedReason})
	if err != nil {
		return status.Error(codes.PermissionDenied, message)
	}
	return s.Err()
}

func PacketWasNotDispatched(err error) bool {
	if err == nil {
		return false
	}
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == "agentwow.policy" && info.Reason == packetNotDispatchedReason {
			return true
		}
	}
	return false
}
