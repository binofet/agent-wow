package world

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

// Read the two ordinary consumers at their actual call sites using an editable
// Lua observer. Dead rows do not appear in the ranked combat queue itself.
func TestEncounterConsumersKeepSparseHealthAndExactTooltip(t *testing.T) {
	probe := fmt.Sprintf(`
local inspect_key="%d"
local classified_health, classified_auras
local queue_intent=encounter_commands.queue_intent
encounter_commands.queue_intent=function(obs)
  local target=obs.targets[inspect_key]
  classified_health=target and target.health
  classified_auras=target and target.visibleAuras
  return queue_intent(obs)
end
local prior=encounter_tactics.decide
encounter_tactics.decide=function(input)
  local out=prior(input)
  if input.kind=="combat_queue" then
    local npc=input.targets[inspect_key]
    out.queue[#out.queue+1]={targetGuid=inspect_key, classifiedHealth=classified_health, tacticsHealth=npc.health,
        level=npc.level, typeFlags=npc.template and npc.template.typeFlags, rank=npc.template and npc.template.rank,
        visibleSpell=classified_auras and classified_auras.slots[1].spellId,
        stacks=npc.visibleAuras and npc.visibleAuras.slots[1].stacksOrCharges}
  end
  return out
end
`, fallbackNPC)
	source := readEncounterBundle(t)
	const before = "local tactics = assert(encounter_tactics)"
	if strings.Count(source, before) != 1 {
		t.Fatal("regular tactics call site unavailable")
	}
	ctx := context.Background()
	host, err := behavior.NewSession(ctx, fallbackSession, strings.Replace(source, before, probe+before, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	for _, tc := range []struct {
		name             string
		known, qualified bool
	}{{"known zero and ordinary rank", true, true}, {"unknown health", false, true}, {"another unit's tooltip", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{
				"guid": fallbackGUID(fallbackNPC), "entry": 11319, "present": true, "mapKnown": true, "mapId": 0,
				"aliveKnown": true, "alive": false, "healthKnown": tc.known, "maxHealth": 4377, "levelKnown": true, "level": 39,
			}
			if !tc.known {
				row["health"] = float64(17)
			}
			if tc.qualified {
				row["templateEntry"] = 11319
			}
			if tc.known {
				row["visibleAuras"] = map[string]any{"complete": false, "slots": []any{map[string]any{"spellId": 18173, "stacksOrCharges": 3}}}
			}
			input := map[string]any{"selfGuid": fallbackGUID(fallbackSelf), "sessionId": fallbackSession, "visitId": "1", "mapId": 0, "worldObservationRevision": "1", "hostOwned": true,
				"players":           map[string]any{fallbackGUID(fallbackSelf): map[string]any{"present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": 0}},
				"targets":           map[string]any{fallbackGUID(fallbackNPC): row},
				"creatureTemplates": map[string]any{"11319": map[string]any{"entry": 11319, "rank": 0, "typeFlags": 0}}}
			seq, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: input})
			if err != nil {
				t.Fatal(err)
			}
			decision := (&fallbackPolicyTest{t: t, host: host}).decide(fallbackGUID(seq))
			last := decision.Output["queue"].([]any)[0].(map[string]any)
			if last["level"] != float64(39) {
				t.Fatal("numeric level", last)
			}
			if tc.known && (last["visibleSpell"] != float64(18173) || last["stacks"] != float64(3)) ||
				!tc.known && (last["visibleSpell"] != nil || last["stacks"] != nil) {
				t.Fatal("encounter command or tactics lost per-creature aura evidence", last)
			}
			for _, key := range []string{"classifiedHealth", "tacticsHealth"} {
				v, present := last[key]
				if present != tc.known || present && v != float64(0) {
					t.Fatal("sparse health", key, last)
				}
			}
			for _, key := range []string{"typeFlags", "rank"} {
				v, present := last[key]
				if present != tc.qualified || present && v != float64(0) {
					t.Fatal("per-GUID tooltip", key, last)
				}
			}
		})
	}
}
