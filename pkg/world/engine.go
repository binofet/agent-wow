package world

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/status"
)

type timings struct{ ping, pong, logout, write time.Duration }

var defaultTimings = timings{30 * time.Second, 90 * time.Second, 30 * time.Second, 10 * time.Second}

// engine is private to the owner loop; none of its fields require locks.
type engine struct {
	s                     *Session
	timing                timings
	ready                 chan struct{}
	phase                 Status
	verified, synced      bool
	logout                *logoutOperation
	logoutTimer           *time.Timer
	logoutDeadline        <-chan time.Time
	preparation           <-chan error
	finalPackets          <-chan error
	pongTimer             *time.Timer
	pingSequence, latency uint32
	pings                 map[uint32]time.Time
	movement              movementControl
	transport             transportScopeState
	writeSerial           uint64
	beforeScopedWrite     func() error
	ownMovementReport     *modv1.WorldPacket
	ownMovementAttempt    string
}

func (e *engine) write(op uint32, body []byte) error {
	_, _, err := e.writeContext(context.Background(), op, body)
	return err
}

func (e *engine) writeContext(ctx context.Context, op uint32, body []byte, scopes ...*modv1.TransportScope) (GameplayWriteOutcome, bool, error) {
	if ownMovementReportOpcode(op) {
		// A failed or unsupported new write must not leave the prior report
		// available as the most recent known own movement during transport.
		e.ownMovementReport = nil
		e.ownMovementAttempt = ""
		e.publishTransportScope()
	}
	deadline := time.Now().Add(e.timing.write)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if err := e.s.conn.SetWriteDeadline(deadline); err != nil {
		return GameplayDeadlineFailed, false, err
	}
	var requested *modv1.TransportScope
	if len(scopes) != 0 {
		requested = scopes[0]
	}
	if requested != nil || op == opcode.CMSGMoveSplineDone {
		for {
			if !e.scopedWriteAvailable(ctx) {
				return "", false, modcb.PacketNotDispatchedError("scoped gameplay write requires the uncanceled current live world session")
			}
			serial := e.writeSerial
			if e.beforeScopedWrite != nil {
				if err := e.beforeScopedWrite(); err != nil {
					return "", false, err
				}
			}
			if serial == e.writeSerial {
				break
			}
			// A protocol ACK issued while processing a ready inbound packet
			// changed the socket deadline. Restore the caller's deadline and
			// drain again before the final reservation; no callback follows it.
			if err := e.s.conn.SetWriteDeadline(deadline); err != nil {
				return GameplayDeadlineFailed, false, err
			}
		}
		if !e.scopedWriteAvailable(ctx) {
			return "", false, modcb.PacketNotDispatchedError("scoped gameplay write requires the uncanceled current live world session")
		}
	}
	// No callback, deadline setter or owner-state transition follows the final
	// scope comparison and attempt reservation before entering WritePacket.
	if err := e.transport.reserve(op, body, requested); err != nil {
		return "", false, err
	}
	e.writeSerial++
	anyBytes, err := e.s.conn.WritePacket(op, body)
	if err != nil {
		return GameplayWriteFailed, anyBytes, err
	}
	e.movement.sent(op, body, e.s.state.Character.GUID)
	e.transport.sent(op)
	e.retainOwnMovementReport(op, body)
	if ownMovementReportOpcode(op) {
		e.ownMovementAttempt = modsession.WriteOriginFromContext(ctx).MovementAttempt
	}
	e.publishTransportScope()
	logPacket(e.s.logger, "write", op, len(body))
	return GameplayWritten, anyBytes, nil
}

func (e *engine) scopedWriteAvailable(ctx context.Context) bool {
	if ctx.Err() != nil || e.phase != InWorld || e.logout != nil || e.finalPackets != nil || e.s == nil ||
		e.transport.scope == nil || e.s.state.Character.GUID != e.transport.scope.SelfGuid || e.s.ctx != nil && e.s.ctx.Err() != nil {
		return false
	}
	select {
	case <-e.s.abort:
		return false
	default:
		return true
	}
}

func (e *engine) timestamp() uint32 { return e.s.Clock() }

func (e *engine) publish() {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	e.s.state.Status = e.phase
	if e.logout != nil {
		e.s.state.Status = LoggingOut
	}

}

func (e *engine) checkReady() {
	if !e.verified || !e.synced {
		return
	}
	e.phase = InWorld
	e.publish()
	if e.ready != nil {
		close(e.ready)
		e.ready = nil
	}
}

func (e *engine) loop(packets <-chan packet) error {
	login := (<-chan struct{})(e.s.startLogin)
	ping := time.NewTimer(e.timing.ping)
	defer ping.Stop()
	e.pongTimer = time.NewTimer(e.timing.pong)
	for {
		select {
		case <-e.s.abort:
			return ErrClosed
		case <-e.s.modules.Done():
			return e.s.modules.Err()
		case <-login:
			if err := e.write(opcode.CMSGPlayerLogin, binary.LittleEndian.AppendUint64(nil, e.s.state.Character.GUID)); err != nil {
				return err
			}
			e.s.loginStarted.Store(true)
			login = nil
		case r := <-e.s.writes:
			if err := r.ctx.Err(); err != nil {
				r.reply <- status.FromContextError(err).Err()
				continue
			}
			if (r.transportScope != nil || r.op == opcode.CMSGMoveSplineDone) && (e.phase != InWorld || e.logout != nil || e.finalPackets != nil) {
				r.reply <- modcb.PacketNotDispatchedError("scoped gameplay write requires the current live world session")
				continue
			}
			if r.transportScope != nil || r.op == opcode.CMSGMoveSplineDone {
				e.beforeScopedWrite = func() error {
					for packets != nil {
						select {
						case p := <-packets:
							complete, err := e.processIncoming(p)
							if err != nil {
								return err
							}
							if complete {
								packets = nil
							}
						default:
							return nil
						}
					}
					return nil
				}
			}
			outcome, anyBytes, err := e.writeContext(r.ctx, r.op, r.body, r.transportScope)
			e.beforeScopedWrite = nil
			if modcb.PacketWasNotDispatched(err) {
				r.reply <- err
				continue
			}
			if trace := e.s.gameplayTrace; trace != nil && outcome != "" {
				trace.record(r.ctx, e.s.state.Character.GUID, r.op, r.body, outcome, anyBytes)
			}
			r.reply <- err
			if err != nil {
				return err
			}
		case err := <-e.preparation:
			e.preparation = nil
			if err != nil {
				return fmt.Errorf("prepare logout: %w", err)
			}
			if err := e.write(opcode.CMSGLogoutRequest, nil); err != nil {
				return err
			}
		case err := <-e.finalPackets:
			return err
		case reply := <-e.s.logout:
			if e.logout != nil {
				reply <- e.logout
				continue
			}
			op := &logoutOperation{done: make(chan struct{})}
			reply <- op
			if e.phase != InWorld {
				op.err = fmt.Errorf("cannot log out while session is %s", e.phase)
				close(op.done)
				continue
			}
			e.logout = op
			e.publish()
			e.logoutTimer = time.NewTimer(e.timing.logout)
			e.logoutDeadline = e.logoutTimer.C
			e.s.modules.BeginLogout()
			prepared := make(chan error, 1)
			e.preparation = prepared
			ctx, cancel := context.WithTimeout(e.s.ctx, e.timing.logout)
			e.s.workers.Add(1)
			go func() { defer e.s.workers.Done(); defer cancel(); prepared <- e.s.modules.PrepareLogout(ctx) }()

		case <-e.logoutDeadline:
			return errors.New("logout timed out without server confirmation")
		case <-e.pongTimer.C:
			if e.finalPackets != nil {
				continue
			}
			return errors.New("worldserver pong timeout")
		case <-ping.C:
			if e.finalPackets != nil {
				continue
			}
			now := time.Now()
			// Expire unanswered requests even if delayed replies keep arriving.
			// This bounds memory and prevents an ancient pong renewing liveness.
			for id, sent := range e.pings {
				if now.Sub(sent) >= e.timing.pong {
					delete(e.pings, id)
				}
			}
			e.pingSequence++
			body := binary.LittleEndian.AppendUint32(nil, e.pingSequence)
			body = binary.LittleEndian.AppendUint32(body, e.latency)
			e.pings[e.pingSequence] = now
			if err := e.write(opcode.CMSGPing, body); err != nil {
				return err
			}
			// Space actual sends apart, including after a temporarily blocked
			// write; AzerothCore rejects clients which send pings too quickly.
			ping.Reset(e.timing.ping)
		case p := <-packets:
			complete, err := e.processIncoming(p)
			if err != nil {
				return err
			}
			if complete {
				packets = nil
			}
		}
	}
}

func (e *engine) processIncoming(p packet) (bool, error) {
	if p.err != nil {
		select {
		case <-e.s.abort:
			return false, ErrClosed
		default:
		}
		return false, fmt.Errorf("worldserver disconnected: %w", p.err)
	}
	complete, err := e.handle(p.opcode, p.body)
	if err != nil {
		return false, fmt.Errorf("world packet 0x%03x: %w", p.opcode, err)
	}
	if err := e.publishObserved(p.opcode, p.body); err != nil {
		return false, err
	}
	if complete {
		// Keep callback writes serviced while subscribed logout-complete
		// packets drain. The caller then disables further inbound processing.
		flushed := make(chan error, 1)
		e.finalPackets = flushed
		e.s.workers.Add(1)
		go func() {
			defer e.s.workers.Done()
			ctx, cancel := context.WithTimeout(e.s.ctx, 5*time.Second)
			defer cancel()
			flushed <- e.s.modules.FlushPackets(ctx)
		}()
	}
	return complete, nil
}
