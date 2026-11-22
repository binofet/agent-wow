package behavior

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
)

func TestFargodeepReadinessDefaultCLI(t *testing.T) {
	source := behaviors.StarterSpellSource
	const threshold = "minimum_health=95"
	if strings.Count(source, threshold) != 1 {
		t.Fatal("actual shared Fargodeep tuning is missing or ambiguous")
	}
	edited := strings.Replace(source, threshold, "minimum_health=94", 1)
	for _, test := range []struct {
		name, source, want         string
		health, mana, base, energy uint32
		baseKnown                  bool
	}{
		{"full", source, "ready", 100, 128, 0, 128, false},
		{"health refusal", source, "wait", 94, 128, 0, 128, false},
		{"edited health", edited, "ready", 94, 128, 0, 128, false},
		{"fallback mana refusal", source, "wait", 100, 57, 0, 128, false},
		{"fallback mana boundary", source, "ready", 100, 58, 0, 128, false},
		{"base mana refusal", source, "wait", 100, 45, 102, 128, true},
		{"base mana boundary", source, "ready", 100, 46, 102, 128, true},
		{"energy refusal", source, "wait", 100, 128, 0, 89, false},
		{"energy boundary", source, "ready", 100, 128, 0, 90, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			players := map[string]any{}
			for _, member := range []struct {
				guid             string
				class, powerType uint32
			}{{"1", 5, 0}, {"4", 8, 0}, {"5", 4, 3}} {
				power, health, base, known := uint32(128), uint32(100), uint32(0), false
				if member.guid == "1" {
					power, health, base, known = test.mana, test.health, test.base, test.baseKnown
				}
				if member.guid == "5" {
					power = test.energy
				}
				players[member.guid] = map[string]any{"guid": member.guid, "classKnown": true, "classId": member.class,
					"healthKnown": true, "health": health, "maxHealth": uint32(100), "powerKnown": true, "powerType": member.powerType,
					"power": power, "maxPower": uint32(128), "baseManaKnown": known, "baseMana": base}
			}
			frame := map[string]any{"sessionId": "fargodeep-default", "selfGuid": "5", "autoAttackKnown": true, "selection": "fargodeep_pull_readiness",
				"observation": map[string]any{"sessionId": "fargodeep-default", "selfGuid": "5", "visitId": "19", "worldObservationRevision": "7", "players": players},
				"mission":     map[string]any{"partySessionId": "actual-party-body", "targetGuid": "17379390974694817793", "engagerGuid": "5"}}
			// The maintained helper verifies clean-source CLI provenance and invokes
			// the ordinary executable once with the real 5ms/32KiB defaults.
			out, err := evaluateDisposalProduction(t, test.source, frame)
			want := map[string]any{"kind": test.want, "spellId": float64(0), "selection": "fargodeep_pull_readiness"}
			if err != nil || !reflect.DeepEqual(out, want) {
				t.Fatal("ordinary CLI readiness result", out, want, err)
			}
		})
	}
}
