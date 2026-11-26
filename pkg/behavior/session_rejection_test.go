package behavior

import (
	"context"
	"errors"
	"testing"
)

func TestRejectActionRecordsOnlyAnUnenteredProposal(t *testing.T) {
	s := newTestSession(t)
	d := sessionDecide(t, s)
	sessionEnqueue(t, s, 1, sessionObservation("new input fences send"))
	if _, err := s.ReserveAction(d); !errors.Is(err, ErrFenced) {
		t.Fatal(err)
	}
	id, err := s.RejectAction(d)
	if err != nil || id != 1 {
		t.Fatal(id, err)
	}
	_, pending := s.Inspect()
	if len(pending) != 1 || !pending[0].NeverDispatched || pending[0].Outcome != ActionReserved {
		t.Fatal(pending)
	}
	if _, err = s.ReserveAction(d); !errors.Is(err, ErrFenced) {
		t.Fatal("rejected proposal still dispatchable", err)
	}
	if _, err = s.RejectAction(d); !errors.Is(err, ErrFenced) {
		t.Fatal("duplicate rejection", err)
	}
	if _, err = s.Enqueue(sessionReceipt(id, ActionUnknown)); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatal("unsent rejection accepted UNKNOWN", err)
	}
	sessionEnqueue(t, s, 2, sessionReceipt(id, ActionNotDispatched))
	if err = s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.Outstanding()) != 0 {
		t.Fatal(s.Outstanding())
	}
	if got := sessionDecide(t, s).Output["transcript"]; got != "/1:new input fences send/2:not_dispatched:1" {
		t.Fatal(got)
	}
}

func TestRejectActionCannotRewriteEnteredUnknownOrReplacement(t *testing.T) {
	s := newTestSession(t)
	d := sessionDecide(t, s)
	id := sessionReserve(t, s, d)
	spoofed := sessionReceipt(id, ActionUnknown)
	spoofed.Receipt.NeverDispatched = true // Caller metadata cannot rewrite an entered reservation.
	sessionEnqueue(t, s, 1, spoofed)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectAction(d); !errors.Is(err, ErrFenced) {
		t.Fatal("entered action reclassified", err)
	}
	pending := s.Outstanding()
	if len(pending) != 1 || pending[0].Outcome != ActionUnknown || pending[0].NeverDispatched {
		t.Fatal(pending)
	}
	next := sessionDecide(t, s)
	foreign := newTestSession(t)
	if _, err := foreign.RejectAction(next); !errors.Is(err, ErrFenced) {
		t.Fatal("foreign rejection", err)
	}
	if _, err := s.Reload(context.Background(), sessionPolicyV1+"\n-- replacement", s.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectAction(next); !errors.Is(err, ErrFenced) {
		t.Fatal("old revision rejection", err)
	}
}

func TestRejectedReceiptSurvivesReloadWithoutNativePermission(t *testing.T) {
	s := newTestSession(t)
	d := sessionDecide(t, s)
	id, err := s.RejectAction(d)
	if err != nil {
		t.Fatal(err)
	}
	sessionEnqueue(t, s, 1, sessionReceipt(id, ActionNotDispatched))
	if _, err = s.Reload(context.Background(), sessionPolicyV1+"\n-- exact rejected receipt", s.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if len(s.Outstanding()) != 0 {
		t.Fatal(s.Outstanding())
	}
	if got := sessionDecide(t, s).Output["transcript"]; got != "/1:not_dispatched:1" {
		t.Fatal(got)
	}
	if _, err = s.ReserveAction(d); !errors.Is(err, ErrFenced) {
		t.Fatal("old rejected ticket survived reload", err)
	}
}
