package buddyactions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func starterNativeRig(t *testing.T) (*Service, *fakeSession, *worldv1.CreatureState) {
	t.Helper()
	s, f, target, _, _ := fargodeepRig(t, 4)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("missing observed body pose")
	}
	return s, f, target
}

func TestStarterSpellNativeSameBodyCASChangesDecisionWithoutReconnect(t *testing.T) {
	s, f, target := starterNativeRig(t)
	point, _ := fargodeepCreaturePosition(target)
	before := s.starterSpellDecisionLocked(context.Background(), f.world, target, point)
	if !before.valid || before.kind != "cast" || before.spell.ID != fireball {
		t.Fatal(before, s.starterSpells.failure())
	}
	// This is the existing identity-bound source editor, not a new controller.
	req := &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID,
		Source: `return {decide=function(input) return {kind="wait",spellId=0} end}`}
	accepted, err := s.ReloadBehavior(context.Background(), req)
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	after := s.starterSpellDecisionLocked(context.Background(), f.world, target, point)
	if !after.valid || after.kind != "wait" || s.starterSpells.revision != accepted.Revision {
		t.Fatal(after, s.starterSpells.failure())
	}
	req.Source = behaviors.StarterSpellSource
	if _, err := s.ReloadBehavior(context.Background(), req); err == nil {
		t.Fatal("stale editor revision replaced the accepted source")
	}
	req.ExpectedRevision = accepted.Revision
	if _, err := s.ReloadBehavior(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	final := s.starterSpellDecisionLocked(context.Background(), f.world, target, point)
	if !final.valid || final.kind != "cast" || final.spell.ID != fireball || len(f.packets) != 0 {
		t.Fatal("same body did not read accepted replacement without game mutation", final, f.packets)
	}
}

func TestStarterSpellNativeCurrentSourceFailureNeverFallsBackToCastOrMelee(t *testing.T) {
	for _, mode := range []string{"missing export", "transport", "extra field", "unknown rank", "fractional rank", "wrong kind", "missing rank"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target := starterNativeRig(t)
			s.state.KnownAutoAttack = true
			s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
				out := map[string]any{"kind": "cast", "spellId": fireball}
				switch mode {
				case "missing export":
					return false, strings.Repeat("a", 64), nil, nil
				case "transport":
					return false, "", nil, errors.New("active policy unavailable")
				case "extra field":
					out["fallback"] = true
				case "unknown rank":
					out["spellId"] = 4294967295
				case "fractional rank":
					out["spellId"] = 133.5
				case "wrong kind":
					out["kind"] = "native"
				case "missing rank":
					delete(out, "spellId")
				}
				return true, strings.Repeat("a", 64), out, nil
			}
			if s.starterAssistObservedTargetLocked(context.Background(), f.world, target) ||
				countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.starterSpells.lastError == "" {
				t.Fatal("unavailable current source invoked a fallback", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStarterSpellNativeQuarantinedAuthoredSourceCannotRestoreOlderCast(t *testing.T) {
	s, f, target := starterNativeRig(t)
	s.starterSpells.remote = func(context.Context, map[string]any) (bool, string, map[string]any, error) {
		return false, "", nil, nil // Connected host has no attached source.
	}
	req := &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: behaviors.StarterSpellSource}
	accepted, err := s.ReloadBehavior(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	point, _ := fargodeepCreaturePosition(target)
	if initial := s.starterSpellDecisionLocked(context.Background(), f.world, target, point); !initial.valid || initial.kind != "cast" {
		t.Fatal(initial, s.starterSpells.failure())
	}
	req.ExpectedRevision, req.Source = accepted.Revision, `return {decide=function(input) error("current starter failed") end}`
	broken, err := s.ReloadBehavior(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	s.state.KnownAutoAttack = true
	for range 2 {
		if s.starterAssistObservedTargetLocked(context.Background(), f.world, target) ||
			countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 ||
			s.starterSpells.revision != broken.Revision || !strings.Contains(s.starterSpells.lastError, "current starter failed") {
			t.Fatal("quarantined source restored an older cast", s.starterSpells, s.stateLocked(), f.packets)
		}
	}
	// The existing runtime exposes the restored active revision to its editor.
	// Only an explicit successful CAS edit releases this adapter's refusal.
	req.ExpectedRevision, req.Source = s.behavior.Status().Revision, behaviors.StarterSpellSource
	if _, err := s.ReloadBehavior(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if fixed := s.starterSpellDecisionLocked(context.Background(), f.world, target, point); !fixed.valid || fixed.kind != "cast" {
		t.Fatal("accepted corrective source remained blocked", fixed, s.starterSpells.failure())
	}
}

func TestStarterSpellNativeRefusesFactsChangedDuringPureRead(t *testing.T) {
	for _, mode := range []string{"mana", "target health", "target pose", "body revision", "visit", "spellbook", "own aura"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target := starterNativeRig(t)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				switch mode {
				case "mana":
					f.world.Self.Power--
				case "target health":
					target.Health--
				case "target pose":
					target.X++
				case "body revision":
					f.world.Self.PositionRevision++
				case "visit":
					f.world.VisitRevision++
				case "spellbook":
					s.spellbookRevision++
				case "own aura":
					// The actual observed aura family becomes policy input.
					s.ownDamageAuras = map[damageAuraSlot]observedBuffAura{{target: target.Guid, slot: 0}: {spell: fireball}}
				}
				return true, runtime.Status().Revision, out, err
			}
			if s.starterAssistObservedTargetLocked(context.Background(), f.world, target) || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("cast used observations from before a callback", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStarterSpellNativePrewriteReusesLuaAndRechecksPartyAfterRead(t *testing.T) {
	for _, mode := range []string{"changed policy", "party changed"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, req, _ := fargodeepRig(t, 4)
			req.EngagerGuid = 4
			armFargodeep(t, s, req)
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			reads := 0
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				reads++
				out, err := runtime.EvaluateContext(ctx, input)
				if reads == 2 {
					if mode == "changed policy" {
						out = map[string]any{"kind": "wait", "spellId": 0}
					} else {
						f.party.Group.InGroup = false
					}
				}
				return true, runtime.Status().Revision, out, err
			}
			s.Tick(context.Background())
			if reads != 2 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.state.AssistAttempts != 0 || s.state.LastCastTargetGuid == target.Guid {
				t.Fatal("prewrite did not re-read policy and final native party facts", reads, s.stateLocked(), f.packets)
			}
		})
	}
}
