package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func practiceCasterRig(t *testing.T, class uint32) (*Service, *fakeSession, *worldv1.CreatureState, uint32, func(time.Duration)) {
	t.Helper()
	s, f, advance := practiceRig(t, 8)
	best := uint32(2136) // Observed learned Fire Blast, beyond baseline Fireball.
	if class == 5 {
		if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
			t.Fatal(err)
		}
		f.world.Self.ClassId = 5
		f.world.SelfQuestLogKnown = true
		f.world.SelfQuestLog = make([]*worldv1.QuestLogEntry, 25)
		for slot := range f.world.SelfQuestLog {
			f.world.SelfQuestLog[slot] = &worldv1.QuestLogEntry{Slot: uint32(slot)}
		}
		f.world.SelfQuestLog[0].QuestId = 33
		best = 8092 // Observed learned Mind Blast, beyond baseline Smite.
		learnSpells(s, autoAttack, smite, lesserHeal, best)
		if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
			t.Fatal("priest requires its own incomplete quest33", err)
		}
	} else {
		learnSpells(s, autoAttack, fireball, best)
	}
	f.party.Group.InGroup = false
	target := f.world.NearbyCreatures[0]
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("fixture lost the observed body pose")
	}
	return s, f, target, best, advance
}

func practiceCasterConfirmObservedCast(t *testing.T, s *Service, target uint64) {
	t.Helper()
	spell := s.supportOp.view.SpellId
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellGo, starterWeaponObservedGo(s, target, s.castCount))
	deliverHealReceiptPacket(t, s, opcode.SMSGSpellnonmeleedamagelog, exactOffenseDamageLog(s.selfGUID, target, spell, 10))
	if s.state.CastPending || !s.supportOp.view.ServerCastGo || !s.supportOp.view.EffectObserved {
		t.Fatal("actual matching GO and damage did not settle the selected direct cast", s.stateLocked())
	}
}

func TestPracticeCasterLuaChoosesLearnedDamageBeyondFixedBaseline(t *testing.T) {
	for _, class := range []uint32{8, 5} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, target, best, _ := practiceCasterRig(t, class)
			s.Tick(context.Background())
			op := s.stateLocked().GetLastSpellOperation()
			if lastCast(f) != best || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
				op == nil || op.SpellId != best || op.TargetGuid != target.Guid || !op.TransportWritten || op.ServerCastGo || op.EffectObserved ||
				s.nextAssist != s.now().Add(1500*time.Millisecond) || s.nextHeal != s.nextAssist ||
				s.categoryReady[19] != s.now().Add(8*time.Second) || s.state.AssistDamageEvents != 0 {
				t.Fatal("Practice bypassed shared Lua choice or exact catalog transport/cooldowns", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPracticeCasterLuaReservesUnavailableLearnedHealAfterAuthorizedPull(t *testing.T) {
	s, f, target, _, advance := practiceCasterRig(t, 5)
	learnSpells(s, smite, lesserHeal)
	s.Tick(context.Background())
	if lastCast(f) != smite || countPackets(f, opcode.CMSGCastSpell) != 1 || !s.practicePullStarted {
		t.Fatal("fixture did not complete the sufficiently funded initial quest pull", s.stateLocked(), f.packets)
	}
	practiceCasterConfirmObservedCast(t, s, target.Guid)
	advance(s.nextAssist.Sub(s.now()))
	ranks := s.learnedContractsLocked()
	damageCost := spellCost(ranks[smite], f.world.Self)
	healReserve := spellCost(ranks[lesserHeal], f.world.Self)
	boundary := damageCost + healReserve
	if damageCost == 0 || healReserve == 0 || boundary > uint64(f.world.Self.MaxPower) {
		t.Fatal("fixture lacks a meaningful observed cost/reserve boundary", damageCost, healReserve)
	}
	if s.spellReady == nil {
		s.spellReady = map[uint32]time.Time{}
	}
	s.spellReady[lesserHeal] = s.now().Add(time.Minute)
	if s.encounterRankResourcesAvailable(ranks[lesserHeal], f.world.Self) {
		t.Fatal("fixture did not make the learned heal mechanically unavailable")
	}
	// Initial-pull readiness already succeeded with ample mana. These ticks
	// isolate the Lua future-heal reserve from the mission's initial 45% gate.
	f.world.Self.Power = uint32(boundary - 1)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 1 || !s.state.Practicing {
		t.Fatal("unavailable learned heal was omitted from the future reserve", s.stateLocked(), f.packets)
	}
	f.world.Self.Power = uint32(boundary)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 2 || lastCast(f) != smite || s.state.PracticePulls != 1 {
		t.Fatal("exact damage cost plus learned heal reserve did not admit the same ongoing fight", s.stateLocked(), f.packets)
	}
}

func TestPracticeCasterLuaAcceptedSharedEditChangesExactCastInSameBody(t *testing.T) {
	s, f, target, best, advance := practiceCasterRig(t, 8)
	s.Tick(context.Background())
	if lastCast(f) != best {
		t.Fatal("fixture did not use the learned shared choice", s.stateLocked())
	}
	practiceCasterConfirmObservedCast(t, s, target.Guid)
	source := strings.Replace(behaviors.StarterSpellSource, "M.race_profiles = {}",
		"M.race_profiles = {}\nM.tuning.family_weights={[2136]=0}", 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("test did not edit the actual shared selector tuning")
	}
	request := &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: source}
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), request)
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	request.Source = behaviors.StarterSpellSource
	if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), request); err == nil {
		t.Fatal("stale revision replaced the accepted same-body edit")
	}
	// Both choices are ready again; the source edit determines the new cast.
	advance(9 * time.Second)
	s.Tick(context.Background())
	if lastCast(f) != fireball || countPackets(f, opcode.CMSGCastSpell) != 2 || s.starterSpells.revision != accepted.Revision ||
		s.selfGUID != f.world.Self.Guid || !s.state.Practicing || s.state.PracticePulls != 1 {
		t.Fatal("accepted Lua edit did not change the exact cast in the same mission/body", s.stateLocked(), f.packets)
	}
}

func TestPracticeCasterLuaCallerCancellationPreservesAcceptedAuthoredSource(t *testing.T) {
	s, f, target, best, _ := practiceCasterRig(t, 8)
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
	if err != nil || accepted.Revision == "" {
		t.Fatal("body-bound shared source was not accepted", accepted, err)
	}
	point, known, _ := creaturePosition(target)
	if !known {
		t.Fatal("fixture lost the observed target pose")
	}
	authored := s.behavior
	first := s.starterSpellDecisionLocked(context.Background(), f.world, target, point)
	if !first.valid || first.kind != "cast" || first.spell.ID != best || s.starterSpells.revision != accepted.Revision {
		t.Fatal("accepted shared source did not produce the initial pure choice", first, s.starterSpells.failure())
	}
	before := authored.Status()
	if before.Revision != accepted.Revision || before.Failures != 0 || before.QuarantinedRevision != "" {
		t.Fatal("fixture did not start with a healthy accepted authored source", before)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	refused := s.starterSpellDecisionLocked(canceled, f.world, target, point)
	after := authored.Status()
	if refused.valid || after.Revision != before.Revision || after.Failures != before.Failures ||
		after.QuarantinedRevision != "" || s.starterSpells.failedAuthored != nil || len(f.packets) != 0 {
		t.Fatal("caller cancellation poisoned valid source or produced a native write", refused, before, after, s.starterSpells.failure(), f.packets)
	}
	fresh := s.starterSpellDecisionLocked(context.Background(), f.world, target, point)
	if !fresh.valid || fresh.kind != "cast" || fresh.spell.ID != best || s.behavior != authored ||
		s.starterSpells.revision != accepted.Revision || s.starterSpells.failedAuthored != nil || len(f.packets) != 0 {
		t.Fatal("fresh caller could not reuse the same accepted source without edit or reconnect", fresh, authored.Status(), s.starterSpells.failure(), f.packets)
	}
}

func TestPracticeCasterLuaCurrentPolicyNeverFallsBackToHostileWrite(t *testing.T) {
	for _, class := range []uint32{8, 5} {
		for _, mode := range []string{"broken", "transport", "missing export", "malformed", "wait", "melee"} {
			t.Run(fmt.Sprintf("%d/%s", class, mode), func(t *testing.T) {
				s, f, _, _, _ := practiceCasterRig(t, class)
				if mode == "broken" {
					if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
						SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: `return {decide=function(input)error("current caster failed")end}`}); err != nil {
						t.Fatal(err)
					}
				} else {
					s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
						if mode == "transport" {
							return false, "", nil, errors.New("current source disconnected")
						}
						if mode == "missing export" {
							return false, strings.Repeat("a", 64), nil, nil
						}
						out := map[string]any{"kind": mode, "spellId": 0}
						if mode == "malformed" {
							out = map[string]any{"kind": "cast", "spellId": 133.5}
						}
						return true, strings.Repeat("a", 64), out, nil
					}
				}
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.state.AssistAttempts != 0 {
					t.Fatal("unavailable/refusing current caster policy invoked fixed-family or melee fallback", s.stateLocked(), f.packets)
				}
			})
		}
	}
}

// Lifecycle and approach reads use the same bridge. Only the unnamed frame
// with an actual target and spell observations is a caster rank decision.
func practiceCasterSpellSelectionFrame(input map[string]any) bool {
	if selection, present := input["selection"]; present && selection != nil {
		value, ok := selection.(string)
		if !ok || value != "" {
			return false
		}
	}
	_, target := input["target"].(map[string]any)
	observation, observed := input["observation"].(map[string]any)
	_, spells := observation["spells"].(map[string]any)
	return target && observed && spells
}

func TestPracticeCasterLuaFinalPolicyReadRevalidatesCurrentFacts(t *testing.T) {
	for _, class := range []uint32{8, 5} {
		for _, mode := range []string{"mana", "body", "target", "quest", "cooldown", "policy"} {
			t.Run(fmt.Sprintf("%d/%s", class, mode), func(t *testing.T) {
				s, f, target, best, _ := practiceCasterRig(t, class)
				runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
				reads := 0
				s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
					out, err := runtime.EvaluateContext(ctx, input)
					if !practiceCasterSpellSelectionFrame(input) {
						return true, runtime.Status().Revision, out, err
					}
					reads++
					if reads == 2 {
						switch mode {
						case "mana":
							f.world.Self.Power = 0
						case "body":
							f.world.Self.PositionRevision++
						case "target":
							target.X++
						case "quest":
							if class == 5 {
								f.world.SelfQuestLog[0].QuestId = 0
							} else {
								f.world.Self.Level = 2
							}
						case "cooldown":
							if s.spellReady == nil {
								s.spellReady = map[uint32]time.Time{}
							}
							s.spellReady[best] = s.now().Add(time.Minute)
						case "policy":
							out = map[string]any{"kind": "wait", "spellId": 0}
						}
					}
					return true, runtime.Status().Revision, out, err
				}
				s.Tick(context.Background())
				if reads != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.AssistAttempts != 0 {
					t.Fatal("Practice cast used a choice from before its final policy/native recheck", reads, s.stateLocked(), f.packets)
				}
			})
		}
	}
}

func TestPracticeCasterLuaFinalWorldReturnCannotExtendAuthority(t *testing.T) {
	for _, class := range []uint32{8, 5} {
		for _, mode := range []string{"lease", "context"} {
			t.Run(fmt.Sprintf("%d/%s", class, mode), func(t *testing.T) {
				s, f, _, _, advance := practiceCasterRig(t, class)
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
				reads := 0
				s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
					if practiceCasterSpellSelectionFrame(input) {
						reads++
					}
					out, err := runtime.EvaluateContext(ctx, input)
					return true, runtime.Status().Revision, out, err
				}
				// Reuse the native callback seam: binding World read first,
				// final authority World read second, after the second Lua choice.
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
					t.Fatal("last World response extended revoked Practice authority", reads, late.finalWorldReads, s.stateLocked(), f.packets)
				}
			})
		}
	}
}

func TestPracticeCasterEarlyLifecycleCooldownAllowsOnlyCurrentLuaBaseline(t *testing.T) {
	for _, class := range []uint32{8, 5} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, target, best, _ := practiceCasterRig(t, class)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			lifecycleReads, spellReads := 0, 0
			injected := false
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				if practiceCasterSpellSelectionFrame(input) {
					spellReads++
				}
				if input["selection"] == practiceLifecycleSelection && input["stage"] == "tick" {
					lifecycleReads++
					if lifecycleReads == 2 {
						if spellReads != 0 {
							t.Fatal("fixture did not place cooldown before the first spell selection", spellReads)
						}
						if s.spellReady == nil {
							s.spellReady = map[uint32]time.Time{}
						}
						s.spellReady[best] = s.now().Add(time.Minute)
						injected = true
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			baseline := uint32(fireball)
			if class == 5 {
				baseline = smite
			}
			op := s.stateLocked().GetLastSpellOperation()
			if !injected || spellReads != 2 || lastCast(f) != baseline || lastCast(f) == best ||
				countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.state.AssistAttempts != 1 ||
				!s.now().Before(s.spellReady[best]) || op == nil || op.SpellId != baseline || op.TargetGuid != target.Guid ||
				!op.TransportWritten || op.ServerCastGo || op.EffectObserved {
				t.Fatal("earlier lifecycle cooldown bypassed current Lua choice or native cast admission", lifecycleReads, spellReads, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPracticeCasterLuaFinishesOnlyItsProvenCurrentQuestFight(t *testing.T) {
	for _, own := range []bool{true, false} {
		t.Run(fmt.Sprint(own), func(t *testing.T) {
			s, f, target, _, advance := practiceCasterRig(t, 5)
			s.Tick(context.Background())
			practiceCasterConfirmObservedCast(t, s, target.Guid)
			f.world.SelfQuestLog[0].Complete = true
			f.world.Self.UnitFlags |= 0x80000
			target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
			if !own {
				target.AttackVictimGuid++
			}
			advance(9 * time.Second)
			s.Tick(context.Background())
			want := 1
			if own {
				want = 2
			}
			if countPackets(f, opcode.CMSGCastSpell) != want || own && (!s.state.Practicing ||
				s.state.PracticePhase != "finishing_current_fight_to_goal") || !own && s.state.Practicing {
				t.Fatal("quest completion granted authority beyond the proven own fight", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPracticeCasterLuaUncertainCastSurvivesEditRearmAndLaterHealReceipt(t *testing.T) {
	for _, class := range []uint32{8, 5} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, target, best, advance := practiceCasterRig(t, class)
			s.session = starterWeaponAmbiguousSession{f}
			s.Tick(context.Background())
			uncertain := s.starterSpells.uncertainStrike
			if countPackets(f, opcode.CMSGCastSpell) != 1 || uncertain == nil || uncertain.view.SpellId != best ||
				uncertain.view.TargetGuid != target.Guid || uncertain.view.TransportWritten || uncertain.view.ServerCastGo || uncertain.view.EffectObserved {
				t.Fatal("ambiguous caster transport lost the exact native receipt", uncertain, s.stateLocked(), f.packets)
			}
			advance(6 * time.Second)
			s.Tick(context.Background())
			if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
				SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource + "\n-- accepted after ambiguous caster write"}); err != nil {
				t.Fatal(err)
			}
			for _, enabled := range []bool{false, true} {
				if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: enabled}); err != nil {
					t.Fatal("same own quest/XP mission could not rearm after timeout", err)
				}
			}
			heal, known := s.spellData.Lookup(lesserHeal)
			if !known {
				t.Fatal("missing actual catalog heal")
			}
			s.reserveHealReceiptLocked("", heal, s.selfGUID)
			for range 2 {
				s.Tick(context.Background())
				advance(time.Second)
			}
			if countPackets(f, opcode.CMSGCastSpell) != 1 || s.starterSpells.uncertainStrike != uncertain || s.supportOp == uncertain ||
				uncertain.view.Phase != "outcome_unconfirmed_no_retry" || uncertain.view.ServerCastGo || uncertain.view.EffectObserved ||
				s.state.AssistDamageEvents != 0 || s.state.ServerAssistGo != 0 {
				t.Fatal("Lua edit/rearmed mission/later receipt erased an uncertain caster write", uncertain, s.stateLocked(), f.packets)
			}
		})
	}
}
