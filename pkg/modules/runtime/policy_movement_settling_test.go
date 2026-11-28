package modrt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestOwnedWalkCommandUsesTypedBodyOnce(t *testing.T) {
	ctx := context.Background()
	m, native, cb := friendlyEncounterManager(t)
	cb.world.ObservedPackets = 4
	book := binary.LittleEndian.AppendUint32([]byte{0, 1, 0}, 585)
	native.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: append(book, 0, 0, 0, 0)})
	source := followFallbackBundle(t)
	host, err := m.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	step := func() (map[string]any, *behavior.ActionReceipt) {
		t.Helper()
		current, seq := observeFriendlyEncounter(t, m, host)
		out, receipt, err := host.Step(current, map[string]any{"kind": "encounter", "observationSequence": seq}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out, receipt
	}
	send := func(value string) {
		t.Helper()
		_, err := m.InvokeJSON(ctx, "session.send_event", json.RawMessage(`{"sessionId":"friendly-encounter","characterGuid":"22","kind":"command","value":`+value+`}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	const walk = `{"task":"move_body","id":"one-step","selfGuid":"22","visitId":"1","bearingRadians":-0.4,"durationMs":180,"jump":false}`
	step()
	send(strings.Replace(walk, `"jump":false`, `"jump":true`, 1))
	send(strings.Replace(walk, `"visitId":"1"`, `"visitId":"3"`, 1))
	if _, r := step(); r != nil {
		t.Fatal("unbound or jumping request entered", r)
	}
	send(walk)
	out, r := step()
	a, _ := out["action"].(map[string]any)
	// This actual native body has no received run speed. Its precise refusal
	// proves the request reached the own-body writer without claiming motion.
	if a["bearingRadians"] != -.4 || a["durationMs"] != float64(180) || a["jump"] != false || out["followFormation"] != nil ||
		r == nil || r.Outcome != behavior.ActionNotDispatched || r.Payload["self_guid"] != "22" || r.Payload["visit_id"] != "1" ||
		r.Payload["policy_request_id"] != "move_body:one-step" || r.Payload["sequence_id"] != a["observationSequence"] ||
		r.Payload["grpc_message"] != "observed normal movement speed required" || len(cb.writes) != 0 {
		t.Fatal(out, r)
	}
	if _, err = host.Reload(ctx, source+"\n-- same body, updated source\n", host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	send(walk)
	if _, r = step(); r != nil {
		t.Fatal("duplicate command reissued after reload", r)
	}
}

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
