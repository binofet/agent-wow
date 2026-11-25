package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"testing"
)

func trustedOfferPolicy(t *testing.T, tuning string) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse, string) {
	t.Helper()
	module, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "death-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local encounter_death_recovery=(function()\n" + string(module) + "\nend)()\n" + tuning + "\n" + readEncounterBundle(t)
	host, err := behavior.NewSession(context.Background(), fallbackSession, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	p := &fallbackPolicyTest{t: t, host: host}
	obs := positionedHealObservation()
	obs.Navigation = &actionsv1.NavigationObservation{}
	self := obs.Players[fallbackGUID(fallbackSelf)]
	self.Alive = proto.Bool(false)
	self.HealthKnown = true
	self.Health = 0
	self.PositionRevision = 1
	obs.Recovery = &actionsv1.RecoveryState{Enabled: true, DeathEpoch: 1, ResurrectionOffer: &actionsv1.ResurrectionOffer{CasterGuid: 9001, Revision: 7, DeathEpoch: 1}}
	return p, obs, source
}

func TestTrustedExternalResurrectionOfferUsesExactActorTuning(t *testing.T) {
	for _, tc := range []struct {
		name, tuning string
		caster       uint64
		want         string
	}{
		{"default_outsider", "", 9001, "release_spirit"},
		{"explicit_external", `encounter_death_recovery.tuning.trusted_offer_casters={["9001"]=true}`, 9001, "accept_resurrection"},
		{"untrusted_outsider", `encounter_death_recovery.tuning.trusted_offer_casters={["9001"]=true}`, 9002, "release_spirit"},
		{"false_entry", `encounter_death_recovery.tuning.trusted_offer_casters={["9001"]=false}`, 9001, "release_spirit"},
		{"truthy_string_is_not_trust", `encounter_death_recovery.tuning.trusted_offer_casters={["9001"]="true"}`, 9001, "release_spirit"},
		{"numeric_key_is_not_guid", `encounter_death_recovery.tuning.trusted_offer_casters={[9001]=true}`, 9001, "release_spirit"},
		{"malformed_set", `encounter_death_recovery.tuning.trusted_offer_casters=true`, 9001, "release_spirit"},
		{"party_unchanged", "", 3, "accept_resurrection"},
		{"any_offer_unchanged", `encounter_death_recovery.tuning.offer_scope='any_offer'`, 9002, "accept_resurrection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, obs, _ := trustedOfferPolicy(t, tc.tuning)
			obs.Recovery.ResurrectionOffer.CasterGuid = tc.caster
			d := p.decide(p.observe(obs))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != tc.want {
				t.Fatal("wrong composed policy choice", d.Output)
			}
			if tc.want == "accept_resurrection" && (a["casterGuid"] != fallbackGUID(tc.caster) || a["expectedOfferRevision"] != "7" || a["deathEpoch"] != "1") {
				t.Fatal("offer identity lost", d.Output)
			}
		})
	}
}

func TestTrustedExternalResurrectionRetainsOfferMotionAndDeathChecks(t *testing.T) {
	for _, condition := range []string{"no_offer", "old_offer", "wrong_death", "owned_motion", "motion_unknown", "response_attempted", "alive", "released_cross_map"} {
		t.Run(condition, func(t *testing.T) {
			p, obs, _ := trustedOfferPolicy(t, `encounter_death_recovery.tuning.trusted_offer_casters={["9001"]=true}`)
			want := ""
			switch condition {
			case "no_offer":
				obs.Recovery.ResurrectionOffer = nil
				want = "release_spirit"
			case "old_offer":
				obs.Recovery.ResurrectionOffer.AgeMs = 60001
				want = "release_spirit"
			case "wrong_death":
				obs.Recovery.ResurrectionOffer.DeathEpoch = 2
				want = "release_spirit"
			case "owned_motion":
				obs.Recovery.Active = true
				obs.Recovery.MotionPending = true
				want = "stop_corpse"
			case "motion_unknown":
				obs.Navigation.MotionUnconfirmed = true
			case "response_attempted":
				obs.Recovery.ResurrectionOffer.ResponseAttempted = true
			case "alive":
				obs.Players[fallbackGUID(fallbackSelf)].Alive = proto.Bool(true)
				want = "heal"
			case "released_cross_map":
				obs.Recovery.GhostObserved = true
				obs.Players[fallbackGUID(fallbackSelf)].MapId = proto.Uint32(1)
				obs.Recovery.CorpseMapId = 349
				want = "accept_resurrection"
			}
			out := p.decide(p.observe(obs)).Output
			a, _ := out["action"].(map[string]any)
			if want == "" {
				if out["action"] != nil {
					t.Fatal("state fabricated permission to accept", out)
				}
			} else if a["capability"] != want {
				t.Fatal("existing motion/offer check changed", condition, out)
			}
		})
	}
}

func TestTrustedExternalOfferWriteCustodySurvivesReloadAndTrustRemoval(t *testing.T) {
	for _, outcome := range []behavior.ActionOutcome{behavior.ActionUnknown, behavior.ActionSettledUnverified} {
		t.Run(string(outcome), func(t *testing.T) {
			p, obs, _ := trustedOfferPolicy(t, `encounter_death_recovery.tuning.trusted_offer_casters={["9001"]=true}`)
			d := p.decide(p.observe(obs))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "accept_resurrection" {
				t.Fatal(d.Output)
			}
			id, err := p.host.ReserveAction(d)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{"capability": "accept_resurrection", "self_guid": fallbackGUID(fallbackSelf), "death_epoch": "1", "offer_revision": "7"}}})
			if err != nil {
				t.Fatal(err)
			}
			// Remove trust before loading the normal entry; the retained write must
			// not turn into a fresh response or a living-state assertion.
			module, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "death-recovery.lua"))
			if err != nil {
				t.Fatal(err)
			}
			source := "local encounter_death_recovery=(function()\n" + string(module) + "\nend)()\n" + readEncounterBundle(t)
			if _, err = p.host.Reload(context.Background(), source, p.host.Status().Revision); err != nil {
				t.Fatal(err)
			}
			obs.Recovery.ResurrectionOffer.AgeMs = 90000
			for i := 0; i < 2; i++ {
				out := p.decide(p.observe(obs)).Output
				if out["action"] != nil {
					t.Fatal("reload/trust removal replayed response", out)
				}
			}
			if outcome == behavior.ActionUnknown && p.host.Status().Pending != 1 {
				t.Fatal("UNKNOWN lost", p.host.Status())
			}
		})
	}
}
