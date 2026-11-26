package behavior

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func prefixFaultedSession(t *testing.T) (*Session, string, Decision) {
	t.Helper()
	source := strings.Replace(sessionPolicyV1, "decisions = decisions + 1", `if frame.input.fail then error("fixture decide failure") end
 decisions = decisions + 1`, 1)
	source = strings.Replace(source, "local transcript, decisions =", `local arm = {owner="fixture-owner", token="fixture-lease", phase="armed"}
local transcript, decisions =`, 1)
	source = strings.Replace(source, "policy=1, decisions=decisions,", "policy=1, decisions=decisions, arm=arm,", 1)
	source = strings.Replace(source, "transcript = transcript ..", "if label == \"arm-10\" then arm.phase = \"progressed\" end\n        transcript = transcript ..", 1)
	source = strings.Replace(source, "data={transcript=", "data={arm=arm, transcript=", 1)
	source = strings.Replace(source, "transcript = snapshot.data.transcript", "arm = snapshot.data.arm\n        transcript = snapshot.data.transcript", 1)
	s := newTestSessionWithPolicy(t, source)
	sessionEnqueue(t, s, 1, sessionObservation("before"))
	one := sessionReserve(t, s, sessionDecide(t, s))
	sessionEnqueue(t, s, 2, sessionReceipt(one, ActionUnknown))
	two := sessionReserve(t, s, sessionDecide(t, s))
	sessionEnqueue(t, s, 3, sessionReceipt(two, ActionUnknown))
	old := sessionDecide(t, s)
	if _, err := s.Decide(context.Background(), map[string]any{"fail": true}); !errors.Is(err, ErrSessionFault) {
		t.Fatal(err)
	}
	return s, source, old
}

func TestFaultedReloadFullQueuePublishesOnlySafePrefixAndRetainsUnknown(t *testing.T) {
	s, source, old := prefixFaultedSession(t)
	before := s.Status()
	startClock := s.clockStart
	expected := "/1:before/2:unknown:1/3:unknown:2"
	for i := 0; i < maxSessionEvents; i++ {
		var event Event
		label := fmt.Sprintf("arm-%d", i)
		if i == 128 {
			event = sessionReceipt(2, ActionConfirmed)
			label = "confirmed:2"
		} else {
			event = sessionObservation(label)
		}
		sessionEnqueue(t, s, uint64(i+4), event)
		expected += fmt.Sprintf("/%d:%s", i+4, label)
	}
	if _, err := s.Enqueue(sessionObservation("overflow")); !errors.Is(err, ErrEventBackpressure) {
		t.Fatal("fixture not a full legal queue", err)
	}
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Deterministically expire the cumulative window before replay, without
	// spending any callback watchdog or expiring the caller. The old code passed
	// this expired aggregate context into import/on_event and could not recover.
	rev, err := s.reload(parent, source, before.Revision, func() { time.Sleep(sessionCatchupLimit + 20*time.Millisecond) })
	after := s.Status()
	if err != nil || rev != before.Revision || after.SessionID != before.SessionID || after.Epoch != before.Epoch+1 || after.Fault != "" || after.Cursor != 4 || after.Buffered != 255 || after.Pending != 2 || s.clockStart != startClock {
		t.Fatal("safe prefix not published", after, err)
	}
	if _, err := s.ReserveAction(old); !errors.Is(err, ErrFenced) {
		t.Fatal("old ticket survived", err)
	}
	view, err := s.State(context.Background())
	if err != nil || view.LatestDecision != nil {
		t.Fatal("reload invented a fresh decision", view, err)
	}
	if _, err := s.Enqueue(sessionReceipt(2, ActionConfirmed)); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatal("queued terminal receipt admitted twice", err)
	}
	if _, err := s.Enqueue(sessionReceipt(1, ActionUnknown)); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatal("unknown receipt admitted twice", err)
	}
	// An expired scheduling window still completes exactly one atomic event.
	// It never becomes the context used by the legal on_event/export callbacks.
	expired, stop := context.WithCancel(context.Background())
	stop()
	added := false
	rounds := 0
	for s.Status().Buffered > 0 {
		previous := s.Status()
		if err := s.enter(context.Background()); err != nil {
			t.Fatal(err)
		}
		err := s.drainWindow(context.Background(), expired, 0)
		s.leave()
		now := s.Status()
		rounds++
		if err != nil && !errors.Is(err, ErrFenced) || now.Cursor != previous.Cursor+1 || now.Fault != "" || s.nextAction != 2 {
			t.Fatal("prefix lost order or manufactured an action", previous, now, err)
		}
		if now.Cursor < 132 && now.Pending != 2 || now.Cursor >= 132 && now.Pending != 1 {
			t.Fatal("receipt retired outside its prefix", now)
		}
		if _, err := s.ReserveAction(old); !errors.Is(err, ErrFenced) {
			t.Fatal("stale ticket admitted during catch-up", err)
		}
		if !added && rounds == 10 {
			seq, err := s.Enqueue(sessionObservation("arrived-during-recovery"))
			if err != nil || seq != 260 {
				t.Fatal(seq, err)
			}
			expected += "/260:arrived-during-recovery"
			added = true
		}
		if rounds > maxSessionEvents {
			t.Fatal("recovery failed to make finite progress")
		}
	}
	if got := s.Outstanding(); len(got) != 1 || got[0].ActionID != 1 || got[0].Outcome != ActionUnknown {
		t.Fatal("unknown action was replayed or discarded", got)
	}
	decision := sessionDecide(t, s)
	arm, ok := decision.Output["arm"].(map[string]any)
	if !ok || arm["owner"] != "fixture-owner" || arm["token"] != "fixture-lease" || arm["phase"] != "progressed" {
		t.Fatal("owned policy checkpoint lost or replayed", arm)
	}
	if decision.Output["transcript"] != expected || decision.Output["cursor"] != "260" || s.nextAction != 2 {
		t.Fatal("events were skipped, duplicated or reordered", decision.Output)
	}
	if _, err := s.Enqueue(sessionReceipt(2, ActionConfirmed)); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatal("terminal receipt replayed", err)
	}
}

func TestHealthyReloadRetainsAtomicCatchupDeadline(t *testing.T) {
	s := newTestSession(t)
	sessionEnqueue(t, s, 1, sessionObservation("before"))
	old := sessionDecide(t, s)
	sessionEnqueue(t, s, 2, sessionObservation("queued"))
	before, _ := s.State(context.Background())
	_, err := s.reload(context.Background(), sessionPolicyV1+"\n-- healthy replacement", s.Status().Revision, func() { time.Sleep(sessionCatchupLimit + 20*time.Millisecond) })
	after, _ := s.State(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || after.Status.Revision != before.Status.Revision || after.Status.Cursor != before.Status.Cursor || after.Status.Buffered != before.Status.Buffered || after.Status.Fault != "" || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) {
		t.Fatal("healthy reload partially published", after, err)
	}
	if _, err := s.ReserveAction(old); !errors.Is(err, ErrFenced) {
		t.Fatal("failed reload kept old ticket", err)
	}
}

func TestFaultedPrefixRecoveryPreservesCallerCancellationAndWatchdog(t *testing.T) {
	for _, kind := range []string{"caller_cancel", "runaway_event"} {
		t.Run(kind, func(t *testing.T) {
			s, source, _ := prefixFaultedSession(t)
			sessionEnqueue(t, s, 4, sessionObservation("queued"))
			before, _ := s.State(context.Background())
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			fence := func() {}
			if kind == "caller_cancel" {
				fence = cancel
			} else {
				source = strings.Replace(source, "local receipt = event.receipt", "while true do end\n local receipt = event.receipt", 1)
			}
			_, err := s.reload(parent, source, before.Status.Revision, fence)
			after, _ := s.State(context.Background())
			if kind == "caller_cancel" && !errors.Is(err, context.Canceled) || kind == "runaway_event" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("caller/watchdog relaxed", err)
			}
			if after.Status.Revision != before.Status.Revision || after.Status.Cursor != before.Status.Cursor || after.Status.Buffered != before.Status.Buffered || after.Status.Pending != before.Status.Pending || after.Status.Fault != before.Status.Fault || !reflect.DeepEqual(after.Checkpoint, before.Checkpoint) {
				t.Fatal("failed recovery modified custody", after, err)
			}
		})
	}
}

func TestHealthyDrainSchedulingExpiryCommitsOnlyCompleteEvent(t *testing.T) {
	s := newTestSession(t)
	sessionEnqueue(t, s, 1, sessionObservation("first"))
	sessionEnqueue(t, s, 2, sessionObservation("second"))
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := s.drainWindow(context.Background(), expired, 0)
	s.leave()
	status := s.Status()
	if !errors.Is(err, ErrFenced) || status.Fault != "" || status.Cursor != 1 || status.Buffered != 1 {
		t.Fatal("fairness deadline poisoned healthy callback", status, err)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	decision := sessionDecide(t, s)
	if decision.Output["transcript"] != "/1:first/2:second" {
		t.Fatal(decision.Output)
	}
}
