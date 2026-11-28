package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/worldconn"
	"google.golang.org/protobuf/proto"
)

func transferPending(mapID uint32) []byte {
	return binary.LittleEndian.AppendUint32(nil, mapID)
}

func requireTransferKnown(t *testing.T, scope *modv1.TransportScope, from *modv1.TransportScope, target uint32) *modv1.ObservedWorldTransfer {
	t.Helper()
	e := scope.WorldTransfer
	if e == nil || !e.Known || e.Revision == 0 || e.Phase != "ack_written" || e.Reason != "" ||
		e.ConnectionId != from.ConnectionId || e.SelfGuid != from.SelfGuid || e.FromVisitRevision != from.VisitRevision ||
		e.FromMapId != from.MapId || e.FromMotionRevision != from.MotionRevision ||
		e.FromSplineIdKnown != from.SplineIdKnown || e.FromSplineId != from.SplineId || e.FromSplineRevision != from.SplineRevision ||
		e.FromSplineSourceRevision != from.SplineSourceRevision || e.FromSplineSourceOpcode != from.SplineSourceOpcode ||
		e.FromSplineActive != from.SplineActive || e.PendingMotionRevision != from.MotionRevision+1 ||
		!e.TargetMapKnown || e.TargetMapId != target || e.ToMapId != target || e.ToVisitRevision != from.VisitRevision+1 ||
		e.EntryMotionRevision != e.PendingMotionRevision+1 || e.WorldportAckMotionRevision != e.EntryMotionRevision+1 ||
		!e.WorldportAckWritten || e.WorldportAckUnknown || scope.MotionRevision < e.WorldportAckMotionRevision {
		t.Fatal("transfer edge did not retain its exact adjacent observations and actual ACK", scope, from)
	}
	return e
}

func TestWorldTransferOwnerLoopPublishesActualReportPendingEntryAndAck(t *testing.T) {
	r := &scopeObservedRuntime{observedRuntime: newObserved(), facts: make(chan *modv1.WorldPacket, 128)}
	ready, done := make(chan struct{}), make(chan struct{})
	image, entry := scopeCompletion(99, 0), positionBody(1, 10, 20, 30, .5)
	c := peerConnection(t, func(p *testPeer) {
		p.login()
		p.send(opcode.SMSGMonsterMove, scopeSpline(99, 0, false, 4))
		p.sync(77)
		close(ready)
		if body := p.expect(opcode.CMSGMoveSplineDone); !bytes.Equal(body, image) {
			t.Error("completion report was rewritten", body)
		}
		p.send(opcode.SMSGTransferPending, transferPending(1))
		p.send(opcode.SMSGNewWorld, entry)
		if body := p.expect(opcode.MSGMoveWorldportAck); len(body) != 0 {
			t.Error("Worldport ACK acquired invented fields", body)
		}
		// A fresh actual source in the new visit must not erase the historic edge.
		p.send(opcode.SMSGMonsterMove, scopeSpline(99, 0, false, 10))
		p.sync(78)
		close(done)
		p.closed()
	})
	s, err := enter(context.Background(), c, Realm{}, Character{99, "Mira"}, nil, quietTimings(), Options{Modules: r})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("standalone source not established")
	}
	var from *modv1.TransportScope
	for from == nil {
		select {
		case p := <-r.facts:
			if p.Opcode == opcode.SMSGMonsterMove {
				from = p.TransportScope
			}
		case <-time.After(time.Second):
			t.Fatal("missing source observation")
		}
	}
	if err := s.SendScopedPacket(context.Background(), opcode.CMSGMoveSplineDone, image, from); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unchanged normal transfer handshake stalled")
	}
	var pending, entered, next *modv1.TransportScope
	for next == nil {
		select {
		case p := <-r.facts:
			switch p.Opcode {
			case opcode.SMSGTransferPending:
				pending = p.TransportScope
			case opcode.SMSGNewWorld:
				if !bytes.Equal(p.Payload, entry) {
					t.Fatal("original NEW_WORLD payload changed")
				}
				entered = p.TransportScope
			case opcode.SMSGMonsterMove:
				next = p.TransportScope
			}
		case <-time.After(time.Second):
			t.Fatal("missing transfer observations")
		}
	}
	if pending == nil || pending.VisitBound || pending.WorldTransfer == nil || pending.WorldTransfer.Known ||
		!pending.WorldTransfer.CompletionReportWritten || pending.WorldTransfer.CompletionReportMotionRevision != from.MotionRevision {
		t.Fatal("pending packet retained old authority or invented/lost written-report evidence", pending)
	}
	if entered == nil {
		t.Fatal("missing post-ACK entry metadata")
	}
	e := requireTransferKnown(t, entered, from, 1)
	if !e.CompletionReportWritten || e.CompletionReportMotionRevision != from.MotionRevision ||
		!proto.Equal(e, next.WorldTransfer) || next.VisitRevision != e.ToVisitRevision || !next.SplineActive {
		t.Fatal("new source erased history or report write implied source/arrival", entered, next)
	}
	before := proto.Clone(s.TransportScope().WorldTransfer).(*modv1.ObservedWorldTransfer)
	entered.WorldTransfer.ConnectionId = "subscriber mutation"
	pending.WorldTransfer.TargetMapId = 999
	if !proto.Equal(s.TransportScope().WorldTransfer, before) {
		t.Fatal("published edge aliases owner-loop state")
	}
}

func TestWorldTransferKnownGenericEdgeDoesNotInventTaxiReportAndKeepsZeroMap(t *testing.T) {
	e := scopeEngine(t, &scopeWriteConn{})
	from := e.transport.snapshot()
	if _, err := e.handle(opcode.SMSGTransferPending, transferPending(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.handle(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5)); err != nil {
		t.Fatal(err)
	}
	fact := requireTransferKnown(t, e.transport.scope, from, 0)
	if fact.CompletionReportWritten || fact.CompletionReportMotionRevision != 0 {
		t.Fatal("generic transfer invented a taxi completion report", fact)
	}
	first := proto.Clone(fact).(*modv1.ObservedWorldTransfer)
	from = e.transport.snapshot()
	e.handle(opcode.SMSGTransferPending, transferPending(2))
	e.handle(opcode.SMSGNewWorld, positionBody(2, 4, 5, 6, .5))
	second := requireTransferKnown(t, e.transport.scope, from, 2)
	if second.Revision <= first.Revision || first.ToMapId != 0 || second.FromVisitRevision != first.ToVisitRevision {
		t.Fatal("latest immediate edge rewrote its predecessor or failed monotonic publication", first, second)
	}
	e.handle(opcode.SMSGNewWorld, []byte{2, 0, 0})
	if latest := e.transport.scope.WorldTransfer; latest.Known || latest.WorldportAckWritten || latest.Revision <= second.Revision || latest.Reason == "" {
		t.Fatal("malformed new boundary retained a previous certified edge", latest)
	}
}

func TestWorldTransferIncompleteOrSupersededSequenceCannotCertifyButKeepsLegacyAck(t *testing.T) {
	for _, change := range []string{"missing pending", "short pending", "trailing pending", "wrong old ship map", "overlap", "mismatch", "login", "abort", "source", "stop", "control", "update", "outbound motion", "body", "connection"} {
		t.Run(change, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopeEngine(t, c)
			pending := transferPending(1)
			switch change {
			case "short pending":
				pending = pending[:3]
			case "trailing pending":
				pending = append(pending, 0)
			case "wrong old ship map":
				pending = binary.LittleEndian.AppendUint32(pending, 123)
				pending = binary.LittleEndian.AppendUint32(pending, e.transport.scope.MapId+1)
			}
			if change != "missing pending" {
				e.handle(opcode.SMSGTransferPending, pending)
			}
			switch change {
			case "overlap":
				e.handle(opcode.SMSGTransferPending, transferPending(1))
			case "login":
				e.handle(opcode.SMSGLoginVerifyWorld, positionBody(0, 1, 2, 3, .5))
			case "abort":
				e.handle(opcode.SMSGTransferAborted, nil)
			case "source":
				e.handle(opcode.SMSGMonsterMove, scopeSpline(99, 1, false, 8))
			case "stop":
				e.handle(opcode.SMSGMonsterMove, scopeSpline(99, 0, true, 4))
			case "control":
				e.handle(opcode.SMSGClientControlUpdate, []byte{1, 99, 0})
			case "update":
				e.handle(opcode.SMSGUpdateObject, []byte{0, 0, 0, 0})
			case "outbound motion":
				e.transport.sent(opcode.MSGMoveStartForward)
			case "body":
				e.s.state.Character.GUID = 100
			case "connection":
				e.transport.scope.ConnectionId = "replacement connection"
			}
			mapID := uint32(1)
			if change == "mismatch" {
				mapID = 2
			}
			if _, err := e.handle(opcode.SMSGNewWorld, positionBody(mapID, 10, 20, 30, .5)); err != nil {
				t.Fatal("fact qualification changed legacy ACK behavior", err)
			}
			fact := e.transport.scope.WorldTransfer
			if fact == nil || fact.Known || fact.Reason == "" || !fact.WorldportAckWritten || fact.WorldportAckUnknown ||
				len(c.opcodes) != 1 || c.opcodes[0] != opcode.MSGMoveWorldportAck {
				t.Fatal("incomplete edge became certified or changed automatic ACK", fact, c.opcodes)
			}
		})
	}
	// Established clearly-foreign GUID packets cannot supersede the own edge.
	e := scopeEngine(t, &scopeWriteConn{})
	from := e.transport.snapshot()
	e.handle(opcode.SMSGTransferPending, transferPending(1))
	e.handle(opcode.SMSGMonsterMove, scopeSpline(100, 999, false, 10))
	e.handle(opcode.SMSGClientControlUpdate, []byte{1, 100, 0})
	e.handle(opcode.SMSGNewWorld, positionBody(1, 10, 20, 30, .5))
	requireTransferKnown(t, e.transport.scope, from, 1)
}

func passengerWorldportCreate(t *testing.T, mover uint64, i movementinfo.Info) []byte {
	t.Helper()
	wire, err := movementinfo.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	b := append([]byte{3}, scopePackedGUID(mover)...)
	b = binary.LittleEndian.AppendUint16(append(b, 4), 0x20)
	b = append(b, wire...)
	for _, speed := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(speed))
	}
	b = append(b, 2)
	for _, value := range []uint32{1 << 0x18, 1 | (1 << (0x3b - 32)), 100, 100, 8} {
		b = binary.LittleEndian.AppendUint32(b, value)
	}
	return compressedBodyUpdates(t, b)
}

func TestPassengerWorldTransferRequiresParsedGapAndReceivingHull(t *testing.T) {
	const ship uint64 = 0x1fc0000000000034
	const template uint32 = 77 // matches the synthetic routeHullCreate
	for _, scenario := range []string{"both directions", "no update", "own update", "malformed update", "unknown subject", "abort",
		"wrong pending map", "zero entry", "other template", "other hull", "foreign rider", "missing old hull", "other departure rider hull", "unknown ACK",
		"stale body envelope", "detach and reboard", "second hull birth", "body death and restore", "batched death and restore", "malformed later pending",
		"old hull removed", "old hull recreated", "old hull entry changed", "old hull entry unchanged"} {
		t.Run(scenario, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopeEngine(t, c)
			w := buddyworld.NewService(99)
			observe := func(op uint16, data []byte) *worldv1.SnapshotResponse {
				t.Helper()
				_, err := e.handle(op, data)
				if err != nil && !(op == opcode.SMSGNewWorld && scenario == "unknown ACK") {
					t.Fatal(err)
				}
				scope := e.transport.snapshot()
				if scenario == "stale body envelope" && op == opcode.SMSGCompressedUpdateObject {
					scope.VisitRevision--
				}
				if _, err = w.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(op), Payload: data, TransportScope: scope}); err != nil {
					t.Fatal(err)
				}
				v, err := w.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 60, 70, 80, .5))
			observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 60, 70, 80, .5))
			if scenario != "missing old hull" {
				observe(opcode.SMSGUpdateObject, routeHullCreate(ship))
			}
			if scenario == "other departure rider hull" {
				observe(opcode.SMSGUpdateObject, routeHullCreate(ship+1))
				observe(opcode.SMSGCompressedUpdateObject, passengerWorldportCreate(t, 99, movementinfo.Info{Flags: 0x200, TransportPresent: true,
					Transport: movementinfo.Transport{GUID: ship + 1, Seat: -1}}))
			}
			cross := func(from, to uint32) {
				t.Helper()
				entry, old := template, from
				switch scenario {
				case "zero entry":
					entry = 0
				case "other template":
					entry++
				case "wrong pending map":
					old++
				}
				pending := binary.LittleEndian.AppendUint32(transferPending(to), entry)
				pending = binary.LittleEndian.AppendUint32(pending, old)
				pre := observe(opcode.SMSGTransferPending, pending)
				if pre.WorldTransfer.Known || pre.WorldTransfer.PassengerTransportGuid != 0 || entry != 0 && pre.Self.PositionKnown {
					t.Fatal("pending supplied an attachment, position or completed edge")
				}
				// The field record establishes 12/81/20/0 byte packet sizes
				// and ordering only. These nine visibility removals are synthetic.
				visibility := binary.LittleEndian.AppendUint32(nil, 1)
				visibility = binary.LittleEndian.AppendUint32(append(visibility, 4), 9)
				for id := uint64(1); id <= 9; id++ {
					visibility = append(visibility, scopePackedGUID(0x00f130aabbccdd00|id)...)
				}
				if len(visibility) != 81 {
					t.Fatal("synthetic object fixture no longer matches observed length")
				}
				switch scenario {
				case "own update":
					visibility = append(binary.LittleEndian.AppendUint32(nil, 1), bodyValuesBlock(99, 0x18, 0)...)
				case "malformed update":
					visibility = visibility[:80]
				case "unknown subject":
					visibility[9] = 0
				case "old hull removed":
					visibility = binary.LittleEndian.AppendUint32(nil, 1)
					visibility = binary.LittleEndian.AppendUint32(append(visibility, 4), 1)
					visibility = append(visibility, scopePackedGUID(ship)...)
				case "old hull recreated":
					visibility = routeHullCreate(ship)
				case "old hull entry changed", "old hull entry unchanged":
					entryValue := template
					if scenario == "old hull entry changed" {
						entryValue++
					}
					visibility = append(binary.LittleEndian.AppendUint32(nil, 1), 0)
					visibility = append(visibility, scopePackedGUID(ship)...)
					visibility = binary.LittleEndian.AppendUint32(append(visibility, 1), 1<<3)
					visibility = binary.LittleEndian.AppendUint32(visibility, entryValue)
				case "abort":
					observe(opcode.SMSGTransferAborted, transferPending(to))
				}
				if scenario != "no update" {
					observe(opcode.SMSGUpdateObject, visibility)
				}
				c.fail = scenario == "unknown ACK"
				writes := c.writes
				v := observe(opcode.SMSGNewWorld, positionBody(to, 1, 2, 3, .25))
				c.fail = false
				if c.writes != writes+1 || c.opcodes[writes] != opcode.MSGMoveWorldportAck || v.Self.PositionKnown || v.WorldTransfer.PassengerTransportGuid != 0 {
					t.Fatal("entry changed ACK or acquired absolute coordinates/ship from deck offset")
				}
				mover, receiving := uint64(99), ship
				if scenario == "foreign rider" {
					mover = 3
				}
				if scenario == "other hull" {
					receiving++
				}
				i := movementinfo.Info{Flags: 0x200, Time: 125, X: 120, Y: 130, Z: 140, Orientation: 2,
					TransportPresent: true, Transport: movementinfo.Transport{GUID: receiving, X: 1, Y: 2, Z: 3, Orientation: .25, Time: 42, Seat: -1}}
				observe(opcode.SMSGCompressedUpdateObject, passengerWorldportCreate(t, mover, i))
				v = observe(opcode.SMSGUpdateObject, routeHullCreate(receiving))
				wantEdge, wantShip := true, true
				switch scenario {
				case "own update", "malformed update", "unknown subject", "abort", "wrong pending map", "zero entry", "unknown ACK":
					wantEdge, wantShip = false, false
				case "other template", "other hull", "foreign rider", "missing old hull", "other departure rider hull", "stale body envelope", "old hull removed", "old hull recreated", "old hull entry changed":
					wantShip = false
				}
				f := v.WorldTransfer
				if f.Known != wantEdge || (f.PassengerTransportGuid == ship) != wantShip || f.ContinuationKnown || f.PredecessorQualified || f.ArrivalKnown ||
					f.BookingCausalityKnown || wantEdge && (f.PassengerTransportEntry != entry || f.Core.Phase != "passenger_ack_written" || !f.Core.WorldportAckWritten || f.Core.WorldportAckUnknown ||
					f.Core.EntryMotionRevision != f.Core.PendingMotionRevision+2 && scenario != "no update") {
					t.Fatalf("generic=%t want %t, ship=%x want %t, World=%q Core=%q/%q", f.Known, wantEdge, f.PassengerTransportGuid, wantShip,
						f.Reason, e.transport.scope.WorldTransfer.GetPhase(), e.transport.scope.WorldTransfer.GetReason())
				}
				if mover == 99 && (!v.Self.PositionKnown || v.Self.X != i.X || v.Self.GetPassengerMovement().GetInfo().GetTransport().GetGuid() != receiving) {
					t.Fatal("receiving-side actual absolute observation was lost")
				}
				switch scenario {
				case "detach and reboard":
					observe(opcode.SMSGCompressedUpdateObject, passengerWorldportCreate(t, 99, movementinfo.Info{X: i.X, Y: i.Y, Z: i.Z}))
				case "second hull birth":
					observe(opcode.SMSGUpdateObject, routeHullCreate(ship))
				case "body death and restore":
					observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(99, 0x18, 0)))
				case "batched death and restore":
					v = observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(99, 0x18, 0), bodyValuesBlock(99, 0x18, 100)))
					if !v.WorldTransfer.Known || v.WorldTransfer.PassengerTransportGuid != 0 || !v.Self.Alive {
						t.Fatal("one complete batch hid an actual intervening death")
					}
					return
				case "malformed later pending":
					v = observe(opcode.SMSGTransferPending, []byte{1, 2, 3})
					if v.WorldTransfer.Known || v.WorldTransfer.PassengerTransportGuid != 0 {
						t.Fatal("malformed later boundary preserved the old ship claim")
					}
					return
				default:
					return
				}
				v = observe(opcode.SMSGCompressedUpdateObject, passengerWorldportCreate(t, 99, i))
				if !v.WorldTransfer.Known || v.WorldTransfer.PassengerTransportGuid != 0 || !v.Self.PositionKnown || v.Self.GetPassengerMovement().GetInfo().GetTransport().GetGuid() != ship {
					t.Fatal("later receiving body was not independent of the departed or replaced historical ship")
				}
			}
			cross(0, 1)
			if scenario == "both directions" {
				cross(1, 0)
			}
		})
	}
}

type transferDeadlineConn struct {
	*scopeWriteConn
}

func (*transferDeadlineConn) SetWriteDeadline(time.Time) error {
	return errors.New("fixture deadline failure before any write")
}

func TestWorldTransferAckFailureRetainsTruthWithoutPublishingEntryOrResending(t *testing.T) {
	for _, failure := range []string{"unknown partial write", "known deadline refusal"} {
		t.Run(failure, func(t *testing.T) {
			c := &scopeWriteConn{fail: failure == "unknown partial write"}
			e := scopeEngine(t, c)
			if failure == "known deadline refusal" {
				e.s.conn = worldconn.New(&transferDeadlineConn{c})
			}
			r := &scopeObservedRuntime{observedRuntime: newObserved(), facts: make(chan *modv1.WorldPacket, 8)}
			e.s.modules = r
			defer r.Close()
			if _, err := e.processIncoming(packet{opcode: opcode.SMSGTransferPending, body: transferPending(1)}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.processIncoming(packet{opcode: opcode.SMSGNewWorld, body: positionBody(1, 10, 20, 30, .5)}); err == nil {
				t.Fatal("ACK transport error stopped propagating")
			}
			fact := e.s.TransportScope().WorldTransfer
			if fact == nil || fact.Known || fact.WorldportAckWritten || fact.Reason == "" ||
				fact.WorldportAckUnknown != (failure == "unknown partial write") || fact.WorldportAckMotionRevision != 0 {
				t.Fatal("ACK failure invented known write/edge or lost unknown outcome", fact)
			}
			if c.writes != map[string]int{"unknown partial write": 1, "known deadline refusal": 0}[failure] {
				t.Fatal("ACK was retried", c.writes)
			}
			for len(r.facts) != 0 {
				if p := <-r.facts; p.Opcode == opcode.SMSGNewWorld {
					t.Fatal("failed ACK incorrectly published a successfully handled entry")
				}
			}
			before := proto.Clone(fact)
			e.s.TransportScope()
			e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(100, 0, false, 4))
			if !proto.Equal(before, e.transport.scope.WorldTransfer) {
				t.Fatal("read/foreign packet repaired missing ACK transport evidence")
			}
		})
	}
}

func TestWorldTransferCompletionReportRequiresActualSuccessfulExactSource(t *testing.T) {
	for _, change := range []string{"written", "written then update", "written then foreign", "unknown write", "reserved only", "new source", "duplicate source", "ordinary movement"} {
		t.Run(change, func(t *testing.T) {
			c := &scopeWriteConn{fail: change == "unknown write"}
			e := scopeEngine(t, c)
			request := e.transport.snapshot()
			if change == "reserved only" {
				if err := e.transport.reserve(opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), request); err != nil {
					t.Fatal(err)
				}
			} else {
				_, _, err := e.writeContext(context.Background(), opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), request)
				if (err != nil) != (change == "unknown write") {
					t.Fatal(err)
				}
			}
			c.fail = false
			switch change {
			case "written then update":
				e.transport.observe(opcode.SMSGUpdateObject, []byte{0, 0, 0, 0})
			case "written then foreign":
				e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(100, 999, false, 4))
			case "new source":
				e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(99, 1, false, 5))
			case "duplicate source":
				e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(99, 0, false, 4))
			case "ordinary movement":
				e.transport.sent(opcode.MSGMoveStartForward)
			}
			if change == "unknown write" {
				if _, _, err := e.writeContext(context.Background(), opcode.CMSGMoveSplineDone, scopeCompletion(99, 0), e.transport.snapshot()); !modcb.PacketWasNotDispatched(err) || c.writes != 1 {
					t.Fatal("unknown completion became replayable", err, c.writes)
				}
			}
			from := e.transport.snapshot()
			e.handle(opcode.SMSGTransferPending, transferPending(1))
			e.handle(opcode.SMSGNewWorld, positionBody(1, 10, 20, 30, .5))
			fact := requireTransferKnown(t, e.transport.scope, from, 1)
			want := change == "written" || change == "written then update" || change == "written then foreign"
			if fact.CompletionReportWritten != want || want && fact.CompletionReportMotionRevision != request.MotionRevision ||
				!want && fact.CompletionReportMotionRevision != 0 {
				t.Fatal("transfer relabeled reservation/unknown/stale report as an exact known write", fact)
			}
		})
	}
}

func TestWorldTransferMalformedEntryLateAckChangesAndRevisionExhaustionFailClosed(t *testing.T) {
	valid := positionBody(1, 10, 20, 30, .5)
	bad := make([][]byte, 0, len(valid)+4)
	for n := 0; n < len(valid); n++ {
		bad = append(bad, valid[:n])
	}
	bad = append(bad, append(bytes.Clone(valid), 0))
	for _, bits := range []uint32{0x7fc00000, 0x7f800000, math.Float32bits(50000)} {
		b := bytes.Clone(valid)
		binary.LittleEndian.PutUint32(b[4:], bits)
		bad = append(bad, b)
	}
	for n, body := range bad {
		c := &scopeWriteConn{}
		e := scopeEngine(t, c)
		e.handle(opcode.SMSGTransferPending, transferPending(1))
		e.handle(opcode.SMSGNewWorld, body)
		fact := e.transport.scope.WorldTransfer
		if fact == nil || fact.Known || fact.WorldportAckWritten || fact.WorldportAckUnknown || len(c.opcodes) != 0 {
			t.Fatal("invalid/ACK-ineligible entry certified or changed legacy ACK admission", n, fact, c.opcodes)
		}
	}
	for _, change := range []string{"source", "body", "connection"} {
		t.Run("ACK deadline "+change, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopeEngine(t, c)
			e.handle(opcode.SMSGTransferPending, transferPending(1))
			c.deadline = func() {
				switch change {
				case "source":
					e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(99, 7, false, 8))
				case "body":
					e.s.state.Character.GUID++
				case "connection":
					e.transport.scope.ConnectionId = "late replacement"
				}
			}
			if _, err := e.handle(opcode.SMSGNewWorld, valid); err != nil {
				t.Fatal(err)
			}
			fact := e.transport.scope.WorldTransfer
			if fact == nil || fact.Known || !fact.WorldportAckWritten || fact.Reason == "" || c.writes != 1 {
				t.Fatal("late callback certified stale edge or changed existing ACK execution", fact, c.writes)
			}
		})
	}
	e := scopeEngine(t, &scopeWriteConn{})
	e.transport.transfer.revision = math.MaxUint64
	e.handle(opcode.SMSGTransferPending, transferPending(1))
	e.handle(opcode.SMSGNewWorld, valid)
	fact := e.transport.scope.WorldTransfer
	if fact == nil || fact.Known || fact.Revision != math.MaxUint64 || fact.Reason != "transfer_revision_exhausted" {
		t.Fatal("transfer sequence wrapped or rearmed", fact)
	}
}
