package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func urgencyModule(t *testing.T, file, body string) *behavior.Session {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "behaviors", file))
	if err != nil {
		t.Fatal(err)
	}
	source := "local module=(function()\n" + string(data) + "\nend)()\nreturn {decide=function(frame) " + body + " end}"
	host, err := behavior.NewSession(context.Background(), "urgency-module", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	return host
}

func TestQueueEmergencyUsesKnownFactsBeforeGuidanceAndProtectsControl(t *testing.T) {
	for _, mode := range []string{"finish", "healing", "unknown_healing", "unknown_timing", "protected", "idle", "high_threat"} {
		t.Run(mode, func(t *testing.T) {
			host := urgencyModule(t, "encounter-tactics.lua", "return module.decide(frame.input)")
			unit := func(hp float64) map[string]any {
				return map[string]any{"present": true, "aliveKnown": true, "alive": true, "attackable": true, "engaged": true, "health": hp, "maxHealth": float64(100)}
			}
			marked, other := unit(90), unit(15)
			want, reason := "2", "finish"
			switch mode {
			case "healing":
				other["health"] = float64(95)
				other["healingCastKnown"] = true
				other["healingCastRemainingMs"] = float64(900)
				reason = "prevent_heal"
			case "unknown_healing":
				other["health"] = float64(95)
				other["healingCastRemainingMs"] = float64(900)
				want = "1"
			case "unknown_timing":
				delete(other, "maxHealth")
				other["health"] = float64(1)
				want = "1"
			case "protected":
				other["protected"] = true
				want = "1"
			case "idle":
				marked["engaged"], other["engaged"] = false, false
				want = ""
			case "high_threat":
				marked["incomingDPS"], other["incomingDPS"] = float64(1), float64(50)
				reason = "high_threat_finish"
			}
			out, err := host.Decide(context.Background(), map[string]any{"kind": "combat_queue", "targets": map[string]any{"1": marked, "2": other}, "explicitOrder": []any{"1"}, "ownerFocus": "1"})
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				if out.Output["focusGuid"] != nil || out.Output["burstIntent"] != nil {
					t.Fatal("idle queue pulled", out.Output)
				}
				return
			}
			if out.Output["focusGuid"] != want {
				t.Fatal("wrong shared queue focus", out.Output)
			}
			if want == "2" {
				burst, _ := out.Output["burstIntent"].(map[string]any)
				if burst["targetGuid"] != "2" || burst["reason"] != reason {
					t.Fatal("missing explicit burst reason", out.Output)
				}
				row := out.Output["queue"].([]any)[0].(map[string]any)
				if row["targetGuid"] != "2" || row["reason"] != reason || row["burstIntent"] == nil {
					t.Fatal("execution and queue diverged", out.Output)
				}
			} else if out.Output["burstIntent"] != nil {
				t.Fatal("invented emergency", out.Output)
			}
		})
	}
}

func TestQueueKeepsHumanGuidanceAfterTheEmergency(t *testing.T) {
	host := urgencyModule(t, "encounter-tactics.lua", "return module.decide(frame.input)")
	targets := map[string]any{}
	for id, health := range map[string]float64{"1": 90, "2": 10, "3": 30} {
		targets[id] = map[string]any{"present": true, "aliveKnown": true, "alive": true, "attackable": true, "engaged": true, "health": health, "maxHealth": float64(100)}
	}
	out, err := host.Decide(context.Background(), map[string]any{"kind": "combat_queue", "targets": targets, "ownerFocus": "1"})
	if err != nil {
		t.Fatal(err)
	}
	queue := out.Output["queue"].([]any)
	if queue[0].(map[string]any)["targetGuid"] != "2" || queue[1].(map[string]any)["targetGuid"] != "1" {
		t.Fatal("emergency discarded the remaining human guidance", out.Output)
	}
}

func TestUrgentQueueDrivesBoundPlanExecutionAfterLeaderDies(t *testing.T) {
	const intent = `local encounter_intent={plan={id="urgent",groupGuid="7",leaderGuid="3",killOrder={"skull","moon"},controls={{mark="moon",technique="polymorph",casterGuid="4",spellId=118}}},damage={scope="focus",spellId=585}}` + "\n"
	p := newFallbackPolicyTest(t, intent)
	obs := fallbackObservation()
	obs.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true), RaidTargets: &partyv1.RaidTargetState{GroupGuid: fallbackGroup, Complete: true, Revision: 1, Marks: []*partyv1.RaidTarget{{Icon: 8, TargetGuid: fallbackNPC}, {Icon: 5, TargetGuid: fallbackNPC + 2}}}}
	obs.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	addFallbackTarget(obs, fallbackNPC, fallbackLeader, 900)
	addFallbackTarget(obs, fallbackNPC+1, fallbackLeader, 80)
	addFallbackTarget(obs, fallbackNPC+2, fallbackLeader, 1)
	for _, dead := range []bool{false, true} {
		obs.Players[fallbackGUID(fallbackLeader)].Alive = proto.Bool(!dead)
		out := p.expectOffense(p.observe(obs), "engaged_focus", fallbackNPC+1, 585).Output
		burst, _ := out["burstIntent"].(map[string]any)
		if burst["targetGuid"] != fallbackGUID(fallbackNPC+1) || burst["reason"] != "finish" {
			t.Fatal(out)
		}
		for _, v := range out["queue"].([]any) {
			row := v.(map[string]any)
			if row["targetGuid"] == fallbackGUID(fallbackNPC+2) && (row["protected"] != true || row["burstIntent"] != nil) {
				t.Fatal("urgent finish broke Moon reservation", out)
			}
		}
	}
}

func TestSpellSelectionUsesLethalActionBeforeComboFinisherAndHonorsBurst(t *testing.T) {
	host := urgencyModule(t, "encounter-spells.lua", "return {spellId=module.damage_spell(frame.input.observation,frame.input.target,nil,frame.input.options)}")
	spell := func(family, damage, cooldown float64) map[string]any {
		return map[string]any{"known": true, "ready": true, "capability": "offense", "metadataKnown": true, "familyId": family, "directDamage": damage, "cost": float64(0), "globalCooldownMs": float64(1000), "cooldownMs": cooldown}
	}
	self := map[string]any{"comboKnown": true, "comboPoints": float64(5), "comboTargetGuid": "8"}
	target := map[string]any{"guid": "8", "healthKnown": true, "health": float64(30), "maxHealth": float64(1000)}
	finisher := spell(2, 20, 0)
	finisher["requiresComboPoints"] = true
	obs := map[string]any{"selfGuid": "4", "players": map[string]any{"4": self}, "spells": map[string]any{"1": spell(1, 40, 0), "2": finisher}}
	choose := func(want float64, options map[string]any) {
		t.Helper()
		out, err := host.Decide(context.Background(), map[string]any{"observation": obs, "target": target, "options": options})
		if err != nil || out.Output["spellId"] != want {
			t.Fatal(want, out.Output, err)
		}
	}
	choose(1, map[string]any{})
	target["health"] = float64(80)
	obs["spells"] = map[string]any{"1": spell(1, 100, 0), "2": spell(2, 150, 30000)}
	choose(1, map[string]any{})
	choose(2, map[string]any{"burst": true})
}

func TestEncounterPropagatesBurstToAvailableAndReadySpellChoices(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := fallbackObservation()
	// Use the legacy observation shape to isolate routing from range/LoS motion.
	obs.WorldObservationRevision = 0
	obs.Spells = map[string]*actionsv1.EncounterSpell{
		"1": {Known: true, Ready: true, Available: true, Capability: "offense", MetadataKnown: true, FamilyId: 1, DirectDamage: 100, GlobalCooldownMs: 1000},
		"2": {Known: true, Ready: true, Available: true, Capability: "offense", MetadataKnown: true, FamilyId: 2, DirectDamage: 150, GlobalCooldownMs: 1000, CooldownMs: 30000},
	}
	addFallbackTarget(obs, fallbackNPC, fallbackSelf, 80)
	out := p.expectOffense(p.observe(obs), "engaged_defense", fallbackNPC, 2).Output
	if out["burstIntent"] == nil {
		t.Fatal("burst was not routed", out)
	}
}
