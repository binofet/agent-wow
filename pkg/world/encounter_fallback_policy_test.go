package world

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	fallbackSession = "encounter-fallback"
	fallbackSelf    = uint64(4)
	fallbackLeader  = uint64(3)
	fallbackGroup   = uint64(7)
	fallbackNPC     = uint64(0xf130002c37000001)
)

func fallbackGUID(guid uint64) string { return strconv.FormatUint(guid, 10) }

func fallbackPlayer(guid uint64) *actionsv1.EncounterPlayer {
	return &actionsv1.EncounterPlayer{
		Guid: guid, Present: true, MapKnown: true, MapId: proto.Uint32(0), AliveKnown: true, Alive: proto.Bool(true),
	}
}

func fallbackObservation() *actionsv1.EncounterObservationResponse {
	return &actionsv1.EncounterObservationResponse{
		SessionId: fallbackSession, SelfGuid: fallbackSelf, VisitId: 1, MapId: proto.Uint32(0),
		HostOwned: proto.Bool(true), WorldObservationRevision: 10, SpellbookKnown: true,
		Players:    map[string]*actionsv1.EncounterPlayer{fallbackGUID(fallbackSelf): fallbackPlayer(fallbackSelf)},
		Targets:    map[string]*actionsv1.EncounterCreature{},
		Engagement: map[string]*actionsv1.EncounterEngagement{},
		Spells: map[string]*actionsv1.EncounterSpell{
			"17":   {Known: true, Ready: true, Capability: "heal"},
			"116":  {Known: true, Ready: false, Capability: "offense"},
			"133":  {Known: true, Ready: true, Capability: "offense"},
			"585":  {Known: true, Ready: true, Capability: "offense"},
			"1088": {Known: true, Ready: true, Capability: "offense"},
		},
	}
}

func addFallbackTarget(obs *actionsv1.EncounterObservationResponse, guid, victim uint64, health uint32) {
	key := fallbackGUID(guid)
	obs.Targets[key] = &actionsv1.EncounterCreature{
		Guid: guid, Present: true, MapKnown: true, MapId: proto.Uint32(0), AliveKnown: true, Alive: proto.Bool(true),
		PositionKnown: true, Attackable: true, HealthKnown: true, Health: health, MaxHealth: 1000, Engaged: victim != 0,
	}
	if victim != 0 {
		obs.Engagement[key] = &actionsv1.EncounterEngagement{VictimGuid: victim}
	}
}

func fallbackRoster(online bool) *partyv1.Group {
	return &partyv1.Group{Known: true, InGroup: true, GroupGuid: fallbackGroup, LeaderGuid: fallbackLeader,
		Members: []*partyv1.Member{{Guid: fallbackSelf, Self: true, Online: true}, {Guid: fallbackLeader, Online: online}}}
}

type fallbackPolicyTest struct {
	t    *testing.T
	host *behavior.Session
}

func newFallbackPolicyTest(t *testing.T, intent string) *fallbackPolicyTest {
	t.Helper()
	host, err := behavior.NewSession(context.Background(), fallbackSession, intent+readEncounterBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	return &fallbackPolicyTest{t: t, host: host}
}

func (p *fallbackPolicyTest) observe(obs *actionsv1.EncounterObservationResponse) string {
	p.t.Helper()
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
	if err != nil {
		p.t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		p.t.Fatal(err)
	}
	seq, err := p.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
	if err != nil {
		p.t.Fatal(err)
	}
	return fallbackGUID(seq)
}

func (p *fallbackPolicyTest) decide(seq string) behavior.Decision {
	p.t.Helper()
	got, err := p.host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": seq})
	if err != nil {
		p.t.Fatal(err)
	}
	return got
}

func (p *fallbackPolicyTest) expectOffense(seq, phase string, target uint64, spell float64) behavior.Decision {
	p.t.Helper()
	decision := p.decide(seq)
	out := decision.Output
	action, _ := out["action"].(map[string]any)
	if out["phase"] != phase || out["focusGuid"] != fallbackGUID(target) ||
		action["capability"] != "offense" || action["sessionId"] != fallbackSession ||
		action["selfGuid"] != fallbackGUID(fallbackSelf) || action["targetGuid"] != fallbackGUID(target) ||
		action["spellId"] != spell || action["observationSequence"] != seq {
		p.t.Fatalf("expected exact %s offense on %d with spell %v from %s, got %+v", phase, target, spell, seq, out)
	}
	return decision
}

func (p *fallbackPolicyTest) expectNoAction(seq string) map[string]any {
	p.t.Helper()
	out := p.decide(seq).Output
	if out["action"] != nil {
		p.t.Fatalf("unexpected action: %+v", out)
	}
	return out
}

func TestEncounterClaimsOnlyCurrentPlayableObservation(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := fallbackObservation()
	seq := p.observe(obs)
	if got := p.expectNoAction("0"); got["encounterOwnership"] != nil {
		t.Fatalf("stale observation claimed the encounter: %+v", got)
	}
	claim, _ := p.expectNoAction(seq)["encounterOwnership"].(map[string]any)
	if claim["visitId"] != "1" || claim["observationSequence"] != seq {
		t.Fatalf("playable observation did not claim its visit: %+v", claim)
	}
	obs.SpellbookKnown = false
	if got := p.expectNoAction(p.observe(obs)); got["encounterOwnership"] != nil {
		t.Fatalf("unknown spellbook claimed encounter ownership: %+v", got)
	}
}

func TestEncounterFallbackRequiresExactThreatAndActionCustody(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := fallbackObservation()
	attacker := fallbackNPC
	// Even the selected low-health target and a target with an uncorroborated
	// engaged bit must yield to an exact self-victim observation.
	addFallbackTarget(obs, attacker, fallbackSelf, 900)
	addFallbackTarget(obs, attacker+1, 0, 1)
	addFallbackTarget(obs, attacker+2, 0, 2)
	obs.Targets[fallbackGUID(attacker+2)].Engaged = true
	obs.ObservedOwnerFocus = attacker + 1
	addFallbackTarget(obs, attacker+3, fallbackSelf, 3)
	obs.Targets[fallbackGUID(attacker+3)].Protected = true
	addFallbackTarget(obs, attacker+4, fallbackSelf, 4)
	obs.ProtectedAuras = []*actionsv1.ProtectedTarget{
		{TargetGuid: attacker + 4, CasterGuid: 3, SpellId: 118, Revision: 1},
	}

	seq := p.observe(obs)
	decision := p.expectOffense(seq, "engaged_defense", attacker, 133)
	queue, _ := decision.Output["queue"].([]any)
	if len(queue) == 0 {
		t.Fatalf("defensive focus did not come from the queue: %+v", decision.Output)
	}
	for _, value := range queue {
		row := value.(map[string]any)
		if row["engaged"] != true {
			t.Fatalf("unengaged target entered the defensive queue: %+v", queue)
		}
	}
	id, err := p.host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	seq = p.observe(obs)
	p.expectNoAction(seq)
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionNotDispatched,
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.expectNoAction(seq) // Settlement requires fresh evidence.
	seq = p.observe(obs)
	p.expectNoAction("1")
	p.expectOffense(seq, "engaged_defense", attacker, 133)

	obs.HostOwned = proto.Bool(false)
	p.expectNoAction(p.observe(obs))
	obs.HostOwned = proto.Bool(true)
	obs.TargetProtectionOverflow = true
	p.expectNoAction(p.observe(obs))
	obs.TargetProtectionOverflow = false
	obs.Players[fallbackGUID(fallbackSelf)].Alive = proto.Bool(false)
	p.expectNoAction(p.observe(obs))
}

func TestEncounterFallbackRequiresCurrentPartyVictim(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := fallbackObservation()
	obs.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	obs.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	obs.Players["17"] = fallbackPlayer(17)
	addFallbackTarget(obs, fallbackNPC, fallbackLeader, 900)
	addFallbackTarget(obs, fallbackNPC+1, 17, 1)
	p.expectOffense(p.observe(obs), "engaged_defense", fallbackNPC, 133)
	obs.Party.Group.Members[1].Online = false
	p.expectNoAction(p.observe(obs))
	obs.Party.Group.Members[1].Online = true
	obs.Players[fallbackGUID(fallbackLeader)].MapId = proto.Uint32(1)
	p.expectNoAction(p.observe(obs))
}

func TestEncounterFallbackPreservesBoundPlanAndControl(t *testing.T) {
	const intent = `
local encounter_intent = {
    plan={id="pull", groupGuid="7", leaderGuid="3", killOrder={"skull", "moon"},
          controls={{mark="moon", technique="polymorph", casterGuid="4", spellId=118}}},
    damage={scope="focus", spellId=585}
}
`
	p := newFallbackPolicyTest(t, intent)
	obs := fallbackObservation()
	skull, moon, attacker := fallbackNPC, fallbackNPC+1, fallbackNPC+2
	obs.Party = &actionsv1.EncounterParty{
		Group: fallbackRoster(true),
		RaidTargets: &partyv1.RaidTargetState{GroupGuid: fallbackGroup, Complete: true, Revision: 10,
			Marks: []*partyv1.RaidTarget{{Icon: 8, TargetGuid: skull}, {Icon: 5, TargetGuid: moon}}},
	}
	obs.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	addFallbackTarget(obs, skull, 0, 900)
	addFallbackTarget(obs, moon, fallbackSelf, 1)
	addFallbackTarget(obs, attacker, fallbackSelf, 20)
	// Current marks bind this plan. It still waits for its own Skull pull.
	if out := p.expectNoAction(p.observe(obs)); out["focusGuid"] != nil {
		t.Fatalf("fallback escaped a current authored pull: %+v", out)
	}

	marks := obs.Party.RaidTargets
	obs.Party.RaidTargets = nil
	obs.VisitId, obs.MapId = 0, nil
	p.expectNoAction(p.observe(obs))
	obs.VisitId, obs.MapId = 1, proto.Uint32(0)
	// A temporarily unbound world view cannot discard the current visit's
	// reservation. The add remains the only available target.
	p.expectOffense(p.observe(obs), "engaged_defense", attacker, 585)
	obs.Spells["585"].Ready = false
	p.expectNoAction(p.observe(obs))
	obs.Spells["585"].Ready = true
	obs.Party.RaidTargets = marks
	marks.Revision = 9
	p.expectOffense(p.observe(obs), "engaged_defense", attacker, 585)

	marks.Revision = 11
	currentMarks := marks.Marks
	marks.Marks = []*partyv1.RaidTarget{{Icon: 8, TargetGuid: attacker}, currentMarks[1]}
	if out := p.expectNoAction(p.observe(obs)); out["phase"] != "raid_mark_changed_replan" {
		t.Fatalf("a fresh changed Skull bypassed the authored plan: %+v", out)
	}
	marks.Marks = currentMarks[1:]
	if out := p.expectNoAction(p.observe(obs)); out["phase"] != "waiting_for_mark_skull" {
		t.Fatalf("a fresh cleared live Skull bypassed the authored plan: %+v", out)
	}
	marks.Marks = currentMarks
	marks.Revision = 12
	addFallbackTarget(obs, skull, fallbackLeader, 900)
	obs.ProtectedAuras = []*actionsv1.ProtectedTarget{
		{TargetGuid: moon, CasterGuid: fallbackSelf, SpellId: 118, Revision: 2},
	}
	p.expectOffense(p.observe(obs), "engaged_focus", skull, 585)

	obs.VisitId++
	obs.Party.RaidTargets = nil
	obs.ProtectedAuras = nil
	p.expectOffense(p.observe(obs), "engaged_defense", moon, 585)
}

func TestEncounterMarkedMovingTargetKeepsEligibleAttacks(t *testing.T) {
	const intent = `
local encounter_intent = {
    plan={id="moving-pull", groupGuid="7", leaderGuid="3", killOrder={"skull"}},
    damage={scope="focus", spellId=585}
}
`
	for _, melee := range []bool{false, true} {
		name := "ranged spell"
		if melee {
			name = "melee in reach"
		}
		t.Run(name, func(t *testing.T) {
			p := newFallbackPolicyTest(t, intent)
			obs := fallbackObservation()
			obs.Party = &actionsv1.EncounterParty{
				Group: fallbackRoster(true),
				RaidTargets: &partyv1.RaidTargetState{GroupGuid: fallbackGroup, Complete: true, Revision: 1,
					Marks: []*partyv1.RaidTarget{{Icon: 8, TargetGuid: fallbackNPC}}},
			}
			obs.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
			addFallbackTarget(obs, fallbackNPC, fallbackLeader, 100)
			target := obs.Targets[fallbackGUID(fallbackNPC)]
			target.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: false}
			if melee {
				obs.Spells["585"].Ready = false
				target.MeleeReady = true
				decision := p.decide(p.observe(obs))
				action, _ := decision.Output["action"].(map[string]any)
				if decision.Output["phase"] != "engaged_focus" || action["capability"] != "melee_start" || action["targetGuid"] != fallbackGUID(fallbackNPC) {
					t.Fatalf("moving target lost ready melee: %+v", decision.Output)
				}
				target.MeleeReady = false
				p.expectNoAction(p.observe(obs))
			} else {
				p.expectOffense(p.observe(obs), "engaged_focus", fallbackNPC, 585)
			}
			// Target motion grants neither a missing position nor protected-target damage.
			target.PositionKnown = false
			p.expectNoAction(p.observe(obs))
			target.PositionKnown, target.Protected = true, true
			p.expectNoAction(p.observe(obs))
		})
	}
}
