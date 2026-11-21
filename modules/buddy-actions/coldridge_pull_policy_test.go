package buddyactions

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestColdridgePullLuaDefaultResourceBoundariesReachActualCast(t *testing.T) {
	for _, test := range []struct {
		quest182     bool
		health, mana uint32
		ready        bool
	}{
		{false, 95, 59, true}, {false, 94, 59, false}, {false, 95, 58, false},
		{true, 94, 62, true}, {true, 93, 62, false}, {true, 94, 61, false},
	} {
		t.Run(fmt.Sprintf("182_%v/health%d/mana%d", test.quest182, test.health, test.mana), func(t *testing.T) {
			var s *Service
			var f *fakeSession
			if test.quest182 {
				s, f, _ = cold182Rig(t, 2, 4, 3)
			} else {
				s, f, _ = cold183Rig(t, 2)
			}
			f.world.Self.Health, f.world.Self.Power = test.health, test.mana
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			pullReads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == "coldridge_pull_readiness" {
					pullReads++
					if input["target"] != nil {
						t.Fatal("preselection policy invented a target", input)
					}
				}
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			want := 0
			if test.ready {
				want = 1
			}
			if pullReads == 0 || countPackets(f, opcode.CMSGCastSpell) != want || int(s.state.PracticePulls) != want ||
				s.practicePullStarted != test.ready || s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 {
				t.Fatal("default Lua threshold changed ceiling/inclusive boundaries or invented progress", pullReads, s.stateLocked(), f.packets)
			}
			if !test.ready && (s.practiceTarget != 0 || s.practiceRoute != nil || !s.state.Practicing) {
				t.Fatal("unready resources selected a future pull", s.stateLocked())
			}
		})
	}
}

func TestColdridgePullLuaPublicTuningCASEnablesAllSitesAtUnchangedResources(t *testing.T) {
	for _, resource := range []string{"health", "mana"} {
		t.Run(resource, func(t *testing.T) {
			s, f, _ := cold183Rig(t, 2)
			f.world.Self.Health, f.world.Self.Power = 95, 59
			old, replacement := "health_percent=95", "health_percent=94"
			if resource == "health" {
				f.world.Self.Health = 94
			} else {
				f.world.Self.Power = 58
				old, replacement = "base_mana_percent=60", "base_mana_percent=59"
			}
			accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
				SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
			if err != nil {
				t.Fatal(err)
			}
			authored := s.behavior
			observed := proto.Clone(f.world)
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || s.practiceTarget != 0 || s.practiceRoute != nil || s.state.PracticePulls != 0 || !s.state.Practicing {
				t.Fatal("default source did not wait before target selection", s.stateLocked(), f.packets)
			}
			request := &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID,
				ExpectedRevision: accepted.Revision, Source: editColdridgePullTuning(t, behaviors.StarterSpellSource, old, replacement)}
			changed, err := reloadStarterSemanticBehavior(t, s, context.Background(), request)
			if err != nil || changed.Revision == accepted.Revision || s.behavior != authored {
				t.Fatal("real same-body tuning CAS did not retain its accepted runtime", changed, err)
			}
			request.Source = behaviors.StarterSpellSource
			if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), request); err == nil {
				t.Fatal("stale revision replaced accepted readiness tuning")
			}
			s.Tick(context.Background()) // The newly ready policy selects and verifies the route.
			s.Tick(context.Background()) // The same accepted source must admit all cast-stage checks.
			if lastCast(f) != shadowBolt || countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.PracticePulls != 1 ||
				s.starterSpells.revision != changed.Revision || s.behavior != authored || s.selfGUID != f.world.Self.Guid ||
				!proto.Equal(observed, f.world) || s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 {
				t.Fatal("unchanged resources hit a hidden native threshold or lost accepted-source identity", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestColdridgePullLuaRechecksChangedResourcesAndFinalCallbackAuthority(t *testing.T) {
	for _, stage := range []string{"after_plan", "after_facing", "second_choice", "cooldown_low_mana", "recovered_negative", "final_lease", "final_context"} {
		t.Run(stage, func(t *testing.T) {
			s, f, advance := cold183Rig(t, 2)
			f.world.Self.Health, f.world.Self.Power = 95, 59
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			castReads := 0
			changed := false
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == "coldridge_cast" {
					castReads++
				}
				out, err := runtime.EvaluateContext(ctx, input)
				if stage == "recovered_negative" && !changed && input["selection"] == "coldridge_cast" {
					if err != nil || out["kind"] != "pull_wait" {
						t.Fatal("actual low-resource Lua choice did not refuse the selected pull", out, err)
					}
					f.world.Self.Power = 59 // Recovery occurs after the actual negative decision.
					changed = true
				}
				if stage == "second_choice" && input["selection"] == "coldridge_cast" && castReads == 2 {
					f.world.Self.BaseMana = 99 // The unchanged59 mana now falls short of ceil(99*.60).
					changed = true
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(ctx)
			if stage == "after_plan" || stage == "cooldown_low_mana" || stage == "recovered_negative" {
				f.world.Self.Power = 58
				changed = stage != "recovered_negative"
				if stage == "cooldown_low_mana" {
					s.nextAssist = s.now().Add(time.Second)
				}
			} else if stage == "after_facing" {
				f.onClock = func() {
					f.onClock = nil
					f.world.Self.Health = 94
					changed = true
				}
			} else if stage == "final_lease" || stage == "final_context" {
				late := &starterWeaponFinalWorldSession{fakeSession: f, weaponReads: &castReads, afterFinalWorld: func() {
					changed = true
					if stage == "final_context" {
						cancel()
					} else {
						s.practiceUntil = s.now()
					}
				}}
				s.session = late
			}
			s.Tick(ctx)
			if !changed || countPackets(f, opcode.CMSGCastSpell) != 0 || s.practicePullStarted || s.state.PracticePulls != 0 || s.state.AssistAttempts != 0 {
				t.Fatal("stale readiness or revoked final authority admitted a first pull", stage, castReads, s.stateLocked(), f.packets)
			}
			if stage == "after_plan" || stage == "after_facing" || stage == "second_choice" || stage == "cooldown_low_mana" || stage == "recovered_negative" {
				if s.state.Practicing {
					t.Fatal("selected but unpulled readiness refusal did not stop the old mission", stage, s.stateLocked())
				}
				f.world.Self.Health, f.world.Self.Power, f.world.Self.BaseMana = 95, 59, 98
				s.Tick(context.Background())
				if s.state.Practicing || countPackets(f, opcode.CMSGCastSpell) != 0 {
					t.Fatal("resource recovery silently resumed a stopped selected pull", stage, s.stateLocked(), f.packets)
				}
				if remaining := s.nextAssist.Sub(s.now()); remaining > 0 {
					advance(remaining) // Keep the original real cooldown through refusal and recovery.
				}
				if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
					t.Fatal("explicit public rearm could not start a fresh authorized run", err)
				}
				s.Tick(context.Background())
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.PracticePulls != 1 {
					t.Fatal("explicit public rearm failed to restore a freshly observed ready pull", stage, s.stateLocked(), f.packets)
				}
			}
		})
	}
}

type coldridgePullWorldSession struct {
	*fakeSession
	policyRead *bool
	after      func()
}

func (f *coldridgePullWorldSession) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	response, err := f.fakeSession.InvokeModule(ctx, request, options...)
	if err == nil && *f.policyRead && request.Module == "buddy-world" && request.Method == "snapshot" && f.after != nil {
		after := f.after
		f.after = nil
		after()
	}
	return response, err
}

func TestColdridgePullLuaPreselectionReadKeepsBodyAndReplacementIntentBound(t *testing.T) {
	for _, mode := range []string{"body", "replacement", "world_replacement", "stop_replacement"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold183Rig(t, 2)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			changed := false
			policyRead := false
			pullReads := 0
			var replacementUntil time.Time
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				if input["selection"] == "coldridge_pull_readiness" {
					policyRead = true
					pullReads++
					if !changed && (mode == "body" || mode == "replacement") {
						changed = true
						if mode == "body" {
							s.selfGUID++
						} else {
							replacementUntil = coldridgeCasterReplaceBinding(s)
						}
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			if mode == "world_replacement" {
				s.session = &coldridgePullWorldSession{fakeSession: f, policyRead: &policyRead, after: func() {
					changed = true
					replacementUntil = coldridgeCasterReplaceBinding(s)
				}}
			} else if mode == "stop_replacement" {
				callback := &coldridgeCasterSendSession{fakeSession: f}
				callback.after = func(packet *modv1.SendPacketRequest) {
					if packet.Opcode == opcode.MSGMoveStop {
						callback.after = nil
						changed = true
						replacementUntil = coldridgeCasterReplaceBinding(s)
					}
				}
				s.session = callback
				s.state.Moving = true
			}
			s.Tick(context.Background())
			if !changed || s.practiceTarget != 0 || s.practiceRoute != nil || s.practicePullStarted ||
				s.state.PracticePulls != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal("foreign or replaced readiness read selected a target or wrote gameplay", s.stateLocked(), f.packets)
			}
			if mode != "body" && (!s.state.Practicing || s.practiceUntil != replacementUntil ||
				s.state.PracticePhase != "replacement_waiting_for_new_pull") {
				t.Fatal("older readiness result clobbered replacement intent", s.stateLocked())
			}
			if mode == "stop_replacement" && (countPackets(f, opcode.MSGMoveStop) != 1 || pullReads != 0 ||
				countPackets(f, opcode.MSGMoveSetFacing) != 0) {
				t.Fatal("old no-target STOP frame evaluated source or faced after task replacement", pullReads, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestColdridgePullLuaOngoingOwnFightKeepsExactAffordableCastBelowPullThreshold(t *testing.T) {
	s, f, advance := cold183Rig(t, 2)
	s.Tick(context.Background())
	s.Tick(context.Background())
	practiceCasterConfirmObservedCast(t, s, cold183GUID)
	advance(s.nextAssist.Sub(s.now()))
	f.world.Self.Health, f.world.Self.Power, f.world.Self.UnitFlags = 50, 10, 0x80000
	target := f.world.NearbyCreatures[0]
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
	if spellCost(s.learnedContractsLocked()[shadowBolt], f.world.Self) != 10 {
		t.Fatal("fixture does not isolate exact cast cost from pull readiness")
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 2 || lastCast(f) != shadowBolt || s.state.PracticePulls != 1 ||
		!s.state.Practicing || s.state.ShadowBoltDamageEvents != 1 || s.state.ServerAssistGo != 1 {
		t.Fatal("pull tuning blocked the same affordable own fight or invented another effect", s.stateLocked(), f.packets)
	}
}

func TestColdridgePullLuaTuningCASCannotEraseUncertainCastAcrossRearm(t *testing.T) {
	s, f, advance := cold183Rig(t, 2)
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
	if err != nil {
		t.Fatal(err)
	}
	s.session = starterWeaponAmbiguousSession{f}
	s.Tick(context.Background())
	s.Tick(context.Background())
	uncertain := s.starterSpells.uncertainStrike
	if uncertain == nil || uncertain.view.SpellId != shadowBolt || uncertain.owner != cold183GUID || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("fixture did not retain the actual uncertain Coldridge cast", s.stateLocked(), f.packets)
	}
	advance(6 * time.Second)
	s.Tick(context.Background())
	edited := editColdridgePullTuning(t, behaviors.StarterSpellSource, "health_percent=95", "health_percent=94")
	if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: accepted.Revision, Source: edited}); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: enabled}); err != nil {
			t.Fatal(err)
		}
	}
	heal, known := s.spellData.Lookup(lesserHeal)
	if !known {
		t.Fatal("missing existing native receipt fixture")
	}
	s.reserveHealReceiptLocked("", heal, s.selfGUID)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 1 || s.starterSpells.uncertainStrike != uncertain || s.supportOp == uncertain ||
		uncertain.view.Phase != "outcome_unconfirmed_no_retry" || uncertain.view.ServerCastGo || uncertain.view.EffectObserved ||
		s.state.PracticePulls != 1 || s.practicePullStarted || s.state.AssistDamageEvents != 0 || s.state.ServerAssistGo != 0 {
		t.Fatal("ready tuning, task rearm or later receipt replayed an uncertain cast", s.stateLocked(), f.packets)
	}
}
