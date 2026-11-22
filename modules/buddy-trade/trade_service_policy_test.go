package buddytrade

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
)

func tradeServicePolicyFixture() (*Service, *fakeSession, *tradeConsentSession) {
	s, f := tradePickLockRig()
	packet(s, opcode.SMSGTradeStatusExtended, offeredLockbox(5))
	w := &tradeConsentSession{fakeSession: f, response: tradeConsentResponse("prepare")}
	s.session = w
	return s, f, w
}

func servicePolicyInput(t *testing.T, w *tradeConsentSession) map[string]any {
	t.Helper()
	if len(w.inputs) != 1 {
		t.Fatalf("expected one accepted-source preparation read, got %d: %v", len(w.inputs), w.calls)
	}
	input, ok := w.inputs[0]["input"].(map[string]any)
	if !ok {
		t.Fatal("preparation request omitted its pure factual input", w.inputs)
	}
	return input
}

func TestTradeServicePreparationUsesPolicyAndReservesExactSlotSixWire(t *testing.T) {
	s, f, w := tradeServicePolicyFixture()
	// Signed observed bonuses remain facts, rather than a substituted rank oracle.
	f.world.SelfSkills[0].TemporaryBonus, f.world.SelfSkills[0].PermanentBonus = -5, 5
	w.beforeWrite = func(request *modv1.SendPacketRequest) {
		if request.Opcode != opcode.CMSGCastSpell {
			return
		}
		if s.pickLock.item != s.serviceItem || s.pickLock.count != 1 || s.pickLock.until.IsZero() ||
			f.count(opcode.CMSGCastSpell) != 0 || s.pickLock.queued || s.pickLock.accepted {
			t.Fatal("slot-six preparation was not reserved before transport", s.pickLock)
		}
		if len(w.calls) == 0 || w.calls[len(w.calls)-1] != "buddy-world.snapshot" {
			t.Fatal("a dependency followed the final authoritative World observation", w.calls)
		}
	}
	s.Tick(context.Background())
	if f.count(opcode.CMSGCastSpell) != 1 || f.count(opcode.CMSGAcceptTrade) != 0 {
		t.Fatal("preparation did not remain one native cast before trade acceptance", s.snapshot(), w.calls)
	}
	want := []byte{1, 0x0c, 0x07, 0, 0, 0, 0, 0x10, 0, 0, 1, 6}
	for _, request := range f.packets {
		if request.Opcode == opcode.CMSGCastSpell && !bytes.Equal(request.Payload, want) {
			t.Fatal("preparation fabricated an item GUID or changed the fixed slot-six packet", request.Payload)
		}
	}
	if len(w.renewals) != 2 {
		t.Fatal("expected exact-token renewals on both sides of nearby's Actions observation", w.calls)
	}
	for _, renewal := range w.renewals {
		if renewal.Token != "lease-token" || renewal.Owner != "trade" || renewal.LeaseMs != 30000 {
			t.Fatal("preparation replaced or failed to bind the retained trade lease", renewal)
		}
	}
	input := servicePolicyInput(t, w)
	if len(input) != 10 || input["kind"] != "prepare_trade_service" || input["sessionId"] != "session-A" || input["selfGuid"] != "1" {
		t.Fatal("preparation changed the ordinary consent shape instead of using a separate typed kind", input)
	}
	service := input["service"].(map[string]any)
	if len(service) != 6 || service["slot"] != float64(6) || service["entry"] != float64(4632) ||
		service["count"] != float64(1) || service["lockId"] != float64(5) || service["wrapped"] != false || service["partnerSpell"] != float64(0) {
		t.Fatal("service facts were not the actual nontraded server slot", service)
	}
	spell := input["spell"].(map[string]any)
	if spell["spellId"] != float64(1804) || spell["present"] != true || spell["metadataKnown"] != true || spell["castTimeMs"] != float64(5000) {
		t.Fatal("preparation did not reflect the exact learned Pick Lock row", spell)
	}
	world := input["world"].(map[string]any)
	skills := world["skills"].(map[string]any)
	rows := skills["rows"].([]any)
	if world["visitId"] != "1" || skills["known"] != true || skills["complete"] != true || skills["revision"] != "1" || len(rows) != 1 {
		t.Fatal("unknown or unbounded skills/visit facts were presented as complete", world)
	}
	skill := rows[0].(map[string]any)
	if skill["skillId"] != float64(633) || skill["value"] != float64(75) || skill["temporaryBonus"] != float64(-5) || skill["permanentBonus"] != float64(5) {
		t.Fatal("raw signed skill facts changed", skill)
	}
	reference := input["reference"].(map[string]any)
	if reference["catalog"] != "client_build_12340_lockpicking" || reference["lockId"] != float64(5) || reference["known"] != true || reference["requiredSkillRank"] != float64(1) {
		t.Fatal("client reference facts were missing or mislabeled", reference)
	}
	for _, key := range []string{"token", "itemGuid", "eligible", "toolkit"} {
		if _, exists := input[key]; exists {
			t.Fatal("private authority, an invented slot GUID or a Go policy oracle leaked", key, input)
		}
		if _, exists := service[key]; exists {
			t.Fatal("service facts invented native authority", key, service)
		}
	}
}

func TestTradeServicePreparationHasNoMissingOrOldPolicyFallback(t *testing.T) {
	for name, response := range map[string]map[string]any{
		"wait":         tradeConsentResponse("wait"),
		"old_accept":   tradeConsentResponse("accept"),
		"unavailable":  {"available": false, "revision": strings.Repeat("ab", 32), "decision": map[string]any{}},
		"no_revision":  {"available": true, "revision": "", "decision": map[string]any{"kind": "prepare", "reason": "missing source"}},
		"no_decision":  {"available": true, "revision": strings.Repeat("ab", 32)},
		"extra_field":  {"available": true, "revision": strings.Repeat("ab", 32), "decision": map[string]any{"kind": "prepare", "reason": "bad result", "spellId": 1804}},
		"empty_reason": {"available": true, "revision": strings.Repeat("ab", 32), "decision": map[string]any{"kind": "prepare", "reason": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, w := tradeServicePolicyFixture()
			w.response = response
			s.Tick(context.Background())
			s.Tick(context.Background())
			if f.count(opcode.CMSGCastSpell) != 0 || f.count(opcode.CMSGAcceptTrade) != 0 || s.pickLock.item.entry != 0 {
				t.Fatal("unavailable, malformed or older policy triggered native gameplay fallback", s.snapshot(), w.calls)
			}
		})
	}
	s, f, w := tradeServicePolicyFixture()
	w.fault = errors.New("accepted policy failed")
	s.Tick(context.Background())
	if f.count(opcode.CMSGCastSpell) != 0 || s.pickLock.item.entry != 0 {
		t.Fatal("policy failure caused a cast or reserved a nonexistent write", s.snapshot())
	}
}

func TestTradeServicePreparationRebindsExactPrivateWindowAfterPolicy(t *testing.T) {
	for name, change := range map[string]func(*Service){
		"revision":      func(s *Service) { s.state.Revision++ },
		"session":       func(s *Service) { s.state.SessionId = "replacement" },
		"gift_terms":    func(s *Service) { s.state.PartnerItems[0].Count++ },
		"service":       func(s *Service) { s.serviceItem.lockID = 4 },
		"partner_spell": func(s *Service) { s.partnerSpell = 1804 },
		"attempt":       func(s *Service) { s.pickLock.queued = true },
		"allocator":     func(s *Service) { s.pickLockCount++ },
		"token":         func(s *Service) { s.token = "replacement-token" },
		"deadline":      func(s *Service) { s.deadline = s.deadline.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, w := tradeServicePolicyFixture()
			changed := false
			w.before = func(_ context.Context, method string) {
				if method == "session.trade_consent" && !changed {
					changed = true
					s.mu.Lock()
					change(s)
					s.mu.Unlock()
				}
			}
			s.Tick(context.Background())
			if !changed || f.count(opcode.CMSGCastSpell) != 0 {
				t.Fatal("changed private trade context inherited old preparation consent", name, w.calls)
			}
		})
	}
}

func TestTradeServicePreparationRejectsFinalWorldChangesAndOldFacts(t *testing.T) {
	for name, change := range map[string]func(*fakeSession){
		"same_map_visit":  func(f *fakeSession) { f.world.VisitRevision++ },
		"map":             func(f *fakeSession) { f.world.Self.MapId = 1 },
		"pose_revision":   func(f *fakeSession) { f.world.Self.PositionRevision++ },
		"skills_revision": func(f *fakeSession) { f.world.SelfSkillsRevision++ },
		"skill_value":     func(f *fakeSession) { f.world.SelfSkills[0].Value = 1 },
		"combat":          func(f *fakeSession) { f.world.Owner.UnitFlags = 0x80000 },
		"book_revision":   func(f *fakeSession) { f.toolkit.SpellbookRevision++ },
		"cast_metadata":   func(f *fakeSession) { f.toolkit.Spells[0].CastTimeMs = 4000 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, w := tradeServicePolicyFixture()
			changed := false
			w.before = func(_ context.Context, method string) {
				if method == "buddy-actions.status" && !changed {
					changed = true
					change(f)
				}
			}
			s.Tick(context.Background())
			if !changed || f.count(opcode.CMSGCastSpell) != 0 {
				t.Fatal("final World context changed after source admission", name, w.calls)
			}
		})
	}
	for name, change := range map[string]func(*fakeSession){
		"unknown_visit":        func(f *fakeSession) { f.world.VisitRevision = 0 },
		"partial_skills":       func(f *fakeSession) { f.world.SelfSkills = f.world.SelfSkills[:1] },
		"unknown_skills_epoch": func(f *fakeSession) { f.world.SelfSkillsRevision = 0 },
		"unknown_book_epoch":   func(f *fakeSession) { f.toolkit.SpellbookRevision = 0 },
		"other_toolkit_body":   func(f *fakeSession) { f.toolkit.SelfGuid = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := tradeServicePolicyFixture()
			change(f)
			s.Tick(context.Background())
			if f.count(opcode.CMSGCastSpell) != 0 {
				t.Fatal("Lua's prepare proposal waived unknown native execution facts", name)
			}
		})
	}
}

func TestTradeServicePreparationLeaseAndContextMustStillCoverDispatch(t *testing.T) {
	for _, mode := range []string{"wrong_token", "wrong_owner", "short_ttl", "long_ttl", "elapsed_renewal", "cancelled", "provider_error", "revoked_after_status"} {
		t.Run(mode, func(t *testing.T) {
			s, f, w := tradeServicePolicyFixture()
			now := s.now()
			s.now = func() time.Time { return now }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.before = func(_ context.Context, method string) {
				if mode == "cancelled" && method == "session.trade_consent" {
					cancel()
				}
				if mode == "revoked_after_status" && method == "buddy-actions.status" {
					s.token = "new-owner-token"
				}
			}
			w.renewalCheck = func(*actionsv1.InteractionRequest) error {
				if mode == "provider_error" {
					return errors.New("lease provider unavailable")
				}
				return nil
			}
			w.after = func(method string, response *modv1.InvokeModuleResponse) {
				if method != "buddy-actions.acquire_interaction" {
					return
				}
				lease := &actionsv1.InteractionLease{}
				if err := response.Result.UnmarshalTo(lease); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "wrong_token":
					lease.Token = "replacement"
				case "wrong_owner":
					lease.State.InteractionOwner = "loot"
				case "short_ttl":
					lease.State.InteractionLeaseRemainingMs = 2999
				case "long_ttl":
					lease.State.InteractionLeaseRemainingMs = 30001
				case "elapsed_renewal":
					now = now.Add(31 * time.Second)
				}
				response.Result, _ = anypb.New(lease)
			}
			s.Tick(ctx)
			if f.count(opcode.CMSGCastSpell) != 0 || s.pickLock.item.entry != 0 {
				t.Fatal("preparation reserved/wrote outside its exact bounded custody", mode, w.calls)
			}
		})
	}
}

type serviceFinalWorldFailure struct {
	*tradeConsentSession
	afterStatus bool
}

func (f *serviceFinalWorldFailure) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	method := request.Module + "." + request.Method
	if method == "buddy-world.snapshot" && f.afterStatus {
		f.calls = append(f.calls, method)
		return nil, errors.New("final World unavailable")
	}
	response, err := f.tradeConsentSession.InvokeModule(ctx, request, opts...)
	if method == "buddy-actions.status" {
		f.afterStatus = true
	}
	return response, err
}

func TestTradeServicePreparationUnknownTransportNeverReplays(t *testing.T) {
	goPacket := []byte{1, 1, 1, 1, 0, 0x0c, 0x07, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0, 0, 0x81, 0x2a, 0x40}
	t.Run("still_queued_service_accepts_fresh_ordinary_consent_without_new_cast", func(t *testing.T) {
		for name, reopen := range map[string]func(*Service){
			"partner_terms": func(s *Service) { packet(s, opcode.SMSGTradeStatusExtended, offeredLockbox(5)) },
			"back_to_trade": func(s *Service) { statusPacket(s, 7) },
		} {
			t.Run(name, func(t *testing.T) {
				s, f, w := tradeServicePolicyFixture()
				s.Tick(context.Background())
				packet(s, opcode.SMSGTradeStatusExtended, extended(false, 0, 0, 0, 1804))
				statusPacket(s, 4)
				w.response = tradeConsentResponse("accept")
				s.Tick(context.Background())
				if f.count(opcode.CMSGCastSpell) != 1 || f.count(opcode.CMSGAcceptTrade) != 1 || !s.pickLock.queued || !s.pickLock.accepted {
					t.Fatal("service did not first reach one reserved native acceptance", s.snapshot(), s.pickLock)
				}
				reopen(s)
				s.Tick(context.Background())
				if f.count(opcode.CMSGAcceptTrade) != 1 {
					t.Fatal("reopening alone reused the old partner acceptance", name, s.snapshot())
				}
				statusPacket(s, 4)
				s.Tick(context.Background())
				s.Tick(context.Background())
				if f.count(opcode.CMSGCastSpell) != 1 || f.count(opcode.CMSGAcceptTrade) != 2 {
					t.Fatal("fresh partner and Lua consent did not accept the same still-queued service", name, s.snapshot(), w.calls)
				}
				packet(s, opcode.SMSGSpellGo, goPacket)
				if !s.pickLock.castGo || s.snapshot().CompletedTrades != 0 {
					t.Fatal("late own GO was lost or claimed trade completion alone", s.snapshot(), s.pickLock)
				}
				statusPacket(s, 8)
				if s.snapshot().CompletedTrades != 1 || s.snapshot().Reason != "server_confirmed_trade_and_pick_lock_cast" {
					t.Fatal("exact GO and subsequent completion did not settle the queued service", s.snapshot())
				}
			})
		}
	})
	t.Run("confirmed_preaccept_queue_cancellation_can_prepare_new_own_terms", func(t *testing.T) {
		s, f, _ := tradeServicePolicyFixture()
		ctx := context.Background()
		s.Tick(ctx)
		packet(s, opcode.SMSGTradeStatusExtended, extended(false, 0, 0, 0, 1804))
		if !s.pickLock.queued || s.pickLock.accepted || s.pickLock.castGo {
			t.Fatal("fixture did not establish an unaccepted server-confirmed queue", s.pickLock)
		}
		state := s.snapshot()
		if _, err := s.OfferGold(ctx, &tradev1.GoldRequest{SessionId: state.SessionId, Revision: state.Revision, Copper: 1}); err != nil {
			t.Fatal("explicit own offer could not change the open window", err)
		}
		own := extended(false, 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(own[13:], 1)
		packet(s, opcode.SMSGTradeStatusExtended, own)
		if s.pickLock.item.entry != 0 || f.count(opcode.CMSGSetTradeGold) != 1 {
			t.Fatal("confirmed pre-accept queue removal did not retire only the old stage", s.pickLock)
		}
		s.Tick(ctx)
		if f.count(opcode.CMSGCastSpell) != 2 || s.pickLock.count != 2 || f.count(opcode.CMSGAcceptTrade) != 0 || s.serviceItem.entry != 4632 {
			t.Fatal("new Lua preparation did not stage the same service against the explicit changed own terms", s.snapshot(), s.pickLock)
		}
	})
	t.Run("postaccept_queue_clear_retains_unknown_and_late_go_without_new_cast", func(t *testing.T) {
		s, f, w := tradeServicePolicyFixture()
		s.Tick(context.Background())
		packet(s, opcode.SMSGTradeStatusExtended, extended(false, 0, 0, 0, 1804))
		statusPacket(s, 4)
		w.response = tradeConsentResponse("accept")
		s.Tick(context.Background())
		prior := s.pickLock
		if f.count(opcode.CMSGCastSpell) != 1 || f.count(opcode.CMSGAcceptTrade) != 1 || !prior.accepted {
			t.Fatal("fixture did not reserve the first service acceptance", s.snapshot(), prior)
		}
		packet(s, opcode.SMSGTradeStatusExtended, extended(false, 0, 0, 0, 0))
		statusPacket(s, 4)
		w.response = tradeConsentResponse("prepare")
		s.Tick(context.Background())
		s.Tick(context.Background())
		if s.pickLock.queued || !s.pickLock.accepted || s.pickLock.item != prior.item || s.pickLock.count != prior.count ||
			f.count(opcode.CMSGCastSpell) != 1 || f.count(opcode.CMSGAcceptTrade) != 1 {
			t.Fatal("queue removal after ACCEPT erased uncertainty or permitted a new cast", s.snapshot(), s.pickLock)
		}
		packet(s, opcode.SMSGSpellGo, goPacket)
		if !s.pickLock.castGo || s.snapshot().CompletedTrades != 0 {
			t.Fatal("queue removal lost late GO or invented trade completion", s.snapshot(), s.pickLock)
		}
	})
	t.Run("missing_final_world", func(t *testing.T) {
		s, f, w := tradeServicePolicyFixture()
		s.session = &serviceFinalWorldFailure{tradeConsentSession: w}
		s.Tick(context.Background())
		if f.count(opcode.CMSGCastSpell) != 0 || s.pickLock.item.entry != 0 {
			t.Fatal("missing last World observation authorized preparation", w.calls)
		}
	})
	t.Run("ambiguous_write", func(t *testing.T) {
		s, f, w := tradeServicePolicyFixture()
		now := s.now()
		s.now = func() time.Time { return now }
		f.fail = opcode.CMSGCastSpell
		s.Tick(context.Background())
		reserved := s.pickLock
		if f.count(opcode.CMSGCastSpell) != 1 || reserved.item.entry == 0 || reserved.count == 0 {
			t.Fatal("ambiguous transport discarded its already-reserved attempt", reserved)
		}
		if len(w.inputs) == 0 {
			t.Fatal("ambiguous attempt has no recorded source decision")
		}
		before, err := json.Marshal(w.inputs[0])
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(15 * time.Second)
		// Repeated unchanged server terms and a non-132 result cannot permit replay.
		packet(s, opcode.SMSGTradeStatusExtended, offeredLockbox(5))
		packet(s, opcode.SMSGCastFailed, []byte{reserved.count, 0x0c, 0x07, 0, 0, 8})
		statusPacket(s, 4)
		s.Tick(context.Background())
		s.Tick(context.Background())
		if f.count(opcode.CMSGCastSpell) != 1 || s.pickLock.item != reserved.item || s.pickLock.retry || f.count(opcode.CMSGAcceptTrade) != 0 {
			t.Fatal("time, duplicate terms or ordinary cast failure replayed uncertain preparation", s.pickLock, w.calls)
		}
		after, err := json.Marshal(w.inputs[0])
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("later processing mutated the retained preparation evidence")
		}
	})
}
