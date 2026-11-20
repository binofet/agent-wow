package behavior

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSessionNewIngressFencesDecisionAndReceiptMustMatchSession(t *testing.T) {
	s := newTestSession(t)
	before := sessionDecide(t, s)
	sessionEnqueue(t, s, 1, sessionObservation("newer"))
	if _, err := s.ReserveAction(before); !errors.Is(err, ErrFenced) {
		t.Fatalf("ticket survived newer ingress: %v", err)
	}
	id := sessionReserve(t, s, sessionDecide(t, s))
	foreign := sessionReceipt(id, ActionConfirmed)
	foreign.Receipt.SessionID = "different-session"
	if _, err := s.Enqueue(foreign); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("matching ID from a different session was accepted: %v", err)
	}
	sessionEnqueue(t, s, 2, sessionReceipt(id, ActionTransportAccepted))
	sessionEnqueue(t, s, 3, sessionReceipt(id, ActionUnknown))
	if _, err := s.Enqueue(sessionReceipt(id, ActionNotDispatched)); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("known transport acceptance was forgotten: %v", err)
	}
	sessionEnqueue(t, s, 4, sessionReceipt(id, ActionConfirmed))
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out := s.Outstanding(); len(out) != 0 || s.Status().Cursor != 4 {
		t.Fatalf("confirmed receipt did not retire the original action: %+v %+v", out, s.Status())
	}
}

func TestSessionResultAfterActivationRetiresOriginalReservation(t *testing.T) {
	s := newTestSession(t)
	sessionEnqueue(t, s, 1, sessionObservation("before"))
	id := sessionReserve(t, s, sessionDecide(t, s))
	sessionEnqueue(t, s, 2, sessionReceipt(id, ActionUnknown))
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	revision, err := s.Reload(context.Background(), sessionPolicyV2, s.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if out := s.Outstanding(); len(out) != 1 || out[0].ActionID != id || out[0].Outcome != ActionUnknown {
		t.Fatalf("swap discarded unresolved action: %+v", out)
	}
	sessionEnqueue(t, s, 3, sessionReceipt(id, ActionConfirmed))
	got := sessionDecide(t, s)
	if got.Output["transcript"] != "/1:before/2:unknown:1/3:confirmed:1" ||
		s.Status().Revision != revision || s.Status().Pending != 0 {
		t.Fatalf("late result not applied to active revision: %+v %+v", got.Output, s.Status())
	}
}

func TestSessionImportFitsFullCheckpointAndReservationLedger(t *testing.T) {
	size := MaxJSONBytes - 128
	source := fmt.Sprintf(`
local block, text = "x", ""
for n = 0, 14 do
    if n >= 7 then text = text .. block end
    block = block .. block
end
return {
    decide=function(frame) return {pending=#frame.pending} end,
    on_event=function(event) return true end,
    export_state=function() return {version=1, data={text=text}} end,
    import_state=function(snapshot)
        assert(#snapshot.data.text == %d and #snapshot.pending == %d)
        assert(snapshot.pending[#snapshot.pending].action_id == "18446744073709551615")
        text = snapshot.data.text
        return true
    end,
}`, size, maxPendingActions)
	s := newTestSessionWithPolicy(t, source)
	// Populate the longest possible reservation IDs and outcomes. The
	// individual reservation API is tested independently above.
	s.mu.Lock()
	for i := range maxPendingActions {
		id := ^uint64(0) - uint64(i)
		s.pending[id] = pendingAction{applied: ActionTransportAccepted, queued: ActionTransportAccepted, accepted: true}
	}
	s.nextAction = ^uint64(0)
	s.mu.Unlock()
	before := s.Status()
	revision, err := s.Reload(context.Background(), source+"\n-- next revision", before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionDecide(t, s); got.Output["pending"] != float64(maxPendingActions) ||
		s.Status().Revision != revision || s.Status().Pending != maxPendingActions {
		t.Fatalf("full ledger not available to candidate: %+v %+v", got.Output, s.Status())
	}
}
