package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/worldconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func enterTracedTestSession(t *testing.T, conn *worldconn.Conn, timing timings, guid uint64, trace *GameplayTrace) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := enter(ctx, conn, Realm{1, "Offline"}, Character{guid, "Test"}, nil, timing, Options{GameplayTrace: trace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGameplayTraceRecordsWireBytesAndWriterWithoutCoreTraffic(t *testing.T) {
	trace := NewGameplayTrace(4)
	timing := quietTimings()
	timing.ping = 400 * time.Millisecond
	const target = uint64(17379391151907274754)
	body := binary.LittleEndian.AppendUint64(nil, target)
	acked, heartbeat := make(chan struct{}), make(chan struct{})
	conn := peerConnection(t, func(peer *testPeer) {
		peer.login()
		peer.send(opcode.SMSGNewWorld, positionBody(389, 1, 2, 3, .5))
		if body := peer.expect(opcode.MSGMoveWorldportAck); len(body) != 0 {
			t.Error("nonempty worldport acknowledgment")
		}
		close(acked)
		for range 3 {
			if got := peer.expect(opcode.CMSGAttackswing); !bytes.Equal(got, body) {
				t.Errorf("wire attack payload %x, want %x", got, body)
			}
		}
		peer.expect(opcode.CMSGPing)
		close(heartbeat)
		peer.closed()
	})
	s := enterTracedTestSession(t, conn, timing, 99, trace)
	select {
	case <-acked:
	case <-s.Done():
		t.Fatal(s.Err())
	}
	origin := modsession.WriteOrigin{SessionID: "body", Module: "buddy-actions", Writer: "lua",
		Generation: 8, PolicyRevision: "r8", ActionID: "focus-1"}
	ctx := modsession.WithWriteOrigin(context.Background(), origin)
	if err := s.SendPacket(ctx, opcode.CMSGPing, nil); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("module attempted core ping: %v", err)
	}
	for _, call := range []context.Context{ctx, ctx, context.Background()} {
		if err := s.SendPacket(call, opcode.CMSGAttackswing, body); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-heartbeat:
	case <-s.Done():
		t.Fatal(s.Err())
	}
	got := trace.Snapshot()
	if got.Dropped != 0 || len(got.Attempts) != 3 {
		t.Fatalf("expected repeated tagged write and untagged write only: %+v", got)
	}
	for i, entry := range got.Attempts {
		wantOrigin := origin
		if i == 2 {
			wantOrigin = modsession.WriteOrigin{}
		}
		if entry.Sequence != uint64(i+1) || entry.CharacterGUID != 99 || entry.Opcode != opcode.CMSGAttackswing ||
			entry.PayloadHex != hex.EncodeToString(body) || entry.Outcome != GameplayWritten ||
			!entry.AnyBytes || entry.Origin != wantOrigin {
			t.Fatalf("attempt %d incorrect: %+v", i, entry)
		}
	}
	// A snapshot consumer cannot change the accumulated write history.
	got.Attempts[0] = GameplayWrite{}
	if entry := trace.Snapshot().Attempts[0]; entry.Origin != origin {
		t.Fatalf("snapshot aliased retained history: %+v", entry)
	}
}

func TestGameplayTraceReportsPartialFailure(t *testing.T) {
	trace := NewGameplayTrace(1)
	conn := peerConnection(t, func(peer *testPeer) {
		peer.login()
		// Close after reading only part of the requested frame.
		var prefix [2]byte
		if _, err := io.ReadFull(peer.conn, prefix[:]); err != nil {
			t.Error(err)
		}
	})
	s := enterTracedTestSession(t, conn, quietTimings(), 99, trace)
	err := s.SendPacket(context.Background(), opcode.CMSGAttackswing, binary.LittleEndian.AppendUint64(nil, 8))
	if err == nil {
		t.Fatal("a partial write returned success")
	}
	awaitDone(t, s)
	got := trace.Snapshot()
	if len(got.Attempts) != 1 || got.Attempts[0].Outcome != GameplayWriteFailed || !got.Attempts[0].AnyBytes {
		t.Fatalf("partial write lost: %+v", got)
	}
}

func TestGameplayTraceReportsWriteTimeoutWithoutBytes(t *testing.T) {
	trace := NewGameplayTrace(1)
	releasePeer := make(chan struct{})
	conn := peerConnection(t, func(peer *testPeer) {
		peer.login()
		// Keep the socket open without accepting any bytes until the deadline.
		<-releasePeer
		peer.closed()
	})
	defer close(releasePeer)
	s := enterTracedTestSession(t, conn, quietTimings(), 99, trace)
	if err := s.SendPacket(context.Background(), opcode.CMSGAttackstop, nil); err == nil {
		t.Fatal("blocked write returned success")
	}
	awaitDone(t, s)
	got := trace.Snapshot()
	if len(got.Attempts) != 1 || got.Attempts[0].Outcome != GameplayWriteFailed || got.Attempts[0].AnyBytes {
		t.Fatalf("write timeout lost: %+v", got)
	}
}

func TestGameplayTraceSeparatesCanceledCallerFromWireOutcome(t *testing.T) {
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
		var remainder [12]byte
		if _, err := io.ReadFull(peer.conn, remainder[:]); err != nil {
			t.Error(err)
		}
		peer.closed()
	})
	timing := quietTimings()
	timing.write = 2 * time.Second
	s := enterTracedTestSession(t, conn, timing, 99, trace)
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		returned <- s.SendPacket(ctx, opcode.CMSGAttackswing, binary.LittleEndian.AppendUint64(nil, 8))
	}()
	<-readPrefix
	cancel()
	err := <-returned
	close(resume)
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
		t.Fatalf("in-flight write lost to caller cancellation: %+v", got)
	}
}

func TestGameplayTraceOverflowDoesNotSuppressWrites(t *testing.T) {
	trace := NewGameplayTrace(1)
	conn := peerConnection(t, func(peer *testPeer) {
		peer.login()
		for range 2 {
			peer.expect(opcode.CMSGAttackstop)
		}
		peer.closed()
	})
	s := enterTracedTestSession(t, conn, quietTimings(), 99, trace)
	for range 2 {
		if err := s.SendPacket(context.Background(), opcode.CMSGAttackstop, nil); err != nil {
			t.Fatal(err)
		}
	}
	got := trace.Snapshot()
	if len(got.Attempts) != 1 || got.Dropped != 1 || got.Attempts[0].Sequence != 1 {
		t.Fatalf("capture gap missing: %+v", got)
	}
}

func TestGameplayTraceRejectsMalformedAndCanceledBeforeAdmission(t *testing.T) {
	trace := NewGameplayTrace(1)
	conn := peerConnection(t, func(peer *testPeer) {
		peer.login()
		peer.closed()
	})
	s := enterTracedTestSession(t, conn, quietTimings(), 99, trace)
	if err := s.SendPacket(context.Background(), opcode.SMSGNewWorld, nil); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.SendPacket(ctx, opcode.CMSGAttackstop, nil); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	if got := trace.Snapshot(); len(got.Attempts) != 0 || got.Dropped != 0 {
		t.Fatalf("admission failure appeared as a transport attempt: %+v", got)
	}
}
