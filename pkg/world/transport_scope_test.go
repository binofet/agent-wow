package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"net"
	"testing"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/worldconn"
	"google.golang.org/protobuf/proto"
)

func scopePackedGUID(guid uint64) []byte {
	b := []byte{0}
	for n := uint(0); n < 8; n++ {
		if v := byte(guid >> (8 * n)); v != 0 {
			b[0] |= 1 << n
			b = append(b, v)
		}
	}
	return b
}

func scopeSpline(self uint64, id uint32, stop bool, destination float32) []byte {
	b := append(scopePackedGUID(self), 0)
	for _, f := range []float32{1, 2, 3} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(f))
	}
	b = binary.LittleEndian.AppendUint32(b, id)
	if stop {
		return append(b, 1)
	}
	b = append(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0x40000) // uncompressed path
	b = binary.LittleEndian.AppendUint32(b, 1000)
	b = binary.LittleEndian.AppendUint32(b, 1)
	for _, f := range []float32{destination, 5, 6} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(f))
	}
	return b
}

func scopeCompletion(self uint64, id uint32) []byte {
	b := scopePackedGUID(self)
	b = binary.LittleEndian.AppendUint32(b, 0x70000000) // preserved passive facts, no conditional tail
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint32(b, math.MaxUint32)
	for _, f := range []float32{4, 5, 6, .5} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(f))
	}
	b = binary.LittleEndian.AppendUint32(b, 0)
	return binary.LittleEndian.AppendUint32(b, id)
}

func scopeRig(t *testing.T, id uint32) transportScopeState {
	t.Helper()
	scope, err := newTransportScope(99)
	if err != nil {
		t.Fatal(err)
	}
	r := transportScopeState{scope: scope}
	r.observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 1, 2, 3, .5))
	r.observe(opcode.SMSGMonsterMove, scopeSpline(99, id, false, 4))
	if !r.scope.VisitBound || r.scope.VisitRevision != 1 || !r.scope.SplineIdKnown || !r.scope.SplineActive || r.scope.SplineId != id {
		t.Fatal("unbound standalone-spline fixture", r.scope)
	}
	return r
}

func TestTransportScopeOwnHeaderZeroIDVisitAndForeignFiltering(t *testing.T) {
	r := scopeRig(t, 0)
	if r.scope.ConnectionId == "" || r.scope.SelfGuid != 99 || r.scope.SplineSourceOpcode != opcode.SMSGMonsterMove ||
		r.scope.SplineSourceRevision != r.scope.MotionRevision || r.scope.SplineRevision == 0 {
		t.Fatal("opaque zero ID lost actual source identity", r.scope)
	}
	other := scopeRig(t, 0)
	if other.scope.ConnectionId == r.scope.ConnectionId {
		t.Fatal("fresh transports reused a connection instance")
	}
	if err := other.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("same body/visit/counter from another connection authorized a write", err)
	}
	transport := scopeRig(t, math.MaxUint32)
	b := scopeSpline(99, math.MaxUint32, false, 4)
	// Transport GUID0 and signed seat-1 precede the ordinary spline header.
	b = append(append(append([]byte(nil), b[:2]...), 0, 0xff), b[2:]...)
	transport.observe(opcode.SMSGMonsterMoveTransport, b)
	if !transport.scope.SplineIdKnown || transport.scope.SplineId != math.MaxUint32 || !transport.scope.SplineActive ||
		transport.scope.SplineSourceOpcode != opcode.SMSGMonsterMoveTransport {
		t.Fatal("transport GUID/seat shifted the opaque own header ID", transport.scope)
	}
	if err := transport.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, math.MaxUint32), transport.snapshot()); err != nil {
		t.Fatal("opaque maximum counter could not be encoded/checked", err)
	}
	before := r.snapshot()
	for _, op := range []uint16{opcode.SMSGMonsterMove, opcode.SMSGForceMoveRoot, opcode.SMSGForceMoveUnroot,
		opcode.SMSGForceRunSpeedChange, opcode.SMSGForceWalkSpeedChange, opcode.SMSGMoveKnockBack,
		opcode.SMSGClientControlUpdate, opcode.MSGMoveHeartbeat} {
		r.observe(op, scopeSpline(100, 999, false, 8))
		if !proto.Equal(r.scope, before) {
			t.Fatal("clearly foreign mover changed own scope", op, r.scope)
		}
	}
	r.observe(opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, 100), 0))
	if !proto.Equal(r.scope, before) {
		t.Fatal("foreign destroy changed own scope", r.scope)
	}
	r.observe(opcode.SMSGUpdateObject, []byte{0, 0, 0, 0})
	if r.scope.MotionRevision != before.MotionRevision+1 || r.scope.SplineRevision != before.SplineRevision ||
		r.scope.SplineSourceRevision != before.SplineSourceRevision || !r.scope.SplineActive {
		t.Fatal("general update erased source or impersonated a new spline", r.scope)
	}
	r.observe(opcode.SMSGFlightSplineSync, []byte{0, 0, 0, 0})
	if r.scope.MotionRevision != before.MotionRevision+2 || r.scope.SplineRevision != before.SplineRevision {
		t.Fatal("unsupported flight timing packet failed to impose a barrier", r.scope)
	}
	r.observe(opcode.SMSGMonsterMove, scopeSpline(99, 0, true, 4))
	if !r.scope.SplineIdKnown || r.scope.SplineId != 0 || r.scope.SplineActive {
		t.Fatal("actual stop became completion authority", r.scope)
	}
	r.observe(opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
	if r.scope.VisitBound || r.scope.VisitRevision != 0 || r.scope.SplineActive || r.scope.SplineIdKnown {
		t.Fatal("transfer retained old visit authority", r.scope)
	}
	r.observe(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5))
	if !r.scope.VisitBound || r.scope.VisitRevision != 2 || r.scope.SplineActive {
		t.Fatal("same-map transfer did not establish a new unarmed visit", r.scope)
	}
}

func TestTransportScopeRequiresEveryCurrentFieldAndExactCompletionImage(t *testing.T) {
	for _, field := range []string{"connection", "self", "bound", "visit", "map", "motion", "ID known", "ID", "generation", "source", "opcode", "active"} {
		t.Run(field, func(t *testing.T) {
			r := scopeRig(t, 0)
			bad := r.snapshot()
			switch field {
			case "connection":
				bad.ConnectionId += "old"
			case "self":
				bad.SelfGuid++
			case "bound":
				bad.VisitBound = false
			case "visit":
				bad.VisitRevision++
			case "map":
				bad.MapId++
			case "motion":
				bad.MotionRevision++
			case "ID known":
				bad.SplineIdKnown = false
			case "ID":
				bad.SplineId++
			case "generation":
				bad.SplineRevision++
			case "source":
				bad.SplineSourceRevision++
			case "opcode":
				bad.SplineSourceOpcode = opcode.SMSGUpdateObject
			case "active":
				bad.SplineActive = false
			}
			if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), bad); !modcb.PacketWasNotDispatched(err) || len(r.consumed) != 0 {
				t.Fatal("stale field admitted or consumed an attempt", field, err)
			}
			if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); err != nil {
				t.Fatal("known refusal consumed valid authority", err)
			}
		})
	}
	r := scopeRig(t, 0)
	wire := scopeCompletion(99, 0)
	bad := [][]byte{scopeCompletion(100, 0), scopeCompletion(99, 1), append(bytes.Clone(wire), 1)}
	for end := 0; end < len(wire); end++ {
		bad = append(bad, wire[:end])
	}
	nonfinite := bytes.Clone(wire)
	binary.LittleEndian.PutUint32(nonfinite[12:], 0x7fc12345)
	bad = append(bad, nonfinite)
	for n, payload := range bad {
		if err := r.reserve(opcode.CMSGMoveSplineDone, payload, r.snapshot()); !modcb.PacketWasNotDispatched(err) || len(r.consumed) != 0 {
			t.Fatal("malformed/wrong-body/counter image reached reservation", n, err)
		}
	}
	if err := r.reserve(opcode.CMSGMoveSplineDone, wire, nil); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("unscoped completion was admitted", err)
	}
	if err := r.reserve(opcode.CMSGMessagechat, []byte{1}, r.snapshot()); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("scope silently applied to an unsupported opcode", err)
	}
	if err := r.reserve(opcode.CMSGMessagechat, []byte{1}, nil); err != nil {
		t.Fatal("unscoped legacy opcode changed", err)
	}
	if err := r.reserve(opcode.CMSGMoveSplineDone, wire, r.snapshot()); err != nil {
		t.Fatal("complete finite image with opaque counter0 was rejected", err)
	}
}

func TestTransportScopeConsumedSourcesCannotReplayAcrossDuplicateOrMalformedHeaders(t *testing.T) {
	r := scopeRig(t, 0)
	a := scopeSpline(99, 0, false, 4)
	generation := r.scope.SplineRevision
	r.observe(opcode.SMSGMonsterMove, a)
	if r.scope.SplineRevision != generation {
		t.Fatal("duplicate before attempt minted a generation", r.scope)
	}
	if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); err != nil {
		t.Fatal(err)
	}
	r.sent(opcode.CMSGMoveSplineDone)
	if !r.scope.SplineActive {
		t.Fatal("completion write invented server arrival")
	}
	for _, intermediary := range []struct {
		op   uint16
		body []byte
	}{{opcode.SMSGUpdateObject, []byte{0, 0, 0, 0}}, {opcode.SMSGMonsterMove, scopeSpline(100, 3, false, 8)},
		{opcode.SMSGMonsterMove, []byte{1, 99}}, {opcode.SMSGMonsterMove, scopeSpline(99, 8, false, 7)[:22]}} {
		r.observe(intermediary.op, intermediary.body)
		r.observe(opcode.SMSGMonsterMove, a)
		if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); !modcb.PacketWasNotDispatched(err) {
			t.Fatal("consumed source became replayable after an intervening barrier/candidate", err)
		}
	}
	// A complete distinct geometry may reuse ID0; it is a distinct server source.
	r.observe(opcode.SMSGMonsterMove, scopeSpline(99, 0, false, 9))
	if r.scope.SplineRevision <= generation {
		t.Fatal("distinct source was ordered by opaque ID instead of observation", r.scope)
	}
	if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); err != nil {
		t.Fatal("distinct observed source could not obtain its own attempt", err)
	}
	r.observe(opcode.SMSGMonsterMove, a)
	if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("older consumed exact source escaped the pervisit ledger", err)
	}
}

func TestTransportScopeHasNoArtificialCompletionCountLimitOrEviction(t *testing.T) {
	r := scopeRig(t, 0)
	for n := uint32(0); n < 300; n++ {
		r.observe(opcode.SMSGMonsterMove, scopeSpline(99, n, false, float32(n)+10))
		if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, n), r.snapshot()); err != nil {
			t.Fatal("distinct attempt hit a synthetic journey limit", n, err)
		}
	}
	if len(r.consumed) != 300 {
		t.Fatal("consumed sources were evicted", len(r.consumed))
	}
	r.observe(opcode.SMSGMonsterMove, scopeSpline(99, 0, false, 10))
	if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("first source was evicted after later attempts", err)
	}
	r.observe(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5))
	r.observe(opcode.SMSGMonsterMove, scopeSpline(99, 0, false, 10))
	if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); err != nil || len(r.consumed) != 1 {
		t.Fatal("actual new visit did not isolate old attempt history", err)
	}
}

func TestTransportScopeOutboundMovementInvalidatesOldAuthorityWithoutRewritingHistory(t *testing.T) {
	for _, op := range []uint32{opcode.MSGMoveStartForward, opcode.MSGMoveStop, opcode.MSGMoveHeartbeat, opcode.MSGMoveTeleportAck,
		opcode.MSGMoveWorldportAck, opcode.CMSGForceMoveRootAck, opcode.CMSGForceMoveUnrootAck, opcode.CMSGSetActiveMover,
		opcode.CMSGActivatetaxi, opcode.CMSGActivatetaxiexpress} {
		r := scopeRig(t, 0)
		before := r.snapshot()
		r.sent(op)
		if r.scope.SplineActive || r.scope.MotionRevision != before.MotionRevision+1 || !r.scope.SplineIdKnown ||
			r.scope.SplineId != before.SplineId || r.scope.SplineRevision != before.SplineRevision ||
			r.scope.SplineSourceRevision != before.SplineSourceRevision || r.scope.SplineSourceOpcode != before.SplineSourceOpcode {
			t.Fatal("outbound control write retained authority or rewrote observed history", op, r.scope)
		}
		if err := r.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), r.snapshot()); !modcb.PacketWasNotDispatched(err) {
			t.Fatal("older header dispatched after ordinary own movement", op, err)
		}
	}
}

func TestTransportScopeInvalidWorldEntriesAndRevisionOverflowFailClosed(t *testing.T) {
	r := scopeRig(t, 0)
	for _, v := range []float32{1e6, float32(math.NaN()), float32(math.Inf(1))} {
		r.observe(opcode.SMSGNewWorld, positionBody(0, v, 2, 3, .5))
		if r.visit != 1 || r.scope.VisitBound || r.scope.VisitRevision != 0 {
			t.Fatal("invalid World entry advanced or retained authority", r.scope)
		}
	}
	// Visit correlation follows World's existing bounds, without widening the
	// old protocol ACK coordinate predicate.
	entry := positionBody(0, 50000, 2, 3, .5)
	r.observe(opcode.SMSGNewWorld, entry)
	if !r.scope.VisitBound || r.scope.VisitRevision != 2 {
		t.Fatal("valid World visit counter diverged from World parser", r.scope)
	}
	if _, _, valid := teleportAcknowledgement(opcode.SMSGNewWorld, entry, 99, 0); valid {
		t.Fatal("transport-scope visit logic widened legacy teleport ACK authority")
	}
	r.scope.MotionRevision = math.MaxUint64
	r.observe(opcode.SMSGUpdateObject, []byte{0, 0, 0, 0})
	if !r.exhausted || r.scope.VisitBound || r.scope.SplineActive || r.scope.VisitRevision != 0 {
		t.Fatal("motion revision wrapped into reusable authority", r.scope)
	}
}

type scopeWriteConn struct {
	net.Conn
	deadline func()
	writes   int
	fail     bool
	opcodes  []uint32
}

func (c *scopeWriteConn) SetWriteDeadline(time.Time) error {
	if c.deadline != nil {
		c.deadline()
	}
	return nil
}

func (c *scopeWriteConn) Write(b []byte) (int, error) {
	c.writes++
	if len(b) >= 6 {
		c.opcodes = append(c.opcodes, binary.LittleEndian.Uint32(b[2:6]))
	}
	if c.fail {
		return min(1, len(b)), errors.New("fixture partial transport write")
	}
	return len(b), nil
}

func scopeEngine(t *testing.T, c *scopeWriteConn) *engine {
	t.Helper()
	s := &Session{conn: worldconn.New(c), logger: slog.New(slog.DiscardHandler), state: State{Status: InWorld, Character: Character{GUID: 99}}}
	return &engine{s: s, phase: InWorld, timing: quietTimings(), transport: scopeRig(t, 0)}
}

func TestTransportScopeFinalDeadlineCallbackCannotChangeBodyVisitSplineOrCancellation(t *testing.T) {
	for _, change := range []string{"body", "actual body", "visit", "spline", "stop", "movement", "cancel", "close"} {
		t.Run(change, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopeEngine(t, c)
			request := e.transport.snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.deadline = func() {
				switch change {
				case "body":
					e.transport.scope.SelfGuid++
				case "actual body":
					e.s.state.Character.GUID++
				case "visit":
					e.transport.observe(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5))
				case "spline":
					e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(99, 1, false, 4))
				case "stop":
					e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(99, 0, true, 4))
				case "movement":
					e.transport.sent(opcode.MSGMoveStartForward)
				case "cancel":
					cancel()
				case "close":
					e.s.abort = make(chan struct{})
					close(e.s.abort)
				}
			}
			_, any, err := e.writeContext(ctx, opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), request)
			if !modcb.PacketWasNotDispatched(err) || any || c.writes != 0 || len(e.transport.consumed) != 0 {
				t.Fatal("deadline callback bypassed last owner-loop fence", change, err, c.writes)
			}
		})
	}
}

func TestTransportScopeAmbiguousWriteConsumesAttemptBeforeTransport(t *testing.T) {
	c := &scopeWriteConn{fail: true}
	e := scopeEngine(t, c)
	_, any, err := e.writeContext(context.Background(), opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), e.transport.snapshot())
	if err == nil || !any || modcb.PacketWasNotDispatched(err) || c.writes != 1 || len(e.transport.consumed) != 1 {
		t.Fatal("partial transport failure lost its reserved unknown attempt", any, err, c.writes)
	}
	c.fail = false
	if _, _, err := e.writeContext(context.Background(), opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), e.transport.snapshot()); !modcb.PacketWasNotDispatched(err) || c.writes != 1 {
		t.Fatal("ambiguous attempt was sent again", err, c.writes)
	}
}

type scopeObservedRuntime struct {
	*observedRuntime
	facts chan *modv1.WorldPacket
}

func (r *scopeObservedRuntime) PublishObserved(op uint16, body []byte, scope *modv1.TransportScope) error {
	r.facts <- &modv1.WorldPacket{Opcode: uint32(op), Payload: bytes.Clone(body), TransportScope: cloneTransportScope(scope)}
	return r.observedRuntime.Publish(op, body)
}

func TestTransportScopeOwnerLoopRetainsLegacyWritesAndRejectsReplayWithoutClosing(t *testing.T) {
	r := &scopeObservedRuntime{observedRuntime: newObserved(), facts: make(chan *modv1.WorldPacket, 128)}
	ready := make(chan struct{})
	image := scopeCompletion(99, 0)
	c := peerConnection(t, func(p *testPeer) {
		p.login()
		p.send(opcode.SMSGMonsterMove, scopeSpline(99, 0, false, 4))
		p.sync(77)
		close(ready)
		if body := p.expect(opcode.CMSGMoveSplineDone); !bytes.Equal(body, image) {
			t.Error("core rewrote the completion's actual flags/tails/counter", body)
		}
		if body := p.expect(opcode.CMSGMessagechat); !bytes.Equal(body, []byte{1, 2, 3}) {
			t.Error("legacy opaque gameplay write changed", body)
		}
		p.closed()
	})
	trace := NewGameplayTrace(16)
	s, err := enter(context.Background(), c, Realm{}, Character{99, "Mira"}, nil, quietTimings(), Options{Modules: r, GameplayTrace: trace})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("synthetic server did not establish standalone source")
	}
	var packetScope *modv1.TransportScope
	for packetScope == nil {
		select {
		case p := <-r.facts:
			if p.Opcode == opcode.SMSGMonsterMove {
				packetScope = p.TransportScope
			}
		case <-time.After(time.Second):
			t.Fatal("runtime did not receive immutable inbound scope metadata")
		}
	}
	if r.session.SendScopedPacket == nil || r.session.TransportScope == nil || packetScope.SplineId != 0 || !packetScope.SplineIdKnown ||
		!proto.Equal(packetScope, s.TransportScope()) {
		t.Fatal("callback/inbound scope facts disagree", packetScope, s.TransportScope())
	}
	if err := s.SendPacket(context.Background(), opcode.CMSGMoveSplineDone, image); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("unscoped spline completion reached transport", err)
	}
	stale := cloneTransportScope(packetScope)
	stale.MotionRevision--
	if err := r.session.SendScopedPacket(context.Background(), opcode.CMSGMoveSplineDone, image, stale); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("stale module callback scope reached transport", err)
	}
	if err := r.session.SendScopedPacket(context.Background(), opcode.CMSGMoveSplineDone, image, packetScope); err != nil {
		t.Fatal("current module callback completion was not written", err)
	}
	fresh := s.TransportScope()
	if !proto.Equal(fresh, packetScope) || !fresh.SplineActive {
		t.Fatal("write rewrote immutable inbound metadata or invented arrival", packetScope, fresh)
	}
	if err := s.SendScopedPacket(context.Background(), opcode.CMSGMoveSplineDone, image, fresh); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("fresh barrier snapshot replayed a consumed spline", err)
	}
	if err := s.SendPacket(context.Background(), opcode.CMSGMessagechat, []byte{1, 2, 3}); err != nil {
		t.Fatal("known scope refusals killed the owner loop", err)
	}
	writes := trace.Snapshot().Attempts
	if len(writes) != 2 || writes[0].Opcode != opcode.CMSGMoveSplineDone || writes[1].Opcode != opcode.CMSGMessagechat {
		t.Fatal("known zero-write refusals appeared as transport attempts", writes)
	}
	packetScope.ConnectionId = "caller-mutated"
	if s.TransportScope().ConnectionId == "caller-mutated" {
		t.Fatal("subscriber scope aliases owner authority")
	}
}

func TestTransportScopeOwnerLoopProcessesReadyInboundSupersessionAfterDeadlineBeforeWrite(t *testing.T) {
	for _, change := range []string{"new header", "stop", "transfer", "same-map new world"} {
		t.Run(change, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopeEngine(t, c)
			r := &scopeObservedRuntime{observedRuntime: newObserved(), facts: make(chan *modv1.WorldPacket, 128)}
			e.s.modules, e.s.ctx, e.s.started = r, context.Background(), time.Now()
			e.s.abort, e.s.startLogin, e.s.writes = make(chan struct{}), make(chan struct{}), make(chan writeRequest)
			incoming := make(chan packet, 1)
			old := e.transport.snapshot()
			armed := true
			c.deadline = func() {
				if !armed {
					return
				}
				armed = false
				p := packet{opcode: opcode.SMSGMonsterMove, body: scopeSpline(99, 1, false, 7)}
				switch change {
				case "stop":
					p.body = scopeSpline(99, 0, true, 4)
				case "transfer":
					p = packet{opcode: opcode.SMSGTransferPending, body: []byte{0, 0, 0, 0}}
				case "same-map new world":
					p = packet{opcode: opcode.SMSGNewWorld, body: positionBody(0, 1, 2, 3, .5)}
				}
				incoming <- p
			}
			finished := make(chan error, 1)
			go func() { finished <- e.loop(incoming) }()
			t.Cleanup(func() {
				close(e.s.abort)
				<-finished
				if e.pongTimer != nil {
					e.pongTimer.Stop()
				}
				r.Stop()
				r.Close()
			})
			request := writeRequest{ctx: context.Background(), op: opcode.CMSGMoveSplineDone,
				body: scopeCompletion(99, 0), transportScope: old, reply: make(chan error, 1)}
			e.s.writes <- request
			select {
			case err := <-request.reply:
				if !modcb.PacketWasNotDispatched(err) {
					t.Fatal("ready inbound supersession did not refuse the stale write", err)
				}
			case <-time.After(time.Second):
				t.Fatal("owner loop blocked while draining ready packet/protocol ACK")
			}
			select {
			case observed := <-r.facts:
				if observed.TransportScope == nil || observed.TransportScope.MotionRevision <= old.MotionRevision {
					t.Fatal("superseding packet was not published before refusal", observed)
				}
			default:
				t.Fatal("ready inbound packet was bypassed by the queued write")
			}
			legacy := writeRequest{ctx: context.Background(), op: opcode.CMSGMessagechat, body: []byte{7}, reply: make(chan error, 1)}
			e.s.writes <- legacy
			if err := <-legacy.reply; err != nil {
				t.Fatal("semantic scope refusal killed subsequent legacy writes", err)
			}
			for _, op := range c.opcodes {
				if op == opcode.CMSGMoveSplineDone {
					t.Fatal("already-read superseding packet lost priority to old completion", c.opcodes)
				}
			}
		})
	}
}
