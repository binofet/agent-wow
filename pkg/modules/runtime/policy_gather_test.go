package modrt

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const policyGatherSource uint64 = 0xf110000000000000 | uint64(161557)<<24 | 42

func policyGatherAction() map[string]any {
	return map[string]any{"capability": "loot_gather", "sessionId": "corpse-body", "selfGuid": "6",
		"sourceGuid": strconv.FormatUint(policyGatherSource, 10), "questId": float64(3904), "itemId": float64(11119), "openSpell": float64(6478),
		"requestId": "gather-selected-1", "visitId": "1", "expectedPositionRevision": "1", "expectedObjectRevision": "1",
		"expectedInventoryRevision": "1", "expectedQuestLogRevision": "1", "approach": true}
}

func TestPolicyGatherPacketBindsExactRecipeAndPackedGameObject(t *testing.T) {
	op := &policySpell{target: policyGatherSource, gather: &policyGather{openSpell: 6478}}
	golden, err := hex.DecodeString("074e1900000000080000f92a15770210f1")
	if err != nil || !op.matchesActionPacket(opcode.CMSGCastSpell, golden) {
		t.Fatal("exact native gather opening was refused", err)
	}
	for i := 0; i < len(golden); i++ {
		if op.matchesActionPacket(opcode.CMSGCastSpell, golden[:i]) {
			t.Fatalf("truncated opening accepted at %d", i)
		}
	}
	for _, at := range []int{1, 5, 6, 10, 11, len(golden) - 1} {
		other := append([]byte(nil), golden...)
		other[at]++
		if op.matchesActionPacket(opcode.CMSGCastSpell, other) {
			t.Fatalf("unbound opening byte accepted at %d", at)
		}
	}
	if op.matchesActionPacket(opcode.CMSGCastSpell, append(golden, 0)) ||
		op.matchesActionPacket(opcode.CMSGLoot, golden) || op.matchesActionPacket(opcode.MSGMoveStop, golden) {
		t.Fatal("gather token admitted trailing data, a different opcode or movement")
	}
}

func TestPolicyGatherKnownTransportRefusalRetainsNoOpeningAttempt(t *testing.T) {
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	t.Cleanup(m.cancel)
	calls := 0
	m.session.SendPacket = func(context.Context, uint32, []byte) error {
		calls++
		return modcb.PacketNotDispatchedError("final world binding refused before transport")
	}
	op := &policySpell{target: policyGatherSource, gather: &policyGather{openSpell: 6478}, admitted: true,
		origin: modsession.WriteOrigin{SessionID: "corpse-body", Module: "buddy-loot", Writer: "lua", ActionID: "selected-opening"}}
	m.policySpells.active = map[string]*policySpell{"exact-token": op}
	golden, _ := hex.DecodeString("074e1900000000080000f92a15770210f1")
	err = m.writePolicySpell(context.Background(), "exact-token", "corpse-body", "buddy-loot", opcode.CMSGCastSpell, golden)
	if !modcb.PacketWasNotDispatched(err) || calls != 1 || op.attempted || op.written {
		t.Fatal("known refusal became an uncertain opening attempt", err, calls, op.attempted, op.written)
	}
	op.admitted = false
	if err = m.writePolicySpell(context.Background(), "exact-token", "corpse-body", "buddy-loot", opcode.CMSGCastSpell, golden); !modcb.PacketWasNotDispatched(err) || calls != 1 {
		t.Fatal("retired dispatch token entered transport again", err, calls)
	}
}

func policyGatherBinding(t *testing.T, registry *moddisc.Registry, respond func(context.Context, string, proto.Message, proto.Message) error) (*Manager, *SessionPolicy, *int) {
	t.Helper()
	m := New(registry, nil, nil)
	m.started, m.dir = true, "/private/corpse-body"
	m.session.Identity = modsession.Identity{CharacterGUID: 6}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	calls := new(int)
	conn, err := grpc.NewClient("passthrough:///no-native-network", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, result any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			*calls++
			return respond(ctx, method, request.(proto.Message), result.(proto.Message))
		}))
	if err != nil {
		t.Fatal(err)
	}
	m.instances["buddy-loot"].conn = conn
	t.Cleanup(func() { m.cancel(); conn.Close() })
	p, err := m.StartPolicy(context.Background(), idleInteractionPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return m, p, calls
}

func TestPolicyGatherApproachCustodyIsNotAFalseNonDispatchOrReplay(t *testing.T) {
	for _, mode := range []string{"approach_reserved", "known_not_dispatched", "reply_unavailable", "wrong_receipt_source", "old_binary_unimplemented"} {
		t.Run(mode, func(t *testing.T) {
			registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
			if err != nil {
				t.Fatal(err)
			}
			m, p, calls := policyGatherBinding(t, registry, func(_ context.Context, method string, request, result proto.Message) error {
				if method != lootv1.BuddyLoot_OpenGather_FullMethodName {
					t.Fatal("gather used a different native method", method)
				}
				raw, _ := protojson.Marshal(request)
				typed := &lootv1.OpenGatherRequest{}
				if err := protojson.Unmarshal(raw, typed); err != nil || typed.SourceGuid != policyGatherSource ||
					typed.QuestId != 3904 || typed.ItemId != 11119 || typed.OpenSpell != 6478 || !typed.Approach {
					t.Fatal("selected recipe/source did not reach the exact typed RPC", string(raw), err)
				}
				if mode == "reply_unavailable" {
					return status.Error(codes.Unavailable, "reply lost after source admission may have occurred")
				}
				if mode == "old_binary_unimplemented" {
					return status.Error(codes.Unimplemented, "new method not installed")
				}
				receipt := &lootv1.CorpseOperation{SessionId: typed.SessionId, SelfGuid: typed.SelfGuid,
					SourceGuid: typed.SourceGuid, RequestId: typed.RequestId, Phase: "gather_approaching"}
				if mode == "known_not_dispatched" {
					receipt.Phase, receipt.Terminal = "not_dispatched", true
				}
				if mode == "wrong_receipt_source" {
					receipt.SourceGuid++
				}
				raw, _ = proto.Marshal(receipt)
				return proto.Unmarshal(raw, result)
			})
			_, receipt, err := p.Step(context.Background(), map[string]any{"request": policyGatherAction()}, nil)
			want := behavior.ActionSettledUnverified
			if mode == "known_not_dispatched" || mode == "old_binary_unimplemented" {
				want = behavior.ActionNotDispatched
			}
			if err != nil || receipt == nil || receipt.Outcome != want || *calls != 1 ||
				receipt.Payload["opening_transport_attempted"] != false || receipt.Payload["transport_written"] != false {
				t.Fatal("source custody/refusal lost truthful no-opening disposition", receipt, err, *calls)
			}
			if mode == "approach_reserved" && (receipt.Payload["source_reserved"] != true || receipt.Payload["reconciliationRequired"] != true) {
				t.Fatal("accepted approach was not retained as source-bound pending", receipt)
			}
			if mode == "reply_unavailable" {
				// Unavailable follows the existing manager-failure path, which
				// cancels this host's lifetime. It cannot take a later normal
				// turn to drain the queued result; custody must remain inspectable.
				snapshot := p.dispatchSnapshot(p.vm.Status())
				outstanding := p.vm.Outstanding()
				if status.Code(m.Err()) != codes.Unavailable || p.lifetime.Err() == nil ||
					snapshot == nil || snapshot.ActionID != strconv.FormatUint(receipt.ActionID, 10) ||
					snapshot.TransportOutcome != behavior.ActionSettledUnverified ||
					snapshot.Payload["source_guid"] != strconv.FormatUint(policyGatherSource, 10) ||
					snapshot.Payload["request_id"] != "gather-selected-1" || snapshot.Payload["reconciliationRequired"] != true ||
					len(outstanding) != 1 || outstanding[0].ActionID != receipt.ActionID ||
					outstanding[0].Outcome != behavior.ActionReserved || p.vm.Status().Buffered != 1 {
					t.Fatal("module failure dropped source-bound dispatch or reservation evidence", m.Err(), snapshot, outstanding, p.vm.Status())
				}
				if _, next, err := p.Step(context.Background(), nil, nil); !errors.Is(err, behavior.ErrSessionClosed) || next != nil ||
					*calls != 1 || len(m.policySpells.active) != 0 || p.vm.Status().Pending != 1 {
					t.Fatal("failed manager replayed or cleared unresolved gather custody", err, next, *calls, p.vm.Status())
				}
				return
			}
			if _, _, err = p.Step(context.Background(), nil, nil); err != nil || *calls != 1 ||
				len(m.policySpells.active) != 0 || p.vm.Status().Pending != 0 {
				t.Fatal("gather replayed, retained a dead callback token or blocked support", err, *calls, p.vm.Status())
			}
		})
	}
}

func gatherInstalledContract(t *testing.T, mode string) *moddisc.Registry {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"buddy-loot", "buddy-actions", "buddy-party", "buddy-world"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "modules", name, file))
			if err != nil {
				t.Fatal(err)
			}
			if name == "buddy-loot" && file == "module.yaml" {
				if mode == "absent" {
					var lines []string
					for _, line := range strings.Split(string(raw), "\n") {
						if !strings.HasPrefix(line, "  open_gather:") {
							lines = append(lines, line)
						}
					}
					raw = []byte(strings.Join(lines, "\n"))
				} else if mode == "corpse_alias" {
					raw = []byte(strings.Replace(string(raw), lootv1.BuddyLoot_OpenGather_FullMethodName, lootv1.BuddyLoot_OpenCorpse_FullMethodName, 1))
				}
			}
			if name == "buddy-loot" && file == "module.pb" && mode == "wrong_tag" {
				set := &descriptorpb.FileDescriptorSet{}
				if err = proto.Unmarshal(raw, set); err != nil {
					t.Fatal(err)
				}
				for _, file := range set.File {
					for _, message := range file.MessageType {
						if message.GetName() == "OpenGatherRequest" {
							for _, field := range message.Field {
								if field.GetName() == "source_guid" {
									field.Number = proto.Int32(33)
								}
							}
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
	return registry
}

func TestPolicyGatherOldOrWrongInstalledContractNeverDispatches(t *testing.T) {
	for _, mode := range []string{"absent", "corpse_alias", "wrong_tag"} {
		t.Run(mode, func(t *testing.T) {
			_, p, calls := policyGatherBinding(t, gatherInstalledContract(t, mode), func(context.Context, string, proto.Message, proto.Message) error {
				t.Fatal("incompatible installed gather contract reached RPC")
				return nil
			})
			_, receipt, err := p.Step(context.Background(), map[string]any{"request": policyGatherAction()}, nil)
			if err == nil || receipt != nil || *calls != 0 {
				t.Fatal("incompatible binding did not fail closed before dispatch", receipt, err, *calls)
			}
		})
	}
}

// Keep the action JSON detached and transport-free when body/revision facts
// are malformed; descriptor decoding is not a replacement for these bindings.
func TestPolicyGatherMalformedBodyAndRevisionsNeverDispatch(t *testing.T) {
	for _, key := range []string{"sessionId", "selfGuid", "sourceGuid", "visitId", "expectedObjectRevision", "questId", "approach"} {
		t.Run(key, func(t *testing.T) {
			registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
			if err != nil {
				t.Fatal(err)
			}
			_, p, calls := policyGatherBinding(t, registry, func(context.Context, string, proto.Message, proto.Message) error {
				t.Fatal("malformed action reached RPC")
				return nil
			})
			action := policyGatherAction()
			delete(action, key)
			before, _ := json.Marshal(action)
			_, receipt, err := p.Step(context.Background(), map[string]any{"request": action}, nil)
			after, _ := json.Marshal(action)
			if err == nil || receipt != nil || *calls != 0 || string(before) != string(after) {
				t.Fatal("malformed gather request changed input or dispatched", receipt, err, *calls)
			}
		})
	}
}
