package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"os"
	"testing"
)

func warriorPolicy(t *testing.T, intent string) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse) {
	t.Helper()
	raw, err := os.ReadFile("../../behaviors/classes/warrior.lua")
	if err != nil {
		t.Fatal(err)
	}
	p := newFallbackPolicyTest(t, "local encounter_warrior=(function()\n"+string(raw)+"\nend)()\nlocal encounter_intent="+intent+"\n")
	obs := fallbackObservation()
	self := obs.Players["4"]
	self.ClassKnown, self.ClassId, self.PositionKnown, self.PowerKnown, self.PowerType, self.Power, self.MaxPower = true, 1, true, true, 1, 1000, 1000
	self.HealthKnown, self.Health, self.MaxHealth = true, 100, 100
	obs.MeleeKnown = true
	obs.ActiveMeleeTargetGuid = fallbackNPC
	obs.Spells = map[string]*actionsv1.EncounterSpell{}
	addFallbackTarget(obs, fallbackNPC, fallbackSelf, 1000)
	target := obs.Targets[fallbackGUID(fallbackNPC)]
	target.X = 3
	target.MeleeReady = true
	return p, obs
}
func warriorRank(family, rank uint32, cost uint64) *actionsv1.EncounterSpell {
	return &actionsv1.EncounterSpell{Known: true, MetadataKnown: true, FamilyId: family, Rank: rank, Ready: true, Available: true, Capability: "generic", PowerType: 1, Cost: cost, MaxRange: 5, GlobalCooldownMs: 1500}
}
func warriorShield(t *testing.T, p *fallbackPolicyTest) {
	t.Helper()
	_, err := p.host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "equipmentKnown": true,
		"equipment": []any{map[string]any{"slot": 16, "slotKnown": true, "metadataKnown": true, "entry": 100}}, "gearContext": map[string]any{"templates": map[string]any{"100": map[string]any{"available": true, "itemClass": 4, "subclass": 6}}}}})
	if err != nil {
		t.Fatal(err)
	}
}
func TestWarriorProtectionUsesKnownRankRageAndExistingJournalSoloOrParty(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "solo", true: "Arlen-led"}[group], func(t *testing.T) {
			p, obs := warriorPolicy(t, `{spec="protection",role="tank"}`)
			warriorShield(t, p)
			if group {
				obs.Party = &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 7, LeaderGuid: 1, Members: []*partyv1.Member{{Guid: 1, Online: true}, {Guid: 4, Self: true, Online: true}}}}
			}
			obs.Spells["23922"] = warriorRank(23922, 1, 200)
			obs.Spells["25258"] = warriorRank(23922, 4, 200)
			d := p.decide(p.observe(obs))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "cast_spell" || a["spellId"] != float64(25258) || a["targets"].(map[string]any)["objectGuid"] != fallbackGUID(fallbackNPC) {
				t.Fatal(d.Output)
			}
			if _, err := p.host.ReserveAction(d); err != nil {
				t.Fatal(err)
			}
			before := p.host.Status().Revision
			if _, err := p.host.Reload(context.Background(), "local encounter_warrior=(function()\n"+func() string { b, _ := os.ReadFile("../../behaviors/classes/warrior.lua"); return string(b) }()+"\nend)()\nlocal encounter_intent={spec='protection',role='tank'}\n"+readEncounterBundle(t), before); err != nil {
				t.Fatal(err)
			}
			out := p.decide(p.observe(obs)).Output
			if out["action"] != nil || p.host.Status().Pending != 1 {
				t.Fatal("pending/UNKNOWN cast replaced", out, p.host.Status())
			}
		})
	}
}
func TestWarriorSafetyRageShieldAndProtectedTarget(t *testing.T) {
	for _, mode := range []string{"rage", "shield", "protected", "unlearned", "not-ready", "other-class", "overflow", "outside-range", "not-facing"} {
		t.Run(mode, func(t *testing.T) {
			p, obs := warriorPolicy(t, `{spec="protection",role="tank"}`)
			if mode != "shield" {
				warriorShield(t, p)
			}
			obs.Spells["23922"] = warriorRank(23922, 1, 200)
			switch mode {
			case "rage":
				obs.Players["4"].Power = 199
			case "protected":
				obs.Targets[fallbackGUID(fallbackNPC)].Protected = true
			case "unlearned":
				obs.Spells["23922"].Known = false
			case "not-ready":
				obs.Spells["23922"].Ready = false
			case "other-class":
				obs.Players["4"].ClassId = 5
			case "overflow":
				obs.TargetProtectionOverflow = true
			case "outside-range":
				obs.Targets[fallbackGUID(fallbackNPC)].X = 20
			case "not-facing":
				obs.Players["4"].Orientation = 3.14159265
			}
			out := p.decide(p.observe(obs)).Output
			a, _ := out["action"].(map[string]any)
			if a["capability"] == "cast_spell" {
				t.Fatal(mode, out)
			}
		})
	}
}
func TestWarriorTauntRequiresExplicitTankRoleAndObservedTeammateVictim(t *testing.T) {
	for _, role := range []string{"tank", "damage"} {
		t.Run(role, func(t *testing.T) {
			p, obs := warriorPolicy(t, `{role="`+role+`"}`)
			obs.Spells["355"] = warriorRank(355, 1, 0)
			obs.Spells["355"].MaxRange = 30
			obs.Party = &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 7, LeaderGuid: 1, Members: []*partyv1.Member{{Guid: 1, Online: true}, {Guid: 4, Self: true, Online: true}}}}
			obs.Players["1"] = fallbackPlayer(1)
			obs.Engagement[fallbackGUID(fallbackNPC)].VictimGuid = 1
			out := p.decide(p.observe(obs)).Output
			a, _ := out["action"].(map[string]any)
			if role == "tank" && a["spellId"] != float64(355) || role != "tank" && a["spellId"] == float64(355) {
				t.Fatal(out)
			}
		})
	}
}
func TestWarriorArmsAndFuryUseLearnedSpecializationWithoutAoE(t *testing.T) {
	for _, family := range []uint32{12294, 23881} {
		p, obs := warriorPolicy(t, `{}`)
		obs.Spells["40000"] = warriorRank(family, 1, 300)
		out := p.decide(p.observe(obs)).Output
		a, _ := out["action"].(map[string]any)
		if a["spellId"] != float64(40000) {
			t.Fatal(family, out)
		}
	}
}

func TestWarriorBeginsOrdinarySwingBeforeSpendingRage(t *testing.T) {
	p, obs := warriorPolicy(t, `{spec="protection",role="tank"}`)
	warriorShield(t, p)
	obs.ActiveMeleeTargetGuid = 0
	obs.Spells["23922"] = warriorRank(23922, 1, 200)
	out := p.decide(p.observe(obs)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "melee_start" || a["targetGuid"] != fallbackGUID(fallbackNPC) {
		t.Fatal(out)
	}
}
func TestWarriorShieldBlockUsesImplicitSelfOnlyUnderObservedPressure(t *testing.T) {
	p, obs := warriorPolicy(t, `{spec="protection",role="tank"}`)
	warriorShield(t, p)
	obs.Players["4"].Health = 40
	obs.Spells["2565"] = warriorRank(2565, 1, 0)
	out := p.decide(p.observe(obs)).Output
	a, _ := out["action"].(map[string]any)
	targets, _ := a["targets"].(map[string]any)
	if a["spellId"] != float64(2565) || len(targets) != 0 || a["targetGuid"] != "4" {
		t.Fatal(out)
	}
}

func TestCreepingSludgeHonorsAssignedTankAndExplicitRangedOrder(t *testing.T) {
	for _, mode := range []string{"tank", "backline", "explicit_ranged"} {
		t.Run(mode, func(t *testing.T) {
			role := "tank"
			if mode == "backline" {
				role = "damage"
			}
			p, obs := warriorPolicy(t, `{spec="protection",role="`+role+`"}`)
			warriorShield(t, p)
			obs.MapId = proto.Uint32(349)
			for _, unit := range obs.Players {
				unit.MapId = proto.Uint32(349)
			}
			obs.Navigation = &actionsv1.NavigationObservation{BodyMovementSupported: true, MovementOwner: &actionsv1.MovementOwnerState{}}
			obs.Spells["23922"] = warriorRank(23922, 1, 200)
			n := obs.Targets[fallbackGUID(fallbackNPC)]
			n.Entry, n.MapId, n.PositionRevision = 12222, proto.Uint32(349), 1
			n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 4}
			if mode == "explicit_ranged" {
				waitTaskCommand(t, p, map[string]any{"task": "ranged_only", "id": "assigned-tank-at-range", "mapId": 349, "visitId": "1", "creatureEntry": 12222,
					"minRangeYards": 14, "maxRangeYards": 36, "clearanceYards": 2, "durationMs": 60000})
			}
			out := p.decide(observeRanged(t, p, obs, false)).Output
			a, _ := out["action"].(map[string]any)
			if mode == "tank" {
				if a["capability"] != "cast_spell" || a["spellId"] != float64(23922) || out["encounterPrior"] != nil {
					t.Fatal("assigned tank lost its ordinary Shield Slam", out)
				}
			} else if a["capability"] != "melee_stop" {
				t.Fatal("backline or explicitly ranged warrior kept swinging", out)
			}
			if mode == "explicit_ranged" && out["rangedStance"].(map[string]any)["origin"] != "explicit" {
				t.Fatal(out)
			}
		})
	}
}
