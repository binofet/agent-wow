package modrt

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const attachIdentity = `{"sessionId":"aw-4","characterGuid":"17379390969991956137"}`

func TestAttachPinsIdentityAndReadsCurrentPolicy(t *testing.T) {
	m := catalogManager(t, 17379390969991956137, "aw-4")
	m.started = false
	policy := PolicySnapshot{Revision: "first", Generation: "1", StateVersion: 2, PendingActionIDs: []string{"cast-1"}}
	reads := 0
	err := m.BindPolicyObserver(func(_ context.Context, sid, guid string) (PolicySnapshot, error) {
		reads++
		if sid != "aw-4" || guid != "17379390969991956137" {
			t.Fatalf("observer called for %q %q", sid, guid)
		}
		return policy, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.started = true
	for _, params := range []string{`{"sessionId":"aw-old","characterGuid":"17379390969991956137"}`, `{"sessionId":"aw-4","characterGuid":"4"}`} {
		_, err := m.InvokeJSON(context.Background(), "session.attach", json.RawMessage(params))
		if status.Code(err) != codes.FailedPrecondition || reads != 0 {
			t.Fatalf("wrong body reached observer: reads=%d err=%v", reads, err)
		}
	}
	for _, rev := range []string{"first", "second"} {
		policy.Revision = rev
		body, err := m.InvokeJSON(context.Background(), "session.attach", json.RawMessage(attachIdentity))
		if err != nil {
			t.Fatal(err)
		}
		var got attachment
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.SessionID != "aw-4" || got.CharacterGUID != "17379390969991956137" || len(got.Methods) != 1 || got.Policy == nil || got.Policy.Revision != rev || len(got.Policy.PendingActionIDs) != 1 || got.Policy.PendingActionIDs[0] != "cast-1" {
			t.Fatalf("unexpected attachment: %s", body)
		}
	}
}

func TestAttachRejectsSessionClosingDuringObservation(t *testing.T) {
	m := catalogManager(t, 17379390969991956137, "aw-4")
	m.started = false
	entered, release := make(chan struct{}), make(chan struct{})
	if err := m.BindPolicyObserver(func(context.Context, string, string) (PolicySnapshot, error) {
		close(entered)
		<-release
		return PolicySnapshot{Revision: "finished", Generation: "1"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	m.started = true
	finished := make(chan error, 1)
	go func() {
		_, err := m.InvokeJSON(context.Background(), "session.attach", json.RawMessage(attachIdentity))
		finished <- err
	}()
	<-entered
	m.BeginLogout()
	close(release)
	if err := <-finished; status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("policy observation escaped the logout boundary: %v", err)
	}
}
