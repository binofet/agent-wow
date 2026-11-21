package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func starterWeaponNativeRig(t *testing.T, mission string) (*Service, *fakeSession, *worldv1.CreatureState, func(time.Duration)) {
	t.Helper()
	var s *Service
	var f *fakeSession
	var target *worldv1.CreatureState
	var advance func(time.Duration)
	if mission == "practice" {
		s, f, advance = practiceRig(t, 4)
		s.sessionID = "starter-weapon-practice"
		target = f.world.NearbyCreatures[0]
		target.UnitFlagsKnown, target.DynamicFlagsKnown = true, true
		// This named mission is authorized from its own observed wolf and body,
		// without an owner or another party member supplying combat authority.
		f.party.Group.InGroup = false
	} else {
		var request *actionsv1.FargodeepEncounterRequest
		s, f, target, request, advance = fargodeepRig(t, 5)
		s.sessionID = request.SessionId
		armFargodeep(t, s, request)
		target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, 5, 100
		f.world.Self.UnitFlags |= 0x80000
	}
	learnSpells(s, autoAttack, sinisterStrike, 53)
	proof := s.weapons[2092]
	proof.damageKnown, proof.damageMin, proof.damageMax = true, 20, 30
	s.weapons[2092] = proof
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("fixture has no current observed body pose")
	}
	return s, f, target, advance
}

func starterWeaponObservedStealth(t *testing.T, s *Service, f *fakeSession) {
	t.Helper()
	learnSpells(s, autoAttack, sinisterStrike, 53, 1784, 8676)
	// Initial spell knowledge is lazy catalog input. The older stealth fixture
	// reaches this through candidatesLocked before testing the aura itself.
	s.learnedContractsLocked()
	f.world.Self.ShapeshiftFormKnown, f.world.Self.ShapeshiftForm = true, 30
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate,
		Payload: buffAura(s.selfGUID, s.selfGUID, 2, 1784, 0)}); err != nil || !s.stealthedLocked() {
		t.Fatal("fixture did not observe actual own stealth", err)
	}
}

func TestStarterWeaponNativeUsesActualWeaponModelInBothNamedMissions(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		t.Run(mission, func(t *testing.T) {
			s, f, target, _ := starterWeaponNativeRig(t, mission)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == "weapon_strike" {
					reads++
					obs := input["observation"].(map[string]any)
					row := obs["spells"].(map[string]any)["53"].(map[string]any)
					if row["directDamage"] != float64(52.5) || row["damageModelKnown"] != true ||
						row["weaponStrike"] != true || row["cost"] != "60" || row["ready"] != true {
						t.Fatal("named task invented or omitted the reflected weapon model", row)
					}
				}
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			if reads < 2 || lastCast(f) != 53 || countPackets(f, opcode.CMSGCastSpell) != 1 ||
				s.state.LastCastTargetGuid != target.Guid || s.state.AssistDamageEvents != 0 || s.state.ServerAssistGo != 0 {
				t.Fatal("named task did not use Lua's exact mechanically reflected strike", reads, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStarterWeaponNativeCurrentPolicyFailureCannotStartNewAttack(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		for _, mode := range []string{"transport", "missing export", "extra field", "unknown rank", "wrong kind", "fractional rank"} {
			t.Run(mission+"/"+mode, func(t *testing.T) {
				s, f, _, _ := starterWeaponNativeRig(t, mission)
				runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
				reads := 0
				s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
					if input["selection"] != "weapon_strike" {
						out, err := runtime.EvaluateContext(ctx, input)
						return true, runtime.Status().Revision, out, err
					}
					reads++
					out := map[string]any{"kind": "cast", "spellId": 53}
					switch mode {
					case "transport":
						return false, "", nil, errors.New("current source socket unavailable")
					case "missing export":
						return false, strings.Repeat("a", 64), nil, nil
					case "extra field":
						out["nativeFallback"] = true
					case "unknown rank":
						out["spellId"] = 4294967295
					case "wrong kind":
						out["kind"] = "native"
					case "fractional rank":
						out["spellId"] = 53.5
					}
					return true, strings.Repeat("a", 64), out, nil
				}
				s.Tick(context.Background())
				if reads != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 {
					t.Fatal("failed active weapon policy fell back to a new hostile write", reads, s.stateLocked(), f.packets)
				}
			})
		}
	}
}

func TestStarterWeaponNativeStealthOpenerPrecedesAnyAutoswing(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		t.Run(mission, func(t *testing.T) {
			s, f, _, _ := starterWeaponNativeRig(t, mission)
			starterWeaponObservedStealth(t, s, f)
			s.Tick(context.Background())
			if lastCast(f) != 8676 || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 {
				t.Fatal("new autoswing broke the selected observed stealth opener", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStarterWeaponNativeExplicitMeleeStartsOnlyOrdinaryAttack(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		t.Run(mission, func(t *testing.T) {
			s, f, _, _ := starterWeaponNativeRig(t, mission)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == "weapon_strike" {
					return true, strings.Repeat("a", 64), map[string]any{"kind": "melee", "spellId": 0}, nil
				}
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 1 {
				t.Fatal("explicit Lua melee decision used a native strike fallback", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStarterWeaponNativeFinalReadRevalidatesActualMechanicsAndMission(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		for _, mode := range []string{"energy", "weapon", "durability", "behind", "stealth", "combo", "quest", "expiry", "party", "policy"} {
			if mission == "practice" && mode == "party" {
				continue // Practice deliberately requires no party authority.
			}
			t.Run(mission+"/"+mode, func(t *testing.T) {
				s, f, target, advance := starterWeaponNativeRig(t, mission)
				selected := uint32(53)
				if mode == "stealth" {
					selected = 8676
					starterWeaponObservedStealth(t, s, f)
				}
				if mode == "combo" {
					selected = 6762
					learnSpells(s, autoAttack, sinisterStrike, 53, 6762)
					emitPolymorph(s, opcode.SMSGUpdateComboPoints, comboPacket(target.Guid, 5))
				}
				runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
				reads := 0
				s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
					if input["selection"] != "weapon_strike" {
						out, err := runtime.EvaluateContext(ctx, input)
						return true, runtime.Status().Revision, out, err
					}
					reads++
					out := map[string]any{"kind": "cast", "spellId": selected}
					if reads == 2 {
						switch mode {
						case "energy":
							f.world.Self.Power = 0
						case "weapon":
							proof := s.weapons[2092]
							proof.subclass = 7
							proof.damageMin++
							s.weapons[2092] = proof
						case "durability":
							f.world.Self.Mainhand.Durability = 0
						case "behind":
							target.Orientation = float32(math.Pi)
						case "stealth":
							delete(s.buffAuras, s.selfGUID)
							f.world.Self.ShapeshiftForm = 0
						case "combo":
							s.weaponCombo.target = target.Guid + 1
						case "quest":
							if mission == "fargodeep" {
								f.world.SelfQuestLog[0].QuestId = 0
								f.world.SelfQuestLog[1].QuestId = 0
							} else {
								// An XP-only mission cannot issue a new strike after
								// level2 without a proven current own fight.
								f.world.Self.Level = 2
							}
						case "expiry":
							advance(11 * time.Minute)
						case "party":
							f.party.Group.InGroup = false
						case "policy":
							out = map[string]any{"kind": "wait", "spellId": 0}
						}
					}
					return true, strings.Repeat("a", 64), out, nil
				}
				s.Tick(context.Background())
				if reads != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.AssistAttempts != 0 {
					t.Fatal("cast escaped a changed final exact-mechanical/mission observation", reads, s.stateLocked(), f.packets)
				}
			})
		}
	}
}

type starterWeaponFinalWorldSession struct {
	*fakeSession
	weaponReads     *int
	finalWorldReads int
	afterFinalWorld func()
}

func (f *starterWeaponFinalWorldSession) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	response, err := f.fakeSession.InvokeModule(ctx, request, options...)
	if err == nil && request.Module == "buddy-world" && request.Method == "snapshot" && *f.weaponReads >= 2 {
		f.finalWorldReads++
		// The first World read binds the second Lua result. The second is
		// the final native authority snapshot; revoke only after it returns.
		if f.finalWorldReads == 2 {
			f.afterFinalWorld()
		}
	}
	return response, err
}

func TestStarterWeaponNativeFinalWorldReturnCannotOutliveMissionOrContext(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		for _, mode := range []string{"lease", "context"} {
			t.Run(mission+"/"+mode, func(t *testing.T) {
				s, f, _, advance := starterWeaponNativeRig(t, mission)
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
				reads := 0
				s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
					if input["selection"] == "weapon_strike" {
						reads++
					}
					out, err := runtime.EvaluateContext(ctx, input)
					return true, runtime.Status().Revision, out, err
				}
				late := &starterWeaponFinalWorldSession{fakeSession: f, weaponReads: &reads, afterFinalWorld: func() {
					if mode == "lease" {
						advance(11 * time.Minute)
					} else {
						cancel()
					}
				}}
				s.session = late
				s.Tick(ctx)
				if reads != 2 || late.finalWorldReads != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.AssistAttempts != 0 {
					t.Fatal("final World callback extended expired mission/context authority", reads, late.finalWorldReads, s.stateLocked(), f.packets)
				}
			})
		}
	}
}

func TestStarterWeaponNativeSharedSourceCASChangesAbilityInSameBody(t *testing.T) {
	s, f, target, _ := starterWeaponNativeRig(t, "practice")
	point, known, _ := creaturePosition(target)
	if !known {
		t.Fatal("missing actual target pose")
	}
	initial := s.starterWeaponDecisionLocked(context.Background(), f.world, target, point)
	if !initial.valid || initial.kind != "cast" || initial.spell.ID != 53 {
		t.Fatal("initial shared Lua weapon choice unavailable", initial, s.starterSpells.failure())
	}
	source := strings.Replace(behaviors.StarterSpellSource, "M.race_profiles = {}", "M.race_profiles = {}\nM.tuning.family_weights={[53]=0}", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("test did not edit the real shared selector tuning")
	}
	request := &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: source}
	accepted, err := s.ReloadBehavior(context.Background(), request)
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	changed := s.starterWeaponDecisionLocked(context.Background(), f.world, target, point)
	if !changed.valid || changed.kind != "cast" || changed.spell.ID != sinisterStrike ||
		s.starterSpells.revision != accepted.Revision || len(f.packets) != 0 {
		t.Fatal("same-body source edit did not change the shared ability choice", changed, s.starterSpells.failure(), f.packets)
	}
	request.Source = behaviors.StarterSpellSource
	if _, err := s.ReloadBehavior(context.Background(), request); err == nil {
		t.Fatal("stale editor revision replaced the accepted shared tuning")
	}
	request.ExpectedRevision = accepted.Revision
	if _, err := s.ReloadBehavior(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	restored := s.starterWeaponDecisionLocked(context.Background(), f.world, target, point)
	if !restored.valid || restored.spell.ID != 53 || s.selfGUID != f.world.Self.Guid {
		t.Fatal("same body did not use the accepted corrective revision", restored, s.starterSpells.failure())
	}
}

func TestStarterWeaponNativePracticeFinishesOnlyExistingProvenOwnFight(t *testing.T) {
	for _, mode := range []string{"own fight", "foreign fight", "unknown attack"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, advance := starterWeaponNativeRig(t, "practice")
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("fixture did not start its authorized own practice fight", s.stateLocked(), f.packets)
			}
			finishPracticeCast(s)
			f.world.Self.Level, f.world.Self.UnitFlags = 2, 0x80000
			target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
			if mode == "foreign fight" {
				target.AttackVictimGuid++
			} else if mode == "unknown attack" {
				target.AttackVictimKnown = false
			}
			advance(1500 * time.Millisecond)
			s.Tick(context.Background())
			want := 1
			if mode == "own fight" {
				want = 2
			}
			if countPackets(f, opcode.CMSGCastSpell) != want || mode == "own fight" &&
				(!s.state.Practicing || s.state.PracticePhase != "finishing_current_fight_to_level2") || mode != "own fight" && s.state.Practicing {
				t.Fatal("level boundary granted authority without the current own fight", mode, s.stateLocked(), f.packets)
			}
		})
	}
}

type starterWeaponAmbiguousSession struct{ *fakeSession }

func (f starterWeaponAmbiguousSession) SendPacket(ctx context.Context, request *modv1.SendPacketRequest, options ...grpc.CallOption) (*emptypb.Empty, error) {
	response, err := f.fakeSession.SendPacket(ctx, request, options...)
	if request.Opcode == opcode.CMSGCastSpell && err == nil {
		return nil, errors.New("ambiguous weapon strike transport")
	}
	return response, err
}

func TestStarterWeaponNativeAmbiguousStrikeRetainsReceiptAcrossTimeAndLuaEdit(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		t.Run(mission, func(t *testing.T) {
			s, f, target, advance := starterWeaponNativeRig(t, mission)
			s.session = starterWeaponAmbiguousSession{f}
			s.Tick(context.Background())
			operation := s.stateLocked().GetLastSpellOperation()
			if countPackets(f, opcode.CMSGCastSpell) != 1 || operation == nil || operation.GetTransportWritten() ||
				operation.GetServerCastGo() || operation.GetEffectObserved() || s.failedAssistTarget != target.Guid ||
				operation.GetPhase() != "transport_unconfirmed_no_retry" {
				t.Fatal("ambiguous strike lost native uncertainty or invented effect", operation, s.stateLocked(), f.packets)
			}
			before := proto.Clone(operation).(*actionsv1.SpellOperation)
			advance(6 * time.Second)
			accepted, err := s.ReloadBehavior(context.Background(), &actionsv1.BehaviorReloadRequest{
				SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource + "\n-- accepted after uncertain strike"})
			if err != nil || accepted.Revision == "" {
				t.Fatal(accepted, err)
			}
			for range 2 {
				s.Tick(context.Background())
				advance(time.Second)
			}
			after := s.stateLocked().GetLastSpellOperation()
			if countPackets(f, opcode.CMSGCastSpell) != 1 || after == nil || before.SpellId != after.SpellId ||
				before.TargetGuid != after.TargetGuid || before.Capability != after.Capability ||
				after.TransportWritten || after.ServerCastGo || after.EffectObserved || after.Phase != "outcome_unconfirmed_no_retry" ||
				s.state.AssistDamageEvents != 0 || s.state.ServerAssistGo != 0 || s.failedAssistTarget != target.Guid {
				t.Fatal("time/source edit replayed or settled an uncertain strike", before, after, s.stateLocked(), f.packets)
			}
			if mission == "practice" {
				uncertain := s.starterSpells.uncertainStrike
				if uncertain == nil || uncertain.view.SpellId != before.SpellId || uncertain.view.TargetGuid != target.Guid {
					t.Fatal("timed out named strike lost its owned uncertainty receipt", uncertain)
				}
				if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
					t.Fatal(err)
				}
				// A real matching STOP acknowledgment releases movement/attack
				// custody. It does not settle the separate uncertain spell.
				deliverHealReceiptPacket(t, s, opcode.SMSGAttackstop, meleeReceiptStop(s.selfGUID, target.Guid, 0))
				if s.autoTarget != 0 || s.autoStopping || s.state.AutoAttackUnconfirmed {
					t.Fatal("fixture did not observe the ordinary attack stop", s.stateLocked())
				}
				if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
					t.Fatal("same wolf mission could not be explicitly rearmed", err)
				}
				if s.failedAssistTarget != 0 {
					t.Fatal("fixture did not reach the mission's ordinary failed-target reset")
				}
				// Another native capability may own the general receipt slot.
				// Its existence cannot erase the named strike's unresolved write.
				heal, known := s.spellData.Lookup(lesserHeal)
				if !known {
					t.Fatal("missing actual heal rank in fixture catalog")
				}
				s.reserveHealReceiptLocked("", heal, s.selfGUID)
				if s.supportOp == uncertain {
					t.Fatal("fixture did not replace the general native receipt slot")
				}
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 1 || s.starterSpells.uncertainStrike != uncertain ||
					uncertain.view.ServerCastGo || uncertain.view.EffectObserved || uncertain.view.Phase != "outcome_unconfirmed_no_retry" {
					t.Fatal("Practice rearm or a later heal receipt erased named strike uncertainty", uncertain, s.stateLocked(), f.packets)
				}
			}
		})
	}
}

func starterWeaponObservedGo(s *Service, target uint64, count byte) []byte {
	p := append(packedGUID(s.selfGUID), packedGUID(s.selfGUID)...)
	p = append(p, count)
	p = binary.LittleEndian.AppendUint32(p, s.supportOp.view.SpellId)
	p = binary.LittleEndian.AppendUint32(p, 0) // Ordinary unit-target cast flags.
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = append(p, 1)
	p = binary.LittleEndian.AppendUint64(p, target)
	p = append(p, 0)
	p = binary.LittleEndian.AppendUint32(p, 2)
	return append(p, packedGUID(target)...)
}

func TestStarterWeaponNativeMatchingReceiptResolvesOnlyItsUnexpiredUncertaintyFence(t *testing.T) {
	for _, mission := range []string{"practice", "fargodeep"} {
		for _, receipt := range []string{"go", "reject", "control rejection"} {
			t.Run(mission+"/"+receipt, func(t *testing.T) {
				s, f, target, advance := starterWeaponNativeRig(t, mission)
				s.session = starterWeaponAmbiguousSession{f}
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 1 || s.failedAssistTarget != target.Guid ||
					s.failedAssistCastTarget != target.Guid || !s.state.CastPending || s.starterSpells.uncertainStrike != s.supportOp {
					t.Fatal("fixture did not retain owned ambiguous cast", s.stateLocked(), f.packets)
				}
				reservedAssist, reservedHeal := s.nextAssist, s.nextHeal
				advance(250 * time.Millisecond)
				deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, starterWeaponObservedGo(s, target.Guid, s.castCount+1))
				if s.failedAssistTarget != target.Guid || s.failedAssistCastTarget != target.Guid ||
					s.supportOp.view.ServerCastGo || !s.state.CastPending || s.starterSpells.uncertainStrike != s.supportOp {
					t.Fatal("unrelated cast count released the owned uncertainty fence", s.stateLocked())
				}
				if receipt != "go" {
					failure := byte(47)
					if receipt == "control rejection" {
						failure = 26
					}
					packet := binary.LittleEndian.AppendUint32([]byte{s.castCount}, s.supportOp.view.SpellId)
					packet = append(packet, failure)
					deliverHealReceiptPacket(t, s, opcode.SMSGCastFailed, packet)
					op := s.stateLocked().GetLastSpellOperation()
					if s.starterSpells.uncertainStrike != nil || s.state.CastPending || op == nil || op.ServerCastGo ||
						op.EffectObserved || op.Phase != "server_rejected_no_replay" || s.state.LastCastFailure != uint32(failure) {
						t.Fatal("definite rejection lost existing failed-target semantics", op, s.stateLocked())
					}
					if receipt == "control rejection" {
						if s.failedAssistTarget != 0 || s.failedAssistCastTarget != 0 || s.nextAssist != reservedAssist ||
							s.nextHeal != reservedHeal || s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 ||
							countPackets(f, opcode.CMSGCastSpell) != 1 {
							t.Fatal("definite temporary control rejection retained ambiguity or changed the real GCD", op, s.stateLocked())
						}
						return
					}
					if s.failedAssistTarget != target.Guid || s.failedAssistCastTarget != target.Guid {
						t.Fatal("ordinary definite rejection lost the existing named-target pause", s.stateLocked())
					}
					advance(6 * time.Second)
					s.Tick(context.Background())
					if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.AssistDamageEvents != 0 || s.state.ServerAssistGo != 0 {
						t.Fatal("definite rejected strike replayed or invented success", s.stateLocked(), f.packets)
					}
					return
				}
				deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, starterWeaponObservedGo(s, target.Guid, s.castCount))
				op := s.stateLocked().GetLastSpellOperation()
				if s.failedAssistTarget != 0 || s.failedAssistCastTarget != 0 || s.state.CastPending || op == nil || s.starterSpells.uncertainStrike != nil ||
					!op.ServerCastGo || op.TransportWritten || op.EffectObserved || op.Phase != "server_cast_go_effect_unverified" ||
					s.state.ServerAssistGo != 1 || s.state.AssistDamageEvents != 0 || countPackets(f, opcode.CMSGCastSpell) != 1 {
					t.Fatal("matching unexpired GO did not resolve only its own cast uncertainty", op, s.stateLocked(), f.packets)
				}
			})
		}
	}
}
