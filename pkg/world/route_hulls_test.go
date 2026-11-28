package world

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
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
