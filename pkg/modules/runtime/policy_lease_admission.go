package modrt

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

// A policy may correlate its selected action across receipt delivery and reload.
// This opaque label has no effect on action selection, authorization or outcome.
func policyActionRequestID(decision behavior.Decision) string {
	id, _ := decision.Output["actionRequestId"].(string)
	if len(id) > 256 {
		return ""
	}
	return id
}

// Only the intended action is known unsent. An earlier prerequisite RPC
// may have an unknown result; this is not a claim that it had no side effects.
// Caller holds advance; no rejected ID is exposed as native write permission.
func (p *SessionPolicy) rejectUnenteredAction(ctx context.Context, decision behavior.Decision, action map[string]any, phase string, cause error) (map[string]any, *behavior.ActionReceipt, error) {
	requestID := policyActionRequestID(decision)
	capability, _ := action["capability"].(string)
	if capability != "movement_owner" && requestID == "" {
		return decision.Output, nil, cause
	}
	// Preserve the original typed error while naming the exact no-Send gate.
	mode, _ := action["mode"].(string)
	owner, _ := action["owner"].(string)
	cause = fmt.Errorf("unentered %s %s admission (%s): %w", capability, mode, phase, cause)
	id, err := p.vm.RejectAction(decision)
	if err != nil {
		return decision.Output, nil, errors.Join(cause, err)
	}
	message := cause.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	receipt := &behavior.ActionReceipt{SessionID: p.vm.Status().SessionID, ActionID: id, Outcome: behavior.ActionNotDispatched, NeverDispatched: true, Payload: map[string]any{
		"capability": capability, "mode": mode, "owner": owner,
		"error": message, "admissionPhase": phase, "admissionRejected": true, "transport_written": false,
		"prerequisiteOutcomeUnverified": phase == "encounter_admission" || phase == "point_delivery",
	}}
	if requestID != "" {
		receipt.Payload["policy_request_id"] = requestID
	}
	// Own both the record and its scalar payload; a caller may mutate its return.
	retained := *receipt
	retained.Payload = maps.Clone(receipt.Payload)
	p.rejectedReceipt = &retained
	err = p.flushRejectedReceipt(ctx)
	return decision.Output, receipt, errors.Join(cause, err)
}

// Backpressure retains this exact terminal receipt, never resends an action.
// It must be admitted before another decision can select a replacement action.
func (p *SessionPolicy) flushRejectedReceipt(ctx context.Context) error {
	receipt := p.rejectedReceipt
	if receipt == nil {
		return nil
	}
	_, err := p.Enqueue(behavior.Event{Kind: "action_result", Receipt: receipt})
	if errors.Is(err, behavior.ErrEventBackpressure) {
		if drainErr := p.vm.Drain(ctx); drainErr != nil {
			return drainErr
		}
		_, err = p.Enqueue(behavior.Event{Kind: "action_result", Receipt: receipt})
	}
	if err == nil {
		p.rejectedReceipt = nil
	}
	return err
}
