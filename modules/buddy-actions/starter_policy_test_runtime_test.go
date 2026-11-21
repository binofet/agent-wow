package buddyactions

import (
	"context"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Semantic fixtures need a healthy evaluator so they can isolate admission,
// source ownership and receipts from machine load. These helpers do not prove
// production performance or change any production constructor/default budget.
func newStarterSemanticRuntime(t *testing.T) *behavior.Runtime {
	t.Helper()
	runtime, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return runtime // Empty active source: callers retain the real load/CAS path.
}

func useStarterSemanticPolicy(t *testing.T, s *Service) {
	t.Helper()
	runtime := newStarterSemanticRuntime(t)
	if _, err := runtime.Reload(behaviors.StarterSpellSource, ""); err != nil {
		t.Fatal(err)
	}
	s.starterSpells.runtime = runtime
}

func reloadStarterSemanticBehavior(t *testing.T, s *Service, ctx context.Context, request *actionsv1.BehaviorReloadRequest) (*actionsv1.BehaviorStatus, error) {
	t.Helper()
	if s.behavior == nil && request != nil && !request.UseBuiltin {
		s.behavior = newStarterSemanticRuntime(t)
	}
	// Do not load source or emulate body/revision checks here. The actual public
	// entry point owns request identity, CAS and builtin rollback; ctx is forwarded unchanged.
	return s.ReloadBehavior(ctx, request)
}

func TestStarterNativeProductionWatchdogRejectsRestoredAuthoredFallback(t *testing.T) {
	ctx := context.Background()
	s, fake, _ := practiceRig(t, 4)
	sid, self, until := s.sessionID, s.selfGUID, s.practiceUntil
	questGoal := s.practiceQuestGoal
	target := fake.world.NearbyCreatures[0]
	target.UnitFlagsKnown, target.DynamicFlagsKnown = true, true
	point, known, _ := creaturePosition(target)
	if !known || !s.refreshPositionLocked(fake.world) {
		t.Fatal("fixture lacks fresh observed caster and target poses")
	}
	if behavior.EvaluationLimit != 5*time.Millisecond {
		t.Fatal("production watchdog contract changed", behavior.EvaluationLimit)
	}
	// Deliberately override the healthy semantic fixture. This one contract
	// test uses the real default constructor and its actual 5ms watchdog.
	authored := behavior.New()
	s.behavior = authored
	const sourceA = `return {decide=function(input)return {kind="cast",spellId=1752}end}`
	acceptedA, err := s.ReloadBehavior(ctx, &actionsv1.BehaviorReloadRequest{
		SessionId: sid, SelfGuid: self, Source: sourceA})
	if err != nil || acceptedA == nil || acceptedA.Revision == "" || s.behavior != authored {
		t.Fatal("actual public reload did not accept source A", acceptedA, err)
	}
	working := s.starterWeaponDecisionLocked(ctx, fake.world, target, point)
	good := authored.Status()
	if !working.valid || working.kind != "cast" || working.spell.ID != 1752 ||
		good.Revision != acceptedA.Revision || good.LastGoodRevision != acceptedA.Revision || good.Failures != 0 ||
		len(fake.packets) != 0 || s.state.AssistAttempts != 0 {
		t.Fatal("fresh native read did not record working A without gameplay writes", working, good, fake.packets)
	}
	const sourceB = `return {decide=function(input)while true do end end}`
	acceptedB, err := s.ReloadBehavior(ctx, &actionsv1.BehaviorReloadRequest{
		SessionId: sid, SelfGuid: self, ExpectedRevision: acceptedA.Revision, Source: sourceB})
	if err != nil || acceptedB == nil || acceptedB.Revision == "" || acceptedB.Revision == acceptedA.Revision || s.behavior != authored {
		t.Fatal("actual public CAS did not accept source B", acceptedB, err)
	}
	for tick := 0; tick < 2; tick++ {
		s.Tick(ctx)
		status := authored.Status()
		if status.Revision != acceptedA.Revision || status.LastGoodRevision != acceptedA.Revision ||
			status.QuarantinedRevision != acceptedB.Revision || status.Failures != good.Failures+1 || status.LastError == "" ||
			s.starterSpells.failedAuthored != authored || s.starterSpells.revision != acceptedB.Revision ||
			s.starterSpells.authoredFailedRevision != acceptedB.Revision || s.starterSpells.authoredError == "" {
			t.Fatal("native watchdog failure restored offense authority or lost the attempted source", tick, status, s.starterSpells)
		}
		if s.sessionID != sid || s.selfGUID != self || fake.world.Self.Guid != self || !s.state.Practicing ||
			s.practiceUntil != until || s.practiceQuestGoal != questGoal ||
			countPackets(fake, opcode.CMSGCastSpell) != 0 || countPackets(fake, opcode.CMSGAttackswing) != 0 ||
			s.state.CastPending || s.pendingSpell != 0 || s.pendingTarget != 0 || s.state.AssistAttempts != 0 ||
			s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 || s.starterSpells.uncertainStrike != nil {
			t.Fatal("failed current source wrote offense, invented effects or canceled Practice intent", tick, s.stateLocked(), fake.packets)
		}
	}
	// Practice may already have entered its existing pull stage before asking
	// for a rank. That intent is not a cast, damage or realm-success receipt.
	failedTarget, failedPulls, failedStarted := s.practiceTarget, s.state.PracticePulls, s.practicePullStarted
	const sourceC = `return {decide=function(input)return {kind="wait",spellId=0}end}`
	acceptedC, err := s.ReloadBehavior(ctx, &actionsv1.BehaviorReloadRequest{
		SessionId: sid, SelfGuid: self, ExpectedRevision: acceptedA.Revision, Source: sourceC})
	if err != nil || acceptedC == nil || acceptedC.Revision == acceptedA.Revision || acceptedC.Revision == acceptedB.Revision ||
		s.behavior != authored || s.starterSpells.failedAuthored != nil || s.starterSpells.authoredError != "" ||
		s.starterSpells.authoredFailedRevision != "" {
		t.Fatal("explicit corrective CAS did not release only the source failure fence", acceptedC, err, s.starterSpells)
	}
	corrected := s.starterWeaponDecisionLocked(ctx, fake.world, target, point)
	if !corrected.valid || corrected.kind != "wait" || corrected.spell.ID != 0 || s.starterSpells.revision != acceptedC.Revision {
		t.Fatal("same body did not read the accepted corrective wait source", corrected, s.starterSpells)
	}
	s.Tick(ctx)
	if !s.state.Practicing || s.practiceUntil != until || s.practiceQuestGoal != questGoal || s.practicePullStarted != failedStarted ||
		s.practiceTarget != failedTarget || s.state.PracticePulls != failedPulls ||
		countPackets(fake, opcode.CMSGCastSpell) != 0 || countPackets(fake, opcode.CMSGAttackswing) != 0 ||
		s.state.CastPending || s.pendingSpell != 0 || s.pendingTarget != 0 || s.state.AssistAttempts != 0 ||
		s.state.ServerAssistGo != 0 || s.state.AssistDamageEvents != 0 || s.starterSpells.uncertainStrike != nil {
		t.Fatal("corrective source changed gameplay intent or invented an offensive effect", s.stateLocked(), fake.packets)
	}
}
