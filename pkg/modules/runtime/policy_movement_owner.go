package modrt

import (
	"context"
	"encoding/json"
	"math"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The policy chooses owner, lifetime and release conditions. This adapter only
// binds one typed, reflected custody operation to the current physical body.
func (p *SessionPolicy) resolveMovementOwner(action map[string]any) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	owner, _ := action["owner"].(string)
	token, _ := action["token"].(string)
	mode, _ := action["mode"].(string)
	if sid != p.vm.Status().SessionID || self != p.body || owner == "" {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current session body and movement owner required")
	}
	method := "buddy-actions.acquire_movement"
	params := map[string]any{"sessionId": sid, "selfGuid": self, "owner": owner, "token": token}
	switch mode {
	case "acquire", "renew":
		ms, _ := action["leaseMs"].(float64)
		if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 1 || ms > math.MaxUint32 || ms != math.Trunc(ms) || mode == "renew" && token == "" {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "positive finite integer leaseMs and exact renewal token required")
		}
		params["leaseMs"] = uint32(ms)
	case "release":
		if token == "" {
			return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact movement owner token required")
		}
		method = "buddy-actions.release_movement"
	default:
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "movement owner mode must be acquire renew or release")
	}
	if _, _, err := p.manager.lookup(actionsModule, method[len(actionsModule)+1:], true); err != nil {
		return PolicyDispatch{}, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return PolicyDispatch{}, err
	}
	return PolicyDispatch{Module: actionsModule, Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		payload := map[string]any{"capability": "movement_owner", "mode": mode, "owner": owner}
		response, callErr := p.manager.invokeJSON(ctx, method, raw, true)
		if callErr != nil {
			payload["error"] = callErr.Error()
			switch status.Code(callErr) {
			case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied:
				return behavior.ActionNotDispatched, payload
			}
			return behavior.ActionUnknown, payload
		}
		var value map[string]any
		if err := json.Unmarshal(response, &value); err != nil {
			payload["error"] = "movement ownership result unavailable"
			return behavior.ActionUnknown, payload
		}
		// This confirms custody metadata, not arrival, stopping or combat success.
		payload["result"] = value
		return behavior.ActionConfirmed, payload
	}}, nil
}
