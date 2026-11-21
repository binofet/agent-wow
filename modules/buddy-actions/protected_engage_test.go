package buddyactions

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestExactOffenseAfterProtectionRemovalNeedsAnExplicitLuaSelectedAction(t *testing.T) {
	s, f, r, _ := exactOffenseFixture(t)
	emitPolymorph(s, opcode.SMSGAuraUpdate, sheepAura(r.TargetGuid, 3, 118, 20000))
	if _, err := s.CastOffense(context.Background(), r); err == nil {
		t.Fatal("exact cast bypassed observed protection")
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(r.TargetGuid, 3, 2, 0, 0))
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal("removing protection restarted a native controller")
	}
	if _, err := s.CastOffense(context.Background(), r); err != nil {
		t.Fatal("fresh explicit action failed after actual aura removal", err)
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
