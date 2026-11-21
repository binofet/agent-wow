package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNativeTickNeverChoosesOrdinaryAttackTargetRankOrRotation(t *testing.T) {
	for _, class := range []uint32{4, 5, 8, 9} {
		s, f, _, advance := selfDefenseRig(t, class)
		for _, enabled := range []bool{false, true} {
			s.state.Following, s.state.Assisting = enabled, enabled
			s.followUntil, s.assistUntil = s.now().Add(time.Hour), s.now().Add(time.Hour)
			s.Tick(context.Background())
			advance(5 * time.Hour)
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.state.AssistAttempts != 0 {
				t.Fatal("native Tick selected ordinary offense", class, enabled, s.stateLocked(), f.packets)
			}
		}
	}
}

func TestAssistCompatibilityCannotDisableLuaDefenseOrStopCurrentAttack(t *testing.T) {
	s, f, _ := rig(t)
	s.autoTarget = enemyGUID
	s.state.AutoAttacking = true
	s.state.AutoAttackTargetGuid = enemyGUID
	for _, enabled := range []bool{true, false} {
		out, err := s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: enabled})
		if err != nil || out.GetAssisting() != enabled || !out.GetAutoAttacking() || s.autoTarget != enemyGUID || len(f.packets) != 0 {
			t.Fatal("legacy toggle authored physical combat", out, err, f.packets)
		}
	}
	if _, err := s.EngageNPC(context.Background(), &actionsv1.EngageNPCRequest{Enabled: true, TargetGuid: enemyGUID}); err == nil {
		t.Fatal("module started a second engage controller")
	}
	if out, err := s.EngageNPC(context.Background(), &actionsv1.EngageNPCRequest{}); err != nil || out.GetEngageNpc().GetActive() || len(f.packets) != 0 {
		t.Fatal("legacy intent retirement disabled combat", out, err)
	}
}
