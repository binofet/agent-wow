package buddyactions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const coldridgeLearnedShadowBolt = uint32(695)

func coldridgeCasterRig(t *testing.T) (*Service, *fakeSession, *worldv1.CreatureState, func(time.Duration)) {
	t.Helper()
	// Quest182 already admits an observed level6 warlock. Rank2 is learned at
	// level6; the fixture preserves the actual182 profile and separate client
	// estimate instead of pretending a new low-level spell mechanic exists.
	s, f, advance := cold182Rig(t, 2, 6, 3)
	learnSpells(s, shadowBolt, coldridgeLearnedShadowBolt)
	return s, f, f.world.NearbyCreatures[0], advance
}

func TestColdridgeCasterLuaChoosesActualLearnedRankBeyondFixed686(t *testing.T) {
	s, f, target, _ := coldridgeCasterRig(t)
	rank := s.learnedContractsLocked()[coldridgeLearnedShadowBolt]
	if rank.Family != shadowBolt || rank.Rank != 2 || rank.Level != 6 || directDamage(rank) != 26 ||
		rank.CostPercent != 12 || spellCost(rank, f.world.Self) != 13 || rank.CastMs != 2200 || rank.GcdMs != 1500 || rank.Facing != 1 ||
		rank.MinHostile != 0 || rank.MaxHostile != 30 || rank.CooldownMs != 0 || rank.CategoryCooldownMs != 0 {
		t.Fatal("actual learned catalog alternative changed", rank)
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 0 || s.practicePullStarted || s.state.PracticePulls != 0 || !s.state.NavAvailable {
		t.Fatal("verified route alone reserved or wrote a pull", s.stateLocked(), f.packets)
	}
	s.Tick(context.Background())
	op := s.stateLocked().GetLastSpellOperation()
	if lastCast(f) != coldridgeLearnedShadowBolt || countPackets(f, opcode.CMSGCastSpell) != 1 ||
		countPackets(f, opcode.CMSGAttackswing) != 0 || !s.practicePullStarted || s.state.PracticePulls != 1 ||
		op == nil || op.SpellId != coldridgeLearnedShadowBolt || op.TargetGuid != target.Guid || !op.TransportWritten ||
		op.ServerCastGo || op.EffectObserved || s.nextAssist != s.now().Add(1500*time.Millisecond) || s.nextHeal != s.nextAssist ||
		s.state.ServerAssistGo != 0 || s.state.ShadowBoltDamageEvents != 0 || s.state.PracticeKills != 0 ||
		cold182GoalQuest(f.world).ObjectiveCounts[0] != 2 {
		t.Fatal("real shared rank choice was replaced by fixed686 or invented progress", s.stateLocked(), f.packets)
	}
	practiceCasterConfirmObservedCast(t, s, target.Guid)
	if s.supportOp.view.SpellId != coldridgeLearnedShadowBolt || s.state.ServerAssistGo != 1 ||
		s.state.ShadowBoltDamageEvents != 1 || s.state.PracticeKills != 0 || cold182GoalQuest(f.world).ObjectiveCounts[0] != 2 ||
		countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("matching rank695 GO/damage did not settle only its actual cast effect", s.stateLocked(), f.packets)
	}
}

func TestColdridgeCasterLuaActualSharedRankEditChangesSameBodyCast(t *testing.T) {
	s, f, target, advance := coldridgeCasterRig(t)
	target.Health = 14 // Both ranks are directly lethal; rank1's cast is faster.
	accepted, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	point, known, _ := creaturePosition(target)
	if !known {
		t.Fatal("missing observed target pose")
	}
	first := s.starterSpellDecisionLocked(context.Background(), f.world, target, point)
	if !first.valid || first.kind != "cast" || first.spell.ID != shadowBolt || len(f.packets) != 0 {
		t.Fatal("real all-ranks policy did not choose the faster lethal rank", first, s.starterSpells.failure())
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	if lastCast(f) != shadowBolt || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("initial real shared choice did not produce rank686 in this body", s.stateLocked(), f.packets)
	}
	practiceCasterConfirmObservedCast(t, s, target.Guid)
	advance(s.nextAssist.Sub(s.now()))
	if strings.Count(behaviors.StarterSpellSource, "all_ranks=true") != 1 {
		t.Fatal("shared starter rank profile is no longer uniquely editable")
	}
	authored := s.behavior
	edited := strings.Replace(behaviors.StarterSpellSource, "all_ranks=true", "all_ranks=false", 1)
	request := &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID,
		ExpectedRevision: accepted.Revision, Source: edited}
	changed, err := reloadStarterSemanticBehavior(t, s, context.Background(), request)
	if err != nil || changed.Revision == accepted.Revision || s.behavior != authored {
		t.Fatal("same-body actual source CAS did not retain its runtime", changed, err)
	}
	request.Source = behaviors.StarterSpellSource
	if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), request); err == nil {
		t.Fatal("stale revision replaced the accepted shared rank edit")
	}
	second := s.starterSpellDecisionLocked(context.Background(), f.world, target, point)
	if !second.valid || second.kind != "cast" || second.spell.ID != coldridgeLearnedShadowBolt || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("actual shared rank pruning did not change the same observed-frame choice", second, s.starterSpells.failure())
	}
	s.Tick(context.Background())
	if lastCast(f) != coldridgeLearnedShadowBolt || countPackets(f, opcode.CMSGCastSpell) != 2 ||
		s.behavior != authored || s.starterSpells.revision != changed.Revision || s.selfGUID != f.world.Self.Guid || s.state.PracticePulls != 1 {
		t.Fatal("accepted source edit did not affect the actual same-body cast", s.stateLocked(), f.packets)
	}
}

func TestColdridgeCasterLuaRefusalNeverFallsBackOrReservesFirstPull(t *testing.T) {
	for _, mode := range []string{"broken", "transport", "missing export", "malformed", "wait", "melee"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, _ := coldridgeCasterRig(t)
			learnSpells(s, autoAttack, shadowBolt, coldridgeLearnedShadowBolt)
			if mode == "broken" {
				if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
					SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: `return {decide=function(input)error("current Coldridge source failed")end}`}); err != nil {
					t.Fatal(err)
				}
			} else {
				s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
					if mode == "transport" {
						return false, "", nil, errors.New("active source disconnected")
					}
					if mode == "missing export" {
						return false, strings.Repeat("a", 64), nil, nil
					}
					out := map[string]any{"kind": mode, "spellId": 0}
					if mode == "malformed" {
						out = map[string]any{"kind": "cast", "spellId": 695.5}
					}
					return true, strings.Repeat("a", 64), out, nil
				}
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
				s.practicePullStarted || s.state.PracticePulls != 0 || s.state.AssistAttempts != 0 || s.starterSpells.uncertainStrike != nil {
				t.Fatal("refused current source fell back or granted first-pull authority", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestColdridgeCasterLuaFinalPolicyReadRechecksFactsAndGoalAuthority(t *testing.T) {
	for _, mode := range []string{"mana", "body", "claim", "objective", "cooldown", "baseline", "policy"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, _ := coldridgeCasterRig(t)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == "coldridge_cast" {
					reads++
				}
				out, err := runtime.EvaluateContext(ctx, input)
				if input["selection"] == "coldridge_cast" && reads == 2 {
					switch mode {
					case "mana":
						f.world.Self.Power = 0
					case "body":
						f.world.Self.PositionRevision++
					case "claim":
						target.AttackVictimKnown, target.AttackVictimGuid = true, 3
					case "objective":
						cold182GoalQuest(f.world).ObjectiveCounts[0] = 10
					case "cooldown":
						if s.spellReady == nil {
							s.spellReady = map[uint32]time.Time{}
						}
						s.spellReady[coldridgeLearnedShadowBolt] = s.now().Add(time.Minute)
					case "baseline":
						s.state.KnownShadowBolt = false
					case "policy":
						out = map[string]any{"kind": "wait", "spellId": 0}
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if reads != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.AssistAttempts != 0 ||
				s.practicePullStarted || s.state.PracticePulls != 0 {
				t.Fatal("changed final facts/goal authorized a cast or first-pull reservation", reads, s.stateLocked(), f.packets)
			}
		})
	}
}

type coldridgeCasterSendSession struct {
	*fakeSession
	after func(*modv1.SendPacketRequest)
}

func (f *coldridgeCasterSendSession) SendPacket(ctx context.Context, request *modv1.SendPacketRequest, options ...grpc.CallOption) (*emptypb.Empty, error) {
	response, err := f.fakeSession.SendPacket(ctx, request, options...)
	if err == nil && f.after != nil {
		f.after(request)
	}
	return response, err
}

func coldridgeCasterReplaceBinding(s *Service) time.Time {
	// Tick owns the mutex. This fixture injects a replacement native intent
	// binding; it does not claim public Practice rearm is allowed with a cast
	// pending. Public rearm is separately exercised after terminal uncertainty.
	s.stopPracticeLocked(context.Background(), "fixture_replacement_stop")
	s.state.Practicing = true
	s.practiceUntil = s.now().Add(20 * time.Minute)
	s.state.PracticePhase = "replacement_waiting_for_new_pull"
	return s.practiceUntil
}

func TestColdridgeCasterLuaCallbacksCannotReplaceCurrentBinding(t *testing.T) {
	for _, mode := range []string{"lease", "context", "session", "goal", "target"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, advance := coldridgeCasterRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				if input["selection"] == "coldridge_cast" {
					reads++
				}
				out, err := runtime.EvaluateContext(ctx, input)
				return true, runtime.Status().Revision, out, err
			}
			late := &starterWeaponFinalWorldSession{fakeSession: f, weaponReads: &reads, afterFinalWorld: func() {
				switch mode {
				case "lease":
					advance(11 * time.Minute)
				case "context":
					cancel()
				case "session":
					s.sessionID = "replaced-Coldridge-body"
				case "goal":
					s.coldridgeGoal.questID = 179
				case "target":
					s.practiceTarget = target.Guid + 1
				}
			}}
			s.session = late
			s.Tick(ctx)
			s.Tick(ctx)
			if reads != 2 || late.finalWorldReads != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 ||
				s.state.AssistAttempts != 0 || s.practicePullStarted || s.state.PracticePulls != 0 {
				t.Fatal("final World callback extended or replaced the bound Coldridge mission", reads, late.finalWorldReads, s.stateLocked(), f.packets)
			}
		})
	}
	t.Run("stop_replacement_has_no_later_facing_or_cast", func(t *testing.T) {
		s, f, _, _ := coldridgeCasterRig(t)
		s.Tick(context.Background())
		triggered := false
		var replacementUntil time.Time
		callback := &coldridgeCasterSendSession{fakeSession: f}
		callback.after = func(packet *modv1.SendPacketRequest) {
			if packet.Opcode == opcode.MSGMoveStop {
				triggered = true
				callback.after = nil // Clear before stopPracticeLocked can owe STOP.
				replacementUntil = coldridgeCasterReplaceBinding(s)
			}
		}
		s.session = callback
		s.state.Moving = true
		s.Tick(context.Background())
		if !triggered || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveSetFacing) != 0 ||
			countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.PracticePulls != 0 || s.practicePullStarted ||
			!s.state.Practicing || s.practiceUntil != replacementUntil || s.practiceTarget != 0 ||
			s.state.PracticeTargetGuid != 0 || s.state.PracticePhase != "replacement_waiting_for_new_pull" {
			t.Fatal("old STOP frame wrote later offense or replaced the new task intent", s.stateLocked(), f.packets)
		}
	})
	t.Run("cast_replacement_keeps_receipt_and_new_phase", func(t *testing.T) {
		s, f, target, advance := coldridgeCasterRig(t)
		s.Tick(context.Background())
		s.Tick(context.Background())
		practiceCasterConfirmObservedCast(t, s, target.Guid)
		advance(s.nextAssist.Sub(s.now()))
		cold182GoalQuest(f.world).ObjectiveCounts[0] = 10
		f.world.Self.UnitFlags |= 0x80000
		target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
		triggered := false
		var replacementUntil time.Time
		var reserved *supportAttempt
		callback := &coldridgeCasterSendSession{fakeSession: f}
		callback.after = func(packet *modv1.SendPacketRequest) {
			if packet.Opcode == opcode.CMSGCastSpell {
				triggered = true
				callback.after = nil
				reserved = s.supportOp
				replacementUntil = coldridgeCasterReplaceBinding(s)
			}
		}
		s.session = callback
		s.Tick(context.Background())
		if !triggered || countPackets(f, opcode.CMSGCastSpell) != 2 || !s.state.Practicing || s.practiceUntil != replacementUntil ||
			s.practiceTarget != 0 || s.state.PracticeTargetGuid != 0 || s.practicePullStarted || s.state.PracticePulls != 1 ||
			s.state.PracticePhase != "replacement_waiting_for_new_pull" || reserved == nil || s.supportOp != reserved ||
			reserved.view.SpellId != coldridgeLearnedShadowBolt || reserved.view.TargetGuid != target.Guid || !reserved.view.TransportWritten ||
			reserved.view.ServerCastGo || reserved.view.EffectObserved || !s.state.CastPending || s.pendingTarget != target.Guid {
			t.Fatal("old finishing frame resurrected a pull, overwrote the new phase or lost its admitted receipt", s.stateLocked(), f.packets)
		}
	})
}

func TestColdridgeCasterLuaCompletedObjectiveFinishesOnlyProvenOwnFight(t *testing.T) {
	for _, proof := range []string{"own", "foreign", "none"} {
		t.Run(proof, func(t *testing.T) {
			s, f, target, advance := coldridgeCasterRig(t)
			s.Tick(context.Background())
			s.Tick(context.Background())
			practiceCasterConfirmObservedCast(t, s, target.Guid)
			cold182GoalQuest(f.world).ObjectiveCounts[0] = 10
			f.world.Self.UnitFlags |= 0x80000
			if proof != "none" {
				target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
				if proof == "foreign" {
					target.AttackVictimGuid = 3
				}
			}
			advance(s.nextAssist.Sub(s.now()))
			s.Tick(context.Background())
			want := 1
			if proof == "own" {
				want = 2
			}
			if countPackets(f, opcode.CMSGCastSpell) != want || s.state.PracticePulls != 1 || s.state.PracticeKills != 0 ||
				cold182GoalQuest(f.world).ObjectiveCounts[0] != 10 || proof == "own" &&
				(!s.state.Practicing || s.state.PracticePhase != "finishing_current_fight_to_goal") || proof != "own" && s.state.Practicing {
				t.Fatal("objective completion extended authority beyond its proven own fight", proof, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestColdridgeCasterLuaUncertainCastSurvivesSourceEditAndRearm(t *testing.T) {
	s, f, target, advance := coldridgeCasterRig(t)
	s.session = starterWeaponAmbiguousSession{f}
	s.Tick(context.Background())
	s.Tick(context.Background())
	uncertain := s.starterSpells.uncertainStrike
	if countPackets(f, opcode.CMSGCastSpell) != 1 || uncertain == nil || uncertain.owner != target.Guid ||
		uncertain.view.SpellId != coldridgeLearnedShadowBolt || uncertain.view.TransportWritten || uncertain.view.ServerCastGo || uncertain.view.EffectObserved {
		t.Fatal("ambiguous learned-rank transport lost its actual native receipt", uncertain, s.stateLocked(), f.packets)
	}
	advance(6 * time.Second)
	s.Tick(context.Background())
	if _, err := reloadStarterSemanticBehavior(t, s, context.Background(), &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource + "\n-- accepted after uncertain Coldridge cast"}); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: enabled}); err != nil {
			t.Fatal("current own182 goal could not rearm", err)
		}
	}
	if s.failedAssistTarget != 0 {
		t.Fatal("fixture did not reach the existing goal's failed-target reset")
	}
	heal, known := s.spellData.Lookup(lesserHeal)
	if !known {
		t.Fatal("missing actual catalog receipt fixture")
	}
	// Exercise replacement of the general bookkeeping slot without sending or
	// claiming that this warlock learned or performed a healing game action.
	s.reserveHealReceiptLocked("", heal, s.selfGUID)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 1 || s.starterSpells.uncertainStrike != uncertain || s.supportOp == uncertain ||
		uncertain.view.Phase != "outcome_unconfirmed_no_retry" || uncertain.view.ServerCastGo || uncertain.view.EffectObserved ||
		s.state.ServerAssistGo != 0 || s.state.ShadowBoltDamageEvents != 0 || s.state.PracticePulls != 1 || s.practicePullStarted {
		t.Fatal("source edit/rearmed goal/general receipt replacement replayed an uncertain cast", uncertain, s.stateLocked(), f.packets)
	}
}

func TestColdridgeCasterLuaDoesNotInventUnsupportedLowLevelWarlockMechanics(t *testing.T) {
	s, f, _ := cold182Rig(t, 2, 4, 3)
	learnSpells(s, shadowBolt, 172, 348)
	target := f.world.NearbyCreatures[0]
	point, known, _ := creaturePosition(target)
	if !known {
		t.Fatal("missing actual target pose")
	}
	input, ranks, known := s.starterSpellFactsLocked(f.world, target, point)
	if !known || ranks[172].ID != 0 || ranks[348].ID != 0 {
		t.Fatal("unsupported Corruption/Immolate became exact native cast capabilities", ranks)
	}
	spells := input["observation"].(map[string]any)["spells"].(map[string]any)
	if spells["172"] != nil || spells["348"] != nil || ranks[shadowBolt].ID != shadowBolt {
		t.Fatal("reflection invented low-level spell mechanics instead of preserving known baseline", spells)
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	if lastCast(f) != shadowBolt || countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.PracticePulls != 1 {
		t.Fatal("unsupported learned rows displaced the actual supported baseline", s.stateLocked(), f.packets)
	}
}
