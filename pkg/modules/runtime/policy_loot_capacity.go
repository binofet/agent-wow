package modrt

import (
	"context"
	"encoding/json"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func (p *SessionPolicy) resolveLootCapacityResume(action map[string]any) (PolicyDispatch, error) {
	sid, _ := action["sessionId"].(string)
	self, _ := action["selfGuid"].(string)
	if sid != p.vm.Status().SessionID || self != p.body {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "current owned session body required")
	}
	params := map[string]any{}
	for _, key := range []string{"sessionId", "selfGuid", "sourceGuid", "gatherId", "expectedInventoryRevision", "pauseToken"} {
		params[key] = action[key]
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return PolicyDispatch{}, err
	}
	request := &lootv1.ResumeSourceRequest{}
	if err = protojson.Unmarshal(raw, request); err != nil || request.SourceGuid == 0 || request.GatherId == 0 || request.ExpectedInventoryRevision == 0 || request.PauseToken == "" {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "exact source gather and inventory revision required")
	}
	if _, _, err = p.manager.lookup("buddy-loot", "resume_source", true); err != nil {
		return PolicyDispatch{}, err
	}
	return PolicyDispatch{Module: "buddy-loot", Send: func(ctx context.Context) (behavior.ActionOutcome, map[string]any) {
		payload := map[string]any{"capability": "loot_resume_source", "source_guid": action["sourceGuid"], "gather_id": action["gatherId"]}
		response, callErr := p.manager.invokeJSON(ctx, "buddy-loot.resume_source", raw, true)
		if callErr != nil {
			payload["error"] = callErr.Error()
			switch status.Code(callErr) {
			case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied:
				return behavior.ActionNotDispatched, payload
			}
			// Metadata uncertainty is reconciled by the next factual read; it
			// has no physical packet/effect receipt and must not occupy support.
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		var receipt map[string]any
		typed := &lootv1.ResumeSourceReceipt{}
		if json.Unmarshal(response, &receipt) != nil || protojson.Unmarshal(response, typed) != nil || !typed.Cleared || typed.SessionId != request.SessionId ||
			typed.SelfGuid != request.SelfGuid || typed.SourceGuid != request.SourceGuid || typed.GatherId != request.GatherId || typed.InventoryRevision != request.ExpectedInventoryRevision || typed.PauseToken != request.PauseToken {
			payload["reconciliationRequired"] = true
			return behavior.ActionSettledUnverified, payload
		}
		payload["result"] = receipt
		return behavior.ActionConfirmed, payload // Local pause metadata only.
	}}, nil
}
