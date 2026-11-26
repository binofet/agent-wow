package modrt

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestObservedPolicySlowLegalPrefixStillDecidesCapturedFeed(t *testing.T) {
	const source = `local s={events=0,work=0};return {
  on_event=function(e)
   local n=0;for i=1,10000 do n=n+i end
   s.work=n;s.events=s.events+1;return true
  end,
  decide=function(f)return {events=s.events,cursor=f.cursor,observationSequence=f.input.observationSequence}end,
  export_state=function()return {version=1,data=s}end,
  import_state=function(c)s=c.data;return true end
 }`
	// The supported actor profile is 250 ms per hook. Work is finite, with no
	// sleeping/clock-driven Lua and no larger product/runtime default budget.
	r := newMovementPointsActorRig(t, source, "4", behavior.SessionOptions{ExecutionLimit: 250 * time.Millisecond})
	for i := 0; i < 200; i++ {
		if _, err := r.host.Enqueue(behavior.Event{Kind: "retained_observation", Value: map[string]any{"label": strconv.Itoa(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	if err := r.host.stepObservation(ctx, "points", []policyFeed{{phase: "encounter", method: encounterObservationMethod}}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed <= 500*time.Millisecond {
		t.Fatalf("fixture did not cross the scheduling slice: %s", elapsed)
	}
	state, err := r.host.vm.State(ctx)
	if err != nil || state.Status.Fault != "" || state.Status.Cursor != 201 || state.Status.Buffered != 0 || state.LatestDecision == nil {
		t.Fatal(state, err)
	}
	out := state.LatestDecision.Output
	if out["events"] != float64(201) || out["cursor"] != "201" || out["observationSequence"] != "201" {
		t.Fatal("decision did not cover original feed prefix", out)
	}
	r.mu.Lock()
	reads := r.reads
	r.mu.Unlock()
	if reads != 1 {
		t.Fatal("catch-up collected replacement observations", reads)
	}
}

func TestMovementPointsAdvisoryDoesNotReadWhileOrdinaryVMIsBusy(t *testing.T) {
	r := newMovementPointsRig(t, movementPointsTestSource)
	r.host.advance.Lock()
	done := make(chan error, 1)
	go func() { done <- r.host.stepMovementPointsObservation(context.Background(), "points") }()
	select {
	case err := <-done:
		r.host.advance.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		r.host.advance.Unlock()
		<-done
		t.Fatal("advisory waited behind ordinary VM without an entered dispatch")
	}
	r.mu.Lock()
	reads := r.reads
	r.mu.Unlock()
	if reads != 0 {
		t.Fatal("advisory polled a large frame without an external dispatch", reads)
	}
	// A registered dispatch can still be recording its receipt after Send
	// returns. That bookkeeping phase must not start another auxiliary read.
	r.host.advance.Lock()
	r.host.dispatchDone = make(chan struct{})
	r.host.advance.Unlock()
	defer func() { r.host.advance.Lock(); r.host.dispatchDone = nil; r.host.advance.Unlock() }()
	if err := r.host.stepMovementPointsObservation(context.Background(), "points"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	reads = r.reads
	r.mu.Unlock()
	if reads != 0 {
		t.Fatal("idle advisory polled without an external dispatch", reads)
	}
}
