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

func movementOrder(order uint32) []byte {
	return binary.LittleEndian.AppendUint32([]byte{1, 99}, order)
}

func TestRootAcknowledgementExactServerOrderPoseAndClientClock(t *testing.T) {
	var m movementControl
	m.observed(opcode.SMSGLoginVerifyWorld, positionBody(0, 1, 2, 3, .5), 99, 12)
	op, reply, next, ok := m.acknowledgement(opcode.SMSGForceMoveRoot, movementOrder(7), 99, 1000)
	if !ok || op != opcode.CMSGForceMoveRootAck || hex.EncodeToString(reply) != "016307000000000800000000e80300000000803f00000040000040400000003f00000000" {
		t.Fatal(op, hex.EncodeToString(reply), ok)
	}
	m.info = next
	op, reply, _, ok = m.acknowledgement(opcode.SMSGForceMoveUnroot, movementOrder(8), 99, 1001)
	if !ok || op != opcode.CMSGForceMoveUnrootAck || binary.LittleEndian.Uint32(reply[6:]) != 0 || !bytes.Equal(reply[16:32], positionBody(0, 1, 2, 3, .5)[4:]) {
		t.Fatal(op, reply, ok)
	}
	// A successful own movement supplies its actual emitted pose, preserving
	// only observed plain-ground flags. ROOT clears all moving/turning flags.
	info := bytes.Clone(m.info)
	binary.LittleEndian.PutUint32(info, 1|0x40|0x10000000)
	binary.LittleEndian.PutUint32(info[10:], math.Float32bits(4))
	p := append([]byte{1, 99}, info...)
	m.sent(opcode.MSGMoveHeartbeat, p, 99)
	p[12] = 0 // caller cannot mutate the retained baseline
	_, reply, _, ok = m.acknowledgement(opcode.SMSGForceMoveRoot, movementOrder(9), 99, 1002)
	if !ok || binary.LittleEndian.Uint32(reply[6:]) != 0x10000800 || math.Float32frombits(binary.LittleEndian.Uint32(reply[16:])) != 4 {
		t.Fatal(reply, ok)
	}
}

func TestRootAcknowledgementRejectsMalformedUnknownAndUnsupportedPoses(t *testing.T) {
	valid := movementOrder(7)
	for n := 0; n <= len(valid)+1; n++ {
		if n == len(valid) {
			continue
		}
		p := append(bytes.Clone(valid), 0)[:n]
		var m movementControl
		m.observed(opcode.SMSGLoginVerifyWorld, positionBody(0, 1, 2, 3, .5), 99, 1)
		before := bytes.Clone(m.info)
		if _, _, _, ok := m.acknowledgement(opcode.SMSGForceMoveRoot, p, 99, 2); ok || !bytes.Equal(before, m.info) {
			t.Fatal("malformed control changed/acknowledged", n)
		}
	}
	for _, self := range []uint64{0, 4, 0xf130000001000001} {
		var m movementControl
		m.observed(opcode.SMSGLoginVerifyWorld, positionBody(0, 1, 2, 3, .5), 99, 1)
		if _, _, _, ok := m.acknowledgement(opcode.SMSGForceMoveRoot, valid, self, 2); ok {
			t.Fatal("foreign control acknowledged", self)
		}
	}
	for _, change := range []func([]byte){
		func(p []byte) { binary.LittleEndian.PutUint32(p, 0x200) },
		func(p []byte) { binary.LittleEndian.PutUint32(p, 0x1000) },
		func(p []byte) { binary.LittleEndian.PutUint16(p[4:], 1) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[10:], 0x7fc00000) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[14:], 0x7f800000) },
	} {
		var m movementControl
		m.observed(opcode.SMSGLoginVerifyWorld, positionBody(0, 1, 2, 3, .5), 99, 1)
		change(m.info)
		if _, _, _, ok := m.acknowledgement(opcode.SMSGForceMoveRoot, valid, 99, 2); ok {
			t.Fatal("unsupported baseline acknowledged")
		}
	}
	var m movementControl
	if _, _, _, ok := m.acknowledgement(opcode.SMSGForceMoveRoot, valid, 99, 2); ok {
		t.Fatal("invented unknown pose")
	}
}

func TestRootAcknowledgementTransferAndAuthoritativePoseLifecycle(t *testing.T) {
	var m movementControl
	m.observed(opcode.SMSGLoginVerifyWorld, positionBody(0, 1, 2, 3, .5), 99, 1)
	m.observed(opcode.SMSGTransferPending, []byte{1, 0, 0, 0}, 99, 2)
	if _, _, _, ok := m.acknowledgement(opcode.SMSGForceMoveUnroot, movementOrder(8), 99, 3); ok {
		t.Fatal("old map pose survived transfer")
	}
	m.observed(opcode.SMSGNewWorld, positionBody(1, 10, 20, 30, .6), 99, 4)
	_, reply, _, ok := m.acknowledgement(opcode.SMSGForceMoveUnroot, movementOrder(9), 99, 5)
	if !ok || math.Float32frombits(binary.LittleEndian.Uint32(reply[16:])) != 10 {
		t.Fatal("new map pose not used", reply, ok)
	}
	m.observed(opcode.MSGMoveTeleportAck, nearTeleportGolden(t), 99, 6)
	if !plainMovementInfo(m.info) || math.Float32frombits(binary.LittleEndian.Uint32(m.info[10:])) != 1 {
		t.Fatal("authoritative near correction not retained")
	}
	m.observed(opcode.MSGMoveTeleportAck, []byte{1, 99}, 99, 7)
	if m.info != nil {
		t.Fatal("malformed authoritative correction retained old pose")
	}
}

func TestSessionAcknowledgesRootBeforeFollowingGraveyardTeleport(t *testing.T) {
	r := newObserved()
	trigger, done := make(chan struct{}), make(chan struct{})
	c := peerConnection(t, func(p *testPeer) {
		p.login()
		<-trigger
		for _, pair := range []struct {
			request  uint16
			response uint32
			flags    uint32
		}{
			{opcode.SMSGForceMoveRoot, opcode.CMSGForceMoveRootAck, 0x800},
			{opcode.SMSGForceMoveUnroot, opcode.CMSGForceMoveUnrootAck, 0},
		} {
			p.send(pair.request, movementOrder(7))
			ack := p.expect(pair.response)
			if len(ack) != 36 || !bytes.Equal(ack[:6], movementOrder(7)) || binary.LittleEndian.Uint32(ack[6:]) != pair.flags || !bytes.Equal(ack[16:32], positionBody(0, 1, 2, 3, .5)[4:]) {
				t.Error("root/unroot ACK did not preserve actual order/pose", ack)
			}
		}
		p.send(opcode.SMSGTransferPending, []byte{1, 0, 0, 0})
		p.send(opcode.SMSGNewWorld, positionBody(0, 10, 20, 30, .5))
		p.expect(opcode.MSGMoveWorldportAck)
		p.send(opcode.SMSGForceMoveUnroot, movementOrder(10))
		ack := p.expect(opcode.CMSGForceMoveUnrootAck)
		if math.Float32frombits(binary.LittleEndian.Uint32(ack[16:])) != 10 {
			t.Error("graveyard unroot used old map pose", ack)
		}
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
		t.Fatal("root handshake stalled")
	}
	for _, op := range []uint32{opcode.CMSGForceMoveRootAck, opcode.CMSGForceMoveUnrootAck} {
		if err := s.SendPacket(context.Background(), op, nil); status.Code(err) != codes.PermissionDenied {
			t.Fatal("module can duplicate session control ACK", op, err)
		}
	}
	seen := 0
	for seen < 3 {
		select {
		case p := <-r.packets:
			if p.opcode == opcode.SMSGForceMoveRoot || p.opcode == opcode.SMSGForceMoveUnroot {
				seen++
			}
		case <-time.After(time.Second):
			t.Fatal("control packets not published unchanged")
		}
	}
}

func FuzzRootAcknowledgementBounded(f *testing.F) {
	f.Add(uint16(opcode.SMSGForceMoveRoot), movementOrder(1), []byte{})
	f.Fuzz(func(t *testing.T, op uint16, p, info []byte) {
		m := movementControl{info: info}
		_, b, next, ok := m.acknowledgement(op, p, 99, 1)
		if ok && (len(b) > 43 || len(next) != 30 || !plainMovementInfo(next)) {
			t.Fatal("invalid bounded ACK", b)
		}
	})
}
