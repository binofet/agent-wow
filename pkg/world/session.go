package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-wow/agent-wow/pkg/modules/runtime"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/worldconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Session has one reader and one owner loop. Only the loop writes packets and
// changes protocol state; RPC callers read snapshots or submit commands.
type Session struct {
	conn         *worldconn.Conn
	logger       *slog.Logger
	mu           sync.RWMutex
	state        State
	err          error
	done         chan struct{}
	abort        chan struct{}
	closeOnce    sync.Once
	logout       chan chan *logoutOperation
	modules      modrt.Runtime
	started      time.Time
	loginStarted atomic.Bool
	startLogin   chan struct{}
	startupDone  chan struct{}
	closed       chan struct{}
	writes       chan writeRequest
	ctx          context.Context
	cancel       context.CancelFunc
	workers      sync.WaitGroup
	cleanupErr   error
}

// Options injects a session-owned module runtime and separate startup budgets.
// Zero values retain the existing connection-only behavior.
type Options struct {
	Modules       modrt.Runtime
	ModuleTimeout time.Duration
	EntryTimeout  time.Duration
}
type writeRequest struct {
	ctx   context.Context
	op    uint32
	body  []byte
	reply chan error
}

type logoutOperation struct {
	done chan struct{}
	err  error
}
type packet struct {
	opcode uint16
	body   []byte
	err    error
}

// Enter consumes an authenticated transport from worldconn.Dial, including on
// failure. The caller must stop using conn once it is passed to Enter. Applications
// normally use char.Client.EnterWorld, which verifies character ownership first.
// The context controls entry only. Close or Logout ends a successful session.
// Successful packet reads and writes use logger at debug level, including opcode
// and payload size but never payload data. A nil logger disables logging.
func Enter(ctx context.Context, conn *worldconn.Conn, realm Realm, character Character, logger *slog.Logger, options ...Options) (*Session, error) {
	return enter(ctx, conn, realm, character, logger, defaultTimings, options...)
}

func enter(ctx context.Context, conn *worldconn.Conn, realm Realm, character Character, logger *slog.Logger, timing timings, options ...Options) (*Session, error) {
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if character.GUID == 0 {
		_ = conn.Close()
		return nil, errors.New("character GUID must not be zero")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var opts Options
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.ModuleTimeout == 0 {
		opts.ModuleTimeout = 2 * time.Minute
	}
	if opts.Modules == nil {
		opts.Modules = modrt.New(nil, nil, logger)
	}
	lifetime, stop := context.WithCancel(context.Background())
	s := &Session{conn: conn, logger: logger, state: State{Status: EnteringWorld, Realm: realm, Character: character}, done: make(chan struct{}), abort: make(chan struct{}), logout: make(chan chan *logoutOperation), modules: opts.Modules, started: time.Now(), startLogin: make(chan struct{}), startupDone: make(chan struct{}), closed: make(chan struct{}), writes: make(chan writeRequest), ctx: lifetime, cancel: stop}
	ready := make(chan struct{})
	go s.run(ready, timing)
	startup, endStartup := context.WithTimeout(ctx, opts.ModuleTimeout)
	stopStartup := context.AfterFunc(lifetime, endStartup)
	err := s.modules.Start(startup, modsession.Session{Identity: modsession.Identity{CharacterGUID: character.GUID, CharacterName: character.Name, RealmID: realm.ID, RealmName: realm.Name}, SendPacket: s.SendPacket, Clock: s.Clock})
	endStartup()
	stopStartup()
	close(s.startupDone)
	if err != nil {
		_ = s.Close()
		return nil, errors.Join(fmt.Errorf("start modules: %w", err), s.cleanupErr)
	}
	if opts.EntryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.EntryTimeout)
		defer cancel()
	}
	close(s.startLogin)
	select {
	case <-ctx.Done():
		_ = s.Close()
		return nil, ctx.Err()
	case <-s.done:
		_ = s.Close()
		return nil, errors.Join(s.entryError(), s.cleanupErr)
	case <-ready:
		if err := ctx.Err(); err != nil {
			_ = s.Close()
			return nil, err
		}
		select {
		case <-s.done:
			_ = s.Close()
			return nil, errors.Join(s.entryError(), s.cleanupErr)
		default:
		}
		return s, nil
	}
}

func (s *Session) entryError() error {
	err := s.Err()
	if err == nil {
		err = errors.New("server logged the character out during entry")
	}
	return fmt.Errorf("enter world: %w", err)
}

func (s *Session) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Clock is shared with module callbacks and core time synchronization.
func (s *Session) Clock() uint32 { return uint32(time.Since(s.started) / time.Millisecond) }

func (s *Session) InvokeJSON(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	return s.modules.InvokeJSON(ctx, method, params)
}

// SendPacket queues one opaque gameplay payload on the transport owner loop.
// Once the write starts its outcome can be unknown to a canceled caller; callers
// must never retry it automatically.
func (s *Session) SendPacket(ctx context.Context, op uint32, body []byte) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	name := opcode.WorldName(op)
	if !(strings.HasPrefix(name, "CMSG_") || strings.HasPrefix(name, "MSG_")) {
		return status.Error(codes.InvalidArgument, "opcode must be client or bidirectional")
	}
	switch op {
	case opcode.CMSGAuthSession, opcode.CMSGAuthSRP6Begin, opcode.CMSGAuthSRP6Proof, opcode.CMSGAuthSRP6Recode, opcode.CMSGRedirectionAuthProof,
		opcode.CMSGPlayerLogin, opcode.CMSGCheatPlayerLogin, opcode.CMSGCheckLoginCriteria,
		opcode.CMSGPing, opcode.CMSGKeepAlive, opcode.CMSGTimeSyncResp, opcode.CMSGWardenData,
		opcode.MSGMoveTeleportAck, opcode.MSGMoveWorldportAck,
		opcode.CMSGPlayerLogout, opcode.CMSGLogoutRequest, opcode.CMSGLogoutCancel:
		return status.Error(codes.PermissionDenied, "opcode is owned by the session")
	}
	if len(body)+4 >= 10240 {
		return status.Error(codes.InvalidArgument, "outgoing realm packet is too large")
	}
	if !s.loginStarted.Load() {
		return status.Error(codes.FailedPrecondition, "player login has not begun")
	}
	r := writeRequest{ctx: ctx, op: op, body: append([]byte(nil), body...), reply: make(chan error, 1)}
	select {
	case <-s.done:
		return status.Error(codes.FailedPrecondition, "session is closed")
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case s.writes <- r:
	}
	select {
	case err := <-r.reply:
		return err
	case <-s.done:
		return status.Error(codes.FailedPrecondition, "session is closed")
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

func (s *Session) Done() <-chan struct{} { return s.done }

// Err is meaningful after Done closes. A successful server logout returns nil.
func (s *Session) Err() error { s.mu.RLock(); defer s.mu.RUnlock(); return s.err }

// Close interrupts I/O and joins both session goroutines. It does not claim the
// server logged the character out; use Logout when graceful logout is required.
func (s *Session) Close() error {
	s.closeOnce.Do(func() { close(s.abort); _ = s.conn.Close() })
	<-s.done
	<-s.closed
	return s.cleanupErr
}

// Logout shares an outstanding logout operation. ctx cancels only this caller's
// wait; once submitted, the operation has its own 30-second lifetime.
func (s *Session) Logout(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reply := make(chan *logoutOperation, 1)
	select {
	case <-s.done:
		return s.Err()
	case <-ctx.Done():
		return ctx.Err()
	case s.logout <- reply:
	}
	var op *logoutOperation
	select {
	case op = <-reply:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.Err()
	}
	select {
	case <-op.done:
		return op.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) run(ready chan struct{}, timing timings) {
	packets := make(chan packet, 1) // only one bounded payload queued ahead of the owner
	readerStop, readerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			op, body, err := s.conn.ReadPacket()
			if err == nil {
				logPacket(s.logger, "read", uint32(op), len(body))
			}
			select {
			case packets <- packet{op, body, err}:
			case <-readerStop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	e := &engine{s: s, timing: timing, ready: ready, phase: EnteringWorld, pings: make(map[uint32]time.Time)}
	err := e.loop(packets)
	s.modules.Stop()
	s.cancel()
	close(readerStop)
	_ = s.conn.Close()
	<-readerDone
	if e.logoutTimer != nil {
		e.logoutTimer.Stop()
	}
	if e.pongTimer != nil {
		e.pongTimer.Stop()
	}
	s.mu.Lock()
	s.err = err
	s.state.Status = Closed
	if err != nil {
		s.state.Error = err.Error()
		if !errors.Is(err, ErrClosed) {
			s.state.Status = Failed
		}
	}
	s.mu.Unlock()
	if e.logout != nil {
		e.logout.err = err
		close(e.logout.done)
	}
	close(s.done)
	<-s.startupDone
	s.workers.Wait()
	s.cleanupErr = s.modules.Close()
	close(s.closed)
}
