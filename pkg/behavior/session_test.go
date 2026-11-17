package behavior

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const sessionPolicyV1 = `
local transcript, decisions = "", 0
return {
    decide = function(frame)
        decisions = decisions + 1
        return {
            action={target="wolf-a"}, policy=1, decisions=decisions,
            session=frame.session_id, cursor=frame.cursor, transcript=transcript,
        }
    end,
    on_event = function(event)
        local receipt = event.receipt
        local label = receipt and (receipt.outcome .. ":" .. receipt.action_id) or event.value.label
        transcript = transcript .. "/" .. event.sequence .. ":" .. label
        return true
    end,
    export_state = function()
        return {version=1, data={transcript=transcript, decisions=decisions}}
    end,
    import_state = function(snapshot)
        transcript = snapshot.data.transcript
        decisions = snapshot.data.decisions
        return true
    end,
}
`

const sessionPolicyV2 = `
local transcript, decisions, pending = "", 0, ""
return {
    decide = function(frame)
        decisions = decisions + 1
        return {
            action={target="wolf-b"}, policy=2, decisions=decisions,
            session=frame.session_id, cursor=frame.cursor, transcript=transcript,
            imported_pending=pending,
        }
    end,
    on_event = function(event)
        local receipt = event.receipt
        local label = receipt and (receipt.outcome .. ":" .. receipt.action_id) or event.value.label
        transcript = transcript .. "/" .. event.sequence .. ":" .. label
        return true
    end,
    export_state = function()
        return {version=2, data={transcript=transcript, decisions=decisions}}
    end,
    import_state = function(snapshot)
        assert(snapshot.version == 1 and snapshot.session_id == "offline-session")
        assert(snapshot.cursor == "2" and #snapshot.pending == 1)
        assert(snapshot.pending[1].action_id == "1" and snapshot.pending[1].outcome == "unknown")
        transcript = snapshot.data.transcript
        decisions = snapshot.data.decisions
        pending = snapshot.pending[1].action_id
        return true
    end,
}
`

func newTestSession(t *testing.T) *Session {
	t.Helper()
	return newTestSessionWithPolicy(t, sessionPolicyV1)
}

func newTestSessionWithPolicy(t *testing.T, source string) *Session {
	t.Helper()
	s, err := NewSession(context.Background(), "offline-session", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sessionDecide(t *testing.T, s *Session) Decision {
	t.Helper()
	decision, err := s.Decide(context.Background(), map[string]any{"observed": true})
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func sessionReserve(t *testing.T, s *Session, d Decision) uint64 {
	t.Helper()
	id, err := s.ReserveAction(d)
	if err != nil || id == 0 {
		t.Fatalf("reserve id=%d error=%v", id, err)
	}
	return id
}

func sessionEnqueue(t *testing.T, s *Session, seq uint64, event Event) {
	t.Helper()
	got, err := s.Enqueue(event)
	if err != nil || got != seq {
		t.Fatalf("enqueue seq=%d, want %d, error=%v", got, seq, err)
	}
}

func sessionObservation(label string) Event {
	return Event{Kind: "observed", Value: map[string]any{"label": label}}
}

func sessionReceipt(id uint64, outcome ActionOutcome) Event {
	return Event{Kind: "action_result", Receipt: &ActionReceipt{SessionID: "offline-session", ActionID: id, Outcome: outcome}}
}

func waitSessionResult[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("session operation did not finish")
		return *new(T)
	}
}

type sessionReloadResult struct {
	revision string
	err      error
}

func pauseSessionReload(t *testing.T, s *Session, source string) (<-chan sessionReloadResult, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	t.Cleanup(resume)
	result := make(chan sessionReloadResult, 1)
	revision := s.Status().Revision
	go func() {
		next, err := s.reload(context.Background(), source, revision, func() {
			close(entered)
			<-release
		})
		result <- sessionReloadResult{revision: next, err: err}
	}()
	select {
	case <-entered:
	case got := <-result:
		t.Fatalf("reload failed before the barrier: %+v", got)
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not reach the barrier")
	}
	return result, resume
}

func TestSessionPersistentClosureAndOwnedEvent(t *testing.T) {
	s := newTestSession(t)
	first := sessionDecide(t, s)
	if first.Output["session"] != "offline-session" || first.Output["cursor"] != "0" {
		t.Fatalf("initial decision: %+v", first.Output)
	}
	event := sessionObservation("original")
	sessionEnqueue(t, s, 1, event)
	event.Value["label"] = "caller mutation"
	first.Output["transcript"] = "caller mutation"
	second := sessionDecide(t, s)
	if second.Output["decisions"] != first.Output["decisions"].(float64)+1 ||
		second.Output["cursor"] != "1" || second.Output["transcript"] != "/1:original" {
		t.Fatalf("persistent decision: %+v", second.Output)
	}
	status := s.Status()
	if status.SessionID != "offline-session" || status.Cursor != 1 || status.Buffered != 0 || status.Fault != "" {
		t.Fatalf("status: %+v", status)
	}
}

func TestSessionReloadFencesTicketsAndDrainsReceiptInOrder(t *testing.T) {
	s := newTestSession(t)
	other := newTestSession(t)
	foreign := sessionDecide(t, other)
	if _, err := s.ReserveAction(foreign); err == nil {
		t.Fatal("a ticket from another instance acquired an action ID")
	}

	sessionEnqueue(t, s, 1, sessionObservation("before"))
	first := sessionDecide(t, s)
	actionID := sessionReserve(t, s, first)
	if actionID != 1 {
		t.Fatalf("first action ID = %d", actionID)
	}
	if _, err := s.ReserveAction(first); err == nil {
		t.Fatal("ticket reserved the same action twice")
	}
	sessionEnqueue(t, s, 2, sessionReceipt(actionID, ActionUnknown))
	stale := sessionDecide(t, s)
	before := s.Status()
	if before.Pending != 1 || before.Cursor != 2 || stale.Output["transcript"] != "/1:before/2:unknown:1" {
		t.Fatalf("unknown receipt must retain the reservation: %+v %+v", stale.Output, before)
	}
	outstanding := s.Outstanding()
	if len(outstanding) != 1 || outstanding[0].ActionID != actionID || outstanding[0].Outcome != ActionUnknown {
		t.Fatalf("outstanding after unknown: %+v", outstanding)
	}

	reload, resume := pauseSessionReload(t, s, sessionPolicyV2)
	type duringResult struct {
		reserveErr, decideErr, foreignErr, duplicateErr error
	}
	observersDone := make(chan duringResult, 1)
	go func() {
		_, reserveErr := s.ReserveAction(stale)
		_, decideErr := s.Decide(context.Background(), nil)
		_, foreignErr := s.Enqueue(sessionReceipt(actionID+999, ActionConfirmed))
		seq3, err3 := s.Enqueue(sessionObservation("during"))
		seq4, err4 := s.Enqueue(sessionReceipt(actionID, ActionConfirmed))
		_, duplicateErr := s.Enqueue(sessionReceipt(actionID, ActionConfirmed))
		seq5, err5 := s.Enqueue(sessionObservation("after-receipt"))
		if seq3 != 3 || seq4 != 4 || seq5 != 5 || err3 != nil || err4 != nil || err5 != nil {
			t.Errorf("buffered events: %d/%v %d/%v %d/%v", seq3, err3, seq4, err4, seq5, err5)
		}
		observersDone <- duringResult{reserveErr, decideErr, foreignErr, duplicateErr}
	}()
	during := waitSessionResult(t, observersDone)
	if !errors.Is(during.reserveErr, ErrFenced) || !errors.Is(during.decideErr, ErrFenced) ||
		!errors.Is(during.foreignErr, ErrInvalidReceipt) || !errors.Is(during.duplicateErr, ErrInvalidReceipt) {
		t.Fatalf("fence and receipt admission: %+v", during)
	}
	fenced := s.Status()
	if !fenced.Swapping || fenced.Revision != before.Revision || fenced.Epoch <= before.Epoch ||
		fenced.Cursor != 2 || fenced.Buffered != 3 || fenced.Pending != 1 {
		t.Fatalf("state published or lost while fenced: %+v", fenced)
	}
	resume()
	result := waitSessionResult(t, reload)
	if result.err != nil || result.revision != sourceRevision(sessionPolicyV2) || result.revision == before.Revision {
		t.Fatalf("reload revision=%q error=%v", result.revision, result.err)
	}
	after := s.Status()
	if after.Swapping || after.SessionID != before.SessionID || after.Revision != result.revision ||
		after.Cursor != 5 || after.Buffered != 0 || after.Pending != 0 || after.Fault != "" {
		t.Fatalf("post-reload status: %+v", after)
	}
	if _, err := s.ReserveAction(stale); !errors.Is(err, ErrFenced) {
		t.Fatalf("old unsubmitted ticket survived the reload: %v", err)
	}
	if _, err := s.Reload(context.Background(), sessionPolicyV1, before.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale expected revision was accepted: %v", err)
	}
	if _, err := s.Enqueue(sessionReceipt(actionID, ActionConfirmed)); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatal("committed receipt was replayable")
	}
	second := sessionDecide(t, s)
	if second.Output["policy"] != float64(2) || second.Output["imported_pending"] != "1" ||
		second.Output["cursor"] != "5" || second.Output["transcript"] != "/1:before/2:unknown:1/3:during/4:confirmed:1/5:after-receipt" ||
		second.Output["action"].(map[string]any)["target"] != "wolf-b" {
		t.Fatalf("reloaded decision: %+v", second.Output)
	}
	if id := sessionReserve(t, s, second); id != 2 {
		t.Fatalf("second action ID = %d", id)
	}
}

func TestSessionCompileFailureDoesNotFenceWorkingPolicy(t *testing.T) {
	s := newTestSession(t)
	current := sessionDecide(t, s)
	before := s.Status()
	if revision, err := s.Reload(context.Background(), "return {decide=function(", before.Revision); err == nil ||
		revision != before.Revision {
		t.Fatalf("invalid source: revision=%q error=%v", revision, err)
	}
	if got := s.Status(); got != before {
		t.Fatalf("compile failure changed status: before=%+v after=%+v", before, got)
	}
	if id := sessionReserve(t, s, current); id != 1 {
		t.Fatalf("current ticket after compile rejection produced ID %d", id)
	}
}

func TestSessionRejectedReloadRetainsOriginalStateAndUndrainedQueue(t *testing.T) {
	for _, tt := range []struct {
		name, source, failure string
	}{
		{
			name: "import",
			source: strings.Replace(sessionPolicyV2,
				"pending = snapshot.pending[1].action_id\n        return true",
				`transcript = "/candidate-only"
        error("fixture import failed")`, 1),
			failure: "fixture import failed",
		},
		{
			name: "event replay",
			source: strings.Replace(sessionPolicyV2,
				"transcript = transcript .. \"/\" .. event.sequence .. \":\" .. label",
				`transcript = transcript .. "/" .. event.sequence .. ":" .. label
        if event.sequence == "4" then error("fixture replay failed") end`, 1),
			failure: "fixture replay failed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession(t)
			sessionEnqueue(t, s, 1, sessionObservation("before"))
			id := sessionReserve(t, s, sessionDecide(t, s))
			sessionEnqueue(t, s, 2, sessionReceipt(id, ActionUnknown))
			old := sessionDecide(t, s)
			before := s.Status()

			reload, resume := pauseSessionReload(t, s, tt.source)
			buffered := make(chan error, 1)
			go func() {
				if _, err := s.Enqueue(sessionObservation("buffered")); err != nil {
					buffered <- err
					return
				}
				_, err := s.Enqueue(sessionReceipt(id, ActionConfirmed))
				buffered <- err
			}()
			if err := waitSessionResult(t, buffered); err != nil {
				t.Fatal(err)
			}
			resume()
			result := waitSessionResult(t, reload)
			if result.err == nil || !strings.Contains(result.err.Error(), tt.failure) {
				t.Fatalf("expected %q, got revision=%q error=%v", tt.failure, result.revision, result.err)
			}
			after := s.Status()
			if after.SessionID != before.SessionID || after.Revision != before.Revision ||
				after.Cursor != before.Cursor || after.Epoch <= before.Epoch ||
				after.Buffered != 2 || after.Pending != 1 || after.Fault != "" {
				t.Fatalf("candidate partially published or lost queued events: %+v", after)
			}
			if _, err := s.ReserveAction(old); !errors.Is(err, ErrFenced) {
				t.Fatalf("pre-rollback ticket was not fenced: %v", err)
			}
			retained := sessionDecide(t, s)
			if retained.Output["policy"] != float64(1) || retained.Output["cursor"] != "4" ||
				retained.Output["transcript"] != "/1:before/2:unknown:1/3:buffered/4:confirmed:1" {
				t.Fatalf("retained policy did not replay the entire queue: %+v", retained.Output)
			}
			settled := s.Status()
			if settled.Buffered != 0 || settled.Pending != 0 || settled.Cursor != 4 {
				t.Fatalf("rollback events did not settle exactly once: %+v", settled)
			}
			if _, err := s.Enqueue(sessionReceipt(id, ActionConfirmed)); !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("replayed receipt admitted twice: %v", err)
			}
		})
	}
}

func TestSessionReloadRecoversLastCheckpointAfterActiveEventFault(t *testing.T) {
	source := strings.Replace(sessionPolicyV1,
		"transcript = transcript .. \"/\" .. event.sequence .. \":\" .. label",
		`transcript = transcript .. "/" .. event.sequence .. ":" .. label
        if label == "fault" then
            transcript = "/corrupted-active-state"
            error("fixture active callback failed")
        end`, 1)
	s := newTestSessionWithPolicy(t, source)
	sessionEnqueue(t, s, 1, sessionObservation("before"))
	id := sessionReserve(t, s, sessionDecide(t, s))
	sessionEnqueue(t, s, 2, sessionReceipt(id, ActionUnknown))
	old := sessionDecide(t, s)
	before := s.Status()

	sessionEnqueue(t, s, 3, sessionObservation("fault"))
	sessionEnqueue(t, s, 4, sessionReceipt(id, ActionConfirmed))
	if err := s.Drain(context.Background()); !errors.Is(err, ErrSessionFault) ||
		!strings.Contains(err.Error(), "fixture active callback failed") {
		t.Fatalf("faulting event callback: %v", err)
	}
	faulted := s.Status()
	if faulted.Revision != before.Revision || faulted.Cursor != before.Cursor ||
		faulted.Buffered != 2 || faulted.Pending != 1 || faulted.Fault == "" {
		t.Fatalf("failed callback consumed or changed checkpoint: %+v", faulted)
	}
	if _, err := s.ReserveAction(old); !errors.Is(err, ErrFenced) {
		t.Fatalf("faulted VM accepted old ticket: %v", err)
	}
	if _, err := s.Decide(context.Background(), nil); !errors.Is(err, ErrSessionFault) {
		t.Fatalf("faulted VM accepted a new decision: %v", err)
	}

	revision, err := s.Reload(context.Background(), sessionPolicyV2, before.Revision)
	if err != nil || revision != sourceRevision(sessionPolicyV2) {
		t.Fatalf("recovery revision=%q error=%v", revision, err)
	}
	recovered := s.Status()
	if recovered.SessionID != before.SessionID || recovered.Revision != revision ||
		recovered.Cursor != 4 || recovered.Pending != 0 || recovered.Buffered != 0 || recovered.Fault != "" {
		t.Fatalf("recovery did not commit checkpoint and buffered result: %+v", recovered)
	}
	d := sessionDecide(t, s)
	if d.Output["transcript"] != "/1:before/2:unknown:1/3:fault/4:confirmed:1" ||
		d.Output["imported_pending"] != "1" {
		t.Fatalf("recovered state preserved fault mutation or lost events: %+v", d.Output)
	}
	if _, err := s.Enqueue(sessionReceipt(id, ActionConfirmed)); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("recovered receipt admitted twice: %v", err)
	}
}

func TestSessionEventBackpressureDoesNotConsumeReceiptOrSequence(t *testing.T) {
	s := newTestSession(t)
	id := sessionReserve(t, s, sessionDecide(t, s))
	for i := range maxSessionEvents {
		sessionEnqueue(t, s, uint64(i+1), sessionObservation("observed"))
	}
	receipt := sessionReceipt(id, ActionConfirmed)
	if seq, err := s.Enqueue(receipt); !errors.Is(err, ErrEventBackpressure) || seq != 0 {
		t.Fatalf("full queue receipt: sequence=%d error=%v", seq, err)
	}
	status := s.Status()
	actions := s.Outstanding()
	if status.Buffered != maxSessionEvents || status.Cursor != 0 || status.Pending != 1 ||
		len(actions) != 1 || actions[0].ActionID != id || actions[0].Outcome != ActionReserved {
		t.Fatalf("refused receipt affected queue or ledger: %+v %+v", status, actions)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessionEnqueue(t, s, uint64(maxSessionEvents+1), receipt)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = s.Status()
	if status.Buffered != 0 || status.Pending != 0 || status.Cursor != uint64(maxSessionEvents+1) {
		t.Fatalf("retried identical receipt did not settle: %+v", status)
	}
}
