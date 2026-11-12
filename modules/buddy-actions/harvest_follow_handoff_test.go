package buddyactions

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func harvestFollowingRig(t *testing.T) (*Service, *fakeSession) {
	t.Helper()
	s, f, _ := harvestRig(t)
	s.refreshPositionLocked(f.world)
	s.state.Moving = true
	return s, f
}

func TestHarvestFollowHandoffStopsBeforeGrantingOnlyExploration(t *testing.T) {
	for _, rawMoving := range []bool{false, true} {
		s, f := harvestFollowingRig(t)
		until := s.followUntil
		if rawMoving {
			f.world.Self.MovementFlags = 1
			f.world.Owner.MovementFlags, f.world.Owner.PositionAgeMs = 1, 100
		}
		got, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", Harvest: true, LeaseMs: 15000})
		if err != nil || got.Token == "" || got.State.Moving || got.State.MotionUnconfirmed || !got.State.InteractionHarvest || got.State.InteractionOwner != "exploration" || got.State.InteractionLeaseRemainingMs != 15000 || !s.followUntil.Equal(until) {
			t.Fatal("normal following was not safely handed off", got, err)
		}
		if len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStop {
			t.Fatal("handoff must only stop before granting custody", f.packets)
		}
		harvestIntents(t, s)
		renewed, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", Token: got.Token, Harvest: true, LeaseMs: 15000})
		if err != nil || renewed.Token != got.Token || len(f.packets) != 1 {
			t.Fatal("same-token renewal restarted movement", renewed, err, f.packets)
		}
		if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: got.Token}); err != nil || s.state.InteractionOwner != "" || len(f.packets) != 1 {
			t.Fatal("exact cleanup changed stopped movement", err, f.packets)
		}
		harvestIntents(t, s)
	}
}

func TestHarvestFollowHandoffNeverAdmitsOtherMovementOrDefense(t *testing.T) {
	for _, mode := range []string{"not_following", "expired_follow", "cast", "auto_attack", "auto_requested", "auto_unknown", "motion_unknown", "travel", "practice", "recovery", "combat", "injury", "attacker", "unknown_party", "unknown_world", "unsafe_ground"} {
		t.Run(mode, func(t *testing.T) {
			s, f := harvestFollowingRig(t)
			switch mode {
			case "not_following":
				s.state.Following = false
			case "expired_follow":
				s.followUntil = s.now()
			case "cast":
				s.state.CastPending = true
			case "auto_attack":
				s.state.AutoAttacking, s.autoTarget = true, enemyGUID
			case "auto_requested":
				s.state.AutoAttackRequested = true
			case "auto_unknown":
				s.state.AutoAttackUnconfirmed = true
			case "motion_unknown":
				s.state.MotionUnconfirmed = true
			case "travel":
				s.state.Traveling = true
			case "practice":
				s.state.Practicing = true
			case "recovery":
				s.recovery.state.Active = true
			case "combat":
				f.world.Owner.UnitFlags |= 0x80000
			case "injury":
				f.world.Owner.Health = 20
			case "attacker":
				f.world.NearbyCreatures = []*worldv1.CreatureState{harvestAttacker(1)}
			case "unknown_party":
				f.party = nil
			case "unknown_world":
				f.failSnapshot = true
			case "unsafe_ground":
				f.world.Self.MovementFlags = 0x1000
			}
			got, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", Harvest: true})
			if status.Code(err) != codes.FailedPrecondition || got != nil || s.state.InteractionOwner != "" || s.interactionToken != "" || len(f.packets) != 0 {
				t.Fatal("unsafe following granted movement custody or wrote", got, err, f.packets)
			}
		})
	}
}

func TestHarvestFollowHandoffStopFailureNeverGrantsOrRetries(t *testing.T) {
	for _, mode := range []string{"clock", "transport", "last_snapshot"} {
		t.Run(mode, func(t *testing.T) {
			s, f := harvestFollowingRig(t)
			switch mode {
			case "clock":
				f.failClock = true
			case "transport":
				f.failSend = true
			case "last_snapshot":
				f.onClock = func() { f.failSnapshot = true }
			}
			got, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", Harvest: true})
			if status.Code(err) != codes.FailedPrecondition || got != nil || !s.state.MotionUnconfirmed || s.state.InteractionOwner != "" || s.interactionToken != "" || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("ambiguous STOP granted custody", got, err, &s.state, f.packets)
			}
			harvestIntents(t, s)
			writes := len(f.packets)
			f.failClock, f.failSend, f.failSnapshot, f.onClock = false, false, false, nil
			if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", Harvest: true}); err == nil || len(f.packets) != writes {
				t.Fatal("unconfirmed STOP was silently retried", err, f.packets)
			}
		})
	}
}

func TestHarvestFollowHandoffRechecksDangerAndBaselineDuringStop(t *testing.T) {
	for _, mode := range []string{"combat", "attacker", "map", "revision", "party_unavailable", "party_changed"} {
		t.Run(mode, func(t *testing.T) {
			s, f := harvestFollowingRig(t)
			f.onClock = func() {
				switch mode {
				case "combat":
					f.world.Owner.UnitFlags |= 0x80000
				case "attacker":
					f.world.NearbyCreatures = []*worldv1.CreatureState{harvestAttacker(1)}
				case "map":
					f.world.Self.MapId++
				case "revision":
					f.world.Self.PositionRevision++
				case "party_unavailable":
					f.party = nil
				case "party_changed":
					f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: 4, Online: true})
				}
			}
			got, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", Harvest: true})
			if status.Code(err) != codes.FailedPrecondition || got != nil || s.state.InteractionOwner != "" || s.interactionToken != "" || s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || len(f.packets) != 1 {
				t.Fatal("post-clock observation change granted custody", got, err, &s.state, f.packets)
			}
			harvestIntents(t, s)
		})
	}
}

func TestHarvestFollowHandoffKeepsProfessionStationaryAndOtherLeasesUnchanged(t *testing.T) {
	s, f := harvestFollowingRig(t)
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Harvest: true}); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("profession cast lease consumed moving follow", err, f.packets)
	}
	s.state.Moving = false
	f.world.Self.MovementFlags = 1
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", Harvest: true}); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("unowned raw movement became a follow handoff", err, f.packets)
	}
	f.world.Self.MovementFlags = 0
	got, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "professions", Harvest: true})
	if err != nil || got.Token == "" || len(f.packets) != 0 {
		t.Fatal("stationary profession acquisition regressed", got, err)
	}
	s2, f2 := harvestFollowingRig(t)
	ordinary, err := s2.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade"})
	if err != nil || ordinary.State.InteractionHarvest || countPackets(f2, opcode.MSGMoveStop) != 1 {
		t.Fatal("ordinary lease STOP semantics changed", ordinary, err, f2.packets)
	}
}
