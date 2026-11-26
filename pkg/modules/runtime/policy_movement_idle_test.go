package modrt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func policyBodyIdleAction() map[string]any {
	return map[string]any{"capability": "move_body", "sessionId": friendlyEncounterSession, "selfGuid": "22", "visitId": "1",
		"sequenceId": "17", "bearingRadians": float64(.25), "durationMs": float64(100), "jump": false}
}

func TestPolicyBodyMovementOptionalIdleTokenMapsExactlyWithoutNewPacketPrivileges(t *testing.T) {
	for _, token := range []string{"", "IdLe-Token._:aBc123"} {
		name := "legacy_tokenless"
		if token != "" {
			name = "exact_idle_token"
		}
		t.Run(name, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			x.handle = func(ctx context.Context, method string, request, result proto.Message) error {
				if method != actionsv1.BuddyActions_MoveBody_FullMethodName {
					t.Fatal("body input routed a different capability", method)
				}
				r := request.ProtoReflect()
				field := r.Descriptor().Fields().ByName("idle_token")
				if field == nil || field.Number() != 8 || field.Kind() != protoreflect.StringKind || r.Get(field).String() != token ||
					r.Get(r.Descriptor().Fields().ByName("session_id")).String() != friendlyEncounterSession ||
					r.Get(r.Descriptor().Fields().ByName("self_guid")).Uint() != 22 ||
					r.Get(r.Descriptor().Fields().ByName("visit_id")).Uint() != 1 ||
					r.Get(r.Descriptor().Fields().ByName("sequence_id")).Uint() != 17 ||
					r.Get(r.Descriptor().Fields().ByName("bearing_radians")).Float() != .25 ||
					r.Get(r.Descriptor().Fields().ByName("duration_ms")).Uint() != 100 {
					t.Fatal("optional opaque token or existing input binding changed", request)
				}
				incoming := instanceEntryIncoming(ctx)
				writer := x.m.modulePacketWriter(friendlyEncounterSession, actionsModule)
				if token != "" {
					if err := writer(incoming, opcode.MSGMoveStop, policyMovementPacket(22)); !modcb.PacketWasNotDispatched(err) {
						t.Fatal("idle-bound body input used unscoped movement", err)
					}
					writer = func(ctx context.Context, code uint32, body []byte) error {
						return x.m.moduleScopedPacketWriter(friendlyEncounterSession, actionsModule)(ctx, code, body, instanceEntryScope())
					}
				}
				for _, denied := range []struct {
					code uint32
					body []byte
				}{
					{opcode.CMSGAreatrigger, []byte{123, 0, 0, 0}},
					{opcode.CMSGCastSpell, nil},
					{opcode.MSGMoveHeartbeat, policyMovementPacket(23)},
					{opcode.MSGMoveJump, policyMovementPacket(22)},
				} {
					if err := writer(incoming, denied.code, denied.body); !modcb.PacketWasNotDispatched(err) {
						t.Fatal("idle token expanded body packet authority", denied.code, err)
					}
				}
				for _, code := range []uint32{opcode.MSGMoveHeartbeat, opcode.MSGMoveStop} {
					if err := writer(incoming, code, policyMovementPacket(22)); err != nil {
						t.Fatal("ordinary own body packet was refused", code, err)
					}
				}
				// This synthetic typed boundary response is not a physical floor
				// or arrival observation. Native lease/physics tests own admission.
				proto.Merge(result, &actionsv1.State{SessionId: friendlyEncounterSession, SelfGuid: 22,
					BodyMovement: &actionsv1.BodyMovementState{SequenceId: 17, VisitId: 1, Phase: "body_input_written", TransportWritten: true}})
				return nil
			}
			action := policyBodyIdleAction()
			if token != "" {
				action["idleToken"] = token
			}
			receipt := x.step(t, action)
			if receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["phase"] != "body_input_written" ||
				receipt.Payload["transport_written"] != true || receipt.Payload["effect_observed"] != false || len(x.calls) != 1 || len(x.callback.writes) != 2 {
				t.Fatal("optional token changed physical-write receipt semantics", receipt, x.calls, x.callback.writes)
			}
			if len(x.m.policySpells.active) != 0 {
				t.Fatal("body input retained its finished packet token")
			}
		})
	}
}

func TestPolicyBodyMovementRejectsMalformedOptionalIdleToken(t *testing.T) {
	x := newInstanceEntryFixture(t)
	for _, value := range []any{"", " token", "token ", strings.Repeat("a", 129), float64(1), nil} {
		action := policyBodyIdleAction()
		action["idleToken"] = value
		if _, err := x.p.resolveBodyMovement(action); status.Code(err) != codes.InvalidArgument || len(x.calls) != 0 || len(x.callback.writes) != 0 {
			t.Fatal("malformed optional custody was dropped or dispatched", value, err, x.calls)
		}
	}
}

func TestPolicyIdleBodyScopedRefusalAndUnknownNeverPermitUnscopedReplay(t *testing.T) {
	for _, mode := range []string{"first_not_dispatched", "partial_not_dispatched", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			attempts, written := 0, 0
			x.m.session.SendScopedPacket = func(context.Context, uint32, []byte, *modv1.TransportScope) error {
				attempts++
				if mode == "unknown" {
					return status.Error(codes.Unknown, "synthetic uncertain scoped movement")
				}
				if mode == "first_not_dispatched" || attempts == 2 {
					return modcb.PacketNotDispatchedError("synthetic Core visit replacement")
				}
				written++
				return nil
			}
			var token string
			x.handle = func(ctx context.Context, _ string, _, result proto.Message) error {
				incoming := instanceEntryIncoming(ctx)
				token, _ = modcb.IncomingPolicySpell(incoming)
				writer := x.m.moduleScopedPacketWriter(friendlyEncounterSession, actionsModule)
				err := writer(incoming, opcode.MSGMoveHeartbeat, policyMovementPacket(22), instanceEntryScope())
				if mode == "partial_not_dispatched" {
					if err != nil {
						t.Fatal(err)
					}
					err = writer(incoming, opcode.MSGMoveStop, policyMovementPacket(22), instanceEntryScope())
				}
				if err == nil {
					t.Fatal("scoped transport fixture did not refuse or become uncertain")
				}
				if mode == "unknown" {
					before := attempts
					if err := writer(incoming, opcode.MSGMoveStop, policyMovementPacket(22), instanceEntryScope()); !modcb.PacketWasNotDispatched(err) || attempts != before {
						t.Fatal("unknown write permitted another scoped attempt", err, attempts)
					}
				}
				phase := "transport_unconfirmed_no_retry"
				if mode == "first_not_dispatched" {
					phase = "body_scope_not_dispatched"
				}
				proto.Merge(result, &actionsv1.State{MotionUnconfirmed: mode != "first_not_dispatched",
					BodyMovement: &actionsv1.BodyMovementState{SequenceId: 17, VisitId: 1, Phase: phase, TransportWritten: written != 0}})
				return nil
			}
			action := policyBodyIdleAction()
			action["idleToken"] = "parent-idle"
			receipt := x.step(t, action)
			if mode == "first_not_dispatched" {
				if receipt.Outcome != behavior.ActionNotDispatched || receipt.Payload["phase"] != "body_scope_not_dispatched" || receipt.Payload["transport_written"] != false || attempts != 1 || written != 0 {
					t.Fatal("known first scoped refusal became a physical unknown", receipt, attempts, written)
				}
			} else if receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["reconciliationRequired"] != true || receipt.Payload["effect_observed"] != false ||
				receipt.Payload["transport_written"] != (written != 0) {
				t.Fatal("partial/unknown scoped body input lost uncertainty", receipt, attempts, written)
			}
			before := attempts
			ctx := instanceEntryIncoming(modcb.WithPolicySpell(context.Background(), token))
			if err := x.m.moduleScopedPacketWriter(friendlyEncounterSession, actionsModule)(ctx, opcode.MSGMoveStop, policyMovementPacket(22), instanceEntryScope()); !modcb.PacketWasNotDispatched(err) || attempts != before {
				t.Fatal("retired idle body token wrote after return", err, attempts)
			}
		})
	}
}

func TestPolicyScopedReleaseRetainsExactStopPrivilegeAndLegacyPath(t *testing.T) {
	for _, mode := range []string{"scoped", "legacy", "start", "foreign", "retired"} {
		t.Run(mode, func(t *testing.T) {
			m := New(nil, nil, nil)
			t.Cleanup(m.cancel)
			op := &policySpell{capability: "release_interaction", admitted: true, movement: &policyMovement{self: 22, stop: true},
				origin: modsession.WriteOrigin{SessionID: "release", Module: actionsModule, Writer: "lua", ActionID: "1"}}
			token := strings.Repeat("a", 64)
			m.policySpells.active = map[string]*policySpell{token: op}
			writes := 0
			m.session.SendScopedPacket = func(ctx context.Context, code uint32, body []byte, scope *modv1.TransportScope) error {
				if code != opcode.MSGMoveStop || modsession.WriteOriginFromContext(ctx) != op.origin || scope.SelfGuid != 22 || scope.VisitRevision != 9 {
					t.Fatal("scoped release changed source or packet identity", code, scope)
				}
				writes++
				return nil
			}
			m.session.SendPacket = func(context.Context, uint32, []byte) error { writes++; return nil }
			ctx := instanceEntryIncoming(modcb.WithPolicySpell(context.Background(), token))
			code, body := uint32(opcode.MSGMoveStop), policyMovementPacket(22)
			switch mode {
			case "legacy":
				if err := m.modulePacketWriter("release", actionsModule)(ctx, code, body); err != nil || writes != 1 {
					t.Fatal("optional scoped cleanup broke legacy release STOP", err, writes)
				}
				return
			case "start":
				code = opcode.MSGMoveStartForward
			case "foreign":
				body = policyMovementPacket(23)
			case "retired":
				delete(m.policySpells.active, token)
			}
			err := m.moduleScopedPacketWriter("release", actionsModule)(ctx, code, body, instanceEntryScope())
			if mode == "scoped" {
				if err != nil || writes != 1 {
					t.Fatal("exact scoped release STOP was refused", err, writes)
				}
			} else if !modcb.PacketWasNotDispatched(err) || writes != 0 {
				t.Fatal("scoped cleanup expanded release authority", mode, err, writes)
			}
		})
	}
}

func TestPolicyBodyMovementIncompatibleInstalledContractCannotDropRequestedIdleToken(t *testing.T) {
	for _, mode := range []string{"missing", "wrong_tag"} {
		t.Run(mode, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			root := t.TempDir()
			changed := false
			for _, name := range []string{actionsModule, "buddy-world", "buddy-party"} {
				dir := filepath.Join(root, name)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
					raw, err := os.ReadFile(filepath.Join("..", "..", "..", "modules", name, file))
					if err != nil {
						t.Fatal(err)
					}
					if name == actionsModule && file == "module.pb" {
						set := &descriptorpb.FileDescriptorSet{}
						if err := proto.Unmarshal(raw, set); err != nil {
							t.Fatal(err)
						}
						for _, f := range set.File {
							for _, message := range f.MessageType {
								if message.GetName() != "BodyMovementRequest" {
									continue
								}
								var fields []*descriptorpb.FieldDescriptorProto
								for _, field := range message.Field {
									if field.GetName() == "idle_token" {
										if field.GetNumber() != 8 || field.GetType() != descriptorpb.FieldDescriptorProto_TYPE_STRING {
											t.Fatal("new contract does not contain exact field8 string", field)
										}
										changed = true
										if mode == "missing" {
											continue
										}
										field.Number = proto.Int32(78)
									}
									fields = append(fields, field)
								}
								message.Field = fields
							}
						}
						raw, err = proto.Marshal(set)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(filepath.Join(dir, file), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !changed {
				t.Fatal("fixture did not alter the new field in actual installed descriptor")
			}
			registry, err := moddisc.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			x.m.registry = registry
			x.m.instances[actionsModule].definition, _ = registry.Lookup(actionsModule)
			action := policyBodyIdleAction()
			action["idleToken"] = "parent-idle-token"
			if _, err := x.p.resolveBodyMovement(action); status.Code(err) != codes.FailedPrecondition || len(x.calls) != 0 || len(x.callback.writes) != 0 {
				t.Fatal("old installed contract silently removed requested idle binding", err, x.calls)
			}
			delete(action, "idleToken")
			if _, err := x.p.resolveBodyMovement(action); err != nil || len(x.calls) != 0 {
				t.Fatal("optional field broke ordinary tokenless old-contract resolution", err, x.calls)
			}
		})
	}
}
