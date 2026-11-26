package modrt

import (
	"context"
	"errors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

// Only the intended lease action is known unsent. An earlier prerequisite RPC
// may have an unknown result; this is not a claim that it had no side effects.
// Caller holds advance; no rejected ID is exposed as native write permission.
func (p *SessionPolicy) rejectUnenteredLease(ctx context.Context, decision behavior.Decision, action map[string]any, phase string, cause error) (map[string]any, *behavior.ActionReceipt, error) {
	if action["capability"] != "movement_owner" {
		return decision.Output, nil, cause
	}
	id, err := p.vm.RejectAction(decision)
	if err != nil {
		return decision.Output, nil, errors.Join(cause, err)
	}
	message := cause.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	receipt := &behavior.ActionReceipt{SessionID: p.vm.Status().SessionID, ActionID: id, Outcome: behavior.ActionNotDispatched, NeverDispatched: true, Payload: map[string]any{
		"capability": "movement_owner", "mode": action["mode"], "owner": action["owner"],
		"error": message, "admissionPhase": phase, "admissionRejected": true, "transport_written": false,
		"prerequisiteOutcomeUnverified": phase == "encounter_admission" || phase == "point_delivery",
	}}
	p.rejectedReceipt = receipt
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
