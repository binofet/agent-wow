package world

import (
	"context"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func TestEncounterLuaHealingSelfSoloAndObservedNonpartyAlly(t *testing.T) {
	for _, mode := range []string{"solo_self", "nonparty", "dead_owner", "control_recovers", "disabled_legacy_healing"} {
		t.Run(mode, func(t *testing.T) {
			p, obs := newFallbackPolicyTest(t, ""), positionedHealObservation()
			obs.Healing.HostOwned = false
			want := "8"
			switch mode {
			case "solo_self":
				obs.Party = nil
				want = fallbackGUID(fallbackSelf)
				obs.Healing.Patients[0].Guid = fallbackSelf
				obs.Healing.Patients[0].Distance = proto.Float64(0)
				self := obs.Players[want]
				self.HealthKnown, self.Health, self.MaxHealth = true, 20, 100
			case "nonparty":
				obs.Party.Group.Members = obs.Party.Group.Members[:2]
			case "dead_owner":
				owner := fallbackPlayer(fallbackLeader)
				owner.Alive = proto.Bool(false)
				obs.Players[fallbackGUID(fallbackLeader)] = owner
			case "control_recovers":
				obs.Healing.Caster.Ready = false
				p.expectNoAction(observeSparseEncounter(t, p, obs))
				obs.Healing.Caster.Ready = true
			}
			action, _ := p.decide(observeSparseEncounter(t, p, obs)).Output["action"].(map[string]any)
			if action["capability"] != "heal" || action["targetGuid"] != want {
				t.Fatal("living friendly patient lost shared Lua healing", action)
			}
		})
	}
}

func TestEncounterLegacyHealInputAndRankRetuningShareOneLuaPolicy(t *testing.T) {
	p, obs := newFallbackPolicyTest(t, ""), positionedHealObservation()
	patient := obs.Healing.Patients[0]
	patient.Health, patient.MaxHealth = 95, 100
	p.expectNoAction(observeSparseEncounter(t, p, obs))
	if _, err := p.host.Enqueue(behavior.Event{Kind: "healing_request"}); err != nil {
		t.Fatal(err)
	}
	// A queued explicit request survives reload; the current shared source owns it.
	source := strings.Replace(readEncounterBundle(t), `rank_mode="effective_per_resource"`, `rank_mode="largest"`, 1)
	if _, err := p.host.Reload(context.Background(), source, p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	action, _ := p.decide(observeSparseEncounter(t, p, obs)).Output["action"].(map[string]any)
	if action["capability"] != "heal" || action["targetGuid"] != "8" {
		t.Fatal("queued explicit input did not reach current healing policy", action)
	}
	// No cast is reserved by this decision-only replay. A fresh observation uses
	// the reloaded scoring in the same session, without a second native selector.
	patient.Health, patient.MaxHealth = 220, 400
	obs.Healing.Spells = append(obs.Healing.Spells, &actionsv1.HealingRank{SpellId: 2052, Rank: proto.Uint32(2), Level: proto.Uint32(4), Cost: proto.Uint32(20), Amount: 80, CastMs: proto.Uint32(1500), MinRange: proto.Float64(0), MaxRange: proto.Float64(40)})
	obs.Healing.Spells[0].Amount = 40
	obs.Spells["2052"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, Capability: "heal", MetadataKnown: true, MaxRange: 40}
	action, _ = p.decide(observeSparseEncounter(t, p, obs)).Output["action"].(map[string]any)
	if action["spellId"] != float64(2052) || p.host.Status().SessionID != fallbackSession {
		t.Fatal("tuned rank did not execute from the same Lua policy/body", action)
	}
}
