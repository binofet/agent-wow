package buddyactions

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func assertNoAutonomousOffense(t *testing.T, s *Service, f *fakeSession) {
	t.Helper()
	if countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.state.AssistAttempts != 0 {
		t.Fatalf("host-owned encounter emitted native offense: %v (%v)", f.packets, s.stateLocked())
	}
}

func TestHostEncounterWriterDisablesNativeOffense(t *testing.T) {
	ctx := context.Background()
	t.Run("owner's target", func(t *testing.T) {
		s, f, _ := assistRig(t) // Exercise the legacy scheduler even with its intent manually armed.
		s.UseHostEncounterWriter()
		s.Tick(ctx)
		assertNoAutonomousOffense(t, s, f)
	})
	t.Run("current party attacker", func(t *testing.T) {
		s, f, _, _ := partyDefenseRig(t, 8)
		s.state.Assisting = false
		s.UseHostEncounterWriter()
		s.Tick(ctx)
		assertNoAutonomousOffense(t, s, f)
	})
	t.Run("self defense and heal interrupt during task custody", func(t *testing.T) {
		s, f, enemy, _ := selfDefenseRig(t, 4)
		s.state.Assisting = false
		s.setKnownSpell(1766, true)
		s.UseHostEncounterWriter()
		f.world.Self.UnitFlags = 0
		lease, err := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
		if err != nil {
			t.Fatal(err)
		}
		enemy.X, enemy.UnitFlagsKnown = 3, true
		enemy.ActiveCast = &worldv1.CreatureCast{SpellId: 12039, RemainingMs: 1500, Revision: 1}
		f.world.Self.UnitFlags = 0x80000
		s.state.Moving, s.state.Traveling = true, true
		s.Tick(ctx)
		assertNoAutonomousOffense(t, s, f)
		if s.state.InteractionOwner != "" || s.state.Traveling || countPackets(f, opcode.MSGMoveStop) != 1 {
			t.Fatal("attack failed to fence noncombat custody", s.stateLocked(), f.packets)
		}
		if _, err := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "exploration", Token: lease.Token}); err == nil {
			t.Fatal("retired movement token was renewed")
		}
	})
}

func TestHostEncounterWriterKeepsItsMeleeTargetAfterOwnerSelectionChanges(t *testing.T) {
	ctx := context.Background()
	s, f, _ := rig(t)
	s.UseHostEncounterWriter()
	f.world.Owner.TargetKnown, f.world.Owner.TargetGuid = true, enemyGUID+1
	if _, err := s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.autoTarget = enemyGUID
	s.state.AutoAttacking = true
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGAttackstop) != 0 || !s.state.AutoAttacking || s.autoTarget != enemyGUID || !s.state.Following {
		t.Fatal("legacy target tracking interrupted the host's attack or follow", s.stateLocked(), f.packets)
	}
}

func TestHostEncounterWriterKeepsHealingAndExactActions(t *testing.T) {
	ctx := context.Background()
	t.Run("standing party healing", func(t *testing.T) {
		s, f, _, _ := partyDefenseRig(t, 5)
		s.state.Assisting = false
		s.UseHostEncounterWriter()
		f.world.NearbyPlayers[0].Health = 40
		s.Tick(ctx)
		if got := state(t, s); got.HealAttempts != 1 || got.AssistAttempts != 0 || lastCast(f) != lesserHeal || got.LastCastTargetGuid != 4 {
			t.Fatal("host encounter ownership interrupted standing healing", got, f.packets)
		}
	})
	t.Run("explicit cast", func(t *testing.T) {
		const frostArmor = 168
		s, f, _ := rig(t)
		s.UseHostEncounterWriter()
		s.sessionID = "mage-body"
		s.applySpellbookLocked(map[uint32]bool{frostArmor: true})
		f.world.Self.ClassId, f.world.Self.Power = 8, 150
		req := &actionsv1.CastSelfRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: frostArmor}
		st, err := s.CastSelf(ctx, req)
		if err != nil || !st.GetCastPending() || lastCast(f) != frostArmor || countPackets(f, opcode.CMSGCastSpell) != 1 {
			t.Fatal("exact action could not use the learned spell", st, err, f.packets)
		}
	})
}

func TestHostEncounterWriterDoesNotAdmitLegacyIntents(t *testing.T) {
	ctx := context.Background()
	s, f, _ := rig(t)
	s.UseHostEncounterWriter()
	for _, intent := range []struct {
		name string
		call func(bool) (*actionsv1.State, error)
	}{
		{"assist", func(enable bool) (*actionsv1.State, error) {
			return s.Assist(ctx, &actionsv1.ToggleRequest{Enabled: enable})
		}},
		{"engage", func(enable bool) (*actionsv1.State, error) {
			return s.EngageNPC(ctx, &actionsv1.EngageNPCRequest{Enabled: enable, TargetGuid: enemyGUID})
		}},
		{"practice", func(enable bool) (*actionsv1.State, error) {
			return s.Practice(ctx, &actionsv1.ToggleRequest{Enabled: enable})
		}},
		{"Fargodeep", func(enable bool) (*actionsv1.State, error) {
			return s.FargodeepEncounter(ctx, &actionsv1.FargodeepEncounterRequest{Enabled: enable, SessionId: "current-body"})
		}},
	} {
		t.Run(intent.name, func(t *testing.T) {
			if _, err := intent.call(true); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("a second combat writer was admitted", err)
			}
			s.autoTarget = enemyGUID
			s.state.AutoAttacking, s.state.Moving = true, true
			if st, err := intent.call(false); err != nil || !st.GetAutoAttacking() || !st.GetMoving() || len(f.packets) != 0 {
				t.Fatal("inactive legacy intent interfered with current writes", err, st, f.packets)
			}
		})
	}
}
