package modrt

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPolicyBodyHopAcceptsOnlyOwnNormalFallingLayout(t *testing.T) {
	packet := policyMovementPacket(9)
	binary.LittleEndian.PutUint32(packet[2:], 0x1001)
	for _, value := range []float32{7.955547, 0, 1, 7} {
		packet = binary.LittleEndian.AppendUint32(packet, math.Float32bits(value))
	}
	op := &policySpell{movement: &policyMovement{self: 9, body: true, jump: true}}
	if !op.matchesMovementPacket(opcode.MSGMoveJump, packet) {
		t.Fatal("normal own jump rejected")
	}
	for _, mode := range []string{"old_action", "foreign_body", "truncated", "flying", "extra_flags", "vertical_speed"} {
		body := append([]byte(nil), packet...)
		candidate := *op
		movement := *op.movement
		candidate.movement = &movement
		switch mode {
		case "old_action":
			movement.body = false
		case "foreign_body":
			body[1] = 8
		case "truncated":
			body = body[:len(body)-1]
		case "flying":
			binary.LittleEndian.PutUint32(body[2:], 0x2001001)
		case "extra_flags":
			binary.LittleEndian.PutUint16(body[6:], 0x20)
		case "vertical_speed":
			binary.LittleEndian.PutUint32(body[32:], math.Float32bits(20))
		}
		if candidate.matchesMovementPacket(opcode.MSGMoveJump, body) {
			t.Fatal("unrelated or unsupported jump admitted", mode)
		}
	}
}

func policyMovementPacket(guid uint64) []byte {
	// PackedGUID(9), flags, flags2, time, xyzo, fallTime.
	out := make([]byte, 32)
	out[0], out[1] = 1, byte(guid)
	return out
}

func TestPolicyMovementAttributionAllowsOnlyCurrentBodyGroundPackets(t *testing.T) {
	op := &policySpell{movement: &policyMovement{self: 9}}
	packet := policyMovementPacket(9)
	for _, test := range []struct {
		name string
		code uint32
		body []byte
		want bool
	}{
		{"heartbeat", opcode.MSGMoveHeartbeat, packet, true},
		{"start", opcode.MSGMoveStartForward, packet, true},
		{"stop", opcode.MSGMoveStop, packet, true},
		{"face", opcode.MSGMoveSetFacing, packet, true},
		{"other body", opcode.MSGMoveHeartbeat, policyMovementPacket(8), false},
		{"cast", opcode.CMSGCastSpell, packet, false},
		{"attack stop", opcode.CMSGAttackstop, nil, false},
		{"truncated", opcode.MSGMoveHeartbeat, packet[:31], false},
		{"trailing", opcode.MSGMoveHeartbeat, append(append([]byte(nil), packet...), 0), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := op.matchesActionPacket(test.code, test.body); got != test.want {
				t.Fatalf("admitted=%v want=%v", got, test.want)
			}
		})
	}
	for _, flags := range []uint32{1, movementflags.PassiveModifiers, 1 | movementflags.PassiveModifiers, 0x200000, 0x200, 0x8000000} {
		p := append([]byte(nil), packet...)
		binary.LittleEndian.PutUint32(p[2:], flags)
		if got := op.matchesMovementPacket(opcode.MSGMoveHeartbeat, p); got != (flags & ^(uint32(1)|movementflags.PassiveModifiers) == 0) {
			t.Fatalf("flags %#x admitted=%v", flags, got)
		}
	}
	invalid := append([]byte(nil), packet...)
	binary.LittleEndian.PutUint32(invalid[12:], math.Float32bits(float32(math.NaN())))
	if op.matchesMovementPacket(opcode.MSGMoveHeartbeat, invalid) {
		t.Fatal("nonfinite pose admitted")
	}
	op.movement.stop = true
	if op.matchesMovementPacket(opcode.MSGMoveStartForward, packet) || !op.matchesMovementPacket(opcode.MSGMoveStop, packet) {
		t.Fatal("stop request can start motion")
	}
}

func TestPolicyMovementInvocationCanWriteStepAndStopWithoutCastLedgerReplay(t *testing.T) {
	m := New(nil, nil, nil)
	t.Cleanup(m.cancel)
	origin := modsession.WriteOrigin{SessionID: "move", Module: actionsModule, Writer: "lua", PolicyRevision: "p", ActionID: "7"}
	op := &policySpell{capability: "approach_target", movement: &policyMovement{self: 9}, origin: origin, admitted: true}
	m.policySpells.active = map[string]*policySpell{"token": op}
	var writes []modsession.WriteOrigin
	m.session.SendPacket = func(ctx context.Context, _ uint32, _ []byte) error {
		writes = append(writes, modsession.WriteOriginFromContext(ctx))
		return nil
	}
	packet := policyMovementPacket(9)
	for _, code := range []uint32{opcode.MSGMoveHeartbeat, opcode.MSGMoveStop} {
		if err := m.writePolicySpell(context.Background(), "token", "move", actionsModule, code, packet); err != nil {
			t.Fatal(err)
		}
	}
	if len(writes) != 2 || writes[0] != origin || writes[1] != origin {
		t.Fatal("movement lost Lua attribution", writes)
	}
	if err := m.writePolicySpell(context.Background(), "token", "move", actionsModule, opcode.CMSGAttackstop, nil); status.Code(err) != codes.PermissionDenied {
		t.Fatal("movement authorized attack stop", err)
	}
	op.admitted = false
	if err := m.writePolicySpell(context.Background(), "token", "move", actionsModule, opcode.MSGMoveHeartbeat, packet); status.Code(err) != codes.PermissionDenied {
		t.Fatal("closed movement callback replayed", err)
	}
}
