package world

import (
	"encoding/json"
	"math"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestEncounterWeaponStartsSwingBeforeBehindAndStrikesFromSparseJSON(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := fallbackObservation()
	self := obs.Players[fallbackGUID(fallbackSelf)]
	self.PositionKnown, self.ClassKnown, self.ClassId = true, true, 4
	self.PowerKnown, self.PowerType, self.Power, self.MaxPower = true, 3, 100, 100
	obs.MeleeKnown = true
	obs.Spells = map[string]*actionsv1.EncounterSpell{
		"53": {Known: true, Ready: true, Available: true, Capability: "offense", MetadataKnown: true,
			FamilyId: 53, Cost: 60, PowerType: 3, DirectDamage: 52, MaxRange: 5,
			WeaponStrike: true, RequiresBehind: true, RequiresFacing: true, GlobalCooldownMs: 1000},
		"1752": {Known: true, Ready: true, Available: true, Capability: "offense", MetadataKnown: true,
			FamilyId: 1752, Cost: 45, PowerType: 3, DirectDamage: 35, MaxRange: 5,
			WeaponStrike: true, RequiresFacing: true, GlobalCooldownMs: 1000},
	}
	addFallbackTarget(obs, fallbackNPC, fallbackSelf, 900)
	target := obs.Targets[fallbackGUID(fallbackNPC)]
	target.X, target.Orientation, target.MeleeReady = 3, float32(math.Pi), true
	check := func(capability string, spell any) map[string]any {
		t.Helper()
		raw, err := protojson.Marshal(obs)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		seq, err := p.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		out := p.decide(fallbackGUID(seq)).Output
		action, _ := out["action"].(map[string]any)
		if action["capability"] != capability || action["targetGuid"] != fallbackGUID(fallbackNPC) || action["spellId"] != spell {
			t.Fatalf("want %s spell=%v on current attacker, got %+v", capability, spell, out)
		}
		return action
	}
	// First reach normal melee range; a positional opener must not delay the
	// first swing until an entire route behind the enemy has completed.
	target.X, target.MeleeReady = 20, false
	if action := check("approach_target", nil); action["behind"] != false {
		t.Fatal("idle rogue chased behind before starting autoswing", action)
	}
	// A moving rogue can use an in-range instant even before native melee
	// readiness permits a swing start. Do not replace it with another chase.
	target.X, target.MeleeReady = 3, false
	target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 500,
		DestinationKnown: true, DestinationX: 4}
	self.MovementKnown, self.Moving = true, true
	check("offense", float64(1752))
	target.Trajectory = nil
	self.Moving, target.MeleeReady = false, true
	// A stronger available Backstab must not postpone a currently legal swing
	// while the rogue is face-to-face with an enemy attacking it.
	check("melee_start", nil)
	obs.ActiveMeleeTargetGuid = fallbackNPC
	check("offense", float64(1752))
	// Both zero facing angles and zero cast time are omitted by protojson.
	target.Orientation = 0
	check("offense", float64(53))
	// Once the target runs, chase it without dropping the acknowledged swing.
	target.X, target.MeleeReady = 20, false
	if action := check("approach_target", nil); action["behind"] != true {
		t.Fatal("active swing did not retain rogue positional preference", action)
	}
}
