package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func nearTeleportGolden(t *testing.T) []byte {
	t.Helper()
	// Server packet: packed player99, order7, standing MovementInfo with server
	// time123, XYZ1/2/3, orientation0.5, fallTime0. No destination is client input.
	p, err := hex.DecodeString("0163070000000000000000007b0000000000803f00000040000040400000003f00000000")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTeleportAcknowledgementsGoldenUseServerOrderAndSharedClock(t *testing.T) {
	op, body, ok := teleportAcknowledgement(opcode.MSGMoveTeleportAck, nearTeleportGolden(t), 99, 1000)
	if !ok || op != opcode.MSGMoveTeleportAck || hex.EncodeToString(body) != "016307000000e8030000" {
		t.Fatal(op, body, ok)
	}
	op, body, ok = teleportAcknowledgement(opcode.SMSGNewWorld, positionBody(1, 10, 20, 30, 0.5), 99, 1000)
	if !ok || op != opcode.MSGMoveWorldportAck || len(body) != 0 {
		t.Fatal(op, body, ok)
	}
}

func TestTeleportAcknowledgementsRejectEveryTruncationForeignGUIDAndBadCoordinates(t *testing.T) {
	for _, packet := range []struct {
		op   uint16
		body []byte
	}{{opcode.MSGMoveTeleportAck, nearTeleportGolden(t)}, {opcode.SMSGNewWorld, positionBody(1, 10, 20, 30, 0.5)}} {
		for n := 0; n < len(packet.body); n++ {
			if _, _, ok := teleportAcknowledgement(packet.op, packet.body[:n], 99, 1000); ok {
				t.Fatal("truncated teleport acknowledged", packet.op, n)
			}
		}
		if _, _, ok := teleportAcknowledgement(packet.op, append(bytes.Clone(packet.body), 0), 99, 1000); ok {
			t.Fatal("trailing data acknowledged")
		}
	}
	p := nearTeleportGolden(t)
	for _, self := range []uint64{0, 4, 0xf130000001000001} {
		if _, _, ok := teleportAcknowledgement(opcode.MSGMoveTeleportAck, p, self, 1); ok {
			t.Fatal("foreign/nonplayer mover acknowledged", self)
		}
	}
	for _, bits := range []uint32{0x7fc00000, 0x7f800000, math.Float32bits(50000)} {
		near, far := bytes.Clone(p), positionBody(0, 1, 2, 3, 0)
		binary.LittleEndian.PutUint32(near[16:], bits)
		binary.LittleEndian.PutUint32(far[4:], bits)
		if _, _, ok := teleportAcknowledgement(opcode.MSGMoveTeleportAck, near, 99, 1); ok {
			t.Fatal("bad near coordinate acknowledged")
		}
		if _, _, ok := teleportAcknowledgement(opcode.SMSGNewWorld, far, 99, 1); ok {
			t.Fatal("bad far coordinate acknowledged")
		}
	}
	if _, _, ok := teleportAcknowledgement(opcode.MSGMoveTeleport, p, 99, 1); ok {
		t.Fatal("ordinary broadcast treated as teleport request")
	}
}

func TestTeleportAcknowledgementValidatesCompleteOptionalMovementTails(t *testing.T) {
	// Combined transport/interpolation, swimming pitch, fall jump and spline
	// elevation follows WorldSession.cpp WriteMovementInfo ordering.
	p := []byte{1, 99, 7, 0, 0, 0}
	p = binary.LittleEndian.AppendUint32(p, 0x200|0x200000|0x1000|0x04000000)
	p = binary.LittleEndian.AppendUint16(p, 0x400)
	p = binary.LittleEndian.AppendUint32(p, 123)
	for _, v := range []float32{1, 2, 3, 0.5} {
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(v))
	}
	p = append(p, 1, 42)
	for _, v := range []float32{1, 2, 3, 0.5} {
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(v))
	}
	p = binary.LittleEndian.AppendUint32(p, 12)
	p = append(p, 0)
	p = binary.LittleEndian.AppendUint32(p, 13)
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(0.1))
	p = binary.LittleEndian.AppendUint32(p, 0)
	for _, v := range []float32{1, 0, 1, 7, 0.1} {
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(v))
	}
	if _, _, ok := teleportAcknowledgement(opcode.MSGMoveTeleportAck, p, 99, 1); !ok {
		t.Fatal("valid optional movement body rejected")
	}
	for n := 0; n < len(p); n++ {
		if _, _, ok := teleportAcknowledgement(opcode.MSGMoveTeleportAck, p[:n], 99, 1); ok {
			t.Fatal("optional movement tail truncated", n)
		}
	}
	if _, _, ok := teleportAcknowledgement(opcode.MSGMoveTeleportAck, append(bytes.Clone(p), 0), 99, 1); ok {
		t.Fatal("optional movement trailing byte accepted")
	}
}

func TestSessionAcknowledgesServerTeleportsAndStillPublishesOriginalPackets(t *testing.T) {
	r := newObserved()
	trigger, done := make(chan struct{}), make(chan struct{})
	near, far := nearTeleportGolden(t), positionBody(1, 10, 20, 30, 0.5)
	c := peerConnection(t, func(p *testPeer) {
		p.login()
		<-trigger
		p.send(opcode.MSGMoveTeleportAck, near)
		ack := p.expect(opcode.MSGMoveTeleportAck)
		if len(ack) != 10 || !bytes.Equal(ack[:6], near[:6]) || binary.LittleEndian.Uint32(ack[6:]) > 5000 {
			t.Error("wrong near ACK order/clock", ack)
		}
		p.send(opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
		p.send(opcode.SMSGNewWorld, far)
		if ack := p.expect(opcode.MSGMoveWorldportAck); len(ack) != 0 {
			t.Error("far ACK contains invented coordinates", ack)
		}
		p.sync(99)
		close(done)
		p.closed()
	})
	s, err := enter(context.Background(), c, Realm{}, Character{99, "Mira"}, nil, quietTimings(), Options{Modules: r})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	close(trigger)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("teleport handshake stalled")
	}
	seenNear, seenFar := false, false
	for !seenNear || !seenFar {
		select {
		case p := <-r.packets:
			if p.opcode == opcode.MSGMoveTeleportAck {
				seenNear = bytes.Equal(p.body, near)
			}
			if p.opcode == opcode.SMSGNewWorld {
				seenFar = bytes.Equal(p.body, far)
			}
		case <-time.After(time.Second):
			t.Fatal("teleport observation was not published unchanged")
		}
	}
	for _, op := range []uint32{opcode.MSGMoveTeleportAck, opcode.MSGMoveWorldportAck} {
		if err := s.SendPacket(context.Background(), op, nil); status.Code(err) != codes.PermissionDenied {
			t.Fatal("module can duplicate session-owned ACK", op, err)
		}
	}
}

func FuzzTeleportAcknowledgementBounded(f *testing.F) {
	f.Add(uint16(opcode.MSGMoveTeleportAck), []byte{1, 99}, uint64(99))
	f.Add(uint16(opcode.SMSGNewWorld), positionBody(0, 1, 2, 3, 0), uint64(99))
	f.Fuzz(func(t *testing.T, op uint16, p []byte, self uint64) {
		replyOp, b, ok := teleportAcknowledgement(op, p, self, 1)
		if ok && (replyOp != opcode.MSGMoveTeleportAck && replyOp != opcode.MSGMoveWorldportAck || len(b) > 17) {
			t.Fatal("invalid teleport acknowledgement", replyOp, b)
		}
	})
}
