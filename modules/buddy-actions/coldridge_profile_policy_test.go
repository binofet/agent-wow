package buddyactions

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

var coldProfileFixture struct {
	once    sync.Once
	mu      sync.Mutex
	runtime *behavior.Runtime
}

// Former direct Go profile assertions now execute the same maintained Lua
// helper as the live lifecycle. This wrapper adds no profile or receipt facts.
func coldProfileDecision(c *worldv1.CreatureState, self *worldv1.PlayerState, quest, entry uint32) map[string]any {
	coldProfileFixture.once.Do(func() {
		end := strings.LastIndex(behaviors.StarterSpellSource, "\nreturn M")
		if end < 0 {
			panic("shared starter source return missing")
		}
		source := behaviors.StarterSpellSource[:end] + `
return {decide=function(i) return {
            profile=encounter_starter_objectives.coldridge_profile(i.creature,i.questId),
            eligible=encounter_starter_objectives.coldridge_eligible(i.creature,i.self,i.questId),
            supported=encounter_starter_objectives.coldridge_supports_entry(i.questId,i.entry)} end}`
		var err error
		coldProfileFixture.runtime, err = behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
		if err != nil {
			panic(err)
		}
		if _, err = coldProfileFixture.runtime.Reload(source, ""); err != nil {
			panic(err)
		}
	})
	creature := practiceLifecycleCreatureFacts(c)
	if creature != nil {
		creature["physicalAvailable"] = coldridgeCreatureAvailable(c, self)
	}
	input := map[string]any{"creature": creature, "self": practiceLifecyclePlayerFacts(self), "questId": quest, "entry": entry}
	coldProfileFixture.mu.Lock()
	defer coldProfileFixture.mu.Unlock()
	out, err := coldProfileFixture.runtime.EvaluateContext(context.Background(), input)
	if err != nil {
		panic(err)
	}
	return out
}

func (g coldridgePracticeGoal) eligible(c *worldv1.CreatureState, self *worldv1.PlayerState) bool {
	return coldProfileDecision(c, self, g.questID, 0)["eligible"] == true
}
func (g coldridgePracticeGoal) supportsEntry(entry uint32) bool {
	return coldProfileDecision(nil, nil, g.questID, entry)["supported"] == true
}
func matches182Profile(c *worldv1.CreatureState) bool {
	return coldProfileDecision(c, nil, 182, 0)["profile"] == true
}

func TestColdridgeProfileActualLuaCASEditChangesSameBodyTargetAndCast(t *testing.T) {
	s, f, _ := coldThreatRig(t, 707, 0, 0)
	target := f.world.NearbyCreatures[0]
	// A newly observed tuple is factual input, not a changed compiled profile.
	target.Entry, target.Guid = 709, coldThreatGUID(709)
	target.MaxHealth = 56
	s.Tick(context.Background())
	if s.practiceTarget != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || !s.state.Practicing {
		t.Fatal("default Lua profile did not reject the unconfigured tuple", s.stateLocked(), f.packets)
	}
	ctx := context.Background()
	initial, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource})
	if err != nil {
		t.Fatal(initial, err)
	}
	const profile = "{entry=707,level=2,maxHealth=55,faction=36}"
	if strings.Count(behaviors.StarterSpellSource, profile) != 1 {
		t.Fatal("shared creature profile no longer has one tuning site")
	}
	edited := strings.Replace(behaviors.StarterSpellSource, profile, "{entry=709,level=2,maxHealth=56,faction=36}", 1)
	edited = strings.Replace(edited, "{entry=707,count=6}", "{entry=709,count=6}", 1)
	sid, guid, runtime := s.sessionID, s.selfGUID, s.behavior
	request := &actionsv1.BehaviorReloadRequest{SessionId: sid, SelfGuid: guid, ExpectedRevision: initial.Revision, Source: edited}
	accepted, err := reloadStarterSemanticBehavior(t, s, ctx, request)
	if err != nil || accepted.Revision == initial.Revision || s.behavior != runtime {
		t.Fatal("profile CAS did not retain the actual body/runtime", accepted, err)
	}
	request.Source = behaviors.StarterSpellSource
	if _, err := reloadStarterSemanticBehavior(t, s, ctx, request); err == nil {
		t.Fatal("stale profile revision replaced the accepted edit")
	}
	s.Tick(ctx)
	s.Tick(ctx)
	if s.sessionID != sid || s.selfGUID != guid || target.MaxHealth != 56 || lastCast(f) != shadowBolt ||
		countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.PracticePulls != 1 || s.state.PracticeKills != 0 ||
		s.state.ServerAssistGo != 0 || s.state.ShadowBoltDamageEvents != 0 || coldThreatQuest(f).ObjectiveCounts[0] != 0 {
		t.Fatal("same-body profile edit was shadowed or transport invented game progress", s.stateLocked(), f.packets)
	}
}

func TestColdridgeProfileLateChangeBeforeCastUsesCurrentLuaProfile(t *testing.T) {
	for _, field := range []string{"maxHealth", "faction", "level"} {
		t.Run(field, func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 707, 0, 0)
			target := f.world.NearbyCreatures[0]
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				if input["selection"] == "coldridge_cast" {
					reads++
					if reads == 2 {
						switch field {
						case "maxHealth":
							target.MaxHealth++
						case "faction":
							target.FactionTemplate++
						case "level":
							target.Level++
						}
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if reads != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.AssistAttempts != 0 ||
				s.practicePullStarted || s.state.PracticePulls != 0 {
				t.Fatal("later actual profile change passed the final Lua/native boundary", reads, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestColdridgeProfilePermissiveLuaCannotBypassPhysicalTargetChecks(t *testing.T) {
	for _, field := range []string{"guid_type", "guid_low", "guid_entry", "health", "flags", "npc"} {
		t.Run(field, func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 707, 0, 0)
			target := f.world.NearbyCreatures[0]
			switch field {
			case "guid_type":
				target.Guid = uint64(0xf150)<<48 | uint64(target.Entry)<<24 | 1
			case "guid_low":
				target.Guid = uint64(0xf130)<<48 | uint64(target.Entry)<<24
			case "guid_entry":
				target.Guid = coldThreatGUID(724)
			case "health":
				target.Health = target.MaxHealth + 1
			case "flags":
				target.UnitFlags |= 2
			case "npc":
				target.NpcFlags = 1
			}
			old := s.starterSpells.runtime.Status().Revision
			edited := strings.Replace(behaviors.StarterSpellSource, "c.physicalAvailable==true", "true", 1)
			if edited == behaviors.StarterSpellSource {
				t.Fatal("actual profile availability tuning anchor missing")
			}
			if _, err := s.starterSpells.runtime.Reload(edited, old); err != nil {
				t.Fatal(err)
			}
			selected, _, owned := s.coldridgeLifecycleLocked(context.Background(), f.world, "targets")
			if !owned || !selected.Valid || selected.Kind != "select" || selected.TargetGUID != fmt.Sprint(target.Guid) {
				t.Fatal("fixture did not exercise a real permissive Lua selection", selected, owned)
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.PracticePulls != 0 || s.practicePullStarted {
				t.Fatal("authored profile bypassed generic native physical checks", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestAutomaticCorpseApproachDoesNotReenterColdridgeGoalOrProfilePolicy(t *testing.T) {
	for _, entry := range []uint32{705, 707, 724, 708, 706} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			s, f, _, request := navLootRig(t)
			// The exact target is a Lua caller's choice; the ordinary native
			// constructor does not select a quest/profile.
			f.world.SelfQuestLogKnown, f.world.SelfQuestLog = false, nil
			corpse := f.world.LootableCorpses[0]
			corpse.Entry, corpse.Guid = entry, coldThreatGUID(entry)
			corpse.LevelKnown, corpse.Level, corpse.MaxHealth, corpse.FactionTemplate = true, 99, 1234, 1234
			request.CorpseGuid = corpse.Guid
			plans := 0
			s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
				plans++
				return []position{a, b}, nil
			}
			s.planColdridgePath = func(context.Context, string, position, position) ([]position, error) {
				t.Fatal("retired named corpse exception was called")
				return nil, nil
			}
			out, err := s.ApproachLoot(context.Background(), request)
			if err != nil || !out.Moving || plans != 1 || countPackets(f, opcode.MSGMoveStartForward) != 1 ||
				countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("maintained exact-target approach acquired a compiled scenario dependency", out, err, f.packets)
			}
		})
	}
}
