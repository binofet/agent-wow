package buddyactions

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestProtectedEngageNeedsFreshSelectionAfterAuraRemoval(t *testing.T) {
	s, f, _, _ := polymorphFixture(t)
	ctx := context.Background()
	req := &actionsv1.EngageNPCRequest{Enabled: true, TargetGuid: sheepTarget}
	if _, err := s.EngageNPC(ctx, req); err != nil {
		t.Fatal(err)
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget, 3, 118, 20000))
	if _, err := s.EngageNPC(ctx, req); err == nil {
		t.Fatal("explicit engage bypassed the same protection feed")
	}
	s.Tick(ctx)
	if s.engage.active || s.engage.phase != "protected_target_requires_new_selection" {
		t.Fatal("existing engage intent was not retired", s.engage)
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(sheepTarget, 3, 2, 0, 0))
	s.Tick(ctx)
	if s.engage.active || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal("breaking the aura replayed the old engage intent")
	}
	if _, err := s.EngageNPC(ctx, req); err != nil {
		t.Fatal("fresh explicit selection should be possible after removal", err)
	}
}

func TestProtectedTargetOverflowDoesNotSilentlyUnprotectTarget(t *testing.T) {
	s, _, _, _ := polymorphFixture(t)
	for i := uint64(0); i <= maxProtectedAuras; i++ {
		emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(sheepTarget+i, 3, 118, 20000))
	}
	st := state(t, s)
	if len(st.ProtectedTargets) != maxProtectedAuras || !st.TargetProtectionOverflow ||
		!s.targetProtection.blocks(sheepTarget+maxProtectedAuras, s.now()) {
		t.Fatal("overflow failed open", len(st.ProtectedTargets), st.TargetProtectionOverflow)
	}
	emitPolymorph(s, opcode.SMSGNewWorld, make([]byte, 20))
	st = state(t, s)
	if len(st.ProtectedTargets) != 0 || st.TargetProtectionOverflow || st.ProtectedTargetRevision == 0 {
		t.Fatal("world change did not invalidate old protection", st)
	}
}
