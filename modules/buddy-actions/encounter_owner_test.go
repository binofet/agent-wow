package buddyactions

import (
	"context"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

var testEncounterKey = strings.Repeat("b", 64)

func encounterHostContext(key string, visit uint64) context.Context {
	ctx := modcb.WithPolicyEncounterAccess(context.Background(), key, visit)
	md, _ := metadata.FromOutgoingContext(ctx)
	return metadata.NewIncomingContext(context.Background(), md)
}

func activateEncounterWriter(t *testing.T, s *Service, f *fakeSession) {
	t.Helper()
	if err := s.UseHostEncounterWriter(testEncounterKey); err != nil {
		t.Fatal(err)
	}
	s.encounterVisit = encounterVisit{revision: 1, mapID: f.world.Self.MapId, bound: true}
	f.world.VisitRevision = 1
	if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
}

func TestHostEncounterWriterRequiresUsableVisitWithoutNativeTargetFallbackOnExpiry(t *testing.T) {
	s, f, _, advance := selfDefenseRig(t, 8)
	s.state.Assisting = false
	if err := s.UseHostEncounterWriter(testEncounterKey); err != nil {
		t.Fatal(err)
	}
	ctx := encounterHostContext(testEncounterKey, 1)
	s.encounterVisit = encounterVisit{revision: 1, bound: true}
	f.world.VisitRevision = 1
	wantInactive := func() {
		t.Helper()
		view := encounterTestRead(t, s, f)
		if !view.GetHostRequested() || view.GetHostOwned() || s.hostEncounterActiveLocked() {
			t.Fatal("configuration or a diagnostic read activated the writer", view)
		}
	}
	wantInactive()
	if _, err := s.RenewPolicyEncounterLease(encounterHostContext(strings.Repeat("a", 64), 1), wrapperspb.UInt64(1)); status.Code(err) != codes.PermissionDenied {
		t.Fatal("a foreign key could acquire the writer", err)
	}
	if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(2)); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("wrong Actions visit could acquire the writer", err)
	}
	f.world.VisitRevision = 2
	if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("wrong World visit could acquire the writer", err)
	}
	f.world.VisitRevision = 1
	if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	if view := encounterTestRead(t, s, f); !view.GetHostOwned() {
		t.Fatal("renewal failed to activate the owner", view)
	}
	s.Tick(ctx)
	assertNoAutonomousOffense(t, s, f)
	advance(encounterLeaseDuration)
	wantInactive()
	s.Tick(context.Background())
	assertNoAutonomousOffense(t, s, f)
}

func TestExactEncounterDispatchRequiresCurrentLeaseAndWorldVisit(t *testing.T) {
	for _, scenario := range []string{"matching active visit", "explicit release", "expired lease", "proposal from old visit", "Actions transferred", "World transferred"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, request, advance := exactOffenseFixture(t)
			activateEncounterWriter(t, s, f)
			proposalVisit := uint64(1)
			switch scenario {
			case "explicit release":
				if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(0)); err != nil {
					t.Fatal(err)
				}
			case "expired lease":
				advance(encounterLeaseDuration)
			case "proposal from old visit":
				s.encounterVisit.revision, f.world.VisitRevision = 2, 2
				if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 2), wrapperspb.UInt64(2)); err != nil {
					t.Fatal(err)
				}
			case "Actions transferred":
				encounterTestPacket(t, s, opcode.SMSGTransferPending, make([]byte, 4))
			case "World transferred":
				f.world.VisitRevision++
			}
			ctx := modcb.WithPolicySpell(context.Background(), strings.Repeat("c", 64))
			ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, proposalVisit)
			md, _ := metadata.FromOutgoingContext(ctx)
			ctx = metadata.NewIncomingContext(context.Background(), md)
			if scenario == "matching active visit" {
				if _, err := s.CastOffense(context.Background(), request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
					t.Fatal("unattributed offense competed with the active writer", err, f.packets)
				}
			}
			got, err := s.CastOffense(ctx, request)
			if scenario == "matching active visit" {
				if err != nil || !got.GetCastPending() || countPackets(f, opcode.CMSGCastSpell) != 1 {
					t.Fatal("active host could not cast", got, err, f.packets)
				}
				advance(encounterLeaseDuration)
				s.Tick(context.Background())
				got, err = s.Status(context.Background(), &emptypb.Empty{})
				if err != nil || !got.GetCastPending() || countPackets(f, opcode.CMSGCastSpell) != 1 {
					t.Fatal("fallback interrupted the outstanding Lua cast", got, err, f.packets)
				}
			} else if status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("obsolete hostile proposal reached the game transport", got, err, f.packets)
			}
		})
	}
}

func TestEncounterLeaseWaitsForNativeCrowdControlAndBlocksNewApproaches(t *testing.T) {
	ctx := context.Background()
	s, f, advance, request := polymorphFixture(t)
	if err := s.UseHostEncounterWriter(testEncounterKey); err != nil {
		t.Fatal(err)
	}
	s.encounterVisit = encounterVisit{revision: 1, bound: true}
	f.world.VisitRevision = 1
	request.Approach = true
	if got, err := s.Polymorph(ctx, request); err != nil || !got.GetCrowdControl().GetActive() {
		t.Fatal("inactive lease rejected native approach", got, err)
	}
	renew := func() error {
		_, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1))
		return err
	}
	if err := renew(); status.Code(err) != codes.FailedPrecondition || s.hostEncounterActiveLocked() {
		t.Fatal("takeover competed with an active native approach", err)
	}
	request.Approach, request.Cancel = false, true
	if got, err := s.Polymorph(ctx, request); err != nil || got.GetCrowdControl().GetActive() {
		t.Fatal("native cleanup did not release movement", got, err)
	}
	if err := renew(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Polymorph(ctx, request); err != nil {
		t.Fatal("idempotent cleanup was blocked by active lease", err)
	}
	request.Cancel = false
	for _, approach := range []bool{false, true} {
		request.Approach = approach
		if _, err := s.Polymorph(ctx, request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
			t.Fatal("untagged crowd control competed with the active writer", err, f.packets)
		}
	}
	advance(encounterLeaseDuration)
	request.Approach = false
	request.TargetGuid++
	f.lookupCreatures[request.TargetGuid] = encounterTestCreature(request.TargetGuid, 0)
	if got, err := s.Polymorph(ctx, request); err != nil || !got.GetCastPending() || lastCast(f) != 118 {
		t.Fatal("fallback did not restore public exact casts", got, err, f.packets)
	}
}

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
		activateEncounterWriter(t, s, f)
		s.Tick(ctx)
		assertNoAutonomousOffense(t, s, f)
	})
	t.Run("current party attacker", func(t *testing.T) {
		s, f, _, _ := partyDefenseRig(t, 8)
		s.state.Assisting = false
		activateEncounterWriter(t, s, f)
		s.Tick(ctx)
		assertNoAutonomousOffense(t, s, f)
	})
	t.Run("self defense and heal interrupt during task custody", func(t *testing.T) {
		s, f, enemy, _ := selfDefenseRig(t, 4)
		s.state.Assisting = false
		s.setKnownSpell(1766, true)
		activateEncounterWriter(t, s, f)
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
	activateEncounterWriter(t, s, f)
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
		s, f, _, advance := partyDefenseRig(t, 5)
		s.state.Assisting = false
		activateEncounterWriter(t, s, f)
		advance(encounterLeaseDuration)
		f.world.NearbyPlayers[0].Health = 40
		s.Tick(ctx)
		if got := state(t, s); got.HealAttempts != 1 || got.AssistAttempts != 0 || lastCast(f) != lesserHeal || got.LastCastTargetGuid != 4 {
			t.Fatal("expired target lease interrupted the separate standing healing lane", got, f.packets)
		}
	})
	t.Run("explicit cast", func(t *testing.T) {
		const frostArmor = 168
		s, f, _ := rig(t)
		activateEncounterWriter(t, s, f)
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
	activateEncounterWriter(t, s, f)
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
