package world

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/runtime"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPolicyReloadRetainsActualWorldWriteReceipt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	const target = "17379391151907274754"
	const spell = 585
	payload := fixtureSpellBody(1, spell, 17379391151907274754)
	const nextSpell = 589
	nextPayload := fixtureSpellBody(2, nextSpell, 17379391151907274754)
	conn := peerConnection(t, func(peer *testPeer) {
		peer.login()
		if actual := peer.expect(opcode.CMSGCastSpell); !bytes.Equal(actual, payload) {
			t.Errorf("unexpected wire bytes: %x", actual)
		}
		if actual := peer.expect(opcode.CMSGCastSpell); !bytes.Equal(actual, nextPayload) {
			t.Errorf("unexpected updated spell bytes: %x", actual)
		}
		peer.closed()
	})
	m := modrt.New(nil, nil, nil)
	trace := NewGameplayTrace(4)
	s, err := enter(ctx, conn, Realm{1, "Offline"}, Character{99, "Test"}, nil, quietTimings(), Options{Modules: m, GameplayTrace: trace})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var identity modrt.SessionCatalog
	data, err := s.InvokeJSON(ctx, "session.describe", nil)
	if err != nil || json.Unmarshal(data, &identity) != nil || identity.SessionID == "" {
		t.Fatalf("session discovery: %s %v", data, err)
	}
	params, err := json.Marshal(map[string]string{"sessionId": identity.SessionID, "characterGuid": identity.CharacterGUID})
	if err != nil {
		t.Fatal(err)
	}
	const policy = `
local state = { seen = "none" }
return {
  on_event = function(event)
    if event.kind == "engagement" then state.target = event.value.target end
    if event.receipt then state.seen = event.receipt.outcome end
    return true
  end,
  decide = function(frame)
    if frame.input.act and state.target and #frame.pending == 0 then
      return { action = { technique = "smite", target = state.target } }
    end
    return { seen = state.seen }
  end,
  export_state = function() return { version = 1, data = state } end,
  import_state = function(checkpoint) state = checkpoint.data; return true end,
}`
	host, err := m.StartPolicy(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	view := func() modrt.PolicySnapshot {
		t.Helper()
		data, err := s.InvokeJSON(ctx, "session.attach", params)
		var attached struct {
			SessionID string
			Policy    *modrt.PolicySnapshot
		}
		if err != nil || json.Unmarshal(data, &attached) != nil || attached.SessionID != identity.SessionID || attached.Policy == nil {
			t.Fatalf("session attachment: %s %v", data, err)
		}
		return *attached.Policy
	}
	old := view()
	if _, err := host.Enqueue(behavior.Event{Kind: "engagement", Value: map[string]any{"target": target}}); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	result := make(chan error, 1)
	go func() {
		_, _, err := host.Step(ctx, map[string]any{"act": true}, func(action map[string]any) (modrt.PolicyDispatch, error) {
			if action["technique"] != "smite" || action["target"] != target {
				return modrt.PolicyDispatch{}, fmt.Errorf("unexpected Lua action: %+v", action)
			}
			return modrt.PolicyDispatch{Module: "buddy-actions", Send: func(dispatchCtx context.Context) (behavior.ActionOutcome, map[string]any) {
				err := s.SendPacket(dispatchCtx, opcode.CMSGCastSpell, payload)
				written <- err
				<-release
				if err != nil {
					return behavior.ActionUnknown, nil
				}
				return behavior.ActionTransportAccepted, map[string]any{"target": target, "spell": spell}
			}}, nil
		})
		result <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-result:
		t.Fatalf("policy returned before writing: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// A second Step must wait for the dispatcher, independent of Lua's
	// pending-action guard. Reload and observation remain available.
	if _, _, err := host.Step(ctx, map[string]any{"act": true}, nil); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second Step entered the active dispatcher: %v", err)
	}
	v2 := strings.Replace(policy, `technique = "smite"`, `technique = "shadow_word_pain"`, 1)
	revision, err := host.Reload(ctx, v2, old.Revision)
	if err != nil {
		t.Fatal(err)
	}
	updated := view()
	if updated.Revision != revision || updated.Revision == old.Revision || updated.Generation == old.Generation ||
		len(updated.PendingActionIDs) != 1 || updated.PendingActionIDs[0] != "1" {
		t.Fatalf("revision or pending cast lost: %+v", updated)
	}
	// Release the receipt without closing the test-cleanup channel twice.
	select {
	case release <- struct{}{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	// The reloaded policy wants to act, but an observed write is still pending.
	out, _, err := host.Step(ctx, map[string]any{"act": true}, nil)
	if err != nil || out["seen"] != string(behavior.ActionTransportAccepted) || len(view().PendingActionIDs) != 1 {
		t.Fatalf("transport result was lost or treated as game effect: %+v %v", out, err)
	}
	// This terminal observation is injected by the fixture. The cast packets
	// and transport receipts pass through the session's actual connection.
	if _, err := host.Enqueue(behavior.Event{Kind: "fixture_effect", Receipt: &behavior.ActionReceipt{
		SessionID: identity.SessionID, ActionID: 1, Outcome: behavior.ActionConfirmed,
	}}); err != nil {
		t.Fatal(err)
	}
	_, receipt, err := host.Step(ctx, map[string]any{"act": true}, func(action map[string]any) (modrt.PolicyDispatch, error) {
		if action["technique"] != "shadow_word_pain" || action["target"] != target {
			return modrt.PolicyDispatch{}, fmt.Errorf("unexpected updated Lua action: %+v", action)
		}
		return modrt.PolicyDispatch{Module: "buddy-actions", Send: func(dispatchCtx context.Context) (behavior.ActionOutcome, map[string]any) {
			if err := s.SendPacket(dispatchCtx, opcode.CMSGCastSpell, nextPayload); err != nil {
				return behavior.ActionUnknown, nil
			}
			return behavior.ActionTransportAccepted, map[string]any{"target": target, "spell": nextSpell}
		}}, nil
	})
	if err != nil || receipt == nil || receipt.ActionID != 2 || receipt.Outcome != behavior.ActionTransportAccepted {
		t.Fatalf("updated policy failed to dispatch: %+v %v", receipt, err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "fixture_effect", Receipt: &behavior.ActionReceipt{
		SessionID: identity.SessionID, ActionID: 2, Outcome: behavior.ActionConfirmed,
	}}); err != nil {
		t.Fatal(err)
	}
	_, receipt, err = host.Step(ctx, map[string]any{"act": true}, func(map[string]any) (modrt.PolicyDispatch, error) {
		m.BeginLogout()
		return modrt.PolicyDispatch{Module: "buddy-actions", Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			t.Error("the cast was sent after logout began")
			return behavior.ActionNotDispatched, nil
		}}, nil
	})
	if receipt != nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("logout did not fence the proposed cast: %+v %v", receipt, err)
	}
	writes := trace.Snapshot()
	if writes.Dropped != 0 || len(writes.Attempts) != 2 || writes.Attempts[0].Origin.ActionID != "1" ||
		writes.Attempts[0].Origin.SessionID != identity.SessionID || writes.Attempts[0].Origin.PolicyRevision != old.Revision ||
		writes.Attempts[1].Origin.ActionID != "2" || writes.Attempts[1].Origin.SessionID != identity.SessionID ||
		writes.Attempts[1].Origin.PolicyRevision != updated.Revision {
		t.Fatalf("unexpected transport trace: %+v", writes)
	}
}
