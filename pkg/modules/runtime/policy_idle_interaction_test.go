package modrt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
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
	"google.golang.org/protobuf/types/known/emptypb"
)

const idleInteractionPolicy = `return {
 on_event=function()return true end,
 decide=function(frame)return {action=frame.input.request}end,
 export_state=function()return {version=1,data={}}end,
 import_state=function()return true end}`

type idleInteractionCalls struct{ idle, legacy atomic.Int64 }

func idleInteractionRuntime(t *testing.T) (*Manager, *buddyactions.Service, *friendlyEncounterCallback, *idleInteractionCalls) {
	t.Helper()
	m, native, callback := friendlyEncounterManager(t)
	original := m.instances[actionsModule].conn
	calls := &idleInteractionCalls{}
	// Forward to the original bufconn connection: production native code, not
	// a fake grant implementation, decides every reflected acquisition.
	counted, err := grpc.NewClient("passthrough:///counted-idle-native", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, result any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, options ...grpc.CallOption) error {
			switch method {
			case actionsv1.BuddyActions_AcquireIdleInteraction_FullMethodName:
				calls.idle.Add(1)
			case actionsv1.BuddyActions_AcquireInteraction_FullMethodName:
				calls.legacy.Add(1)
			}
			return original.Invoke(ctx, method, request, result, options...)
		}))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[actionsModule].conn = counted
	t.Cleanup(func() { counted.Close() })
	return m, native, callback, calls
}

func idleInteractionAction() map[string]any {
	return map[string]any{"capability": "acquire_idle_interaction", "sessionId": friendlyEncounterSession,
		"selfGuid": "22", "visitId": "1", "owner": "trade", "leaseMs": float64(10000)}
}

func idleInteractionHost(t *testing.T, m *Manager) *SessionPolicy {
	t.Helper()
	p, err := m.StartPolicy(context.Background(), idleInteractionPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

func idleInteractionSnapshot(t *testing.T, native *buddyactions.Service) *actionsv1.State {
	t.Helper()
	state, err := native.Status(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertIdleInteractionUnchanged(t *testing.T, before, after *actionsv1.State) {
	t.Helper()
	if after.InteractionLeaseRemainingMs > before.InteractionLeaseRemainingMs {
		t.Fatal("refusal extended existing custody", before, after)
	}
	left, right := proto.Clone(before).(*actionsv1.State), proto.Clone(after).(*actionsv1.State)
	// Public status ages advance naturally; every non-aging observable remains.
	left.InteractionLeaseRemainingMs, right.InteractionLeaseRemainingMs = 0, 0
	if left.Position != nil {
		left.Position.AgeMs = 0
	}
	if right.Position != nil {
		right.Position.AgeMs = 0
	}
	if !proto.Equal(left, right) {
		t.Fatal("refused idle acquisition mutated native observations", before, after)
	}
}

func assertIdleInteractionQuiet(t *testing.T, m *Manager, p *SessionPolicy, callback *friendlyEncounterCallback, calls *idleInteractionCalls, revision string, wantCalls int64) {
	t.Helper()
	_, receipt, err := p.Step(context.Background(), map[string]any{}, nil)
	if err != nil || receipt != nil || len(callback.writes) != 0 || calls.idle.Load() != wantCalls || calls.legacy.Load() != 0 {
		t.Fatal("receipt processing replayed acquisition or wrote a physical packet", receipt, err, calls.idle.Load(), calls.legacy.Load(), callback.writes)
	}
	state := p.vm.Status()
	if state.Revision != revision || state.Pending != 0 || state.Fault != "" {
		t.Fatal("idle acquisition changed source or retained invented action uncertainty", state)
	}
	m.policySpells.mu.Lock()
	active := len(m.policySpells.active)
	m.policySpells.mu.Unlock()
	if active != 0 {
		t.Fatal("ephemeral idle packet-denial attribution survived dispatch", active)
	}
}

func TestPolicyIdleInteractionRealNativeGrantAndExactRenewalWriteNoPackets(t *testing.T) {
	m, native, callback, calls := idleInteractionRuntime(t)
	p := idleInteractionHost(t, m)
	revision := p.vm.Status().Revision
	world := proto.Clone(callback.world)
	request := idleInteractionAction()
	input, _ := json.Marshal(request)
	var token string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt != 0 {
			request["token"] = token
		}
		_, receipt, err := p.Step(context.Background(), map[string]any{"request": request}, nil)
		if err != nil || receipt == nil || receipt.Outcome != behavior.ActionConfirmed || receipt.Payload["phase"] != "idle_interaction_acquired" ||
			receipt.Payload["transport_written"] != false || receipt.Payload["effect_observed"] != false {
			t.Fatal("native lease metadata was refused or claimed a physical effect", receipt, err)
		}
		lease, ok := receipt.Payload["result"].(map[string]any)
		if !ok {
			t.Fatal("confirmed lease omitted its actual native result", receipt)
		}
		gotToken, ok := lease["token"].(string)
		if !ok || gotToken == "" || attempt != 0 && gotToken != token {
			t.Fatal("renewal replaced or omitted the native token", lease)
		}
		token = gotToken
		state := idleInteractionSnapshot(t, native)
		if state.SessionId != friendlyEncounterSession || state.SelfGuid != friendlyEncounterSelf ||
			state.InteractionOwner != "trade" || state.InteractionHarvest || state.InteractionLeaseRemainingMs == 0 || state.TransportWrites != 0 {
			t.Fatal("lease was not current idle custody", state)
		}
		assertIdleInteractionQuiet(t, m, p, callback, calls, revision, int64(attempt+1))
	}
	delete(request, "token")
	after, _ := json.Marshal(request)
	if string(input) != string(after) || !proto.Equal(world, callback.world) {
		t.Fatal("native acquisition mutated the caller's request or World observation")
	}
}

func TestPolicyIdleInteractionStaleBodyAndVisitNeverWriteOrChangeCustody(t *testing.T) {
	for _, mode := range []string{"session", "body", "world_visit", "native_visit", "both_visits"} {
		t.Run(mode, func(t *testing.T) {
			m, native, callback, calls := idleInteractionRuntime(t)
			p := idleInteractionHost(t, m)
			revision := p.vm.Status().Revision
			request, wantCalls := idleInteractionAction(), int64(1)
			switch mode {
			case "session":
				request["sessionId"], wantCalls = "replaced-session", 0
			case "body":
				request["selfGuid"], wantCalls = "23", 0
			case "world_visit":
				callback.world.VisitRevision = 2
			case "native_visit", "both_visits":
				if _, err := native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGNewWorld, Payload: make([]byte, 20)}); err != nil {
					t.Fatal(err)
				}
				if mode == "both_visits" {
					callback.world.VisitRevision = 2
				}
			}
			before, world := idleInteractionSnapshot(t, native), proto.Clone(callback.world)
			_, receipt, err := p.Step(context.Background(), map[string]any{"request": request}, nil)
			if wantCalls == 0 {
				if err == nil || receipt != nil {
					t.Fatal("stale body reached reflected dispatch", receipt, err)
				}
			} else if err != nil || receipt == nil || receipt.Outcome != behavior.ActionNotDispatched {
				t.Fatal("native stale visit was not an explicit refusal", receipt, err)
			}
			assertIdleInteractionUnchanged(t, before, idleInteractionSnapshot(t, native))
			if !proto.Equal(world, callback.world) {
				t.Fatal("stale request mutated the raw World observation")
			}
			assertIdleInteractionQuiet(t, m, p, callback, calls, revision, wantCalls)
		})
	}
}

func TestPolicyIdleInteractionCannotBorrowForeignOrLegacyCustody(t *testing.T) {
	for _, mode := range []string{"foreign_owner", "foreign_token", "legacy_token"} {
		t.Run(mode, func(t *testing.T) {
			m, native, callback, calls := idleInteractionRuntime(t)
			p := idleInteractionHost(t, m)
			revision := p.vm.Status().Revision
			var lease *actionsv1.InteractionLease
			var err error
			if mode == "legacy_token" {
				lease, err = native.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: 10000})
			} else {
				lease, err = native.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{
					SessionId: friendlyEncounterSession, SelfGuid: friendlyEncounterSelf, VisitId: 1, Owner: "trade", LeaseMs: 10000})
			}
			if err != nil || lease.GetToken() == "" {
				t.Fatal("existing native custody fixture failed", lease, err)
			}
			request := idleInteractionAction()
			request["leaseMs"], request["token"] = float64(30000), lease.Token
			if mode == "foreign_owner" {
				request["owner"] = "loot"
			} else if mode == "foreign_token" {
				request["token"] = strings.Repeat("b", 32)
			}
			before := idleInteractionSnapshot(t, native)
			_, receipt, err := p.Step(context.Background(), map[string]any{"request": request}, nil)
			if err != nil || receipt == nil || receipt.Outcome != behavior.ActionNotDispatched {
				t.Fatal("idle route borrowed foreign/legacy custody", receipt, err)
			}
			assertIdleInteractionUnchanged(t, before, idleInteractionSnapshot(t, native))
			assertIdleInteractionQuiet(t, m, p, callback, calls, revision, 1)
		})
	}
}

func idleInteractionInstalledContract(t *testing.T, m *Manager, mode string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"buddy-actions", "buddy-party", "buddy-world"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "modules", name, file))
			if err != nil {
				t.Fatal(err)
			}
			if name == "buddy-actions" && file == "module.yaml" {
				if mode == "absent" {
					var lines []string
					for _, line := range strings.Split(string(raw), "\n") {
						if !strings.HasPrefix(line, "  acquire_idle_interaction:") {
							lines = append(lines, line)
						}
					}
					raw = []byte(strings.Join(lines, "\n"))
				} else if mode == "legacy_alias" {
					from := "  acquire_idle_interaction: " + actionsv1.BuddyActions_AcquireIdleInteraction_FullMethodName
					if strings.Count(string(raw), from) != 1 {
						t.Fatal("exact installed idle alias missing")
					}
					raw = []byte(strings.Replace(string(raw), from, "  acquire_idle_interaction: "+actionsv1.BuddyActions_AcquireInteraction_FullMethodName, 1))
				}
			}
			if name == "buddy-actions" && file == "module.pb" {
				set := &descriptorpb.FileDescriptorSet{}
				if err := proto.Unmarshal(raw, set); err != nil {
					t.Fatal(err)
				}
				for _, descriptor := range set.File {
					if mode == "absent" {
						for _, service := range descriptor.Service {
							var methods []*descriptorpb.MethodDescriptorProto
							for _, method := range service.Method {
								if method.GetName() != "AcquireIdleInteraction" {
									methods = append(methods, method)
								}
							}
							service.Method = methods
						}
					}
					for _, message := range descriptor.MessageType {
						if message.GetName() != "IdleInteractionRequest" {
							continue
						}
						if mode == "future_field" {
							message.Field = append(message.Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("future_hint"), Number: proto.Int32(7),
								Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()})
						}
						if mode == "missing_visit" {
							var fields []*descriptorpb.FieldDescriptorProto
							for _, field := range message.Field {
								if field.GetName() != "visit_id" {
									fields = append(fields, field)
								}
							}
							message.Field = fields
						}
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
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m.registry = registry
	m.instances[actionsModule].definition, _ = registry.Lookup(actionsModule)
}

func TestPolicyIdleInteractionRequiresInstalledBoundContractWithoutLegacyFallback(t *testing.T) {
	for _, mode := range []string{"absent", "legacy_alias", "missing_visit", "future_field"} {
		t.Run(mode, func(t *testing.T) {
			m, native, callback, calls := idleInteractionRuntime(t)
			idleInteractionInstalledContract(t, m, mode)
			p := idleInteractionHost(t, m)
			revision := p.vm.Status().Revision
			before := idleInteractionSnapshot(t, native)
			_, receipt, err := p.Step(context.Background(), map[string]any{"request": idleInteractionAction()}, nil)
			wantCalls := int64(0)
			if mode == "future_field" {
				wantCalls = 1
				if err != nil || receipt == nil || receipt.Outcome != behavior.ActionConfirmed {
					t.Fatal("compatible additive contract was rejected by a total-field whitelist", receipt, err)
				}
			} else {
				if err == nil || receipt != nil {
					t.Fatal("old/misbound contract reached a native dispatch", mode, receipt, err)
				}
				assertIdleInteractionUnchanged(t, before, idleInteractionSnapshot(t, native))
			}
			assertIdleInteractionQuiet(t, m, p, callback, calls, revision, wantCalls)
		})
	}
}

func TestPolicyIdleInteractionCallbackDeniesEveryPhysicalPacket(t *testing.T) {
	m := New(nil, nil, nil)
	t.Cleanup(m.cancel)
	token := strings.Repeat("a", 64)
	op := &policySpell{capability: "acquire_idle_interaction", admitted: true, origin: modsession.WriteOrigin{
		SessionID: "idle-denial", Module: actionsModule, Writer: "lua", PolicyRevision: "source", Generation: 1, ActionID: "1"}}
	m.policySpells.active = map[string]*policySpell{token: op}
	writes := 0
	m.session.SendPacket = func(context.Context, uint32, []byte) error { writes++; return nil }
	outgoing := modcb.WithPolicySpell(context.Background(), token)
	md, _ := metadata.FromOutgoingContext(outgoing)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	for _, packet := range []struct {
		code uint32
		body []byte
	}{{opcode.MSGMoveStop, nil}, {opcode.CMSGAttackstop, nil}, {opcode.CMSGCastSpell, []byte{0}},
		{opcode.CMSGUseItem, []byte{0}}, {opcode.CMSGCancelCast, nil}} {
		if op.matchesActionPacket(packet.code, packet.body) {
			t.Fatal("idle metadata capability admitted a physical opcode", packet.code)
		}
		if err := m.modulePacketWriter("idle-denial", actionsModule)(ctx, packet.code, packet.body); status.Code(err) != codes.PermissionDenied {
			t.Fatal("exact idle callback token escaped packet denial", packet.code, err)
		}
	}
	if writes != 0 {
		t.Fatal("idle callback wrote physical packets", writes)
	}
}
