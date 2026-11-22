package buddyactions

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestPracticeTargetChoiceUsesSameBodyLuaEditAndExactCastPacket(t *testing.T) {
	ctx := context.Background()
	s, f, _ := practiceRig(t, 8)
	first := f.world.NearbyCreatures[0]
	second := proto.Clone(first).(*worldv1.CreatureState)
	second.Guid++
	second.X += 3
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, second)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("own pose unavailable")
	}
	sid, body := s.sessionID, s.selfGUID
	before := len(f.packets)
	_, chosen, _, valid := s.practiceTargetDecisionLocked(ctx, f.world, s.practiceApproachCurrentLocked(ctx, 0))
	if !valid || chosen == nil || chosen.Guid != first.Guid || len(f.packets) != before {
		t.Fatal("default Lua target plan", chosen, valid, s.starterSpells.failure())
	}
	source := strings.Replace(behaviors.StarterSpellSource, `search_yards=120,order="nearest"`, `search_yards=120,order="farthest"`, 1)
	if source == behaviors.StarterSpellSource {
		t.Fatal("missing shared target tuning")
	}
	accepted, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{SessionId: sid, SelfGuid: body, Source: source})
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	s.Tick(ctx)
	if s.sessionID != sid || s.selfGUID != body || s.practiceTarget != second.Guid || s.state.LastCastTargetGuid != second.Guid || lastCast(f) != fireball || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("accepted same-body priority did not select/cast exact farther target", s.stateLocked(), f.packets, s.starterSpells.failure())
	}
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGCastSpell && (len(packet.Payload) < 10 || !bytes.Equal(packet.Payload[10:], packedGUID(second.Guid))) {
			t.Fatal("cast wire target differs from Lua choice", packet)
		}
	}
}

func TestPracticeTargetFactsAndPostChoiceAdmissionNeverSelectAReplacement(t *testing.T) {
	for _, change := range []string{"foreign_guid", "changed_spawn", "task_cancelled", "foreign_body", "unknown_source"} {
		t.Run(change, func(t *testing.T) {
			s, f, _ := practiceRig(t, 8)
			if !s.refreshPositionLocked(f.world) {
				t.Fatal("own pose unavailable")
			}
			target := f.world.NearbyCreatures[0]
			initial, known := s.practiceTargetFactsLocked(f.world)
			if !known || len(initial["candidates"].([]any)) != 1 {
				t.Fatal(initial)
			}
			row := initial["candidates"].([]any)[0].(map[string]any)
			if row["entry"] != uint32(299) || row["level"] != uint32(1) || row["creatureObject"] != true || row["eligible"] != nil || row["score"] != nil || initial["preferredTargetGuid"] != nil {
				t.Fatal("native facts contain target policy", initial)
			}
			before := len(f.packets)
			current := s.practiceApproachCurrentLocked(context.Background(), 0)
			s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
				choice := target.Guid
				switch change {
				case "foreign_guid":
					choice += 100
				case "changed_spawn":
					target.Entry = 69
				case "task_cancelled":
					s.state.Practicing = false
				case "foreign_body":
					s.sessionID = "new-body"
				case "unknown_source":
					return true, "current-source-without-target-export", map[string]any{"kind": "wait", "spellId": 0}, nil
				}
				return true, "current-source", map[string]any{"kind": "target", "targetGuid": strconv.FormatUint(choice, 10)}, nil
			}
			_, chosen, _, valid := s.practiceTargetDecisionLocked(context.Background(), f.world, current)
			if valid || chosen != nil || len(f.packets) != before {
				t.Fatal("stale/foreign selection admitted or native fallback wrote", chosen, valid, f.packets)
			}
		})
	}
}

func TestPracticeTargetDerivedAgeAdvancesWithoutAChangedSpawnOrVictim(t *testing.T) {
	for _, change := range []string{"age", "victim", "spawn"} {
		t.Run(change, func(t *testing.T) {
			s, f, _ := practiceRig(t, 8)
			if !s.refreshPositionLocked(f.world) {
				t.Fatal("own pose unavailable")
			}
			target := f.world.NearbyCreatures[0]
			s.practiceTarget, s.state.PracticeTargetGuid, s.practicePullStarted = target.Guid, target.Guid, true
			target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				switch change {
				case "age":
					target.AttackAgeMs++
				case "victim":
					target.AttackVictimGuid++
				case "spawn":
					target.Entry++
				}
				return true, runtime.Status().Revision, out, err
			}
			before := len(f.packets)
			_, chosen, _, valid := s.practiceTargetDecisionLocked(context.Background(), f.world, s.practiceApproachCurrentLocked(context.Background(), target.Guid))
			if valid != (change == "age") || valid && (chosen == nil || chosen.Guid != target.Guid) || len(f.packets) != before {
				t.Fatal("elapsed age was mistaken for changed identity, or new facts were admitted", change, valid, chosen, f.packets)
			}
		})
	}
}

func TestPracticeTargetStopRevalidatesRetainedLookupOutsideDiscovery(t *testing.T) {
	for _, change := range []string{"claim", "unsafe"} {
		t.Run(change, func(t *testing.T) {
			s, f, _, target := lookupPracticeRig(t)
			if change == "claim" {
				target.AttackVictimKnown, target.AttackVictimGuid = true, 3
			} else {
				target.UnitFlags = 2
			}
			w, err := s.snapshot(context.Background(), target.Guid)
			if err != nil {
				t.Fatal(err)
			}
			before, lookups := len(f.packets), len(f.lookups)
			_, chosen, kind, valid := s.practiceTargetDecisionLocked(context.Background(), w, s.practiceApproachCurrentLocked(context.Background(), target.Guid))
			if !valid || chosen != nil || kind != "stop" || len(f.packets) != before {
				t.Fatal("known rejected exact lookup lost stop choice", kind, valid, chosen, f.packets)
			}
			for _, guid := range f.lookups[lookups:] {
				if guid != target.Guid {
					t.Fatal("stopping selected body queried an unrelated target", guid)
				}
			}
		})
	}
}

func TestPracticeTargetFreshSameVictimAttackResetRequiresNewLuaChoice(t *testing.T) {
	s, f, _ := practiceRig(t, 8)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("own pose unavailable")
	}
	target := f.world.NearbyCreatures[0]
	s.practiceTarget, s.state.PracticeTargetGuid, s.practicePullStarted = target.Guid, target.Guid, true
	attacker := proto.Clone(target).(*worldv1.CreatureState)
	attacker.Guid++
	attacker.Entry = 69
	attacker.AttackVictimKnown, attacker.AttackVictimGuid, attacker.AttackAgeMs = true, s.selfGUID, 30001
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, attacker)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
		out, err := runtime.EvaluateContext(ctx, input)
		attacker.AttackAgeMs = 0 // New actual attack after Lua saw the old stale attack.
		return true, runtime.Status().Revision, out, err
	}
	before := len(f.packets)
	current := s.practiceApproachCurrentLocked(context.Background(), target.Guid)
	_, chosen, _, valid := s.practiceTargetDecisionLocked(context.Background(), f.world, current)
	if valid || chosen != nil || len(f.packets) != before {
		t.Fatal("same-victim refresh bypassed post-read observation guard", chosen, valid, f.packets)
	}
	// Once Lua observes the fresh incoming attack, its existing policy stops.
	_, chosen, kind, valid := s.practiceTargetDecisionLocked(context.Background(), f.world, current)
	if !valid || chosen != nil || kind != "stop" || len(f.packets) != before {
		t.Fatal("fresh Lua did not own the incoming-attack choice", kind, valid, chosen, f.packets)
	}
}
