package world

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"testing"
)

// No host band attestation is supplied: this exercises old-client compatibility,
// preserving an ordinary accepted support choice rather than adding a healer.
func TestRangedLegacyStationaryFriendlySupport(t *testing.T) {
	for _, mode := range []string{"heal", "cleanse", "out_of_range", "unknown_position", "estimated_position", "moving", "no_directive"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := positionedHealObservation()
			o.Navigation = &actionsv1.NavigationObservation{MovementOwner: &actionsv1.MovementOwnerState{}}
			self := o.Players[fallbackGUID(fallbackSelf)]
			self.ClassKnown = true
			self.ClassId = 5
			addFallbackTarget(o, fallbackNPC, fallbackSelf, 900)
			enemy := o.Targets[fallbackGUID(fallbackNPC)]
			enemy.Entry = 765432
			enemy.X = 13
			enemy.Y = 10
			enemy.Z = 10
			if mode == "cleanse" {
				o.Healing.Patients = nil
				o.Players["8"].Health = 100
				o.Spells = map[string]*actionsv1.EncounterSpell{"527": {Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "cleanse", MaxRange: 40, FriendlyDispelMask: 2}}
				o.Support = &actionsv1.EncounterSupport{PlayerAuras: []*actionsv1.PlayerAuras{{TargetGuid: 8, Complete: true, Revision: 1, Auras: []*actionsv1.ObservedAura{{SpellId: 172, Revision: 1, Negative: true, MetadataKnown: true, DispelType: 1, AuraTypes: []uint32{3}}}}}}
			}
			if mode != "no_directive" {
				cmd := map[string]any{"task": "ranged_only", "id": "friendly-support", "creatureEntry": float64(765432), "mapId": float64(0), "visitId": "1", "minRangeYards": float64(15), "maxRangeYards": float64(25), "clearanceYards": float64(2), "durationMs": float64(60000), "enabled": true}
				if _, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: cmd}); err != nil {
					t.Fatal(err)
				}
				d := p.decide(p.observe(o))
				a, _ := d.Output["action"].(map[string]any)
				if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
					t.Fatal("missing movement custody", d.Output)
				}
				id, err := p.host.ReserveAction(d)
				if err != nil {
					t.Fatal(err)
				}
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:friendly-support"}
				_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "owner": "ranged:friendly-support", "mode": "acquire", "result": map[string]any{"token": "fixture-support-token", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": fallbackGUID(fallbackSelf), "movementOwner": map[string]any{"active": true, "owner": "ranged:friendly-support"}}}}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "out_of_range":
				o.Players["8"].X = 55
				o.Healing.Patients[0].Distance = nil
			case "unknown_position":
				o.Players["8"].PositionKnown = false
			case "estimated_position":
				o.Players["8"].PositionEstimated = true
			case "moving":
				self.Moving = true
			}
			d := p.decide(p.observe(o))
			a, _ := d.Output["action"].(map[string]any)
			if mode == "heal" || mode == "cleanse" || mode == "no_directive" {
				want := "heal"
				if mode == "cleanse" {
					want = "cleanse"
				}
				if a["capability"] != want || a["targetGuid"] != "8" {
					t.Fatal("friendly support starved", mode, d.Output)
				}
				if mode != "no_directive" && d.Output["rangedStance"].(map[string]any)["phase"] != "stationary_friendly_support" {
					t.Fatal(d.Output)
				}
			} else if a != nil && a["capability"] != "stop_movement" {
				t.Fatal("unknown/out-of-range support bypassed stationary hold", mode, d.Output)
			}
		})
	}
}
