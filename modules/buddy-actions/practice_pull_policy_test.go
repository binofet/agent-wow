package buddyactions

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func relaxedPracticePullSource(t *testing.T) string {
	t.Helper()
	const before = `health_percent=95,mana_percent={[5]=45,[8]=40},energy=90`
	const after = `health_percent=60,mana_percent={[5]=30,[8]=30},energy=50`
	if strings.Count(behaviors.StarterSpellSource, before) != 1 {
		t.Fatal("missing unique shared Practice resource tuning")
	}
	return strings.Replace(behaviors.StarterSpellSource, before, after, 1)
}

func TestPracticePullReadinessRetunesSameBodyBeforeExactCast(t *testing.T) {
	for _, class := range []uint32{4, 8} {
		t.Run(strconv.FormatUint(uint64(class), 10), func(t *testing.T) {
			ctx := context.Background()
			s, f, _ := practiceRig(t, class)
			sid, body := s.sessionID, s.selfGUID
			f.world.Self.Health = 70
			f.world.Self.Power = 35
			if class == 4 {
				f.world.Self.Power = 50
			}
			s.Tick(ctx)
			if !s.state.Practicing || s.state.PracticePulls != 0 || s.state.PracticePhase != "recovering_before_next_pull" ||
				countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 {
				t.Fatal("default Lua readiness was bypassed", s.stateLocked(), f.packets)
			}
			accepted, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{
				SessionId: sid, SelfGuid: body, Source: relaxedPracticePullSource(t)})
			if err != nil || accepted.Revision == "" {
				t.Fatal(accepted, err)
			}
			s.Tick(ctx)
			if s.sessionID != sid || s.selfGUID != body || s.state.PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("native threshold overrode current same-body Lua tuning", s.stateLocked(), f.packets)
			}
			for _, p := range f.packets {
				if p.Opcode == opcode.CMSGCastSpell && (len(p.Payload) < 10 || !bytes.Equal(p.Payload[10:], packedGUID(enemyGUID))) {
					t.Fatal("Lua-authorized pull changed its exact wire target", p)
				}
			}
		})
	}
}

func TestPracticePullReadinessEditNeverReplaysAnUnknownCast(t *testing.T) {
	ctx := context.Background()
	s, f, _ := practiceRig(t, 8)
	s.session = starterWeaponAmbiguousSession{f}
	s.Tick(ctx)
	uncertain := s.starterSpells.uncertainStrike
	if uncertain == nil || countPackets(f, opcode.CMSGCastSpell) != 1 || uncertain.view.ServerCastGo || uncertain.view.EffectObserved {
		t.Fatal("missing real ambiguous first pull", uncertain, s.stateLocked(), f.packets)
	}
	accepted, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: relaxedPracticePullSource(t)})
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	for range 2 {
		s.Tick(ctx)
	}
	if s.starterSpells.uncertainStrike != uncertain || countPackets(f, opcode.CMSGCastSpell) != 1 || uncertain.view.ServerCastGo || uncertain.view.EffectObserved {
		t.Fatal("new resource policy erased unknown cast custody", uncertain, s.stateLocked(), f.packets)
	}
}

func TestPracticePullReadinessRechecksResourcesAndTaskAfterLua(t *testing.T) {
	for _, change := range []string{"health", "mana", "maximum", "base_mana", "body", "task", "visit", "pose", "malformed"} {
		t.Run(change, func(t *testing.T) {
			s, f, _ := practiceRig(t, 8)
			if !s.refreshPositionLocked(f.world) {
				t.Fatal("own pose unavailable")
			}
			runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
			s.starterSpells.remote = func(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
				out, err := runtime.EvaluateContext(ctx, input)
				switch change {
				case "health":
					f.world.Self.Health = 70
				case "mana":
					f.world.Self.Power = 39
				case "maximum":
					f.world.Self.MaxHealth = 200
				case "base_mana":
					f.world.Self.BaseMana = 400
				case "body":
					s.sessionID = "replacement-body"
				case "task":
					s.state.Practicing = false
				case "visit":
					f.world.VisitRevision++
				case "pose":
					f.world.Self.PositionRevision++
				case "malformed":
					out = map[string]any{"kind": "ready", "spellId": 99}
				}
				return true, runtime.Status().Revision, out, err
			}
			before := len(f.packets)
			selected, _ := s.starterPracticePullDecisionLocked(context.Background(), f.world, s.practiceApproachCurrentLocked(context.Background(), 0))
			if selected.valid || len(f.packets) != before || s.practicePullStarted || s.state.PracticePulls != 0 {
				t.Fatal("stale readiness granted a pull or wrote transport", selected, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPracticePullUsesSameLuaReadinessAfterVerifiedApproach(t *testing.T) {
	ctx := context.Background()
	s, f, _ := practiceRig(t, 8)
	path := s.planPath
	s.planPath = func(ctx context.Context, dir string, a, b position) ([]position, error) {
		result, err := path(ctx, dir, a, b)
		f.world.Self.Health, f.world.Self.Power = 70, 35
		return result, err
	}
	s.Tick(ctx)
	if !s.state.Practicing || s.practiceTarget != enemyGUID || s.practicePullStarted || s.state.PracticePulls != 0 ||
		s.state.PracticePhase != "recovering_before_next_pull" || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("pre-approach readiness authorized a weakened pull", s.stateLocked(), f.packets)
	}
	accepted, err := reloadStarterSemanticBehavior(t, s, ctx, &actionsv1.BehaviorReloadRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, Source: relaxedPracticePullSource(t)})
	if err != nil || accepted.Revision == "" {
		t.Fatal(accepted, err)
	}
	s.Tick(ctx)
	if s.state.PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.LastCastTargetGuid != enemyGUID {
		t.Fatal("same Lua edit did not release the retained exact pull", s.stateLocked(), f.packets)
	}
}
