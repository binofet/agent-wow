package world

import (
	"context"
	"os"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
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

func warriorPack(t *testing.T, intent string, size int) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse) {
	t.Helper()
	p, obs := warriorPolicy(t, intent)
	obs.Players["4"].MovementKnown = true
	obs.NearbyCreaturesComplete = true
	obs.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	obs.Players["3"] = fallbackPlayer(fallbackLeader)
	obs.Players["3"].ClassKnown, obs.Players["3"].ClassId = true, 8
	for i := 0; i < size; i++ {
		guid := fallbackNPC + uint64(i)
		addFallbackTarget(obs, guid, 0, uint32(650+i*20))
		mob := obs.Targets[fallbackGUID(guid)]
		victim := fallbackSelf
		if i == size-1 && size > 1 {
			victim = fallbackLeader
		}
		mob.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: victim}
		mob.X, mob.Y, mob.CombatReach = 3, float32(i%3), proto.Float32(1.5)
	}
	obs.Spells["8205"] = warriorRank(6343, 4, 200)
	obs.Spells["8205"].MaxRange, obs.Spells["8205"].CategoryCooldownMs = 0, 6000
	obs.Spells["11608"] = warriorRank(845, 3, 200)
	obs.Spells["11608"].GlobalCooldownMs = 0
	return p, obs
}

func TestWarriorTankChoosesClapAndCleaveWithSeparateReceipts(t *testing.T) {
	// A compact pack includes an attacker on the mage. Observed attack relations
	// admit each creature independently; a cast receipt never supplies them.
	p, obs := warriorPack(t, `{spec="protection",role="tank"}`, 6)
	obs.Spells["6343"] = warriorRank(6343, 1, 200)
	obs.Spells["845"] = warriorRank(845, 1, 200)
	obs.Spells["845"].GlobalCooldownMs = 0
	obs.Spells["7386"] = warriorRank(7386, 1, 150)
	first := p.decide(p.observe(obs))
	clap, _ := first.Output["action"].(map[string]any)
	targets, _ := clap["targets"].(map[string]any)
	if clap["capability"] != "cast_spell" || clap["spellId"] != float64(8205) || clap["targetGuid"] != "4" || len(targets) != 0 {
		t.Fatal("expected highest learned caster-centered Thunder Clap", first.Output)
	}
	reserve := func(d behavior.Decision) uint64 {
		t.Helper()
		id, err := p.host.ReserveAction(d)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	receipt := func(id uint64, outcome behavior.ActionOutcome, spell float64, target string, goSeen bool) {
		t.Helper()
		if _, err := p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
			SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{
				"capability": "cast_spell", "spell_id": spell, "target_guid": target, "server_cast_go": goSeen,
			},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	id := reserve(first)
	receipt(id, behavior.ActionUnknown, 8205, "0", false)
	p.expectNoAction(p.observe(obs))
	receipt(id, behavior.ActionSettledUnverified, 8205, "0", true)
	second := p.decide(p.observe(obs))
	cleave, _ := second.Output["action"].(map[string]any)
	targets, _ = cleave["targets"].(map[string]any)
	if cleave["spellId"] != float64(11608) || cleave["targetGuid"] != fallbackGUID(fallbackNPC) ||
		targets["mask"] != float64(2) || targets["objectGuid"] != cleave["targetGuid"] {
		t.Fatal("expected a learned Cleave on the active swing during the global cooldown", second.Output)
	}
	id = reserve(second)
	receipt(id, behavior.ActionUnknown, 11608, fallbackGUID(fallbackNPC), false)
	p.expectNoAction(p.observe(obs))
	receipt(id, behavior.ActionSettledUnverified, 11608, fallbackGUID(fallbackNPC), false)
	out := p.decide(p.observe(obs)).Output
	if a, _ := out["action"].(map[string]any); a["capability"] == "cast_spell" {
		t.Fatal("replayed an unresolved next swing", out)
	}
	obs.Spells["845"].Ready, obs.Spells["11608"].Ready = false, false
	out = p.decide(p.observe(obs)).Output
	if a, _ := out["action"].(map[string]any); a["capability"] == "cast_spell" {
		t.Fatal("queuing Cleave cleared the running global cooldown", out)
	}
}

func TestWarriorPackGeometryAndProtection(t *testing.T) {
	const tank = `{spec="protection",role="tank"}`
	const marked = `{spec="protection",role="tank",plan={id="marks",groupGuid="7",leaderGuid="3",
		killOrder={"skull","moon"},controls={{mark="moon",technique="polymorph",casterGuid="3",spellId=118}}}}`
	const clap, cleave, swing = 8205, 11608, 78
	type sample struct {
		*actionsv1.EncounterObservationResponse
		second, bystander *actionsv1.EncounterCreature
	}
	move := func(s sample) {
		self := s.Players["4"]
		self.Moving, self.PositionAgeKnown, self.GroundRunSpeed = true, true, proto.Float32(7)
	}
	tests := []struct {
		name, intent         string
		adjust               func(sample)
		wantClap, wantCleave float64
	}{
		{"two close attackers", tank, nil, clap, cleave},
		{"moving in an engaged pack", tank, func(s sample) {
			move(s)
			delete(s.Targets, fallbackGUID(s.bystander.Guid))
		}, clap, cleave},
		{"moving with a distant unpulled creature", tank, move, clap, swing},
		{"moving across an unpulled creature's clearance", tank, func(s sample) { move(s); s.bystander.X = 15 }, swing, swing},
		{"stale moving pose", tank, func(s sample) { move(s); s.Players["4"].PositionAgeMs = 2000 }, swing, swing},
		{"unknown motion", tank, func(s sample) { s.Players["4"].MovementKnown = false }, swing, swing},
		{"only one attacker", tank, func(s sample) { delete(s.Targets, fallbackGUID(s.second.Guid)) }, swing, swing},
		{"second behind", tank, func(s sample) { s.second.X = -3 }, clap, swing},
		{"second outside clap", tank, func(s sample) { s.second.X = 9 }, swing, cleave},
		{"second on another floor", tank, func(s sample) { s.second.Z = 20 }, swing, swing},
		{"idle third", tank, func(s sample) { s.bystander.X = 5 }, swing, swing},
		{"uncertain bystander position", tank, func(s sample) { s.bystander.PositionKnown = false }, swing, swing},
		{"moving bystander", tank, func(s sample) {
			s.bystander.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 2000, DestinationKnown: true, DestinationX: 3}
		}, swing, swing},
		{"unknown bystander hitbox", tank, func(s sample) { s.bystander.CombatReach = nil }, swing, swing},
		{"large idle hitbox", tank, func(s sample) { s.bystander.X, s.bystander.CombatReach = 14, proto.Float32(5) }, swing, swing},
		{"glyph and leeway margin", tank, func(s sample) { s.bystander.X = 13 }, swing, cleave},
		{"observed control aura", tank, func(s sample) {
			s.bystander.X, s.bystander.AttackObservation = 3, s.second.AttackObservation
			s.ProtectedAuras = []*actionsv1.ProtectedTarget{{TargetGuid: s.bystander.Guid, CasterGuid: fallbackLeader, SpellId: 118, Revision: 1}}
		}, swing, swing},
		{"reserved moon before control lands", marked, func(s sample) {
			s.bystander.X, s.bystander.AttackObservation = 3, s.second.AttackObservation
			s.Party.RaidTargets = &partyv1.RaidTargetState{Complete: true, GroupGuid: fallbackGroup, Revision: 1,
				Marks: []*partyv1.RaidTarget{{Icon: 8, TargetGuid: fallbackNPC}, {Icon: 5, TargetGuid: s.bystander.Guid}}}
		}, swing, swing},
		{"partial nearby view", tank, func(s sample) { s.NearbyCreaturesComplete = false }, swing, swing},
		{"unconfirmed motion", tank, func(s sample) { s.Navigation = &actionsv1.NavigationObservation{MotionUnconfirmed: true} }, swing, swing},
		{"damage role", `{spec="protection",role="damage"}`, nil, swing, swing},
		{"cleave reserve boundary", tank, func(s sample) { s.Players["4"].Power = 500 }, clap, cleave},
		{"one below reserve", tank, func(s sample) { s.Players["4"].Power = 499 }, clap, swing},
		{"unavailable rank", tank, func(s sample) { s.Spells["8205"].Available, s.Spells["11608"].Available = false, false }, swing, swing},
		{"no usable mainhand", tank, func(s sample) { s.MeleeKnown = false }, 0, 0},
	}
	for _, tc := range tests {
		for rank, want := range map[string]float64{"8205": tc.wantClap, "11608": tc.wantCleave} {
			t.Run(tc.name+"/"+rank, func(t *testing.T) {
				p, obs := warriorPack(t, tc.intent, 2)
				obs.Spells["8205"].Ready, obs.Spells["11608"].Ready = rank == "8205", rank == "11608"
				obs.Spells["78"] = warriorRank(78, 1, 150)
				addFallbackTarget(obs, fallbackNPC+2, 0, 1000)
				bystander := obs.Targets[fallbackGUID(fallbackNPC+2)]
				bystander.X, bystander.CombatReach = 30, proto.Float32(1.5)
				if tc.adjust != nil {
					tc.adjust(sample{obs, obs.Targets[fallbackGUID(fallbackNPC+1)], bystander})
				}
				out := p.decide(p.observe(obs)).Output
				a, _ := out["action"].(map[string]any)
				if want == 0 && a["capability"] == "cast_spell" || want != 0 && a["spellId"] != want {
					t.Fatalf("want spell %v, got %+v", want, out)
				}
			})
		}
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
