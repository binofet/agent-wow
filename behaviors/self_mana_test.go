package behaviors

import (
	"context"
	"errors"
	"maps"
	"os"
	"strconv"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

const (
	manaSession = "mage-mana-session"
	manaBody    = "23"
	evocation   = 12051
)

func newSelfManaSession(t *testing.T) (*behavior.Session, string) {
	t.Helper()
	source, err := os.ReadFile("self-mana.lua")
	if err != nil {
		t.Fatal(err)
	}
	session, err := behavior.NewSession(context.Background(), manaSession, string(source))
	if errors.Is(err, context.DeadlineExceeded) {
		// Match the other Lua fixture's cold-load retry under -race.
		session, err = behavior.NewSession(context.Background(), manaSession, string(source))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, string(source)
}

func manaObservation(t *testing.T) map[string]any {
	t.Helper()
	spell, ok := spellcatalog.Default().Lookup(evocation)
	if !ok || spell.Family != evocation || spell.Attrs[1]&0x40 == 0 {
		t.Fatalf("fixture needs cataloged channeled Evocation: %+v", spell)
	}
	return map[string]any{
		"sessionId": manaSession, "selfGuid": manaBody, "hostOwned": true, "spellbookKnown": true,
		"caster": map[string]any{
			"guid": manaBody, "present": true, "aliveKnown": true, "alive": true, "mapKnown": true,
			"positionKnown": true, "movementKnown": true, "moving": false, "combatKnown": true, "inCombat": false,
			"channelKnown": true, "channelSpellId": 0, "classKnown": true, "classId": 8,
			"powerKnown": true, "powerType": 0, "power": 350, "maxPower": 1000,
		},
		"activity": map[string]any{"known": true, "idle": true},
		"threat":   map[string]any{"known": true, "attackingSelf": false, "attackingParty": false},
		"spells": []any{map[string]any{
			"spellId": spell.ID, "familyId": spell.Family, "rank": spell.Rank, "metadataKnown": true,
			"selfCastSupported": true, "channeled": true, "readyKnown": true, "ready": true,
			"blockedReason": "channel_requires_handler",
		}},
	}
}

func enqueueManaObservation(t *testing.T, session *behavior.Session, view map[string]any) string {
	t.Helper()
	sequence, err := session.Enqueue(behavior.Event{Kind: "self_mana_observation", Value: view})
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatUint(sequence, 10)
}

func decideSelfMana(t *testing.T, session *behavior.Session, sequence string) behavior.Decision {
	t.Helper()
	result, err := session.Decide(context.Background(), map[string]any{"kind": "self_mana", "observationSequence": sequence})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireSelfMana(t *testing.T, result behavior.Decision, sequence string, expectedSpell int) {
	t.Helper()
	if expectedSpell == 0 {
		if len(result.Output) != 0 {
			t.Fatalf("wanted no cast, got %v", result.Output)
		}
		return
	}
	proposal, _ := result.Output["action"].(map[string]any)
	want := map[string]any{
		"capability": "cast_self", "sessionId": manaSession, "selfGuid": manaBody, "targetGuid": manaBody,
		"spellId": float64(expectedSpell), "observationSequence": sequence,
	}
	if len(result.Output) != 1 || !maps.Equal(proposal, want) {
		t.Fatalf("proposal = %v, want %v", result.Output, want)
	}
}

func TestSelfManaRequiresLearnedReadyRankAndQuietBoundBody(t *testing.T) {
	session, _ := newSelfManaSession(t)
	for _, tc := range []struct {
		name, fieldset, field string
		value                 any
		expectedSpell         int
	}{
		{name: "known Evocation at 35 percent", expectedSpell: evocation},
		{name: "known zero mana", fieldset: "caster", field: "power", value: 0, expectedSpell: evocation},
		{name: "over threshold", fieldset: "caster", field: "power", value: 351},
		{name: "power unknown", fieldset: "caster", field: "powerKnown", value: false},
		{name: "zero max mana", fieldset: "caster", field: "maxPower", value: 0},
		{name: "different power type", fieldset: "caster", field: "powerType", value: 1},
		{name: "different class", fieldset: "caster", field: "classId", value: 5},
		{name: "dead body", fieldset: "caster", field: "alive", value: false},
		{name: "different session", field: "sessionId", value: "previous-session"},
		{name: "different caster", fieldset: "caster", field: "guid", value: "24"},
		{name: "native writer still owns the body", field: "hostOwned", value: false},
		{name: "moving", fieldset: "caster", field: "moving", value: true},
		{name: "unknown movement", fieldset: "caster", field: "movementKnown", value: false},
		{name: "in combat", fieldset: "caster", field: "inCombat", value: true},
		{name: "own attacker before combat flag", fieldset: "threat", field: "attackingSelf", value: true},
		{name: "party under attack", fieldset: "threat", field: "attackingParty", value: true},
		{name: "unknown attackers", fieldset: "threat", field: "known", value: false},
		{name: "channeling", fieldset: "caster", field: "channelSpellId", value: evocation},
		{name: "unknown channel", fieldset: "caster", field: "channelKnown", value: false},
		{name: "shared action custody busy", fieldset: "activity", field: "idle", value: false},
		{name: "spellbook unknown", field: "spellbookKnown", value: false},
		{name: "Evocation not learned", field: "spells", value: []any{}},
		{name: "unrelated learned self cast", fieldset: "first rank", field: "familyId", value: 1459},
		{name: "rank lacks self-cast support", fieldset: "first rank", field: "selfCastSupported", value: false},
		{name: "rank on cooldown", fieldset: "first rank", field: "ready", value: false},
		{name: "rank cooldown unknown", fieldset: "first rank", field: "readyKnown", value: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := manaObservation(t)
			switch tc.fieldset {
			case "":
				if tc.field != "" {
					view[tc.field] = tc.value
				}
			case "first rank":
				view["spells"].([]any)[0].(map[string]any)[tc.field] = tc.value
			default:
				view[tc.fieldset].(map[string]any)[tc.field] = tc.value
			}
			seq := enqueueManaObservation(t, session, view)
			requireSelfMana(t, decideSelfMana(t, session, seq), seq, tc.expectedSpell)
		})
	}
}

func TestSelfManaCannotReplayAnObservationOrOverlapAReservedCast(t *testing.T) {
	ctx := context.Background()
	session, source := newSelfManaSession(t)
	view := manaObservation(t)
	old := enqueueManaObservation(t, session, view)
	current := enqueueManaObservation(t, session, view)
	requireSelfMana(t, decideSelfMana(t, session, old), old, 0)
	decision := decideSelfMana(t, session, current)
	requireSelfMana(t, decision, current, evocation)
	id, err := session.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	requireSelfMana(t, decideSelfMana(t, session, current), current, 0)
	receipt := func(outcome behavior.ActionOutcome) {
		t.Helper()
		_, err := session.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
			SessionID: manaSession, ActionID: id, Outcome: outcome,
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt(behavior.ActionTransportAccepted)
	seq := enqueueManaObservation(t, session, view)
	requireSelfMana(t, decideSelfMana(t, session, seq), seq, 0)

	// An ended channel without verified effect is still pending in this host.
	receipt(behavior.ActionUnknown)
	rev := session.Status().Revision
	reloaded, err := session.Reload(ctx, source+"\n-- reviewed self-mana", rev)
	if err != nil || reloaded == rev {
		t.Fatalf("reload revision=%q error=%v", reloaded, err)
	}
	seq = enqueueManaObservation(t, session, view)
	requireSelfMana(t, decideSelfMana(t, session, seq), seq, 0)
	pending := session.Outstanding()
	if len(pending) != 1 || pending[0].ActionID != id || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatalf("unknown channel outcome was lost or confirmed across reload: %+v", pending)
	}

	receipt(behavior.ActionConfirmed)
	view["caster"].(map[string]any)["power"] = 900
	seq = enqueueManaObservation(t, session, view)
	requireSelfMana(t, decideSelfMana(t, session, seq), seq, 0)
}
