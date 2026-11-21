package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func luaOwnedSupportContext() context.Context {
	ctx := modcb.WithPolicySpell(context.Background(), strings.Repeat("a", 64))
	ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, 1)
	md, _ := metadata.FromOutgoingContext(ctx)
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestLuaTargetOwnerNeverFallsBackToNearestGoAttackerAfterExpiry(t *testing.T) {
	for _, mode := range []string{"owner assist", "party defense", "self defense"} {
		t.Run(mode, func(t *testing.T) {
			var s *Service
			var f *fakeSession
			var advance func(time.Duration)
			switch mode {
			case "owner assist":
				s, f, advance = assistRig(t)
			case "party defense":
				s, f, _, advance = partyDefenseRig(t, 8)
			case "self defense":
				s, f, _, advance = selfDefenseRig(t, 8)
			}
			activateEncounterWriter(t, s, f)
			advance(encounterLeaseDuration)
			s.Tick(context.Background())
			assertNoAutonomousOffense(t, s, f)
			// A fresh lease resumes the same Lua writer, never a new Go target
			// choice. Existing exact action admission still requires that lease.
			if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
				t.Fatal(err)
			}
			s.Tick(context.Background())
			assertNoAutonomousOffense(t, s, f)
		})
	}
}

func TestLuaTargetLeaseExpiryPreservesSeparateFollowAndExactSupport(t *testing.T) {
	t.Run("follow", func(t *testing.T) {
		s, f, advance, target := followNavRig(t)
		activateEncounterWriter(t, s, f)
		advance(encounterLeaseDuration)
		if !s.followFormationStepLocked(context.Background(), target, 0.8) || countPackets(f, opcode.MSGMoveStartForward) != 1 {
			t.Fatal("target owner expired the movement lane", s.stateLocked())
		}
	})
	t.Run("interrupt", func(t *testing.T) {
		s, f, enemy, advance := supportPolicyInterruptRig(t)
		activateEncounterWriter(t, s, f)
		advance(encounterLeaseDuration)
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		request := &actionsv1.InterruptCastRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemy.Guid, SpellId: 1766, CastRevision: 7}
		if _, err := s.InterruptCast(luaOwnedSupportContext(), request); err != nil || lastCast(f) != 1766 {
			t.Fatal("target owner suppressed exact interrupt", err, s.stateLocked())
		}
	})
	t.Run("resurrect", func(t *testing.T) {
		s, f, advance := resRig(t)
		s.sessionID = f.party.SessionId
		f.world.Self.RaceKnown, f.world.Self.RaceId = true, 1
		f.world.Owner.RaceKnown, f.world.Owner.RaceId = true, 1
		activateEncounterWriter(t, s, f)
		advance(encounterLeaseDuration)
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		request := &actionsv1.ResurrectOnceRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: 3, SpellId: 2006}
		if _, err := s.ResurrectOnce(luaOwnedSupportContext(), request); err != nil || lastCast(f) != 2006 {
			t.Fatal("target owner suppressed exact resurrection", err, s.stateLocked())
		}
	})
}
