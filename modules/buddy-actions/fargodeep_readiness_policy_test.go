package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const fargodeepReadinessTestSelection = "fargodeep_pull_readiness"

func fargodeepReadinessRig(t *testing.T, guid uint64) (*Service, *fakeSession, *worldv1.CreatureState, *actionsv1.FargodeepEncounterRequest, func(time.Duration)) {
	t.Helper()
	s, f, target, request, advance := fargodeepRig(t, guid)
	// Use the existing actual-source semantic rig and a concrete connected body.
	// The separate production CLI/watchdog checks retain their normal budgets.
	s.sessionID = request.SessionId
	f.world.VisitRevision, f.world.ObservedPackets = 7, 11
	request.EngagerGuid = guid
	return s, f, target, request, advance
}

func fargodeepReadinessPlayer(t *testing.T, f *fakeSession, guid uint64) *worldv1.PlayerState {
	t.Helper()
	if f.world.Self.Guid == guid {
		return f.world.Self
	}
	for _, player := range f.world.NearbyPlayers {
		if player.Guid == guid {
			return player
		}
	}
	t.Fatalf("missing actual party fixture player %d", guid)
	return nil
}

func requireFargodeepReadinessFrame(t *testing.T, input map[string]any, s *Service, request *actionsv1.FargodeepEncounterRequest) {
	t.Helper()
	observation, observed := input["observation"].(map[string]any)
	mission, bound := input["mission"].(map[string]any)
	players, present := observation["players"].(map[string]any)
	if input["selection"] != fargodeepReadinessTestSelection || input["sessionId"] != s.sessionID ||
		input["selfGuid"] != fmt.Sprint(s.selfGUID) || !observed || !bound || !present || len(players) != 3 ||
		mission["partySessionId"] != request.SessionId || mission["targetGuid"] != fmt.Sprint(request.TargetGuid) ||
		mission["engagerGuid"] != fmt.Sprint(request.EngagerGuid) {
		t.Fatal("readiness did not use the exact body, mission and three observed players", input)
	}
	for _, guid := range []string{"1", "4", "5"} {
		if _, ok := players[guid].(map[string]any); !ok {
			t.Fatal("readiness omitted an observed party player", guid, players)
		}
	}
}

func requireNoFargodeepReadinessDispatch(t *testing.T, s *Service, f *fakeSession) {
	t.Helper()
	if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
		countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
		s.state.AssistAttempts != 0 || s.fargodeep.pulled {
		t.Fatal("refused readiness dispatched movement/offense or marked a pull", s.stateLocked(), f.packets)
	}
}

func requireFargodeepReadinessAttack(t *testing.T, s *Service, f *fakeSession, target uint64) {
	t.Helper()
	if countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 ||
		lastCast(f) != sinisterStrike || s.state.LastCastTargetGuid != target || !s.fargodeep.pulled ||
		s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 || s.state.MeleeDamageEvents != 0 {
		t.Fatal("ready shared policy did not produce the exact ordinary attack without inventing effects", s.stateLocked(), f.packets)
	}
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGAttackswing && (len(packet.Payload) != 8 || binary.LittleEndian.Uint64(packet.Payload) != target) {
			t.Fatal("ordinary attack did not target the bound observed kobold", packet)
		}
	}
}

func TestFargodeepReadinessLuaDefaultBoundariesReachActualWire(t *testing.T) {
	type boundary struct {
		name   string
		guid   uint64
		ready  bool
		change func(*worldv1.PlayerState)
	}
	var cases []boundary
	for _, guid := range []uint64{1, 4, 5} {
		for _, health := range []uint32{94, 95} {
			cases = append(cases, boundary{fmt.Sprintf("health/%d/%d", guid, health), guid, health == 95,
				func(player *worldv1.PlayerState) { player.Health = health }})
		}
	}
	for _, guid := range []uint64{1, 4} {
		for _, power := range []uint32{57, 58} {
			cases = append(cases, boundary{fmt.Sprintf("max_power_fallback/%d/%d", guid, power), guid, power == 58,
				func(player *worldv1.PlayerState) {
					player.BaseManaKnown, player.BaseMana, player.MaxPower, player.Power = false, 0, 128, power
				}})
		}
		for _, power := range []uint32{45, 46} {
			cases = append(cases, boundary{fmt.Sprintf("known_base_mana/%d/%d", guid, power), guid, power == 46,
				func(player *worldv1.PlayerState) {
					player.BaseManaKnown, player.BaseMana, player.MaxPower, player.Power = true, 102, 128, power
				}})
		}
	}
	for _, health := range []uint32{93, 94} {
		cases = append(cases, boundary{fmt.Sprintf("health_ceiling/%d", health), 1, health == 94,
			func(player *worldv1.PlayerState) { player.MaxHealth, player.Health = 98, health }})
	}
	for _, power := range []uint32{89, 90} {
		cases = append(cases, boundary{fmt.Sprintf("energy/%d", power), 5, power == 90,
			func(player *worldv1.PlayerState) { player.Power = power }})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s, f, target, request, _ := fargodeepReadinessRig(t, 5)
			armFargodeep(t, s, request)
			test.change(fargodeepReadinessPlayer(t, f, test.guid))
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == fargodeepReadinessTestSelection {
					reads++
					requireFargodeepReadinessFrame(t, input, s, request)
				}
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background()) // Complete verified route, without walking.
			s.Tick(context.Background()) // Admit the initial attack from a fresh frame.
			if reads == 0 {
				t.Fatal("initial pull bypassed the actual shared readiness mode")
			}
			if test.ready {
				requireFargodeepReadinessAttack(t, s, f, target.Guid)
			} else {
				requireNoFargodeepReadinessDispatch(t, s, f)
				if !s.fargodeep.active || s.fargodeep.route != nil {
					t.Fatal("initial unready resources retired the mission or acquired a route", s.stateLocked())
				}
			}
		})
	}
}

func TestFargodeepReadinessLuaPublicCASChangesAllChecksInSameMission(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	fargodeepReadinessPlayer(t, f, 1).Health = 94
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
	if err != nil {
		t.Fatal(err)
	}
	authored, until := s.behavior, s.fargodeep.until
	observed := proto.Clone(f.world)
	s.Tick(context.Background())
	requireNoFargodeepReadinessDispatch(t, s, f)
	if !s.fargodeep.active || s.fargodeep.route != nil {
		t.Fatal("default readiness did not keep the same waiting mission", s.stateLocked())
	}
	gate := regexp.MustCompile(`(?s)(M\.fargodeep_pull\s*=\s*\{[^}]*\bminimum_health\s*=\s*)95\b`)
	if len(gate.FindAllStringIndex(behaviors.StarterSpellSource, -1)) != 1 {
		t.Fatal("actual shared Fargodeep health tuning is missing or ambiguous")
	}
	edited := gate.ReplaceAllString(behaviors.StarterSpellSource, "${1}94")
	change := &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID,
		ExpectedRevision: accepted.Revision, Source: edited}
	changed, err := reloadStarterSemanticBehavior(t, s, context.Background(), change)
	if err != nil || changed.Revision == accepted.Revision || s.behavior != authored {
		t.Fatal("public CAS did not accept the real edit in the same runtime", changed, err)
	}
	change.Source = behaviors.StarterSpellSource
	if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), change); err == nil || s.behavior.Status().Revision != changed.Revision {
		t.Fatal("stale CAS replaced accepted Fargodeep readiness", err, s.behavior.Status())
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	requireFargodeepReadinessAttack(t, s, f, target.Guid)
	if s.behavior != authored || s.starterSpells.revision != changed.Revision || s.selfGUID != f.world.Self.Guid ||
		s.fargodeep.until != until || s.fargodeep.target != request.TargetGuid || !proto.Equal(observed, f.world) {
		t.Fatal("same-frame tuning used a hidden native threshold or replaced mission/body/source", s.stateLocked())
	}
}

func TestFargodeepReadinessLuaRejectsResourcesChangedDuringPlanning(t *testing.T) {
	for _, resource := range []string{"health", "mana", "energy"} {
		t.Run(resource, func(t *testing.T) {
			s, f, _, request, _ := fargodeepReadinessRig(t, 5)
			armFargodeep(t, s, request)
			planned := false
			s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				planned = true
				switch resource {
				case "health":
					fargodeepReadinessPlayer(t, f, 1).Health = 94
				case "mana":
					player := fargodeepReadinessPlayer(t, f, 1)
					player.BaseManaKnown, player.BaseMana, player.MaxPower, player.Power = false, 0, 128, 57
				case "energy":
					f.world.Self.Power = 89
				}
				return []position{a, b}, nil
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if !planned {
				t.Fatal("fixture never crossed the actual planner callback")
			}
			requireNoFargodeepReadinessDispatch(t, s, f)
		})
	}
}

func TestFargodeepReadinessLuaFinalChoiceRechecksNativeCustodyAndSource(t *testing.T) {
	for _, mode := range []string{"health", "mana", "party", "body", "visit", "quest", "task", "source"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request, _ := fargodeepReadinessRig(t, 4)
			armFargodeep(t, s, request)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads, changed := 0, false
			var replacementUntil time.Time
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				readiness := input["selection"] == fargodeepReadinessTestSelection
				if readiness {
					reads++
					if reads == 2 && mode == "source" {
						if _, err := runtime.Reload(behaviors.StarterSpellSource+"\n-- accepted readiness revision S\n", runtime.Status().Revision); err != nil {
							t.Fatal(err)
						}
					}
				}
				out, err := runtime.EvaluateContext(ctx, input)
				if readiness && reads == 2 {
					changed = true
					switch mode {
					case "health":
						fargodeepReadinessPlayer(t, f, 1).Health = 94
					case "mana":
						player := fargodeepReadinessPlayer(t, f, 1)
						player.BaseManaKnown, player.BaseMana, player.MaxPower, player.Power = false, 0, 128, 57
					case "party":
						f.party.Group.InGroup = false
					case "body":
						s.sessionID = "fargodeep-replacement-body"
					case "visit":
						f.world.VisitRevision++
					case "quest":
						f.world.SelfQuestLogKnown = false
					case "task":
						replacementUntil = s.fargodeep.until.Add(time.Minute)
						s.fargodeep.until = replacementUntil
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			if !changed || reads < 2 {
				t.Fatal("fixture did not reach the final actual readiness read", reads, s.stateLocked())
			}
			requireNoFargodeepReadinessDispatch(t, s, f)
			if mode == "task" && (!s.fargodeep.active || s.fargodeep.until != replacementUntil) {
				t.Fatal("old readiness callback retired replacement mission custody", s.stateLocked())
			}
		})
	}
}

type fargodeepReadinessFinalWorldSession struct {
	*fakeSession
	readinessReads *int
	after          func()
}

func (f *fargodeepReadinessFinalWorldSession) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	response, err := f.fakeSession.InvokeModule(ctx, request, options...)
	if err == nil && *f.readinessReads >= 2 && request.Module == "buddy-world" && request.Method == "snapshot" && f.after != nil {
		after := f.after
		f.after = nil
		after() // Revoke after the encoded World response, before native dispatch.
	}
	return response, err
}

func TestFargodeepReadinessLuaFinalWorldCannotExtendLeaseOrContext(t *testing.T) {
	for _, mode := range []string{"lease", "context"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request, advance := fargodeepReadinessRig(t, 4)
			armFargodeep(t, s, request)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads, changed := 0, false
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == fargodeepReadinessTestSelection {
					reads++
				}
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			s.session = &fargodeepReadinessFinalWorldSession{fakeSession: f, readinessReads: &reads, after: func() {
				changed = true
				if mode == "lease" {
					advance(3 * time.Minute)
				} else {
					cancel()
				}
			}}
			s.Tick(ctx)
			if !changed {
				t.Fatal("fixture did not reach a World return after final readiness", reads, s.stateLocked())
			}
			requireNoFargodeepReadinessDispatch(t, s, f)
		})
	}
}

func TestFargodeepReadinessLuaCurrentMalformedOrMissingPolicyNeverFallsBack(t *testing.T) {
	for _, mode := range []string{"missing_selection", "foreign_selection", "nonzero_spell", "extra_field", "nil_output", "missing_export", "source_error", "wait", "missing_stand", "zero_stand", "negative_stand", "nonfinite_stand", "overbound_stand", "wrong_stand_type", "missing_phase", "overbound_phase", "missing_pulled", "wrong_pulled_type", "missing_validity", "overbound_validity", "fractional_validity"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request, _ := fargodeepReadinessRig(t, 5)
			armFargodeep(t, s, request)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] != fargodeepReadinessTestSelection {
					out, err := runtime.EvaluateContext(ctx, input)
					return true, runtime.Status().Revision, out, err
				}
				reads++
				out := map[string]any{"kind": "approach", "phase": "approaching_designated_kobold_on_verified_route", "pulled": false, "selection": fargodeepReadinessTestSelection, "standYards": 3.5, "validForMs": 250}
				switch mode {
				case "missing_selection":
					delete(out, "selection")
				case "foreign_selection":
					out["selection"] = "coldridge_pull_readiness"
				case "nonzero_spell":
					out["spellId"] = sinisterStrike
				case "extra_field":
					out["unexpected"] = true
				case "nil_output":
					out = nil
				case "missing_export":
					return false, strings.Repeat("a", 64), nil, nil
				case "source_error":
					return true, runtime.Status().Revision, nil, errors.New("current readiness export failed")
				case "wait":
					out["kind"] = "wait"
				case "missing_stand":
					delete(out, "standYards")
				case "zero_stand":
					out["standYards"] = 0
				case "negative_stand":
					out["standYards"] = -1
				case "nonfinite_stand":
					out["standYards"] = math.NaN()
				case "overbound_stand":
					out["standYards"] = 241
				case "wrong_stand_type":
					out["standYards"] = "3.5"
				case "missing_phase":
					delete(out, "phase")
				case "overbound_phase":
					out["phase"] = strings.Repeat("x", 129)
				case "missing_pulled":
					delete(out, "pulled")
				case "wrong_pulled_type":
					out["pulled"] = "false"
				case "missing_validity":
					delete(out, "validForMs")
				case "overbound_validity":
					out["validForMs"] = 30001
				case "fractional_validity":
					out["validForMs"] = 1.5
				}
				return true, runtime.Status().Revision, out, nil
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if reads == 0 {
				t.Fatal("fixture bypassed the current readiness export")
			}
			requireNoFargodeepReadinessDispatch(t, s, f)
		})
	}
}

func TestFargodeepReadinessLuaAttachedFailureNeverUsesEmbeddedReadySource(t *testing.T) {
	s, f, _, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID,
		Source: `return {decide=function(input) error("authored readiness failed") end}`})
	if err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	requireNoFargodeepReadinessDispatch(t, s, f)
	if s.starterSpells.failedAuthored != s.behavior || s.starterSpells.authoredFailedRevision != accepted.Revision {
		t.Fatal("current authored failure did not retain its exact native refusal fence", s.starterSpells.failure(), s.behavior.Status())
	}
}

func TestFargodeepWorkflowLuaRequiresKnownPartyFacts(t *testing.T) {
	for _, mode := range []string{"health_unknown", "power_unknown", "wrong_power_type", "class_unknown", "combat", "dead", "outside_support_range"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request, _ := fargodeepReadinessRig(t, 5)
			armFargodeep(t, s, request)
			player := fargodeepReadinessPlayer(t, f, 1)
			switch mode {
			case "health_unknown":
				player.HealthKnown = false
			case "power_unknown":
				player.PowerKnown = false
			case "wrong_power_type":
				player.PowerType = 3
			case "class_unknown":
				player.ClassKnown = false
			case "combat":
				player.UnitFlags |= 0x80000
			case "dead":
				player.Alive = false
			case "outside_support_range":
				player.X += 40
			}
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			requireNoFargodeepReadinessDispatch(t, s, f)
		})
	}
}

func TestFargodeepReadinessLuaDoesNotGateAnObservedExistingFight(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	fargodeepReadinessPlayer(t, f, 1).Health = 94
	fargodeepReadinessPlayer(t, f, 4).Power = 57
	f.world.Self.Power = 89 // Below the pull threshold, still enough for Sinister Strike.
	f.world.Self.UnitFlags |= 0x80000
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, 5, 100
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	reads, defenses := 0, 0
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		if input["selection"] == fargodeepReadinessTestSelection {
			reads++
		}
		out, err := runtime.EvaluateContext(ctx, input)
		if input["selection"] == fargodeepReadinessTestSelection && out["kind"] == "attack" {
			defenses++
		}
		return true, runtime.Status().Revision, out, err
	}
	s.Tick(context.Background())
	if reads == 0 || defenses != reads {
		t.Fatal("actual Lua lifecycle did not choose defense below its initial-pull resource thresholds", reads, defenses, s.stateLocked())
	}
	requireFargodeepReadinessAttack(t, s, f, target.Guid)
}

func fargodeepReadinessMovingRig(t *testing.T) (*Service, *fakeSession, *worldv1.CreatureState, *actionsv1.FargodeepEncounterRequest, func(time.Duration)) {
	t.Helper()
	s, f, target, request, advance := fargodeepReadinessRig(t, 5)
	target.X += 10
	armFargodeep(t, s, request)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 0 ||
		countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("fixture did not start its original verified approach", s.stateLocked(), f.packets)
	}
	return s, f, target, request, advance
}

func TestFargodeepWorkflowLuaPrecheckLossStopsOnlyItsCurrentApproach(t *testing.T) {
	for _, mode := range []string{"ally_combat", "support_geometry"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, _, advance := fargodeepReadinessMovingRig(t)
			until := s.fargodeep.until
			starts, heartbeats := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == fargodeepReadinessTestSelection {
					reads++
				}
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			ally := fargodeepReadinessPlayer(t, f, 1)
			if mode == "ally_combat" {
				ally.UnitFlags |= 0x80000
			} else {
				// Still within 35 yards of self, but more than 28 from the target.
				ally.X = -9840
			}
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			s.Tick(context.Background()) // An unchanged refusal must not repeat STOP.
			if reads == 0 || s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1 ||
				countPackets(f, opcode.MSGMoveStartForward) != starts || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeats ||
				countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 ||
				!s.fargodeep.active || s.fargodeep.until != until {
				t.Fatal("Lua precheck refusal left its movement running, repeated STOP or changed mission custody", reads, s.stateLocked(), f.packets)
			}
			for _, packet := range f.packets {
				if packet.Opcode == opcode.MSGMoveStop {
					guid, _, err := readGUID(packet.Payload)
					if err != nil || guid != s.selfGUID {
						t.Fatal("bounded cleanup STOP did not belong to the original current body", guid, err)
					}
				}
			}
		})
	}
}

func TestFargodeepReadinessFinalMovementRefusalPreservesReplacementMissionAndRoute(t *testing.T) {
	s, f, _, _, advance := fargodeepReadinessMovingRig(t)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	reads, changed := 0, false
	var replacement fargodeepEncounter
	packetsAtReplacement := 0
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		if input["selection"] == fargodeepReadinessTestSelection {
			reads++
			if reads == 2 {
				if !s.state.Moving {
					t.Fatal("replacement did not occur at the original moving approach")
				}
				changed = true
				replacement = s.fargodeep
				replacement.active = true
				replacement.until = replacement.until.Add(time.Minute)
				replacement.phase = "replacement_movement_owner"
				replacement.route = []position{s.pos, {x: s.pos.x + 4, y: s.pos.y, z: s.pos.z, o: s.pos.o}}
				replacement.routeIndex = 1
				s.fargodeep = replacement
				replacement.route = append([]position(nil), replacement.route...)
				packetsAtReplacement = len(f.packets)
			}
		}
		return true, runtime.Status().Revision, out, err
	}
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !changed || reads < 2 || !s.state.Moving || len(f.packets) != packetsAtReplacement ||
		countPackets(f, opcode.MSGMoveStop) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
		countPackets(f, opcode.MSGMoveStartForward) != 1 || !reflect.DeepEqual(s.fargodeep, replacement) {
		t.Fatal("old map-step refusal stopped, retired or postprocessed the replacement movement mission", reads, s.stateLocked(), s.fargodeep, replacement, f.packets)
	}
}

func TestFargodeepReadinessInnerOffenseRefusalPreservesReplacementCustody(t *testing.T) {
	for _, guid := range []uint64{4, 5} {
		for _, replace := range []string{"until", "task", "body"} {
			for _, outcome := range []string{"invalid", "changed_source"} {
				t.Run(fmt.Sprintf("%d/%s/%s", guid, replace, outcome), func(t *testing.T) {
					s, f, _, request, _ := fargodeepReadinessRig(t, guid)
					armFargodeep(t, s, request)
					if guid == 5 {
						s.Tick(context.Background()) // Rogue's initial verified route.
					}
					runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
					offenseReads, changed := 0, false
					var replacement fargodeepEncounter
					packetsAtReplacement, attemptsAtReplacement := 0, uint64(0)
					s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
						out, err := runtime.EvaluateContext(ctx, input)
						selection, _ := input["selection"].(string)
						offense := guid == 4 && selection == "" || guid == 5 && selection == "weapon_strike"
						if !offense {
							return true, runtime.Status().Revision, out, err
						}
						offenseReads++
						if offenseReads == 2 {
							changed = true
							replacement = s.fargodeep
							replacement.active = true
							replacement.until = replacement.until.Add(time.Minute)
							replacement.phase = "replacement_after_inner_offense"
							if replace == "task" {
								replacement.route = []position{s.pos, {x: s.pos.x + 2, y: s.pos.y, z: s.pos.z, o: s.pos.o}}
								replacement.routeIndex = 1
							} else if replace == "body" {
								s.sessionID = "replacement-inner-offense-body"
								replacement.bodySession = s.sessionID
							}
							s.fargodeep = replacement
							replacement.route = append([]position(nil), replacement.route...)
							packetsAtReplacement, attemptsAtReplacement = len(f.packets), s.state.AssistAttempts
							if outcome == "invalid" {
								out = nil
							} else if _, reloadErr := runtime.Reload(behaviors.StarterSpellSource+"\n-- accepted inner offense revision S\n", runtime.Status().Revision); reloadErr != nil {
								t.Fatal(reloadErr)
							}
						}
						return true, runtime.Status().Revision, out, err
					}
					s.Tick(context.Background())
					if !changed || offenseReads != 2 || len(f.packets) != packetsAtReplacement ||
						s.state.AssistAttempts != attemptsAtReplacement || countPackets(f, opcode.MSGMoveStop) != 0 ||
						!reflect.DeepEqual(s.fargodeep, replacement) {
						t.Fatal("inner selector refusal wrote against or retired replacement mission/body custody", offenseReads, s.stateLocked(), s.fargodeep, replacement, f.packets)
					}
					if guid == 4 && (countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0) {
						t.Fatal("caster dispatched before the final selector admitted its original mission", f.packets)
					}
				})
			}
		}
	}
}

func TestFargodeepReadinessUnrelatedPacketHeartbeatDoesNotChangePullFacts(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	heartbeat := false
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		if input["selection"] == fargodeepReadinessTestSelection && !heartbeat {
			heartbeat = true
			f.world.ObservedPackets++ // No body, visit, player, quest or target fact changed.
		}
		return true, runtime.Status().Revision, out, err
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	if !heartbeat {
		t.Fatal("fixture never crossed readiness with the unrelated observation counter")
	}
	requireFargodeepReadinessAttack(t, s, f, target.Guid)
}

func TestFargodeepStancePublicCASChangesTheSameMissionRouteWithoutNativeThreshold(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 1)
	target.X += 22 // Twenty-five yards: inside the old Priest stance, outside the edited stance.
	armFargodeep(t, s, request)
	if strings.Count(behaviors.StarterSpellSource, "[4]=3.5,[5]=28,[8]=33") != 1 {
		t.Fatal("actual shared stance tuning is missing or ambiguous")
	}
	edited := strings.Replace(behaviors.StarterSpellSource, "[4]=3.5,[5]=28,[8]=33", "[4]=3.5,[5]=20,[8]=33", 1)
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: edited})
	if err != nil {
		t.Fatal(err)
	}
	authored, until, visit := s.behavior, s.fargodeep.until, s.fargodeep.visit
	observed := proto.Clone(f.world)
	plans := 0
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		plans++
		return []position{from, to}, nil
	}
	s.Tick(context.Background())
	if plans != 1 || s.fargodeep.route == nil || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 {
		t.Fatal("edited Lua stance did not choose a verified approach instead of casting", plans, s.stateLocked(), f.packets)
	}
	changed, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: accepted.Revision, Source: behaviors.StarterSpellSource})
	if err != nil || changed.Revision == accepted.Revision || s.behavior != authored {
		t.Fatal("actual CAS did not replace stance in the same accepted runtime", changed, err)
	}
	s.Tick(context.Background())
	if plans != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.MSGMoveStartForward) != 0 ||
		s.state.LastCastTargetGuid != target.Guid || s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 ||
		s.fargodeep.until != until || s.fargodeep.visit != visit || s.fargodeep.target != request.TargetGuid || !proto.Equal(observed, f.world) {
		t.Fatal("restored Lua stance did not govern the same mission's actual route/cast choice", plans, s.stateLocked(), f.packets)
	}
}

func TestFargodeepStanceFactsCannotChangeAcrossAnEngagedMovementWrite(t *testing.T) {
	s, f, target, _, advance := fargodeepReadinessMovingRig(t)
	f.world.Self.UnitFlags |= 0x80000
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	changed := false
	starts, heartbeats := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat)
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		if input["selection"] == fargodeepReadinessTestSelection && !changed {
			changed = true
			fargodeepReadinessPlayer(t, f, 1).Health = 94
		}
		return true, runtime.Status().Revision, out, err
	}
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !changed || countPackets(f, opcode.MSGMoveStartForward) != starts || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeats ||
		countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || !s.fargodeepSeen[target.Guid] {
		t.Fatal("a changed stance input reused the earlier movement choice or erased custody", s.stateLocked(), f.packets)
	}
}

func TestFargodeepStanceCannotRebindAPullChangeInsideItsPolicyRead(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	f.world.Self.UnitFlags |= 0x80000
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	changed := false
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		if input["selection"] == fargodeepReadinessTestSelection && !changed {
			changed = true
			s.fargodeep.pulled = !s.fargodeep.pulled
		}
		return true, runtime.Status().Revision, out, err
	}
	s.Tick(context.Background())
	if !changed || !s.fargodeepSeen[target.Guid] {
		t.Fatal("fixture missed the actual policy read or erased mission custody", s.stateLocked())
	}
	if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
		countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
		s.state.AssistAttempts != 0 || !s.fargodeep.pulled {
		t.Fatal("the old decision wrote or overwrote the callback's changed phase", s.stateLocked(), f.packets)
	}
}

func TestFargodeepStanceFactsCannotChangeAcrossAnEngagedCastWrite(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 4)
	target.X += 22 // Twenty-five yards: cast under the first stance, approach under the changed stance.
	armFargodeep(t, s, request)
	f.world.Self.UnitFlags |= 0x80000
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
	const stance = "local stand=type(self)==\"table\" and self.classKnown==true and tuning.stand_yards[self.classId]"
	if strings.Count(behaviors.StarterSpellSource, stance) != 1 {
		t.Fatal("actual shared stance expression is missing or ambiguous")
	}
	edited := strings.Replace(behaviors.StarterSpellSource, stance, stance+"\n    if stand and self.health<100 then stand=20 end", 1)
	runtime := starterLuaRuntime(t, edited)
	stanceRead, changed := false, false
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		selection, _ := input["selection"].(string)
		if selection == fargodeepReadinessTestSelection {
			stanceRead = true
			if out["standYards"] != float64(33) {
				t.Fatal("initial actual Lua stance did not permit casting at twenty-five yards", out)
			}
		} else if selection == "" && stanceRead && !changed {
			changed = true
			f.world.Self.Health = 99
		}
		return true, runtime.Status().Revision, out, err
	}
	s.Tick(context.Background())
	current, err := runtime.EvaluateContext(context.Background(), s.fargodeepReadinessFactsLocked(f.world, f.party))
	if err != nil || current["standYards"] != float64(20) || !changed || !stanceRead || !s.fargodeepSeen[target.Guid] ||
		countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
		countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || s.state.AssistAttempts != 0 {
		t.Fatal("a changed stance input reused the earlier cast choice or erased custody", current, err, s.stateLocked(), f.packets)
	}
}

func TestFargodeepWorkflowPublicCASChangesTheSameMissionGoal(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
	if err != nil {
		t.Fatal(err)
	}
	authored, visit, until := s.behavior, s.fargodeep.visit, s.fargodeep.until
	s.Tick(context.Background())
	if s.fargodeep.route == nil || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal("fixture did not reach the uncommitted approach", s.stateLocked())
	}
	if strings.Count(behaviors.StarterSpellSource, "quests={47,60}") != 1 {
		t.Fatal("actual Lua goal table is missing or ambiguous")
	}
	edited := strings.Replace(behaviors.StarterSpellSource, "quests={47,60}", "quests={33}", 1)
	changed, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: accepted.Revision, Source: edited})
	if err != nil || changed.Revision == accepted.Revision || s.behavior != authored {
		t.Fatal(changed, err)
	}
	s.Tick(context.Background())
	if s.fargodeep.active || s.fargodeep.phase != "own_quest_goal_complete_or_unknown" || s.fargodeep.target != target.Guid ||
		s.fargodeep.visit != visit || s.fargodeep.until != until || !s.fargodeepSeen[target.Guid] {
		t.Fatal("current Lua goal did not retire its same bound mission without erasing reservation", s.stateLocked())
	}
	requireNoFargodeepReadinessDispatch(t, s, f)
}

func TestFargodeepWorkflowReceivesAllThirtyTwoCreaturesAndCurrentQuestFacts(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	for i := 0; i < 31; i++ {
		other := proto.Clone(target).(*worldv1.CreatureState)
		other.Guid += uint64(i + 1)
		other.X = -9720
		if i == 30 {
			other.X = target.X + 1
		}
		f.world.NearbyCreatures = append(f.world.NearbyCreatures, other)
	}
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	reads := 0
	var capturedFrame []byte
	var capturedOutput map[string]any
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		if input["selection"] == fargodeepReadinessTestSelection {
			reads++
			workflow := input["workflow"].(map[string]any)
			scene := workflow["scene"].(map[string]any)
			if len(scene["nearbyCreatures"].([]any)) != 32 || len(scene["selfQuestLog"].([]any)) != 25 ||
				len(input["observation"].(map[string]any)["players"].(map[string]any)) != 3 || scene["requestedCreature"].(map[string]any)["guid"] != fmt.Sprint(target.Guid) {
				t.Fatal("workflow truncated native facts or lost full GUID precision", input)
			}
			frame, err := json.Marshal(input)
			wire, wireErr := structpb.NewStruct(map[string]any{"sessionId": s.sessionID, "selfGuid": fmt.Sprint(s.selfGUID), "input": input})
			if err != nil || len(frame) > behavior.MaxJSONBytes || wireErr != nil || proto.Size(wire) > modcb.StarterSpellPolicyMaxBytes {
				t.Fatal("complete native workflow frame exceeds actual CLI/callback limits", len(frame), err, wireErr, proto.Size(wire))
			}
			capturedFrame = append([]byte(nil), frame...)
		}
		out, err := runtime.EvaluateContext(ctx, input)
		encoded, encodeErr := json.Marshal(out)
		if encodeErr != nil || json.Unmarshal(encoded, &capturedOutput) != nil {
			t.Fatal("workflow output cannot be detached", encodeErr)
		}
		return true, runtime.Status().Revision, out, err
	}
	s.Tick(context.Background())
	if reads == 0 || s.fargodeep.phase != "waiting_for_isolated_kobold" || s.fargodeep.route != nil || !s.fargodeep.active {
		t.Fatal("last observed neighbor did not block a pull through the actual Lua workflow", reads, s.stateLocked())
	}
	requireNoFargodeepReadinessDispatch(t, s, f)
	practiceDefaultRuntimeCLIParity(t, behaviors.StarterSpellSource)(capturedFrame, capturedOutput)
}

func TestFargodeepWorkflowAdmissionRejectsAQuestRevisionChangeDuringLua(t *testing.T) {
	s, f, _, request, _ := fargodeepReadinessRig(t, 5)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	changed := false
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		if input["selection"] == fargodeepReadinessTestSelection && !changed {
			changed = true
			f.world.SelfQuestLogRevision++
		}
		return true, runtime.Status().Revision, out, err
	}
	if _, err := s.FargodeepEncounter(context.Background(), request); err == nil || !changed || s.fargodeep.active || s.fargodeepSeen[request.TargetGuid] {
		t.Fatal("stale Lua activation acquired a mission or target reservation", err, s.stateLocked())
	}
	requireNoFargodeepReadinessDispatch(t, s, f)
}

func TestFargodeepWorkflowElapsedAgesKeepTheLuaFreshnessDeadline(t *testing.T) {
	for _, test := range []struct {
		name          string
		before, after uint64
		elapsed       time.Duration
		combat, write bool
	}{
		{"elapsed_cached_attack", 1000, 1001, time.Millisecond, false, true},
		{"recent_attack_boundary", 4999, 5001, 2 * time.Millisecond, false, false},
		{"sustained_attack_boundary", 29999, 30001, 2 * time.Millisecond, true, false},
		{"replacement_attack_age_reset", 1000, 0, time.Millisecond, false, false},
		{"unexplained_age_jump", 1000, 2000, time.Millisecond, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f, target, request, advance := fargodeepReadinessRig(t, 4)
			target.X += 22
			armFargodeep(t, s, request)
			if test.combat {
				f.world.Self.UnitFlags |= 0x80000
			}
			target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, test.before
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			changed := false
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				if input["selection"] == fargodeepReadinessTestSelection && !changed {
					changed = true
					target.AttackAgeMs = test.after
					advance(test.elapsed)
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			want := 0
			if test.write {
				want = 1
			}
			if !changed || countPackets(f, opcode.CMSGCastSpell) != want || countPackets(f, opcode.CMSGAttackswing) != 0 ||
				countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
				!s.fargodeepSeen[target.Guid] || s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 {
				t.Fatal("elapsed age reused an expired policy decision or prevented its unchanged current effect", test, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestFargodeepWorkflowFinalOffenseCannotUseTheExpiredLuaDeadline(t *testing.T) {
	for _, combat := range []bool{false, true} {
		t.Run(fmt.Sprint(combat), func(t *testing.T) {
			s, f, target, request, advance := fargodeepReadinessRig(t, 4)
			target.X += 22
			armFargodeep(t, s, request)
			before := uint64(4999)
			if combat {
				f.world.Self.UnitFlags |= 0x80000
				before = 29999
			}
			target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, before
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			changed, spellReads := false, 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				selection, _ := input["selection"].(string)
				if selection == "" {
					spellReads++
				}
				workflow, _ := input["workflow"].(map[string]any)
				if selection == fargodeepReadinessTestSelection && workflow["stage"] == "offense" && !changed {
					if spellReads == 0 || out["validForMs"] != float64(1) {
						t.Fatal("fixture missed the final source-bound offense decision", spellReads, out)
					}
					changed = true
					target.AttackAgeMs = before + 2
					advance(time.Millisecond)
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			if !changed || spellReads == 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
				countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
				s.state.AssistAttempts != 0 || !s.fargodeepSeen[target.Guid] {
				t.Fatal("the final physical consumer crossed Lua's returned deadline or erased custody", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestFargodeepWorkflowExtraGroupMemberDoesNotAcquireAMissionRole(t *testing.T) {
	s, f, target, request, _ := fargodeepReadinessRig(t, 5)
	armFargodeep(t, s, request)
	f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: 87, Online: true})
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, 87, 100
	s.Tick(context.Background())
	if s.fargodeep.active || s.fargodeep.phase != "target_claimed_or_attacking_foreign_player" || !s.fargodeepSeen[target.Guid] {
		t.Fatal("an unrelated group member acquired the explicit three-body mission", s.stateLocked())
	}
	requireNoFargodeepReadinessDispatch(t, s, f)
}

func TestFargodeepWorkflowLuaDistinguishesInitialAndEngagedRouteDrift(t *testing.T) {
	for _, engaged := range []bool{false, true} {
		t.Run(fmt.Sprint(engaged), func(t *testing.T) {
			s, f, target, _, advance := fargodeepReadinessMovingRig(t)
			before := s.fargodeep.replans
			target.X += .5
			target.PositionRevision++
			if engaged {
				f.world.Self.UnitFlags |= 0x80000
				target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
			}
			plans := 0
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				plans++
				return []position{from, to}, nil
			}
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			want := 1
			phase := "verified_route_waiting_fresh_frame"
			if engaged {
				want = 0
				phase = "approaching_designated_kobold_on_verified_route"
			}
			if plans != want || s.fargodeep.replans != before+want || !s.fargodeep.active || s.fargodeep.phase != phase ||
				countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || !s.fargodeepSeen[target.Guid] {
				t.Fatal("Lua reused the stricter initial corridor or replanned the valid engaged route", engaged, plans, s.stateLocked(), f.packets)
			}
		})
	}
}
