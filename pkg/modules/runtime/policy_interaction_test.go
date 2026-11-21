package modrt

import (
	"context"
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestPolicyInteractionReleaseUsesRealNativeLeaseAndRetiresLedger(t *testing.T) {
	for _, mode := range []string{"current", "foreign_token", "foreign_body", "stale_visit"} {
		t.Run(mode, func(t *testing.T) {
			m, native, cb := friendlyEncounterManager(t)
			lease, err := native.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: 30000})
			if err != nil {
				t.Fatal(err)
			}
			body, token, visit := "22", lease.Token, "1"
			if mode == "foreign_token" {
				token += "old"
			}
			if mode == "foreign_body" {
				body = "23"
			}
			if mode == "stale_visit" {
				visit = "2"
			}
			source := fmt.Sprintf(`return {on_event=function(e)return true end,export_state=function()return {version=1,data={}}end,import_state=function(c)return true end,decide=function(f)return {action={capability="release_interaction",sessionId=f.session_id,selfGuid=%q,visitId=%q,owner="trade",token=%q}}end}`, body, visit, token)
			policy, err := m.StartPolicy(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer policy.close()
			_, receipt, stepErr := policy.Step(context.Background(), nil, nil)
			observed, err := native.EncounterObservation(context.Background(), &emptypb.Empty{})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "current" {
				if stepErr != nil || receipt == nil || receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["phase"] != "interaction_released" || receipt.Payload["effect_observed"] != false || observed.GetInteraction().GetOwner() != "" {
					t.Fatal("typed Lua release did not reach actual native owner", receipt, stepErr, observed)
				}
				// Receipt consumption happens at the next actual VM turn. A released
				// metadata lane cannot keep the global pending gate closed for healing.
				if _, _, err = policy.Step(context.Background(), nil, func(map[string]any) (PolicyDispatch, error) {
					return PolicyDispatch{}, fmt.Errorf("no second mutation")
				}); err == nil {
					t.Fatal("test intentionally supplied no second dispatch")
				}
				if policy.vm.Status().Pending != 0 {
					t.Fatal("released interaction stranded global pending", policy.vm.Status())
				}
			} else if observed.GetInteraction().GetToken() != lease.Token || stepErr == nil && (receipt == nil || receipt.Outcome != behavior.ActionNotDispatched) {
				t.Fatal("wrong exact binding released interaction", mode, receipt, stepErr, observed)
			}
			if len(cb.writes) != 0 {
				t.Fatal("stationary release or bad identity wrote a game packet", cb.writes)
			}
		})
	}
}

func TestPolicyInteractionReleasePermitsOnlyOwnStopPacket(t *testing.T) {
	op := &policySpell{capability: "release_interaction", movement: &policyMovement{self: 9, stop: true}}
	if !op.matchesActionPacket(opcode.MSGMoveStop, policyMovementPacket(9)) {
		t.Fatal("own normal STOP refused")
	}
	for _, test := range []struct {
		code uint32
		body []byte
	}{
		{opcode.MSGMoveStop, policyMovementPacket(8)},
		{opcode.MSGMoveStartForward, policyMovementPacket(9)},
		{opcode.MSGMoveHeartbeat, policyMovementPacket(9)},
		{opcode.CMSGAttackstop, nil},
		{opcode.CMSGCastSpell, nil},
	} {
		if op.matchesActionPacket(test.code, test.body) {
			t.Fatal("release could write an unrelated game action", test.code)
		}
	}
}
