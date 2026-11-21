package buddytrade

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// This explicit trusted-session fixture provides a decision, not a duplicate
// classifier. Policy behavior itself is covered by the actual Lua bundle tests.
type junkPolicyTestSession struct {
	*junkDispatchSession
	t         *testing.T
	service   *Service
	callbacks int
	input     map[string]any
	reply     func(map[string]any) (map[string]any, error)
}

func installJunkPolicyFixture(s *Service, wrapper *junkDispatchSession, t *testing.T) *junkPolicyTestSession {
	peer := &junkPolicyTestSession{junkDispatchSession: wrapper, service: s, t: t}
	s.session = peer
	return peer
}

func (f *junkPolicyTestSession) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if request.Module != modcb.DisposalPolicyModule || request.Method != modcb.DisposalPolicyMethod {
		return f.junkDispatchSession.InvokeModule(ctx, request, opts...)
	}
	method := request.Module + "." + request.Method
	f.calls = append(f.calls, method)
	if f.before != nil {
		if err := f.before(ctx, method); err != nil {
			return nil, err
		}
	}
	f.callbacks++
	value := &structpb.Struct{}
	if request.GetRequest().UnmarshalTo(value) != nil {
		return nil, errors.New("untyped disposal callback")
	}
	fields := value.AsMap()
	input, ok := fields["input"].(map[string]any)
	if !ok || len(fields) != 3 || len(input) != 7 || fields["sessionId"] != f.service.state.SessionId ||
		fields["selfGuid"] != strconv.FormatUint(f.service.selfGUID, 10) || input["sessionId"] != fields["sessionId"] ||
		input["selfGuid"] != fields["selfGuid"] || input["kind"] != "owned_disposal" || input["mode"] != "owned_destroy" {
		return nil, errors.New("inexact native-built disposal input")
	}
	f.input = input
	source := &worldv1.BackpackSlot{}
	ref, ok := input["source"].(map[string]any)
	if !ok || junkPolicyDecode(ref, source) != nil {
		return nil, errors.New("invalid native source")
	}
	expected, ok := input["expected"].(map[string]any)
	w := f.world
	if !ok || len(expected) != 3 || expected["inventoryRevision"] != strconv.FormatUint(w.InventoryRevision, 10) ||
		expected["equipmentRevision"] != strconv.FormatUint(w.SelfEquipmentRevision, 10) || expected["questLogRevision"] != strconv.FormatUint(w.SelfQuestLogRevision, 10) {
		return nil, errors.New("inexact native context revisions")
	}
	var observed *worldv1.BackpackSlot
	if source.Bag == 0 {
		for _, row := range w.Backpack {
			if row.GetSlot() == source.Slot {
				observed = row
			}
		}
	} else {
		for _, bag := range w.InventoryBags {
			for _, row := range bag.GetSlots() {
				if row.GetBag() == source.Bag && row.GetSlot() == source.Slot {
					observed = row
				}
			}
		}
	}
	if observed == nil || observed.ItemGuid != source.ItemGuid || observed.Entry != source.Entry || observed.StackCount != source.StackCount {
		return nil, errors.New("native selected source changed")
	}
	proofWorld := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: w.Self.Guid, Present: w.Self.Present},
		InventoryKnown: w.InventoryKnown, InventoryRevision: w.InventoryRevision,
		SelfEquipmentKnown: w.SelfEquipmentKnown, SelfEquipmentRevision: w.SelfEquipmentRevision, SelfEquipment: w.SelfEquipment,
		SelfQuestLogKnown: w.SelfQuestLogKnown, SelfQuestLogRevision: w.SelfQuestLogRevision, SelfQuestLog: w.SelfQuestLog}
	proof, err := junkPolicyObject(proofWorld)
	if err != nil {
		return nil, err
	}
	instance, err := junkPolicyObject(observed)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"available": true, "revision": strings.Repeat("ab", 32), "decision": map[string]any{
		"status": "eligible", "reason": "synthetic_trusted_session_decision", "nativeContext": map[string]any{
			"sessionId": fields["sessionId"], "selfGuid": fields["selfGuid"], "source": instance, "world": proof}}}
	if f.reply != nil {
		result, err = f.reply(result)
		if err != nil {
			return nil, err
		}
	}
	encoded, err := structpb.NewStruct(result)
	if err != nil {
		return nil, err
	}
	packed, err := anypb.New(encoded)
	response := &modv1.InvokeModuleResponse{Result: packed}
	if err == nil && f.after != nil {
		err = f.after(method, response)
	}
	return response, err
}

func junkPolicyTestDecision(result map[string]any) map[string]any {
	return result["decision"].(map[string]any)
}
func junkPolicyTestWitness(result map[string]any) map[string]any {
	return junkPolicyTestDecision(result)["nativeContext"].(map[string]any)
}

func TestJunkPolicyNewIDsUseTrustedDecisionBeforeLeaseAndNativeReservation(t *testing.T) {
	for _, carried := range []bool{false, true} {
		t.Run(fmt.Sprint(carried), func(t *testing.T) {
			s, f, wrapper, request := junkHistoryFixture(carried, "lua-new-id")
			peer := s.session.(*junkPolicyTestSession)
			selected := junkDispatchSource(f, carried)
			// The new request's exact factual template is separate from the
			// retired native business predicate; no fallback may select it.
			delete(s.metadata, selected.Entry)
			reserved := false
			wrapper.beforeWrite = func(packet *modv1.SendPacketRequest) {
				if packet.Opcode != opcode.CMSGDestroyitem {
					return
				}
				reserved = true
				if peer.callbacks != 1 || len(wrapper.calls) == 0 || wrapper.calls[len(wrapper.calls)-1] != "buddy-world.snapshot" ||
					s.junkRequests[request.RequestId] == nil || s.junkPending == nil {
					t.Fatal("new-ID admission, final World or native reservation ordering changed", wrapper.calls)
				}
			}
			result, err := s.DestroyJunk(context.Background(), request)
			if err != nil || !reserved || result.LastJunkDisposal.GetRequestId() != request.RequestId || f.count(opcode.CMSGDestroyitem) != 1 {
				t.Fatal(result, err, wrapper.calls)
			}
			callback, firstLease := -1, -1
			for index, method := range wrapper.calls {
				if method == "session.disposal_decision" {
					callback = index
				}
				if firstLease < 0 && method == "buddy-actions.acquire_interaction" {
					firstLease = index
				}
			}
			if callback < 0 || firstLease <= callback {
				t.Fatal("classification did not precede lease acquisition", wrapper.calls)
			}
			actual := &tradev1.ItemDetails{}
			if junkPolicyDecode(peer.input["item"].(map[string]any), actual) != nil || !proto.Equal(actual, s.lookup.items[selected.Entry]) || peer.input["eligible"] != nil {
				t.Fatal("native input did not use the actual full requested template")
			}
		})
	}
}

func TestJunkPolicyRejectsMissingOrIncompleteActualTemplateBeforeCallback(t *testing.T) {
	for name, change := range map[string]func(*Service, uint32){
		"missing":        func(s *Service, entry uint32) { delete(s.lookup.items, entry) },
		"unavailable":    func(s *Service, entry uint32) { s.lookup.items[entry].Available = false },
		"pending":        func(s *Service, entry uint32) { s.lookup.items[entry].Pending = true },
		"old_raw_absent": func(s *Service, entry uint32) { s.lookup.items[entry].RawTemplateFacts = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, _, request := junkHistoryFixture(true, "template-required")
			peer := s.session.(*junkPolicyTestSession)
			change(s, junkDispatchSource(f, true).Entry)
			if _, err := s.DestroyJunk(context.Background(), request); status.Code(err) != codes.FailedPrecondition || peer.callbacks != 0 || f.leases != 0 || len(s.junkRequests) != 0 || f.count(opcode.CMSGDestroyitem) != 0 {
				t.Fatal("unobserved template reached admission or transport", err)
			}
		})
	}
}

func TestJunkPolicyRefusesUnavailableMalformedOrUnmatchedWitnessWithoutFallback(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unavailable":     func(r map[string]any) { r["available"] = false },
		"no_revision":     func(r map[string]any) { r["revision"] = "" },
		"bad_revision":    func(r map[string]any) { r["revision"] = "stale" },
		"keep":            func(r map[string]any) { junkPolicyTestDecision(r)["status"] = "keep" },
		"unknown":         func(r map[string]any) { junkPolicyTestDecision(r)["status"] = "unknown" },
		"no_reason":       func(r map[string]any) { delete(junkPolicyTestDecision(r), "reason") },
		"no_witness":      func(r map[string]any) { delete(junkPolicyTestDecision(r), "nativeContext") },
		"foreign_session": func(r map[string]any) { junkPolicyTestWitness(r)["sessionId"] = "another" },
		"foreign_body":    func(r map[string]any) { junkPolicyTestWitness(r)["selfGuid"] = "3" },
		"extra_witness":   func(r map[string]any) { junkPolicyTestWitness(r)["eligible"] = true },
		"source_array":    func(r map[string]any) { junkPolicyTestWitness(r)["source"] = []any{} },
		"source_guid": func(r map[string]any) {
			junkPolicyTestWitness(r)["source"].(map[string]any)["itemGuid"] = "4611686018427387999"
		},
		"source_count":    func(r map[string]any) { junkPolicyTestWitness(r)["source"].(map[string]any)["stackCount"] = float64(1) },
		"source_metadata": func(r map[string]any) { junkPolicyTestWitness(r)["source"].(map[string]any)["metadataKnown"] = false },
		"source_enhancement_known": func(r map[string]any) {
			junkPolicyTestWitness(r)["source"].(map[string]any)["enhancementsKnown"] = true
		},
		"source_durability":   func(r map[string]any) { junkPolicyTestWitness(r)["source"].(map[string]any)["durability"] = float64(1) },
		"unknown_world_field": func(r map[string]any) { junkPolicyTestWitness(r)["world"].(map[string]any)["observedPackets"] = "1" },
		"inventory_unknown":   func(r map[string]any) { junkPolicyTestWitness(r)["world"].(map[string]any)["inventoryKnown"] = false },
		"inventory_revision":  func(r map[string]any) { junkPolicyTestWitness(r)["world"].(map[string]any)["inventoryRevision"] = "43" },
		"short_equipment": func(r map[string]any) {
			w := junkPolicyTestWitness(r)["world"].(map[string]any)
			w["selfEquipment"] = w["selfEquipment"].([]any)[:18]
		},
		"duplicate_equipment": func(r map[string]any) {
			w := junkPolicyTestWitness(r)["world"].(map[string]any)
			w["selfEquipment"].([]any)[1].(map[string]any)["slot"] = float64(0)
		},
		"equipment_trait": func(r map[string]any) {
			w := junkPolicyTestWitness(r)["world"].(map[string]any)
			w["selfEquipment"].([]any)[0].(map[string]any)["hasEnhancements"] = true
		},
		"short_log": func(r map[string]any) {
			w := junkPolicyTestWitness(r)["world"].(map[string]any)
			w["selfQuestLog"] = w["selfQuestLog"].([]any)[:24]
		},
		"duplicate_log": func(r map[string]any) {
			w := junkPolicyTestWitness(r)["world"].(map[string]any)
			w["selfQuestLog"].([]any)[1].(map[string]any)["slot"] = float64(0)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, _, request := junkHistoryFixture(true, "bad-admission")
			peer := s.session.(*junkPolicyTestSession)
			peer.reply = func(r map[string]any) (map[string]any, error) { mutate(r); return r, nil }
			if _, err := s.DestroyJunk(context.Background(), request); status.Code(err) != codes.FailedPrecondition || peer.callbacks != 1 || f.leases != 0 || len(s.junkRequests) != 0 || f.count(opcode.CMSGDestroyitem) != 0 {
				t.Fatal("bad callback fell back to native eligibility or reserved a write", err)
			}
		})
	}
	t.Run("transport", func(t *testing.T) {
		s, f, _, request := junkHistoryFixture(true, "callback-disconnected")
		peer := s.session.(*junkPolicyTestSession)
		peer.reply = func(map[string]any) (map[string]any, error) { return nil, errors.New("unknown callback transport") }
		if _, err := s.DestroyJunk(context.Background(), request); status.Code(err) != codes.FailedPrecondition || f.leases != 0 || len(s.junkRequests) != 0 || f.count(opcode.CMSGDestroyitem) != 0 {
			t.Fatal(err)
		}
	})
}

func TestJunkPolicyRechecksEveryReferencedRawFactAfterLastWorld(t *testing.T) {
	changes := map[string]func(*Service, *fakeSession, bool){
		"inventory_revision": func(_ *Service, f *fakeSession, _ bool) { f.world.InventoryRevision++ },
		"source_enhancement_known": func(_ *Service, f *fakeSession, carried bool) {
			junkDispatchSource(f, carried).EnhancementsKnown = true
		},
		"source_durability":             func(_ *Service, f *fakeSession, carried bool) { junkDispatchSource(f, carried).Durability = 1 },
		"same_revision_equipment_trait": func(_ *Service, f *fakeSession, _ bool) { f.world.SelfEquipment[0].HasEnhancements = true },
		"same_revision_quest_state":     func(_ *Service, f *fakeSession, _ bool) { f.world.SelfQuestLog[0].State = 1 },
		"same_revision_quest_counts": func(_ *Service, f *fakeSession, _ bool) {
			f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{1, 0, 0, 0}
		},
		"template": func(s *Service, f *fakeSession, carried bool) {
			s.lookup.items[junkDispatchSource(f, carried).Entry].SellPrice++
		},
	}
	for _, carried := range []bool{false, true} {
		for name, change := range changes {
			t.Run(fmt.Sprintf("%t/%s", carried, name), func(t *testing.T) {
				s, f, wrapper, request := junkHistoryFixture(carried, "context-changed")
				injected := false
				wrapper.before = func(_ context.Context, method string) error {
					if !injected && method == "buddy-actions.acquire_interaction" {
						injected = true
						change(s, f, carried)
					}
					return nil
				}
				if _, err := s.DestroyJunk(context.Background(), request); !injected || status.Code(err) != codes.FailedPrecondition || len(s.junkRequests) != 0 || f.count(opcode.CMSGDestroyitem) != 0 {
					t.Fatal("post-admission facts changed without a new decision", err, wrapper.calls)
				}
			})
		}
	}
}

func TestJunkPolicyExactReplayDoesNotReevaluateOrRetryUnknownWrite(t *testing.T) {
	s, f, wrapper, request := junkHistoryFixture(true, "unknown-original-policy")
	peer := s.session.(*junkPolicyTestSession)
	f.fail = opcode.CMSGDestroyitem
	if _, err := s.DestroyJunk(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	peer.reply = func(map[string]any) (map[string]any, error) {
		t.Fatal("replay reentered a replaced policy")
		return nil, nil
	}
	before := append([]string(nil), wrapper.calls...)
	result, err := s.DestroyJunk(context.Background(), request)
	if err != nil || result.LastJunkDisposal.GetStatus() != "TRANSPORT_UNCONFIRMED_NO_RETRY" || peer.callbacks != 1 || !reflect.DeepEqual(before, wrapper.calls) || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("unknown exact operation lost custody", result, err)
	}
	changed := proto.Clone(request).(*tradev1.JunkRequest)
	changed.ExpectedItemGuid++
	if _, err := s.DestroyJunk(context.Background(), changed); status.Code(err) != codes.AlreadyExists || peer.callbacks != 1 || !reflect.DeepEqual(before, wrapper.calls) {
		t.Fatal("changed request reentered policy", err)
	}
}

func TestJunkPolicyComparesCompleteVectorsBySlotAndKeepsLegacyEmptyID(t *testing.T) {
	s, f, _, request := junkHistoryFixture(true, "reordered-witness")
	peer := s.session.(*junkPolicyTestSession)
	peer.reply = func(r map[string]any) (map[string]any, error) {
		w := junkPolicyTestWitness(r)["world"].(map[string]any)
		for _, field := range []string{"selfEquipment", "selfQuestLog"} {
			rows := w[field].([]any)
			for a, b := 0, len(rows)-1; a < b; a, b = a+1, b-1 {
				rows[a], rows[b] = rows[b], rows[a]
			}
		}
		return r, nil
	}
	if _, err := s.DestroyJunk(context.Background(), request); err != nil || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("reordered complete witness changed its slot identity", err)
	}
	legacy, old := junkFixture()
	if _, err := legacy.DestroyJunk(context.Background(), requestJunk(legacy, old)); err != nil || old.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("legacy empty-ID compatibility changed", err)
	}
}
