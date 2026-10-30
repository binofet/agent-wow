package buddyactions

import (
	"context"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func harvestRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Owner.Health = 100
	s.state.Following, s.state.Healing, s.state.Assisting = true, true, true
	s.followUntil, s.healingUntil, s.assistUntil = s.now().Add(lease), s.now().Add(lease), s.now().Add(lease)
	return s, f, advance
}

func harvestAttacker(guid uint64) *worldv1.CreatureState {
	return &worldv1.CreatureState{Guid: enemyGUID, Present: true, MapKnown: true, PositionKnown: true, X: 10, AliveKnown: true, Alive: true, HealthKnown: true, Health: 10, MaxHealth: 10, AttackVictimKnown: true, AttackVictimGuid: guid}
}

func harvestLease(t *testing.T, s *Service, owner string) *actionsv1.InteractionLease {
	t.Helper()
	held, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: owner, Harvest: true, LeaseMs: 6000})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func harvestIntents(t *testing.T, s *Service) {
	t.Helper()
	if !s.state.Following || !s.state.Healing || !s.state.Assisting {
		t.Fatal("harvest changed enabled defense intent", &s.state)
	}
}

func TestHarvestLeaseKeepsIntentsAndBindsActualOnlineParty(t *testing.T) {
	s, f, _ := harvestRig(t)
	f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: 4, Online: true}, &partyv1.Member{Guid: 5})
	held := harvestLease(t, s, "professions")
	if !held.State.InteractionHarvest || !reflect.DeepEqual(held.State.HarvestGuardGuids, []uint64{1, 3, 4}) {
		t.Fatal(held)
	}
	harvestIntents(t, s)
	got, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "professions", Token: held.Token})
	if err != nil || got.InteractionOwner != "" || got.InteractionHarvest || len(got.HarvestGuardGuids) != 0 {
		t.Fatal(got, err)
	}
	harvestIntents(t, s)
}

func TestHarvestRejectsThreatBeforeStoppingDefenseOrGrantingLease(t *testing.T) {
	for _, mode := range []string{"self_combat", "owner_combat", "fresh_attacker", "injury", "autoattack", "raw_movement", "raw_turn", "unknown_party", "foreign_party_self"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			switch mode {
			case "self_combat":
				f.world.Self.UnitFlags = 0x80000
			case "owner_combat":
				f.world.Owner.UnitFlags = 0x80000
			case "fresh_attacker":
				f.world.NearbyCreatures = []*worldv1.CreatureState{harvestAttacker(1)}
			case "injury":
				f.world.Owner.Health = 84
			case "autoattack":
				s.state.AutoAttacking, s.autoTarget = true, enemyGUID
			case "raw_movement":
				f.world.Self.MovementFlags = 1
			case "raw_turn":
				f.world.Self.MovementFlags = 0x10
			case "unknown_party":
				f.party = nil
			case "foreign_party_self":
				f.party.Group.Members[0].Guid = 5
			}
			if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Harvest: true}); status.Code(err) != codes.FailedPrecondition || s.state.InteractionOwner != "" || len(f.packets) != 0 {
				t.Fatal("unsafe harvest interrupted defense", err, &s.state, f.packets)
			}
			harvestIntents(t, s)
		})
	}
}

func TestHarvestBitCannotChangeForOwnedOrForeignToken(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		s, f, _ := harvestRig(t)
		held, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Harvest: guarded})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Token: held.Token, Harvest: !guarded}); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("guard bit changed", err)
		}
		f.world.Self.UnitFlags = 0x80000
		if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Token: "foreign", Harvest: guarded}); status.Code(err) != codes.FailedPrecondition || s.interactionToken != held.Token || s.state.InteractionHarvest != guarded || len(f.packets) != 0 {
			t.Fatal("foreign token revoked another owner", err, &s.state)
		}
	}
	for _, owner := range []string{"quests", "trade", "loot", "mail", "training"} {
		s, _, _ := harvestRig(t)
		if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: owner, Harvest: true}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(owner, err)
		}
	}
}

func TestHarvestRenewRevokesOnlyItsExactLeaseOnDanger(t *testing.T) {
	s, f, _ := harvestRig(t)
	held := harvestLease(t, s, "professions")
	f.world.NearbyCreatures = []*worldv1.CreatureState{harvestAttacker(1)}
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Token: held.Token, Harvest: true}); status.Code(err) != codes.FailedPrecondition || s.state.InteractionOwner != "" || s.state.InteractionHarvest {
		t.Fatal(err, &s.state)
	}
	harvestIntents(t, s)
	f.world.NearbyCreatures = nil
	replacement, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "professions", Token: held.Token}); status.Code(err) != codes.FailedPrecondition || s.interactionToken != replacement.Token || s.state.InteractionOwner != "trade" {
		t.Fatal("late harvest cleanup damaged replacement", err, &s.state)
	}
}

func TestHarvestTickYieldsToActualPreFlagDefenseAndUrgentHealing(t *testing.T) {
	t.Run("defense", func(t *testing.T) {
		s, f, attacker, _ := selfDefenseRig(t, 8)
		f.world.Self.UnitFlags, f.world.NearbyCreatures = 0, nil
		f.party = &partyv1.StateResponse{SessionId: "this-body", Group: &partyv1.Group{Known: true}}
		s.state.Following, s.state.Healing = true, true
		s.followUntil, s.healingUntil = s.now().Add(lease), s.now().Add(lease)
		harvestLease(t, s, "professions")
		f.world.NearbyCreatures = []*worldv1.CreatureState{attacker}
		s.Tick(context.Background())
		if s.state.InteractionOwner != "" || s.state.InteractionHarvest || lastCast(f) != fireball || !s.state.CastPending {
			t.Fatal("harvest suppressed existing defense", &s.state, f.packets)
		}
		harvestIntents(t, s)
	})
	t.Run("heal", func(t *testing.T) {
		s, f, _ := harvestRig(t)
		learn(s)
		harvestLease(t, s, "professions")
		f.world.Owner.Health = 40
		s.Tick(context.Background())
		if s.state.InteractionOwner != "" || healingCastTarget(f) != 3 || !s.state.Healing {
			t.Fatal("harvest suppressed actual healing", &s.state, f.packets)
		}
	})
	t.Run("heal_with_lagging_raw_own_pose", func(t *testing.T) {
		s, f, _ := harvestRig(t)
		learn(s)
		f.world.Owner.X = 40
		harvestLease(t, s, "exploration")
		// Own movement is not echoed by the server: keep the actual revision
		// while the bound client estimate has advanced20yd along its route.
		s.pos.x, s.posEstimated = 20, true
		f.world.Owner.Health = 40
		s.Tick(context.Background())
		if s.state.InteractionOwner != "" || healingCastTarget(f) != 3 || !s.state.Healing {
			t.Fatal("raw own pose hid healing within client range", &s.state, f.packets)
		}
	})
}

func TestHarvestLastMovementReadCatchesAttackStartAfterClock(t *testing.T) {
	for _, moving := range []bool{false, true} {
		s, f, _ := harvestRig(t)
		harvestLease(t, s, "exploration")
		s.mu.Lock()
		s.refreshPositionLocked(f.world)
		s.state.Moving = moving
		f.onClock = func() { f.world.NearbyCreatures = []*worldv1.CreatureState{harvestAttacker(1)} }
		written := s.movementLocked(context.Background(), opcode.MSGMoveHeartbeat, position{x: 1}, true)
		s.mu.Unlock()
		wantStops := 0
		if moving {
			wantStops = 1
		}
		if written || s.state.InteractionOwner != "" || s.state.InteractionHarvest || s.state.Moving || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveStop) != wantStops {
			t.Fatal("last-read attacker allowed movement", moving, &s.state, f.packets)
		}
		harvestIntents(t, s)
	}
}

func TestHarvestFailedStopIsOwnedUnconfirmedAndNeverRetriedOrDisablesIntent(t *testing.T) {
	s, f, advance := harvestRig(t)
	held := harvestLease(t, s, "exploration")
	s.mu.Lock()
	s.refreshPositionLocked(f.world)
	s.state.Moving = true
	s.mu.Unlock()
	f.failSend, f.world.Self.UnitFlags = true, 0x80000
	s.Tick(context.Background())
	if !s.state.MotionUnconfirmed || s.interactionToken != held.Token || s.state.InteractionOwner != "exploration" || !s.state.InteractionHarvest || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(&s.state, f.packets)
	}
	harvestIntents(t, s)
	advance(31 * time.Second)
	s.Tick(context.Background())
	if countPackets(f, opcode.MSGMoveStop) != 1 || state(t, s).InteractionOwner != "exploration" {
		t.Fatal("ambiguous stop was retried or hidden by expiry", &s.state, f.packets)
	}
	harvestIntents(t, s)
}

func TestOrdinaryInteractionDefenseSemanticsRemainUnchanged(t *testing.T) {
	for _, owner := range []string{"professions", "exploration", "trade", "quests"} {
		s, f, _ := harvestRig(t)
		f.party = nil
		held, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: owner})
		if err != nil || held.State.InteractionHarvest || len(held.State.HarvestGuardGuids) != 0 {
			t.Fatal(owner, held, err)
		}
		f.world.Self.UnitFlags = 0x80000
		s.Tick(context.Background())
		if s.interactionToken != held.Token || s.state.InteractionOwner != owner || len(f.packets) != 0 {
			t.Fatal("ordinary lease behavior changed", owner, &s.state, f.packets)
		}
	}
}

func TestHarvestAcquireMovingRejectsBeforeAnyStopOrIntentChange(t *testing.T) {
	s, f, _ := harvestRig(t)
	s.mu.Lock()
	s.refreshPositionLocked(f.world)
	s.state.Moving = true
	s.mu.Unlock()
	f.failSend = true
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Harvest: true}); status.Code(err) != codes.FailedPrecondition || s.state.InteractionOwner != "" || s.state.InteractionHarvest || s.state.MotionUnconfirmed || len(f.packets) != 0 {
		t.Fatal("initial moving acquire wrote STOP or lost intents", err, &s.state, f.packets)
	}
	harvestIntents(t, s)
	// A rejected acquisition created no ambiguous write for a later Tick to
	// consume. Keep its ordinary following movement safe and already settled.
	f.failSend = false
	s.state.Moving = false
	s.lastStep = s.now()
	s.Tick(context.Background())
	harvestIntents(t, s)
}

func TestHarvestExplorationGuardRechecksAfterPlanningWithoutDisablingDefense(t *testing.T) {
	for _, afterPlan := range []bool{false, true} {
		s, f, _, r := explorationRig(t)
		if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
			t.Fatal(err)
		}
		s.state.Following, s.state.Healing, s.state.Assisting = true, true, true
		s.followUntil, s.healingUntil, s.assistUntil = s.now().Add(lease), s.now().Add(lease), s.now().Add(lease)
		r.Token = harvestLease(t, s, "exploration").Token
		inject := func() {
			attacker := harvestAttacker(1)
			attacker.MapId = f.world.Self.MapId
			f.world.NearbyCreatures = []*worldv1.CreatureState{attacker}
		}
		if afterPlan {
			original := s.planMapPath
			s.planMapPath = func(ctx context.Context, root string, m uint32, from, to position) ([]position, error) {
				path, err := original(ctx, root, m, from, to)
				inject()
				return path, err
			}
		} else {
			inject()
		}
		got, err := s.TravelTo(context.Background(), r)
		if err != nil || got.InteractionOwner != "" || got.InteractionHarvest || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
			t.Fatal(afterPlan, got, err, f.packets)
		}
		harvestIntents(t, s)
	}
}

func TestHarvestLastMovementRejectsLastReadUnsafeGroundState(t *testing.T) {
	for _, mode := range []string{"unknown_movement", "swimming", "falling", "extra_flags", "stunned", "unknown_unit_flags"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			harvestLease(t, s, "exploration")
			s.mu.Lock()
			s.refreshPositionLocked(f.world)
			f.onClock = func() {
				switch mode {
				case "unknown_movement":
					f.world.Self.MovementFlagsKnown = false
				case "swimming":
					f.world.Self.MovementFlags = 0x200000
				case "falling":
					f.world.Self.MovementFlags = 0x1000
				case "extra_flags":
					f.world.Self.ExtraMovementFlags = 1
				case "stunned":
					f.world.Self.UnitFlags = 0x40000
				case "unknown_unit_flags":
					f.world.Self.UnitFlagsKnown = false
				}
			}
			written := s.movementLocked(context.Background(), opcode.MSGMoveStartForward, position{x: 1}, true)
			s.mu.Unlock()
			if written || s.state.InteractionOwner != "" || len(f.packets) != 0 {
				t.Fatal("last unsafe world allowed movement", &s.state, f.packets)
			}
			harvestIntents(t, s)
		})
	}
}

func TestHarvestRenewRefreshesMembershipWithoutInventingOnlineParty(t *testing.T) {
	s, f, _ := harvestRig(t)
	held := harvestLease(t, s, "professions")
	f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: 4, Online: true}, &partyv1.Member{Guid: 5})
	renewed, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Token: held.Token, Harvest: true})
	if err != nil || !reflect.DeepEqual(renewed.State.HarvestGuardGuids, []uint64{1, 3, 4}) || renewed.Token != held.Token {
		t.Fatal(renewed, err)
	}
	f.party.Group.Members[2].Online = false
	renewed, err = s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Token: held.Token, Harvest: true})
	if err != nil || !reflect.DeepEqual(renewed.State.HarvestGuardGuids, []uint64{1, 3}) {
		t.Fatal("offline member retained as current party proof", renewed, err)
	}
}
