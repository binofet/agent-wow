package world

import (
	"bytes"
	"context"
	"encoding/binary"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/worldconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewMovementCanceledCallerHasLaterKnownWrite(t *testing.T) {
	body := ownReportPacket(t, 1)
	trace := NewGameplayTrace(1)
	readPrefix, resume := make(chan struct{}), make(chan struct{})
	conn := peerConnection(t, func(peer *testPeer) {
		peer.login()
		var prefix [2]byte
		if _, err := io.ReadFull(peer.conn, prefix[:]); err != nil {
			t.Error(err)
		}
		close(readPrefix)
		<-resume
		remainder := make([]byte, len(body)+4)
		if _, err := io.ReadFull(peer.conn, remainder); err != nil {
			t.Error(err)
		}
		peer.closed()
	})
	timing := quietTimings()
	timing.write = 2 * time.Second
	s := enterTracedTestSession(t, conn, timing, 99, trace)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := strings.Repeat("a", 64)
	ctx = modsession.WithWriteOrigin(ctx, modsession.WriteOrigin{Module: "buddy-actions", Writer: "module", MovementAttempt: id})
	var release sync.Once
	releaseWrite := func() { release.Do(func() { close(resume) }) }
	t.Cleanup(releaseWrite)
	returned := make(chan error, 1)
	go func() {
		returned <- s.SendPacket(ctx, opcode.MSGMoveStartForward, body)
	}()
	select {
	case <-readPrefix:
	case err := <-returned:
		t.Fatalf("movement never entered fixture write: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("movement fixture write did not begin")
	}
	cancel()
	err := <-returned
	releaseWrite()
	if status.Code(err) != codes.Canceled {
		t.Fatalf("caller should have canceled while write was in flight: %v", err)
	}
	// An already canceled queued request provides a barrier after the first
	// attempt on the owner loop. It never reaches the transport or the trace.
	reply := make(chan error, 1)
	select {
	case s.writes <- writeRequest{ctx: ctx, op: opcode.CMSGAttackswing, reply: reply}:
	case <-s.Done():
		t.Fatal(s.Err())
	}
	if err := <-reply; status.Code(err) != codes.Canceled {
		t.Fatalf("queued cancellation: %v", err)
	}
	got := trace.Snapshot()
	if len(got.Attempts) != 1 || got.Dropped != 0 ||
		got.Attempts[0].Outcome != GameplayWritten || !got.Attempts[0].AnyBytes {
		t.Fatalf("in-flight movement write lost to caller cancellation: %+v", got)
	}
	if r := s.OwnMovementAttempt(id); r == nil || r.Opcode != opcode.MSGMoveStartForward || !bytes.Equal(r.Payload, body) || got.Attempts[0].Origin.MovementAttempt != id {
		t.Fatal("queued canceled caller lost exact successful movement result", r, got)
	}
}

type reviewLateWriteReturn struct {
	net.Conn
	written, resume chan struct{}
}

func (c *reviewLateWriteReturn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && len(p) >= 6 && binary.LittleEndian.Uint32(p[2:6]) == opcode.MSGMoveStop {
		close(c.written)
		<-c.resume
	}
	return n, err
}
func TestReviewMovementDeadlineReturnsBeforeKnownStopResult(t *testing.T) {
	left, right := net.Pipe()
	wrapped := &reviewLateWriteReturn{Conn: left, written: make(chan struct{}), resume: make(chan struct{})}
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		defer right.Close()
		peer := &testPeer{t, right}
		peer.login()
		peer.expect(opcode.MSGMoveStop)
		peer.closed()
	}()
	t.Cleanup(func() { left.Close(); right.Close(); <-peerDone })
	trace := NewGameplayTrace(1)
	s := enterTracedTestSession(t, worldconn.New(wrapped), quietTimings(), 99, trace)
	// The deadline starts after login; the fixture delays only returning an
	// already completed socket write, never the actual kernel acceptance.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id := strings.Repeat("d", 64)
	ctx = modsession.WithWriteOrigin(ctx, modsession.WriteOrigin{Module: "buddy-actions", Writer: "module", MovementAttempt: id})
	var release sync.Once
	releaseWrite := func() { release.Do(func() { close(wrapped.resume) }) }
	t.Cleanup(releaseWrite)
	body := ownReportPacket(t, 0)
	returned := make(chan error, 1)
	go func() { returned <- s.SendPacket(ctx, opcode.MSGMoveStop, body) }()
	select {
	case <-wrapped.written:
	case err := <-returned:
		t.Fatalf("STOP never entered fixture write: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("STOP fixture write did not begin")
	}
	if err := <-returned; status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("caller: %v", err)
	}
	releaseWrite()
	reply := make(chan error, 1)
	select {
	case s.writes <- writeRequest{ctx: ctx, op: opcode.MSGMoveStop, reply: reply}:
	case <-s.Done():
		t.Fatal(s.Err())
	}
	<-reply
	got := trace.Snapshot()
	if len(got.Attempts) != 1 || got.Attempts[0].Outcome != GameplayWritten || !got.Attempts[0].AnyBytes {
		t.Fatalf("known STOP lost: %+v", got)
	}
	if r := s.OwnMovementAttempt(id); r == nil || r.Opcode != opcode.MSGMoveStop || !bytes.Equal(r.Payload, body) || got.Attempts[0].Origin.MovementAttempt != id || s.OwnMovementAttempt(strings.Repeat("e", 64)) != nil {
		t.Fatal("queued deadline caller lost exact successful STOP result", r, got)
	}
}
