package buddyactions

import (
	"context"
	"sync"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
)

type followTickContextRead struct {
	err       error
	remaining time.Duration
	bounded   bool
	deadline  time.Time
}
type followTickContextSession struct {
	sessionAPI
	observed   chan followTickContextRead
	onSnapshot func(context.Context)
}

func (x *followTickContextSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		deadline, bounded := ctx.Deadline()
		x.observed <- followTickContextRead{ctx.Err(), time.Until(deadline), bounded, deadline}
		if x.onSnapshot != nil {
			x.onSnapshot(ctx)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return x.sessionAPI.InvokeModule(ctx, r, opts...)
}
func TestFollowRunBudgetBeginsAfterWriterAdmission(t *testing.T) {
	s, f, _ := rig(t)
	toggle(t, s, true)
	x := &followTickContextSession{sessionAPI: f, observed: make(chan followTickContextRead, 16)}
	s.session = x
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	x.onSnapshot = func(context.Context) { <-release }
	var releaseOnce sync.Once
	releaseRead := func() { releaseOnce.Do(func() { close(release) }) }
	done := make(chan struct{})
	defer func() {
		cancel()
		releaseRead()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("probe worker did not exit")
		}
	}()
	// Model an already-admitted RPC holding the existing single Actions writer.
	// Run's first200ms tick then waits longer than its750ms work budget.
	s.mu.Lock()
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(1100 * time.Millisecond)
	s.mu.Unlock()
	var got followTickContextRead
	select {
	case got = <-x.observed:
	case <-time.After(2 * time.Second):
		t.Fatal("Run never reached World after admission")
	}
	cancel()
	releaseRead()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not release on cancellation")
	}
	if len(f.packets) != 0 {
		t.Fatal("cancelled probe dispatched gameplay", f.packets)
	}
	if got.err != nil || !got.bounded || got.remaining <= 0 || got.remaining > 751*time.Millisecond {
		t.Fatalf("new internal tick spent its work budget waiting for the writer: err=%v remaining=%s bounded=%v", got.err, got.remaining, got.bounded)
	}
}

func TestFollowRunCanceledParentCannotStartFreshTickAfterQueue(t *testing.T) {
	s, f, _ := rig(t)
	x := &followTickContextSession{sessionAPI: f, observed: make(chan followTickContextRead, 16)}
	s.session = x
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	s.mu.Lock()
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(250 * time.Millisecond)
	cancel()
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Run did not exit")
	}
	if len(x.observed) != 0 || len(f.packets) != 0 {
		t.Fatal("cancelled queued tick read or wrote", len(x.observed), f.packets)
	}
}

func TestFollowExplicitTickRetainsCallerDeadlineAcrossQueue(t *testing.T) {
	s, f, _ := rig(t)
	x := &followTickContextSession{sessionAPI: f, observed: make(chan followTickContextRead, 16)}
	s.session = x
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	s.mu.Lock()
	go func() { s.Tick(ctx); close(done) }()
	<-ctx.Done()
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("explicit Tick did not exit")
	}
	select {
	case got := <-x.observed:
		if got.err != context.DeadlineExceeded || got.remaining > 0 {
			t.Fatal("caller deadline refreshed", got)
		}
	default:
		t.Fatal("caller context not observed")
	}
	if len(f.packets) != 0 {
		t.Fatal("expired explicit Tick wrote", f.packets)
	}
}

func TestFollowRunSlowWorldStillUsesExistingWorkDeadline(t *testing.T) {
	s, f, _ := rig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := &followTickContextSession{sessionAPI: f, observed: make(chan followTickContextRead, 16), onSnapshot: func(call context.Context) { <-call.Done(); cancel() }}
	s.session = x
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("slow World escaped bounded tick")
	}
	got := <-x.observed
	if !got.bounded || got.remaining <= 0 || got.remaining > 751*time.Millisecond || s.state.Reason != "observations_unavailable" || len(f.packets) != 0 {
		t.Fatal("slow World became movement authority", got, s.stateLocked(), f.packets)
	}
}

func TestFollowRunPreliminaryReadsShareSameRemainingBudget(t *testing.T) {
	s, f, _ := rig(t)
	s.hostEncounterKey = testEncounterKey
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	x := &followTickContextSession{sessionAPI: f, observed: make(chan followTickContextRead, 16), onSnapshot: func(call context.Context) {
		calls++
		if calls == 1 {
			<-call.Done()
		} else {
			cancel()
		}
	}}
	s.session = x
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("preliminary read escaped bounded tick")
	}
	first, second := <-x.observed, <-x.observed
	delta := second.deadline.Sub(first.deadline)
	if !first.bounded || !second.bounded || first.remaining > 151*time.Millisecond || delta <= 0 || delta > 601*time.Millisecond || len(f.packets) != 0 {
		t.Fatal("read work acquired a new budget or wrote", first, second, delta, f.packets)
	}
}
