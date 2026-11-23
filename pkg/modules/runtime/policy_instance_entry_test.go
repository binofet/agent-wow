package modrt

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

type instanceEntryFixture struct {
	m        *Manager
	p        *SessionPolicy
	callback *friendlyEncounterCallback
	calls    []string
	handle   func(context.Context, string, proto.Message, proto.Message) error
}

func newInstanceEntryFixture(t *testing.T) *instanceEntryFixture {
	t.Helper()
	m, _, cb := friendlyEncounterManager(t)
	x := &instanceEntryFixture{m: m, callback: cb}
	original := m.instances[actionsModule].conn
	// These typed, synthetic boundary replies test the Runtime adapter. Native
	// admission, physical writes and observed map entry have separate tests.
	conn, err := grpc.NewClient("passthrough:///instance-entry-boundary", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, result any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, options ...grpc.CallOption) error {
			if strings.HasPrefix(method, "/grpc.health.") {
				return original.Invoke(ctx, method, request, result, options...)
			}
			x.calls = append(x.calls, method)
			return x.handle(ctx, method, request.(proto.Message), result.(proto.Message))
		}))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[actionsModule].conn = conn
	t.Cleanup(func() { conn.Close() })
	x.p, err = m.StartPolicyWithOptions(context.Background(), idleInteractionPolicy, behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.p.close)
	return x
}

func instanceEntryAction(capability string) map[string]any {
	a := map[string]any{"capability": capability, "sessionId": friendlyEncounterSession, "selfGuid": "22", "visitId": "1"}
	switch capability {
	case "area_trigger_reference":
		a["destinationMapId"] = float64(0)
	case "notify_area_trigger":
		a["token"], a["triggerId"], a["expectedPositionRevision"], a["expectedRevision"], a["clientRequestId"] = "parent-owned", float64(123), "7", "0", "entry-1"
	case "plan_route_leg", "travel_to":
		a["mapId"], a["x"], a["y"], a["z"] = float64(0), float64(1), float64(2), float64(3)
		if capability == "plan_route_leg" {
			a["expectedPositionRevision"] = "7"
		} else {
			a["token"] = "parent-owned"
		}
	}
	return a
}

func (x *instanceEntryFixture) step(t *testing.T, action map[string]any) *behavior.ActionReceipt {
	t.Helper()
	visit, _ := instanceEntryUint64(action["visitId"], false)
	ctx := context.WithValue(context.Background(), policyEncounterVisitKey{}, policyEncounterObservation{visit: visit, sequence: "observed"})
	_, receipt, err := x.p.Step(ctx, map[string]any{"request": action}, nil)
	if err != nil || receipt == nil {
		t.Fatal("typed instance-entry action failed", receipt, err)
	}
	return receipt
}

func instanceEntryIncoming(ctx context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	return metadata.NewIncomingContext(ctx, md)
}

func instanceEntryScope() *modv1.TransportScope {
	return &modv1.TransportScope{ConnectionId: "physical-stream", SelfGuid: 22, VisitBound: true, VisitRevision: 9, MotionRevision: 20}
}

func instanceEntryReply(request proto.Message) *actionsv1.AreaTriggerOperation {
	r := request.(*actionsv1.NotifyAreaTriggerRequest)
	return &actionsv1.AreaTriggerOperation{Revision: r.ExpectedRevision + 1, SessionId: r.SessionId, SelfGuid: r.SelfGuid, VisitId: r.VisitId,
		TriggerId: r.TriggerId, ClientRequestId: r.ClientRequestId, ExpectedPositionRevision: r.ExpectedPositionRevision, Phase: "not_dispatched", NotDispatched: true}
}

func TestPolicyInstanceEntryTypedReadsAndTravelUseInstalledContracts(t *testing.T) {
	for _, capability := range []string{"area_trigger_reference", "area_trigger_observation", "instance_entry_status", "plan_route_leg", "travel_to"} {
		t.Run(capability, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			x.handle = func(ctx context.Context, method string, request, result proto.Message) error {
				if token, supplied := modcb.IncomingPolicySpell(instanceEntryIncoming(ctx)); !supplied || token == "" {
					t.Fatal("typed call lost its packet-denial attribution")
				}
				switch capability {
				case "area_trigger_reference":
					r := request.(*actionsv1.AreaTriggerReferenceRequest)
					if method != actionsv1.BuddyActions_AreaTriggerReference_FullMethodName || r.DestinationMapId != 0 || r.VisitId != 1 {
						t.Fatal("reference request changed zero map or binding", method, r)
					}
					proto.Merge(result, &actionsv1.AreaTriggerReferences{SessionId: r.SessionId, SelfGuid: r.SelfGuid, VisitId: r.VisitId,
						Volumes: []*actionsv1.AreaTriggerVolume{{TriggerId: 123, X: 1.00000000001, ReferenceOnly: true}}})
				case "area_trigger_observation":
					if method != actionsv1.BuddyActions_AreaTriggerObservation_FullMethodName {
						t.Fatal(method)
					}
					// Retained source-visit identity must survive a newer current visit.
					proto.Merge(result, &actionsv1.AreaTriggerOperation{SessionId: friendlyEncounterSession, SelfGuid: 22, VisitId: 1, Phase: "transport_unknown", TransportUnknown: true, Pending: true})
				case "plan_route_leg":
					r := request.(*actionsv1.PlanRouteLegRequest)
					if method != actionsv1.BuddyActions_PlanRouteLeg_FullMethodName || r.ExpectedPositionRevision != 7 || r.MapId != 0 || r.X != 1 || r.Y != 2 || r.Z != 3 {
						t.Fatal("typed route request changed", method, r)
					}
					proto.Merge(result, &actionsv1.RouteLeg{SessionId: r.SessionId, SelfGuid: r.SelfGuid, MapId: r.MapId, ServerPositionRevision: 7})
				case "instance_entry_status", "travel_to":
					want := actionsv1.BuddyActions_Status_FullMethodName
					if capability == "travel_to" {
						want = actionsv1.BuddyActions_TravelTo_FullMethodName
						r := request.(*actionsv1.TravelToRequest)
						if r.Token != "parent-owned" || r.MapId != 0 || r.X != 1 || r.ArrivalDistance != 0 {
							t.Fatal("travel request changed native default", r)
						}
					}
					if method != want {
						t.Fatal(method)
					}
					proto.Merge(result, &actionsv1.State{SessionId: friendlyEncounterSession, SelfGuid: 22})
				}
				return nil
			}
			action := instanceEntryAction(capability)
			if capability == "area_trigger_observation" {
				action["visitId"] = "2"
			}
			receipt := x.step(t, action)
			want := behavior.ActionConfirmed
			if capability == "travel_to" {
				want = behavior.ActionSettledUnverified
			}
			if receipt.Outcome != want || receipt.Payload["effect_observed"] != false || receipt.Payload["transport_written"] != false || len(x.calls) != 1 || len(x.callback.writes) != 0 {
				t.Fatal("metadata or returned travel claimed a physical effect", receipt, x.calls, x.callback.writes)
			}
			result := receipt.Payload["result"].(map[string]any)
			if capability == "area_trigger_reference" {
				volume := result["volumes"].([]any)[0].(map[string]any)
				if volume["x"] != float64(1.00000000001) || volume["sourceMapId"] != float64(0) || volume["destinationMapId"] != float64(0) {
					t.Fatal("double precision or explicit zero map omitted", volume)
				}
			}
			if capability == "area_trigger_observation" && (result["visitId"] != "1" || receipt.Payload["reconciliationRequired"] != true) {
				t.Fatal("retained native uncertainty was relabeled or erased", receipt)
			}
			_, second, err := x.p.Step(context.Background(), map[string]any{}, nil)
			if err != nil || second != nil || len(x.calls) != 1 || x.p.vm.Status().Pending != 0 {
				t.Fatal("receipt processing replayed a typed operation", second, err, x.calls, x.p.vm.Status())
			}
		})
	}
}

func TestPolicyInstanceEntryNotifyScopedAttemptAndUncertainty(t *testing.T) {
	for _, mode := range []string{"written", "unknown", "lost_reply", "not_dispatched"} {
		t.Run(mode, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			var writes []uint32
			var usedToken string
			x.m.session.SendScopedPacket = func(ctx context.Context, code uint32, body []byte, scope *modv1.TransportScope) error {
				origin := modsession.WriteOriginFromContext(ctx)
				if origin.Writer != "lua" || origin.ActionID == "" || origin.SessionID != friendlyEncounterSession || origin.Module != actionsModule || !proto.Equal(scope, instanceEntryScope()) {
					t.Fatal("native scope or trusted attribution changed", origin, scope)
				}
				writes = append(writes, code)
				if mode == "unknown" && code == opcode.CMSGAreatrigger {
					return status.Error(codes.Unknown, "synthetic uncertain physical write")
				}
				return nil
			}
			x.handle = func(ctx context.Context, method string, request, result proto.Message) error {
				if method != actionsv1.BuddyActions_NotifyAreaTrigger_FullMethodName {
					t.Fatal(method)
				}
				incoming := instanceEntryIncoming(ctx)
				usedToken, _ = modcb.IncomingPolicySpell(incoming)
				writer := x.m.moduleScopedPacketWriter(friendlyEncounterSession, actionsModule)
				op := instanceEntryReply(request)
				if mode != "not_dispatched" {
					if err := writer(incoming, opcode.MSGMoveStop, policyMovementPacket(22), instanceEntryScope()); err != nil {
						t.Fatal("exact own STOP refused", err)
					}
					body := binary.LittleEndian.AppendUint32(nil, 123)
					err := writer(incoming, opcode.CMSGAreatrigger, body, instanceEntryScope())
					if mode == "unknown" && err == nil || mode != "unknown" && err != nil {
						t.Fatal("synthetic transport classification changed", err)
					}
					if err := writer(incoming, opcode.CMSGAreatrigger, body, instanceEntryScope()); !modcb.PacketWasNotDispatched(err) {
						t.Fatal("same dispatch replayed an already attempted trigger", err)
					}
					op.NotDispatched, op.Pending = false, true
					op.TransportWritten, op.TransportUnknown = mode != "unknown", mode == "unknown"
					op.Phase = "written_waiting_destination"
					if mode == "unknown" {
						op.Phase = "transport_unknown"
					}
				}
				if mode == "lost_reply" {
					return status.Error(codes.DeadlineExceeded, "synthetic lost unary reply after physical write")
				}
				proto.Merge(result, op)
				return nil
			}
			receipt := x.step(t, instanceEntryAction("notify_area_trigger"))
			if receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["effect_observed"] != false || len(x.calls) != 1 {
				t.Fatal("notify reply claimed arrival or lost finite reservation", receipt, x.calls)
			}
			if mode == "not_dispatched" {
				if len(writes) != 0 || receipt.Payload["phase"] != "not_dispatched" || receipt.Payload["result"].(map[string]any)["notDispatched"] != true {
					t.Fatal("typed native zero-send receipt lost its phase", receipt, writes)
				}
			} else if len(writes) != 2 || writes[0] != opcode.MSGMoveStop || writes[1] != opcode.CMSGAreatrigger || receipt.Payload["transport_written"] != true {
				t.Fatal("notify did not use exactly scoped STOP then trigger", receipt, writes)
			}
			if (mode == "unknown" || mode == "lost_reply") && receipt.Payload["reconciliationRequired"] != true {
				t.Fatal("unknown write or lost reply became safe to retry", receipt)
			}
			err := x.m.writePolicyInstanceEntryScoped(context.Background(), usedToken, friendlyEncounterSession, actionsModule, opcode.MSGMoveStop, policyMovementPacket(22), instanceEntryScope())
			if !modcb.PacketWasNotDispatched(err) {
				t.Fatal("retired dispatch token could write again", err)
			}
			_, second, err := x.p.Step(context.Background(), map[string]any{}, nil)
			if err != nil || second != nil || len(x.calls) != 1 || x.p.vm.Status().Pending != 0 {
				t.Fatal("host receipt processing replayed notify", second, err, x.calls)
			}
		})
	}
}

func TestPolicyInstanceEntryPacketWhitelistAndRetiredTravelToken(t *testing.T) {
	for _, mode := range []string{"read", "unscoped", "no_stop", "wrong_trigger", "foreign_stop", "trailing_stop", "moving_stop", "airborne_stop", "nan_stop", "nil_scope"} {
		t.Run(mode, func(t *testing.T) {
			m := New(nil, nil, nil)
			t.Cleanup(m.cancel)
			op := &policySpell{capability: "notify_area_trigger", admitted: true, origin: modsession.WriteOrigin{SessionID: "s", Module: actionsModule, Writer: "lua", ActionID: "1"}, instanceEntry: &policyInstanceEntry{self: 22, trigger: 123, stopWritten: true}}
			m.policySpells.active = map[string]*policySpell{"token": op}
			writes := 0
			m.session.SendPacket = func(context.Context, uint32, []byte) error { writes++; return nil }
			m.session.SendScopedPacket = func(context.Context, uint32, []byte, *modv1.TransportScope) error { writes++; return nil }
			code, body, scope := uint32(opcode.MSGMoveStop), policyMovementPacket(22), instanceEntryScope()
			switch mode {
			case "read":
				op.capability, op.instanceEntry.trigger = "instance_entry_status", 0
			case "unscoped":
				if err := m.writePolicySpell(context.Background(), "token", "s", actionsModule, code, body); !modcb.PacketWasNotDispatched(err) || writes != 0 {
					t.Fatal("entry token used an unscoped packet fallback", err, writes)
				}
				return
			case "no_stop":
				op.instanceEntry.stopWritten = false
				code, body = opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 123)
			case "wrong_trigger":
				code, body = opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 124)
			case "foreign_stop":
				body = policyMovementPacket(23)
			case "trailing_stop":
				body = append(body, 0)
			case "moving_stop":
				binary.LittleEndian.PutUint32(body[2:], 1)
			case "airborne_stop":
				binary.LittleEndian.PutUint32(body[2:], 0x200)
			case "nan_stop":
				binary.LittleEndian.PutUint32(body[12:], math.Float32bits(float32(math.NaN())))
			case "nil_scope":
				scope = nil
			}
			if err := m.writePolicyInstanceEntryScoped(context.Background(), "token", "s", actionsModule, code, body, scope); !modcb.PacketWasNotDispatched(err) || writes != 0 {
				t.Fatal("packet outside exact native notify escaped", err, writes)
			}
		})
	}
	t.Run("lost_travel_reply", func(t *testing.T) {
		x := newInstanceEntryFixture(t)
		var token string
		x.handle = func(ctx context.Context, _ string, _, _ proto.Message) error {
			incoming := instanceEntryIncoming(ctx)
			token, _ = modcb.IncomingPolicySpell(incoming)
			if err := x.m.modulePacketWriter(friendlyEncounterSession, actionsModule)(incoming, opcode.MSGMoveStartForward, policyMovementPacket(22)); err != nil {
				t.Fatal("normal own travel movement refused", err)
			}
			return status.Error(codes.DeadlineExceeded, "synthetic canceled travel reply")
		}
		r := x.step(t, instanceEntryAction("travel_to"))
		if r.Outcome != behavior.ActionSettledUnverified || r.Payload["reconciliationRequired"] != true || r.Payload["effect_observed"] != false || len(x.callback.writes) != 1 {
			t.Fatal("canceled travel hid its outstanding physical uncertainty", r, x.callback.writes)
		}
		// A late cleanup with this retired token fails closed. It must not
		// downgrade to an unscoped, unattributed STOP or claim it succeeded.
		if err := x.m.writePolicySpell(context.Background(), token, friendlyEncounterSession, actionsModule, opcode.MSGMoveStop, policyMovementPacket(22)); !modcb.PacketWasNotDispatched(err) || len(x.callback.writes) != 1 {
			t.Fatal("late canceled cleanup escaped retired attribution", err, x.callback.writes)
		}
	})
	t.Run("returned_travel_motion_unconfirmed", func(t *testing.T) {
		x := newInstanceEntryFixture(t)
		x.handle = func(ctx context.Context, _ string, _, result proto.Message) error {
			incoming := instanceEntryIncoming(ctx)
			if err := x.m.modulePacketWriter(friendlyEncounterSession, actionsModule)(incoming, opcode.MSGMoveStartForward, policyMovementPacket(22)); err != nil {
				t.Fatal("normal own travel START refused", err)
			}
			// A native nil-error return after an uncertain owed STOP retains
			// its actual uncertainty and transport diagnostic in State.
			proto.Merge(result, &actionsv1.State{SessionId: friendlyEncounterSession, SelfGuid: 22, MotionUnconfirmed: true,
				Reason: "movement_stop_transport_failed", LastTransportFailure: &actionsv1.TransportFailure{
					Sequence: 2, Opcode: opcode.MSGMoveStop, OutcomeUnknown: true, Error: "synthetic uncertain STOP"}})
			return nil
		}
		r := x.step(t, instanceEntryAction("travel_to"))
		result := r.Payload["result"].(map[string]any)
		failure := result["lastTransportFailure"].(map[string]any)
		if r.Outcome != behavior.ActionSettledUnverified || r.Payload["reconciliationRequired"] != true || r.Payload["effect_observed"] != false ||
			r.Payload["transport_written"] != true || result["motionUnconfirmed"] != true || failure["outcomeUnknown"] != true ||
			failure["sequence"] != "2" || len(x.callback.writes) != 1 {
			t.Fatal("nil-error native return erased incomplete travel or its original unknown", r, x.callback.writes)
		}
	})
}

func TestPolicyInstanceEntryRejectsNoncanonicalOrUnboundInput(t *testing.T) {
	x := newInstanceEntryFixture(t)
	for _, mode := range []string{"session", "self", "visit", "scope", "counter", "trigger", "token", "coordinate", "missing_zero_map"} {
		t.Run(mode, func(t *testing.T) {
			a := instanceEntryAction("notify_area_trigger")
			switch mode {
			case "session":
				a["sessionId"] = "other"
			case "self":
				a["selfGuid"] = "022"
			case "visit":
				a["visitId"] = "01"
			case "scope":
				a["transportScope"] = map[string]any{}
			case "counter":
				a["expectedRevision"] = "18446744073709551615"
			case "trigger":
				a["triggerId"] = float64(1.5)
			case "token":
				a["token"] = " owner "
			case "coordinate":
				a = instanceEntryAction("travel_to")
				a["x"] = math.Inf(1)
			case "missing_zero_map":
				a = instanceEntryAction("area_trigger_reference")
				delete(a, "destinationMapId")
			}
			if _, err := x.p.resolveKnownSpell(a); status.Code(err) != codes.InvalidArgument || len(x.calls) != 0 || len(x.callback.writes) != 0 {
				t.Fatal("malformed or invented binding reached native method", err, x.calls)
			}
		})
	}
	for _, mode := range []string{"missing_observation", "replaced_visit"} {
		t.Run(mode, func(t *testing.T) {
			d, err := x.p.resolveKnownSpell(instanceEntryAction("area_trigger_reference"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if mode == "replaced_visit" {
				ctx = context.WithValue(ctx, policyEncounterVisitKey{}, policyEncounterObservation{visit: 2})
			}
			ctx = modsession.WithWriteOrigin(ctx, modsession.WriteOrigin{SessionID: friendlyEncounterSession, Module: actionsModule, Writer: "lua", ActionID: "1"})
			outcome, _ := d.Send(ctx)
			if outcome != behavior.ActionNotDispatched || len(x.calls) != 0 {
				t.Fatal("dispatch guessed its current visit", outcome, x.calls)
			}
		})
	}
}

func TestPolicyInstanceEntryRejectsIncompatibleInstalledReferenceContract(t *testing.T) {
	for _, mode := range []string{"alias", "request_tag", "nested_float"} {
		t.Run(mode, func(t *testing.T) {
			x := newInstanceEntryFixture(t)
			root := t.TempDir()
			for _, name := range []string{"buddy-actions", "buddy-world", "buddy-party"} {
				dir := filepath.Join(root, name)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
					raw, err := os.ReadFile(filepath.Join("..", "..", "..", "modules", name, file))
					if err != nil {
						t.Fatal(err)
					}
					if name == actionsModule && file == "module.yaml" && mode == "alias" {
						from := "  area_trigger_reference: " + actionsv1.BuddyActions_AreaTriggerReference_FullMethodName
						if strings.Count(string(raw), from) != 1 {
							t.Fatal("missing actual installed alias")
						}
						raw = []byte(strings.Replace(string(raw), from, "  area_trigger_reference: "+actionsv1.BuddyActions_AreaTriggerObservation_FullMethodName, 1))
					}
					if name == actionsModule && file == "module.pb" && mode != "alias" {
						set := &descriptorpb.FileDescriptorSet{}
						if err := proto.Unmarshal(raw, set); err != nil {
							t.Fatal(err)
						}
						changed := false
						for _, f := range set.File {
							for _, message := range f.MessageType {
								for _, field := range message.Field {
									if mode == "request_tag" && message.GetName() == "AreaTriggerReferenceRequest" && field.GetName() == "visit_id" {
										field.Number, changed = proto.Int32(30), true
									}
									if mode == "nested_float" && message.GetName() == "AreaTriggerVolume" && field.GetName() == "x" {
										field.Type, changed = descriptorpb.FieldDescriptorProto_TYPE_FLOAT.Enum(), true
									}
								}
							}
						}
						if !changed {
							t.Fatal("mutation did not touch an installed field")
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
			registry, err := moddisc.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			x.m.registry = registry
			x.m.instances[actionsModule].definition, _ = registry.Lookup(actionsModule)
			if _, err := x.p.resolveKnownSpell(instanceEntryAction("area_trigger_reference")); status.Code(err) != codes.FailedPrecondition || len(x.calls) != 0 {
				t.Fatal("incompatible installed contract reached native code", mode, err, x.calls)
			}
		})
	}
}
