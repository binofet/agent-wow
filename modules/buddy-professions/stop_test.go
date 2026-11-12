package buddyprofessions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func stopRequestFixture() *professionsv1.StopRequest {
	return &professionsv1.StopRequest{SessionId: "body", SelfGuid: 7}
}

func TestPrepareStopRequiresExactConnectedIdentityAndLiveCaller(t *testing.T) {
	for _, mode := range []string{"nil", "session", "character", "zero_character", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			r := stopRequestFixture()
			ctx := context.Background()
			want := codes.InvalidArgument
			switch mode {
			case "nil":
				r = nil
			case "session":
				r.SessionId = "other-session"
			case "character":
				r.SelfGuid = 8
			case "zero_character":
				r.SelfGuid = 0
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = codes.Canceled
			}
			s.mu.Lock()
			revision := s.state.Revision
			s.mu.Unlock()
			f.mu.Lock()
			calls, packets := len(f.rpcEvents), len(f.packets)
			f.mu.Unlock()
			if _, err := s.PrepareStop(ctx, r); status.Code(err) != want {
				t.Fatal(mode, err)
			}
			s.mu.Lock()
			if s.state.StopPrepared || s.closed || s.state.Revision != revision {
				t.Fatal("rejected stop changed module state", s.state)
			}
			s.mu.Unlock()
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.rpcEvents) != calls || len(f.packets) != packets || len(f.releases) != 0 {
				t.Fatal("stop validation reached a gameplay callback")
			}
		})
	}
}

func TestPrepareStopPersistentIdempotentLatchBlocksOnlyNewDisenchant(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	s.mu.Lock()
	before := s.state.Revision
	s.mu.Unlock()
	one, err := s.PrepareStop(context.Background(), stopRequestFixture())
	if err != nil || !one.StopPrepared || one.Closing || one.SelfGuid != 7 || one.SessionId != "body" || one.Revision != before+1 {
		t.Fatal(one, err)
	}
	two, err := s.PrepareStop(context.Background(), stopRequestFixture())
	if err != nil || !two.StopPrepared || two.Revision != one.Revision {
		t.Fatal("retry must preserve the same persistent latch", two, err)
	}
	// Even a fresh request with the post-latch revision cannot start DE.
	if _, err := s.Attempt(context.Background(), disenchantBehaviorRequest(s, "after-prepared-stop")); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("fresh revision bypassed the stop latch", err)
	}
	s.mu.Lock()
	de, _ := recipeForSpell(13262)
	ordinary, _ := recipeForSpell(3275)
	if s.readinessLocked(de, nil) != "normal_stop_prepared_no_new_disenchant_this_session" || s.readinessLocked(ordinary, nil) != "" || s.active != nil || len(s.requests) != 0 || s.closed {
		t.Fatal("stop latch changed ordinary practice or allowed a DE reservation")
	}
	s.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.packets) != 0 || len(f.releases) != 0 {
		t.Fatal("stop latch or rejected DE sent a gameplay packet", f.packets)
	}
}

func TestPrepareStopRefusesUnsettledDisenchantWithoutCancellation(t *testing.T) {
	for _, mode := range []string{"reserved", "active_window", "pending", "last_cleanup", "last_outcome", "last_cast_unknown", "last_invalid_window"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			t.Cleanup(func() { s.mu.Lock(); s.active = nil; s.mu.Unlock() })
			s.mu.Lock()
			switch mode {
			case "active_window":
				op.castReserved = true
				op.receipt.OwnGoObserved = true
				op.window = &lootWindow{guid: disenchantBehaviorTarget, slots: []lootSlot{{slot: 0, entry: 10940, count: 2}}}
			case "pending":
				s.active = nil
				s.pendingDisenchant = op
				op.receipt.CleanupUnconfirmed = true
			case "last_cleanup":
				s.active = nil
				op.receipt.CleanupUnconfirmed = true
			case "last_outcome":
				s.active = nil
				op.receipt.Status = professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED
			case "last_cast_unknown":
				s.active = nil
				op.castReserved = true
			case "last_invalid_window":
				s.active = nil
				op.windowInvalid = true
			}
			s.mu.Unlock()
			if _, err := s.PrepareStop(context.Background(), stopRequestFixture()); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("unsafe DE was allowed to stop", mode, err)
			}
			s.mu.Lock()
			if s.state.StopPrepared || s.closed || op.receipt.CancelRequested || op.cancelReserved {
				t.Fatal("refusal latched or cancelled the unsafe operation", op.receipt)
			}
			s.mu.Unlock()
			select {
			case <-op.ctx.Done():
				t.Fatal("stop refusal cancelled the operation context")
			default:
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 0 || len(f.releases) != 0 {
				t.Fatal("refusal attempted cleanup", f.packets, f.releases)
			}
		})
	}
}

func TestPrepareStopAcceptsSettledDisenchantAndGenericActiveOperation(t *testing.T) {
	for _, mode := range []string{"completed_de", "rejected_de", "blocked_before_cast", "generic_active"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			t.Cleanup(func() { s.mu.Lock(); s.active = nil; s.mu.Unlock() })
			s.mu.Lock()
			if mode == "generic_active" {
				op.spec, _ = recipeForSpell(3275)
			} else {
				s.active = nil
				op.receipt.LeaseReleased = true
				switch mode {
				case "completed_de":
					op.castReserved = true
					op.receipt.Status = professionsv1.AttemptStatus_COMPLETED
				case "rejected_de":
					op.castReserved = true
					op.receipt.Status = professionsv1.AttemptStatus_SERVER_REJECTED
					op.receipt.FailureCode = 121
				case "blocked_before_cast":
					op.receipt.Status = professionsv1.AttemptStatus_BLOCKED
				}
			}
			s.mu.Unlock()
			state, err := s.PrepareStop(context.Background(), stopRequestFixture())
			if err != nil || !state.StopPrepared || state.Closing || state.Active != (mode == "generic_active") || op.receipt.CancelRequested {
				t.Fatal(mode, state, err)
			}
			select {
			case <-op.ctx.Done():
				t.Fatal("preparation cancelled an existing generic operation")
			default:
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 0 || len(f.releases) != 0 {
				t.Fatal("preparation changed normal cleanup behavior", f.packets)
			}
		})
	}
}

func TestPrepareStopWinsAgainstDisenchantStillObservingBeforeReservation(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	r := disenchantBehaviorRequest(s, "racing-stop-first")
	entered, resume := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.worldHook = func(_ *worldv1.SnapshotRequest) { close(entered); <-resume }
	f.mu.Unlock()
	result := make(chan error, 1)
	go func() { _, err := s.Attempt(context.Background(), r); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(resume)
		t.Fatal("attempt did not reach the pre-reservation observation")
	}
	state, err := s.PrepareStop(context.Background(), stopRequestFixture())
	close(resume)
	if err != nil || !state.StopPrepared {
		t.Fatal(state, err)
	}
	select {
	case err := <-result:
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatal("preflight attempt crossed the atomic latch", err)
		}
	case <-time.After(time.Second):
		t.Fatal("racing attempt did not return")
	}
	s.mu.Lock()
	if s.active != nil || len(s.requests) != 0 {
		t.Fatal("attempt reserved after stop won")
	}
	s.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.packets) != 0 {
		t.Fatal("attempt wrote after stop won", f.packets)
	}
}

func TestPrepareStopLosesToReservedDisenchantWithoutCancellingIt(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	entered, resume := make(chan struct{}), make(chan struct{})
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		if p.Opcode != opcode.CMSGCastSpell {
			t.Errorf("unexpected cleanup packet%x", p.Opcode)
			return
		}
		close(entered)
		<-resume
		s.mu.Lock()
		count := byte(s.active.receipt.CastCount)
		s.mu.Unlock()
		failed := binary.LittleEndian.AppendUint32([]byte{count}, 13262)
		professionPacket(t, s, opcode.SMSGCastFailed, append(failed, 121))
	}
	if _, err := s.Attempt(context.Background(), disenchantBehaviorRequest(s, "racing-attempt-first")); err != nil {
		close(resume)
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(resume)
		t.Fatal("attempt did not reserve its cast")
	}
	_, err := s.PrepareStop(context.Background(), stopRequestFixture())
	s.mu.Lock()
	op := s.active
	unsafeChange := s.state.StopPrepared || s.closed || op == nil || op.receipt.CancelRequested
	s.mu.Unlock()
	close(resume)
	if status.Code(err) != codes.FailedPrecondition || unsafeChange {
		t.Fatal("stop overtook an already reserved DE", err)
	}
	professionWaitDone(t, op)
	if op.receipt.Status != professionsv1.AttemptStatus_SERVER_REJECTED || op.receipt.CancelRequested || op.receipt.CleanupUnconfirmed {
		t.Fatal("stop refusal disturbed the actual cast result", op.receipt)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell {
		t.Fatal("stop refusal injected a cancel or window packet", f.packets)
	}
}
