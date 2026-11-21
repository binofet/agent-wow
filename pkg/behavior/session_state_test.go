package behavior

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const inspectableSessionPolicy = `local state={decisions=0,seen="0",task={phase="waiting"},padding=""}
return {
 on_event=function(event)state.seen=event.sequence;return true end,
 decide=function(frame)
  state.decisions=state.decisions+1
  state.padding=frame.input.padding or state.padding
  return {decisions=state.decisions,seen=state.seen,task=state.task,clock=frame.clock,
          queue={{guid="123",scope="active"}}}
 end,
 export_state=function()return {version=1,data=state} end,
 import_state=function(checkpoint)state=checkpoint.data;return true end
}`

func TestSessionStateDetachedCheckpointDecisionAndReload(t *testing.T) {
	s := newTestSessionWithPolicy(t, inspectableSessionPolicy)
	read := func() SessionState {
		t.Helper()
		view, err := s.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	if view := read(); view.LatestDecision != nil || view.Status.Cursor != 0 {
		t.Fatal("invented an initial decision", view)
	}
	padding := strings.Repeat("x", 128*1024)
	decision, err := s.Decide(context.Background(), map[string]any{"padding": padding})
	if err != nil {
		t.Fatal(err)
	}
	decision.Output["task"].(map[string]any)["phase"] = "caller mutation"
	view := read()
	data := view.Checkpoint["data"].(map[string]any)
	if data["padding"] != padding || view.LatestDecision.Output["task"].(map[string]any)["phase"] != "waiting" ||
		view.LatestDecision.Revision != view.Status.Revision || view.LatestDecision.Generation != "1" ||
		view.LatestDecision.Cursor != "0" || view.LatestDecision.Clock.MonotonicMs > view.Clock.MonotonicMs {
		t.Fatal("state read truncated or mixed policy data", view.Status)
	}
	data["task"].(map[string]any)["phase"] = "checkpoint mutation"
	view.LatestDecision.Output["queue"].([]any)[0].(map[string]any)["guid"] = "reader mutation"
	if again := read(); again.Checkpoint["data"].(map[string]any)["task"].(map[string]any)["phase"] != "waiting" ||
		again.LatestDecision.Output["queue"].([]any)[0].(map[string]any)["guid"] != "123" {
		t.Fatal("read exposed mutable committed state")
	}
	sessionEnqueue(t, s, 1, sessionObservation("new observation"))
	if queued := read(); queued.Status.Cursor != 0 || queued.Status.Buffered != 1 {
		t.Fatal("read executed or drained queued Lua events", queued.Status)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if drained := read(); drained.Status.Cursor != 1 || drained.Checkpoint["data"].(map[string]any)["seen"] != "1" ||
		drained.LatestDecision.Cursor != "0" {
		t.Fatal("checkpoint and decision cursor provenance were lost", drained)
	}
	old := s.Status().Revision
	if _, err := s.Reload(context.Background(), "return {}", old); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if read().LatestDecision == nil {
		t.Fatal("rejected reload lost the committed decision")
	}
	if _, err := s.Reload(context.Background(), inspectableSessionPolicy+"\n-- next revision", old); err != nil {
		t.Fatal(err)
	}
	if reloaded := read(); reloaded.Status.Revision == old || reloaded.LatestDecision != nil ||
		reloaded.Checkpoint["data"].(map[string]any)["padding"] != padding {
		t.Fatal("reload mixed old decision with new revision or lost checkpoint", reloaded.Status)
	}
	s.Close()
	if _, err := s.State(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
}

func TestSessionStateConcurrentPublicationIsConsistent(t *testing.T) {
	s, err := NewSessionWithOptions(context.Background(), "state-race", inspectableSessionPolicy, SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errorsFound := make(chan error, 4)
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for ctx.Err() == nil {
				view, err := s.State(ctx)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						errorsFound <- err
					}
					return
				}
				data := view.Checkpoint["data"].(map[string]any)
				if data["seen"] != fmt.Sprint(view.Status.Cursor) {
					errorsFound <- fmt.Errorf("checkpoint/cursor torn: %v %+v", data, view.Status)
					return
				}
				if latest := view.LatestDecision; latest != nil {
					if latest.Revision != view.Status.Revision || latest.Output["seen"] != latest.Cursor || latest.Output["decisions"] != data["decisions"] {
						errorsFound <- fmt.Errorf("decision/checkpoint torn: %+v %+v", latest, view.Status)
						return
					}
				}
			}
		})
	}
	for index := 1; index <= 40; index++ {
		if _, err := s.Enqueue(sessionObservation("update")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Decide(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if index%5 == 0 {
			if _, err := s.Reload(context.Background(), inspectableSessionPolicy+fmt.Sprint("\n-- revision ", index), s.Status().Revision); err != nil {
				t.Fatal(err)
			}
		}
	}
	cancel()
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}
