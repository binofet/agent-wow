package modrt

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func policySettlingPacket(guid uint64) []byte {
	p := policyMovementPacket(guid)
	binary.LittleEndian.PutUint32(p[2:], 0x1000)
	binary.LittleEndian.PutUint32(p[28:], 300)
	for _, value := range []float32{0, 0, 1, 0} {
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(value))
	}
	return p
}

func TestPolicyStepAdmitsNormalGravitySettlingButNeverAnUnrequestedJump(t *testing.T) {
	op := &policySpell{movement: &policyMovement{self: 9, body: true}}
	fall := policySettlingPacket(9)
	if !op.matchesMovementPacket(opcode.MSGMoveHeartbeat, fall) || !op.matchesMovementPacket(opcode.MSGMoveFallLand, policyMovementPacket(9)) {
		t.Fatal("a step input could not settle through normal fall and land packets")
	}
	for _, mode := range []string{"jump_opcode", "jump_velocity", "horizontal_fall", "forward_fall", "foreign_body", "truncated", "flying", "bad_direction", "falling_land", "route_only"} {
		t.Run(mode, func(t *testing.T) {
			packet := append([]byte(nil), fall...)
			candidate := *op
			movement := *op.movement
			candidate.movement = &movement
			code := uint32(opcode.MSGMoveHeartbeat)
			switch mode {
			case "jump_opcode":
				code = opcode.MSGMoveJump
				movement.jump = true // A requested jump still cannot use a zero-speed fall as its jump packet.
			case "jump_velocity":
				binary.LittleEndian.PutUint32(packet[32:], math.Float32bits(7.955547))
			case "horizontal_fall":
				binary.LittleEndian.PutUint32(packet[44:], math.Float32bits(7))
			case "forward_fall":
				binary.LittleEndian.PutUint32(packet[2:], 0x1001)
			case "foreign_body":
				packet[1] = 8
			case "truncated":
				packet = packet[:len(packet)-1]
			case "flying":
				binary.LittleEndian.PutUint32(packet[2:], 0x2001000)
			case "bad_direction":
				binary.LittleEndian.PutUint32(packet[40:], math.Float32bits(2))
			case "falling_land":
				code = opcode.MSGMoveFallLand
			case "route_only":
				movement.body = false
			}
			if candidate.matchesMovementPacket(code, packet) {
				t.Fatal("unsupported or foreign settling packet admitted", mode)
			}
		})
	}
}

func TestPolicyBodyMovementReceiptsDistinguishMeasuredRefusalFromWrittenSettling(t *testing.T) {
	for _, mode := range []string{"refused", "settled", "stale_refusal", "written_refusal"} {
		t.Run(mode, func(t *testing.T) {
			m := New(nil, nil, nil)
			t.Cleanup(m.cancel)
			op := &policySpell{token: "body-token", capability: "move_body", encounterVisit: 1,
				movement: &policyMovement{self: 9, body: true, sequence: 17}}
			writes := 0
			m.session.SendPacket = func(context.Context, uint32, []byte) error { writes++; return nil }
			conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithUnaryInterceptor(func(ctx context.Context, method string, _, response any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
					if method == grpc_health_v1.Health_Check_FullMethodName {
						response.(*grpc_health_v1.HealthCheckResponse).Status = grpc_health_v1.HealthCheckResponse_SERVING
						return nil
					}
					state := response.(*actionsv1.State)
					state.BodyMovement = &actionsv1.BodyMovementState{SequenceId: 17, VisitId: 1, Phase: "local_physics_refused", Detail: "no measured floor below the actual start"}
					if mode == "stale_refusal" {
						state.BodyMovement.SequenceId = 16
					}
					if mode == "settled" || mode == "written_refusal" {
						for _, packet := range []struct {
							code uint32
							body []byte
						}{
							{opcode.MSGMoveHeartbeat, policySettlingPacket(9)},
							{opcode.MSGMoveFallLand, policyMovementPacket(9)},
							{opcode.MSGMoveStop, policyMovementPacket(9)},
						} {
							if err := m.writePolicySpell(ctx, op.token, "move", actionsModule, packet.code, packet.body); err != nil {
								t.Fatal(err)
							}
						}
						if mode == "settled" {
							state.BodyMovement.Phase, state.BodyMovement.Detail, state.BodyMovement.TransportWritten = "body_settled_written", "", true
						}
					}
					return nil
				}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			ctx := modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{SessionID: "move", Module: actionsModule, Writer: "lua", ActionID: "17"})
			outcome, payload := m.invokePolicyMovement(ctx, &instance{conn: conn, healthy: true}, moddisc.Method{}, &emptypb.Empty{}, op)
			if mode == "refused" {
				if outcome != behavior.ActionNotDispatched || payload["phase"] != "local_physics_refused" || payload["detail"] != "no measured floor below the actual start" || writes != 0 {
					t.Fatal("known no-write refusal was reported as uncertain transport", outcome, payload, writes)
				}
			} else if mode == "stale_refusal" {
				if outcome == behavior.ActionNotDispatched || payload["detail"] == "no measured floor below the actual start" {
					t.Fatal("a prior sequence supplied proof for the new input", outcome, payload)
				}
			} else if outcome != behavior.ActionSettledUnverified || payload["transport_written"] != true || payload["effect_observed"] != false || writes != 3 {
				t.Fatal("normal movement writes were reported as a confirmed server effect or a non-dispatch", outcome, payload, writes)
			}
			if op.admitted || len(m.policySpells.active) != 0 {
				t.Fatal("completed movement retained its private write token")
			}
		})
	}
}

func TestPolicyBodyMovementAcceptsEveryBoundedShortInputDuration(t *testing.T) {
	vm, err := behavior.NewSession(context.Background(), "move", inertEncounterPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	m := New(packagedPolicySpellRegistry(t), nil, nil)
	t.Cleanup(m.cancel)
	m.instances[actionsModule] = &instance{healthy: true}
	p := &SessionPolicy{vm: vm, manager: m, body: "9"}
	for _, duration := range []float64{0, 1, 50, 99, 100, 500, 501} {
		_, err := p.resolveBodyMovement(map[string]any{"sessionId": "move", "selfGuid": "9", "visitId": "1", "sequenceId": "17", "durationMs": duration})
		if duration >= 1 && duration <= 500 {
			if err != nil {
				t.Fatal("bounded short input was refused by the runtime adapter", duration, err)
			}
		} else if status.Code(err) != codes.InvalidArgument {
			t.Fatal("unbounded input admitted", duration, err)
		}
	}
}
