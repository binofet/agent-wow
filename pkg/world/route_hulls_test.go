package world

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
)

// The normal CREATE2 movement header includes the stationary world pose,
// low GUID, actual path clock and packed rotation. Type 15 is a moving hull.
func routeHullCreate(guid uint64) []byte {
	le := binary.LittleEndian
	b := append(le.AppendUint32(nil, 1), 3)
	b = append(b, scopePackedGUID(guid)...)
	b = le.AppendUint16(append(b, 5), 0x252)
	for _, coord := range []float32{7, 8, 9, 0} {
		b = le.AppendUint32(b, math.Float32bits(coord))
	}
	b = le.AppendUint32(b, uint32(guid))
	b = le.AppendUint32(b, 4000)
	b = le.AppendUint64(b, 0)
	b = le.AppendUint32(append(b, 1), 1<<3|1<<4|1<<8|1<<14|1<<16|1<<17)
	for _, field := range []uint32{77, math.Float32bits(1), 3015, 0x1020001, 300000, 15<<8 | 1} {
		b = le.AppendUint32(b, field)
	}
	return b
}

func TestRouteHullPassengerCheckpointAtPhysicalWrite(t *testing.T) {
	const ship uint64 = 0x1fc0000000000034
	const rider = 23
	pair := func(t *testing.T, ship uint64, clock uint32) []byte {
		t.Helper()
		i := movementinfo.Info{X: 41, Y: 8, Z: 9, Time: clock}
		if ship != 0 {
			i.Flags, i.TransportPresent = 0x200, true
			i.Transport = movementinfo.Transport{GUID: ship, X: 1, Time: clock, Seat: -1}
		}
		wire, err := movementinfo.MoverFrame(rider, i)
		if err != nil {
			t.Fatal(err)
		}
		return wire
	}
	for _, tc := range []struct {
		name      string
		from, to  uint64
		op        uint16
		queued    bool
		wantWrite bool
	}{
		{"standalone board", 0, ship, opcode.MSGMoveHeartbeat, false, false},
		{"later same-ship clock", ship, ship, opcode.MSGMoveHeartbeat, false, false},
		{"detach", ship, 0, opcode.MSGMoveHeartbeat, false, false},
		{"change to other ship", ship, ship + 1, opcode.MSGMoveHeartbeat, false, false},
		{"change from other ship", ship + 1, ship, opcode.MSGMoveHeartbeat, false, false},
		{"other ship clock", ship + 1, ship + 1, opcode.MSGMoveHeartbeat, false, true},
		{"rider disappears", ship, 0, opcode.SMSGDestroyObject, false, false},
		{"unrelated player disappears", ship + 1, 0, opcode.SMSGDestroyObject, false, true},
		{"rider reported dead", ship, 0, opcode.SMSGCompressedUpdateObject, false, false},
		{"queued departure", ship, 0, opcode.MSGMoveHeartbeat, true, false},
		{"queued board", 0, ship, opcode.MSGMoveHeartbeat, true, false},
		{"queued motion on other ship", ship + 1, ship + 1, opcode.MSGMoveHeartbeat, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 7, 8, 9, 0))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 7, 8, 9, 0))
			for _, guid := range []uint64{ship, ship + 1} {
				c.observe(opcode.SMSGUpdateObject, routeHullCreate(guid))
			}
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(rider, 8, 8, 9, 0))
			if tc.from != 0 {
				c.observe(opcode.MSGMoveHeartbeat, pair(t, tc.from, 4000))
			}
			scope := c.e.transport.snapshot()
			witness, ok := c.e.s.currentRouteHull(ship, scope)
			if !ok {
				t.Fatal("missing original ship")
			}
			ctx := modsession.WithRouteHull(modsession.WithRouteBodyInput(context.Background(), false), witness)
			wire := pair(t, tc.to, 5200)
			switch tc.op {
			case opcode.SMSGDestroyObject:
				wire = append(binary.LittleEndian.AppendUint64(nil, rider), 0)
			case opcode.SMSGCompressedUpdateObject:
				wire = compressedBodyUpdates(t, bodyValuesBlock(rider, 0x18, 0))
			}
			c.e.beforeScopedWrite = func() error {
				c.e.beforeScopedWrite = nil
				if tc.queued {
					c.e.observeTransportScope(tc.op, wire)
				} else {
					c.observe(tc.op, wire)
				}
				return nil
			}
			contact, err := movementinfo.MoverFrame(99, movementinfo.Info{Flags: movementinfo.SwimmingFlag | 1, X: 7, Y: 8, Z: 9, PitchPresent: true})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = c.e.writeContext(ctx, opcode.MSGMoveStartSwim, contact, scope)
			if tc.wantWrite {
				if err != nil || conn.writes != 1 {
					t.Fatalf("unaffected ship lost: writes=%d err=%v", conn.writes, err)
				}
			} else if !modcb.PacketWasNotDispatched(err) || conn.writes != 0 {
				t.Fatalf("superseded ship admitted: writes=%d err=%v", conn.writes, err)
			}
		})
	}
}

func TestRouteHullPassengerRepliesFollowReceiveOrder(t *testing.T) {
	const ship uint64 = 0x1fc0000000000034
	c, _ := newBodyContinuitySession(t)
	c.observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 7, 8, 9, 0))
	c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 7, 8, 9, 0))
	c.observe(opcode.SMSGUpdateObject, routeHullCreate(ship))
	c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(23, 7, 8, 9, 0))
	scope := c.e.transport.snapshot()
	original, ok := c.e.s.currentRouteHull(ship, scope)
	if !ok {
		t.Fatal("missing original hull")
	}
	receive := func(clock uint32) []string {
		info := movementinfo.Info{Flags: 0x200, Time: clock, X: 7, Y: 8, Z: 9, TransportPresent: true,
			Transport: movementinfo.Transport{GUID: ship, Time: clock, Seat: -1}}
		wire, err := movementinfo.MoverFrame(23, info)
		if err != nil {
			t.Fatal(err)
		}
		c.e.observeTransportScope(opcode.MSGMoveHeartbeat, wire)
		tr := &bodyUpdateTrailer{}
		p := &modv1.WorldPacket{Opcode: opcode.MSGMoveHeartbeat, Payload: wire, TransportScope: c.e.transport.snapshot()}
		if _, err := c.w.OnPacket(grpc.NewContextWithServerTransportStream(context.Background(), tr), p); err != nil {
			t.Fatal(err)
		}
		return tr.md.Get(modcb.RouteHullTrailer)
	}
	older, newest := receive(4000), receive(5200)
	if scope.MotionRevision != c.e.transport.scope.MotionRevision {
		t.Fatal("foreign heartbeat should preserve the controlled body's motion")
	}
	c.e.s.recordRouteHulls(scope, older)
	if _, ok := c.e.s.currentRouteHull(ship, scope); ok {
		t.Fatal("older parser acknowledgment supplied the later passenger frame")
	}
	c.e.s.recordRouteHulls(scope, newest)
	c.e.s.recordRouteHulls(scope, older)
	current, ok := c.e.s.currentRouteHull(ship, scope)
	if !ok || current.Birth != original.Birth || current.Packets <= original.Packets {
		t.Fatal("fresh source on the same ship was not recovered", original, current)
	}
}

func TestRouteHullParsedSourceAtPhysicalWrite(t *testing.T) {
	const ship uint64 = 0x1fc0000000000034
	for _, tc := range []struct {
		change string
		write  bool
	}{
		{"unchanged", true}, {"other hull removed", true}, {"foreign values", true},
		{"selected hull removed", false}, {"selected hull replaced", false}, {"out of range", false},
		{"path fraction changed", false}, {"malformed batch", false}, {"unparsed batch", false},
		{"old parser reply after destroy", false}, {"foreign hull in payload", false},
	} {
		t.Run(tc.change, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(0, 7, 8, 9, 0))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 7, 8, 9, 0))
			for _, guid := range []uint64{ship, ship + 1} {
				c.observe(opcode.SMSGUpdateObject, routeHullCreate(guid))
			}
			view, err := c.w.Snapshot(context.Background(), &worldv1.SnapshotRequest{})
			if err != nil {
				t.Fatal(err)
			}
			witness := modcb.RouteHull{GUID: ship, Packets: view.ObservedPackets + view.MalformedPackets + view.IgnoredPackets}
			for _, hull := range view.MovingTransports {
				if hull.Guid == ship {
					witness.Birth = hull.MovingTransport.ObjectRevision
				}
			}
			if !witness.Valid() {
				t.Fatal("missing selected hull in actual World")
			}
			ctx := modsession.WithRouteHull(modsession.WithRouteBodyInput(context.Background(), false), witness)
			// First detached water contact still references the selected ship.
			i := movementinfo.Info{Flags: movementinfo.SwimmingFlag | 1, X: 7, Y: 8, Z: 9, PitchPresent: true}
			op := uint32(opcode.MSGMoveStartSwim)
			if tc.change == "foreign hull in payload" {
				op = opcode.MSGMoveStartForward
				i = movementinfo.Info{Flags: 0x201, TransportPresent: true, Transport: movementinfo.Transport{GUID: ship + 1, Seat: -1}}
			}
			wire, err := movementinfo.MoverFrame(99, i)
			if err != nil {
				t.Fatal(err)
			}
			source := c.e.transport.snapshot()
			destroy := func(guid uint64) {
				c.observe(opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, guid), 0))
			}
			c.e.beforeScopedWrite = func() error {
				c.e.beforeScopedWrite = nil
				switch tc.change {
				case "other hull removed":
					destroy(ship + 1)
				case "selected hull removed":
					destroy(ship)
				case "selected hull replaced":
					c.observe(opcode.SMSGUpdateObject, routeHullCreate(ship))
				case "foreign values":
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(23, 0x19, 22)))
				case "out of range":
					gone := append(binary.LittleEndian.AppendUint32([]byte{4}, 1), scopePackedGUID(ship)...)
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, gone))
				case "path fraction changed":
					record := append([]byte{0}, scopePackedGUID(ship)...)
					record = binary.LittleEndian.AppendUint32(append(record, 1), 1<<14)
					record = binary.LittleEndian.AppendUint32(record, 0x44440001)
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, record))
				case "malformed batch":
					c.observe(opcode.SMSGCompressedUpdateObject, []byte{1, 2, 3})
				case "unparsed batch", "old parser reply after destroy":
					c.e.observeTransportScope(opcode.SMSGUpdateObject, routeHullCreate(ship+1))
					if tc.change == "old parser reply after destroy" {
						before := c.e.transport.snapshot()
						destroy(ship)
						c.e.s.recordRouteHulls(before, []string{witness.Wire()})
						c.e.s.recordParsedBodyUpdate(before, modcb.BodyUpdateHarmless)
					}
				}
				return nil
			}
			_, _, err = c.e.writeContext(ctx, op, wire, source)
			if tc.write {
				if err != nil || conn.writes != 1 {
					t.Fatalf("valid frame withheld: writes=%d err=%v", conn.writes, err)
				}
			} else if !modcb.PacketWasNotDispatched(err) || conn.writes != 0 {
				t.Fatalf("superseded hull frame: writes=%d err=%v", conn.writes, err)
			}
		})
	}
}
