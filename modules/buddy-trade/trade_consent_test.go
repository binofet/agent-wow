package buddytrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type tradeConsentSession struct {
	*fakeSession
	response    map[string]any
	fault       error
	inputs      []map[string]any
	calls       []string
	renewals    []*actionsv1.InteractionRequest
	before      func(context.Context, string)
	after       func(string, *modv1.InvokeModuleResponse)
	beforeWrite func(*modv1.SendPacketRequest)
}

func (f *tradeConsentSession) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	method := request.Module + "." + request.Method
	f.calls = append(f.calls, method)
	if f.before != nil {
		f.before(ctx, method)
	}
	if method == modcb.TradeConsentPolicyModule+"."+modcb.TradeConsentPolicyMethod {
		input := &structpb.Struct{}
		if request.GetRequest().UnmarshalTo(input) != nil {
			return nil, errors.New("untyped consent callback")
		}
		f.inputs = append(f.inputs, input.AsMap())
		if f.fault != nil {
			return nil, f.fault
		}
		value, err := structpb.NewStruct(f.response)
		if err != nil {
			return nil, err
		}
		packed, err := anypb.New(value)
		return &modv1.InvokeModuleResponse{Result: packed}, err
	}
	if method == "buddy-actions.acquire_interaction" {
		r := &actionsv1.InteractionRequest{}
		if err := request.GetRequest().UnmarshalTo(r); err != nil {
			return nil, err
		}
		f.renewals = append(f.renewals, r)
	}
	response, err := f.fakeSession.InvokeModule(ctx, request, opts...)
	if err == nil && f.after != nil {
		f.after(method, response)
	}
	return response, err
}

func (f *tradeConsentSession) SendPacket(ctx context.Context, request *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.beforeWrite != nil {
		f.beforeWrite(request)
	}
	f.calls = append(f.calls, fmt.Sprintf("packet:%d", request.Opcode))
	return f.fakeSession.SendPacket(ctx, request, opts...)
}

func tradeConsentResponse(kind string) map[string]any {
	return map[string]any{"available": true, "revision": strings.Repeat("ab", 32),
		"decision": map[string]any{"kind": kind, "reason": "fixture_" + kind}}
}

func tradeConsentFixture() (*Service, *fakeSession, *tradeConsentSession) {
	s, f := fixture()
	openIncoming(s)
	statusPacket(s, 4)
	wrapper := &tradeConsentSession{fakeSession: f, response: tradeConsentResponse("accept")}
	s.session = wrapper
	return s, f, wrapper
}

func TestTradeConsentProjectsOnlyExactWindowFactsIncludingSevenPartnerSlots(t *testing.T) {
	state := &tradev1.State{SessionId: "window", Revision: ^uint64(0), Phase: "OPEN", PartnerGuid: 3,
		Backpack: []*tradev1.Item{{Name: strings.Repeat("unused", 10000)}}, JunkOperations: []*tradev1.JunkDisposal{{Name: strings.Repeat("unused", 10000)}}}
	for slot := uint32(0); slot < 7; slot++ {
		state.PartnerItems = append(state.PartnerItems, &tradev1.Item{Slot: slot})
		if slot < 6 {
			state.OwnRequestedItems = append(state.OwnRequestedItems, &tradev1.Item{Slot: slot})
		}
	}
	frame, view, err := tradeConsentFrame(state)
	if err != nil || len(view) != 10 || view["revision"] != "18446744073709551615" || view["partnerGuid"] != "3" ||
		view["partnerAccepted"] != false || view["acceptTransportWritten"] != false || view["partnerCopper"] != float64(0) || view["ownRequestedCopper"] != float64(0) ||
		len(view["partnerItems"].([]any)) != 7 || len(view["ownRequestedItems"].([]any)) != 6 || frame.Backpack != nil || frame.JunkOperations != nil {
		t.Fatal("projection lost observed zeros/service rows or copied unrelated state", frame, view, err)
	}
	service := view["partnerItems"].([]any)[6].(map[string]any)
	if len(service) != 6 || service["slot"] != float64(6) || service["entry"] != float64(0) || service["count"] != float64(0) || service["itemGuid"] != "0" || service["name"] != "" || service["metadataKnown"] != false {
		t.Fatal("service row invented ownership or omitted explicit zeros", service)
	}
	state.PartnerItems[6].Entry = 123
	if frame.PartnerItems[6].Entry != 0 || service["entry"] != float64(0) {
		t.Fatal("captured projection aliases native mutable state")
	}
	for name, change := range map[string]func(*tradev1.State){
		"duplicate_slot": func(s *tradev1.State) { s.PartnerItems[0].Slot = 1 },
		"excess_own":     func(s *tradev1.State) { s.OwnRequestedItems = append(s.OwnRequestedItems, &tradev1.Item{Slot: 6}) },
		"excess_partner": func(s *tradev1.State) { s.PartnerItems = append(s.PartnerItems, &tradev1.Item{Slot: 7}) },
		"nil_row":        func(s *tradev1.State) { s.PartnerItems[0] = nil },
		"long_name":      func(s *tradev1.State) { s.PartnerItems[0].Name = strings.Repeat("x", 256) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.Clone(state).(*tradev1.State)
			change(changed)
			if _, _, err := tradeConsentFrame(changed); err == nil {
				t.Fatal("malformed native window was projected")
			}
		})
	}
}

func TestTradeConsentAcceptReadsPolicyBeforeFinalGuardsAndReservesOneEmptyPacket(t *testing.T) {
	s, f, wrapper := tradeConsentFixture()
	before := s.snapshot()
	wrapper.beforeWrite = func(packet *modv1.SendPacketRequest) {
		if packet.Opcode != opcode.CMSGAcceptTrade {
			return
		}
		if len(packet.Payload) != 0 || s.snapshot().Phase != "ACCEPT_SENT" || s.snapshot().PartnerAccepted || f.count(opcode.CMSGAcceptTrade) != 0 {
			t.Fatal("exact empty ACCEPT was not reserved before transport", packet, s.snapshot())
		}
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 1 || len(wrapper.inputs) != 1 || len(wrapper.renewals) != 1 || wrapper.renewals[0].Token != "lease-token" ||
		wrapper.renewals[0].Owner != "trade" || wrapper.renewals[0].LeaseMs != 30000 || s.snapshot().CompletedTrades != 0 {
		t.Fatal("native consent was repeated, unbound or invented completion", s.snapshot(), wrapper.calls)
	}
	envelope := wrapper.inputs[0]
	input := envelope["input"].(map[string]any)
	view := input["trade"].(map[string]any)
	if len(envelope) != 3 || envelope["sessionId"] != before.SessionId || envelope["selfGuid"] != "1" || len(input) != 3 ||
		input["sessionId"] != before.SessionId || input["selfGuid"] != "1" || view["revision"] != fmt.Sprint(before.Revision) || view["partnerAccepted"] != true {
		t.Fatal("consent did not bind exact body/session/window", envelope)
	}
	policyAt, acceptAt := -1, -1
	for index, method := range wrapper.calls {
		if method == "session.trade_consent" {
			policyAt = index
		}
		if method == fmt.Sprintf("packet:%d", opcode.CMSGAcceptTrade) {
			acceptAt = index
		}
	}
	if policyAt < 0 || acceptAt <= policyAt+3 || wrapper.calls[policyAt+1] != "buddy-actions.acquire_interaction" || wrapper.calls[policyAt+2] != "buddy-world.snapshot" || wrapper.calls[policyAt+3] != "buddy-actions.status" {
		t.Fatal("policy evaluation followed native final guards", wrapper.calls)
	}
	statusPacket(s, 8)
	s.Tick(context.Background())
	if s.snapshot().CompletedTrades != 1 || s.snapshot().Phase != "COMPLETE" {
		t.Fatal("actual server completion did not settle native state", s.snapshot())
	}
}

func TestTradeConsentWaitFaultAndMalformedExportsNeverFallBack(t *testing.T) {
	for name, response := range map[string]map[string]any{
		"wait":            tradeConsentResponse("wait"),
		"missing_export":  {"available": false, "revision": strings.Repeat("ab", 32), "decision": map[string]any{}},
		"old_response":    {"accept": true},
		"cancel_kind":     tradeConsentResponse("cancel"),
		"empty_reason":    {"available": true, "revision": strings.Repeat("ab", 32), "decision": map[string]any{"kind": "accept", "reason": ""}},
		"extra_authority": {"available": true, "revision": strings.Repeat("ab", 32), "decision": map[string]any{"kind": "accept", "reason": "ok", "eligible": true}},
		"long_reason":     {"available": true, "revision": strings.Repeat("ab", 32), "decision": map[string]any{"kind": "accept", "reason": strings.Repeat("x", 129)}},
		"bad_revision":    {"available": true, "revision": "previous", "decision": map[string]any{"kind": "accept", "reason": "ok"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, wrapper := tradeConsentFixture()
			wrapper.response = response
			s.Tick(context.Background())
			if f.count(opcode.CMSGAcceptTrade) != 0 || f.count(opcode.CMSGCancelTrade) != 0 || len(wrapper.renewals) != 0 || s.snapshot().Phase != "OPEN" || s.snapshot().Reason == "" {
				t.Fatal("unavailable policy caused fallback, cancel or acceptance", s.snapshot(), wrapper.calls)
			}
		})
	}
	s, f, wrapper := tradeConsentFixture()
	wrapper.fault = errors.New("current Lua fault")
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 0 || f.count(opcode.CMSGCancelTrade) != 0 || s.snapshot().Phase != "OPEN" {
		t.Fatal("policy fault did not wait without replay", s.snapshot())
	}
}

func TestTradeConsentRejectsChangedOffersAndCustodyDuringPolicyOrFinalChecks(t *testing.T) {
	for _, moment := range []string{"policy", "final_status"} {
		for name, change := range map[string]func(*Service, *fakeSession){
			"revision":                        func(s *Service, f *fakeSession) { s.mu.Lock(); s.state.Revision++; s.mu.Unlock() },
			"session_without_revision":        func(s *Service, f *fakeSession) { s.mu.Lock(); s.state.SessionId = "replacement"; s.mu.Unlock() },
			"partner_copper_without_revision": func(s *Service, f *fakeSession) { s.mu.Lock(); s.state.PartnerCopper++; s.mu.Unlock() },
			"own_copper_without_revision":     func(s *Service, f *fakeSession) { s.mu.Lock(); s.state.OwnRequestedCopper++; s.mu.Unlock() },
			"partner_item_without_revision": func(s *Service, f *fakeSession) {
				s.mu.Lock()
				s.state.PartnerItems = []*tradev1.Item{{Entry: 117, Count: 2}}
				s.mu.Unlock()
			},
			"server_terms_then_new_consent": func(s *Service, f *fakeSession) {
				packet(s, opcode.SMSGTradeStatusExtended, extended(true, 117, 2, 0, 0))
				statusPacket(s, 4)
			},
			"service_without_revision":  func(s *Service, f *fakeSession) { s.mu.Lock(); s.serviceItem.entry = 123; s.mu.Unlock() },
			"picklock_without_revision": func(s *Service, f *fakeSession) { s.mu.Lock(); s.pickLock.queued = true; s.mu.Unlock() },
			"token":                     func(s *Service, f *fakeSession) { s.token = "replacement" },
			"combat":                    func(s *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
			"self_map_unknown":          func(s *Service, f *fakeSession) { f.world.Self.MapKnown = false },
			"owner_map_unknown":         func(s *Service, f *fakeSession) { f.world.Owner.MapKnown = false },
			"map_mismatch":              func(s *Service, f *fakeSession) { f.world.Owner.MapId++ },
			"both_transferred":          func(s *Service, f *fakeSession) { f.world.Self.MapId++; f.world.Owner.MapId++ },
		} {
			t.Run(moment+"/"+name, func(t *testing.T) {
				s, f, wrapper := tradeConsentFixture()
				changed := false
				wrapper.before = func(_ context.Context, method string) {
					atPolicy := moment == "policy" && method == "session.trade_consent"
					atFinal := moment == "final_status" && method == "buddy-actions.status" && len(wrapper.inputs) != 0
					if !changed && (atPolicy || atFinal) {
						changed = true
						change(s, f)
					}
				}
				s.Tick(context.Background())
				if !changed || f.count(opcode.CMSGAcceptTrade) != 0 {
					t.Fatal("changed policy window or lease reached acceptance", s.snapshot(), wrapper.calls)
				}
			})
		}
	}
}

func TestTradeConsentExactLeaseAndElapsedOrCanceledWorkCannotWrite(t *testing.T) {
	for name, change := range map[string]func(*actionsv1.InteractionLease){
		"token":         func(r *actionsv1.InteractionLease) { r.Token = "other" },
		"missing_state": func(r *actionsv1.InteractionLease) { r.State = nil },
		"owner":         func(r *actionsv1.InteractionLease) { r.State.InteractionOwner = "loot" },
		"short_ttl":     func(r *actionsv1.InteractionLease) { r.State.InteractionLeaseRemainingMs = 2999 },
		"excess_ttl":    func(r *actionsv1.InteractionLease) { r.State.InteractionLeaseRemainingMs = 30001 },
		"busy":          func(r *actionsv1.InteractionLease) { r.State.MotionUnconfirmed = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, wrapper := tradeConsentFixture()
			wrapper.after = func(method string, response *modv1.InvokeModuleResponse) {
				if method != "buddy-actions.acquire_interaction" {
					return
				}
				lease := &actionsv1.InteractionLease{}
				if err := response.Result.UnmarshalTo(lease); err != nil {
					t.Fatal(err)
				}
				change(lease)
				response.Result, _ = anypb.New(lease)
			}
			s.Tick(context.Background())
			if f.count(opcode.CMSGAcceptTrade) != 0 {
				t.Fatal("unconfirmed exact lease wrote ACCEPT", wrapper.calls)
			}
		})
	}
	t.Run("minimum_live_lease", func(t *testing.T) {
		s, f, wrapper := tradeConsentFixture()
		wrapper.after = func(method string, response *modv1.InvokeModuleResponse) {
			if method != "buddy-actions.acquire_interaction" {
				return
			}
			lease := &actionsv1.InteractionLease{}
			if err := response.Result.UnmarshalTo(lease); err != nil {
				t.Fatal(err)
			}
			lease.State.InteractionLeaseRemainingMs = 3000
			response.Result, _ = anypb.New(lease)
		}
		s.Tick(context.Background())
		if f.count(opcode.CMSGAcceptTrade) != 1 {
			t.Fatal("minimum confirmed lease refused normal consent", wrapper.calls)
		}
	})
	for _, moment := range []string{"renewal_delay", "final_world_delay", "policy_cancel", "final_status_cancel"} {
		t.Run(moment, func(t *testing.T) {
			s, f, wrapper := tradeConsentFixture()
			now := time.Unix(1000, 0)
			s.now = func() time.Time { return now }
			s.deadline, s.renewAt = now.Add(time.Minute), now.Add(10*time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wrapper.before = func(_ context.Context, method string) {
				if moment == "policy_cancel" && method == "session.trade_consent" || moment == "final_status_cancel" && method == "buddy-actions.status" && len(wrapper.inputs) != 0 {
					cancel()
				}
			}
			wrapper.after = func(method string, _ *modv1.InvokeModuleResponse) {
				if moment == "renewal_delay" && method == "buddy-actions.acquire_interaction" || moment == "final_world_delay" && method == "buddy-world.snapshot" && len(wrapper.inputs) != 0 {
					now = now.Add(31 * time.Second)
				}
			}
			s.Tick(ctx)
			if f.count(opcode.CMSGAcceptTrade) != 0 {
				t.Fatal("expired or canceled consent wrote ACCEPT", wrapper.calls)
			}
		})
	}
}

func TestTradeConsentAmbiguousAcceptRemainsNativeOnceAcrossPolicyChange(t *testing.T) {
	s, f, wrapper := tradeConsentFixture()
	f.fail = opcode.CMSGAcceptTrade
	s.Tick(context.Background())
	wrapper.response = tradeConsentResponse("wait")
	s.Tick(context.Background())
	s.Tick(context.Background())
	if f.count(opcode.CMSGAcceptTrade) != 1 || len(wrapper.inputs) != 1 || s.snapshot().CompletedTrades != 0 {
		t.Fatal("uncertain native acceptance was redecided or replayed", s.snapshot(), wrapper.calls)
	}
}
