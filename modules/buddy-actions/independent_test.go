package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestIndependentActionsRejectOwnerControlsEvenWithVisibleOwner(t *testing.T) {
	base, f, _ := rig(t)
	s := NewIndependentService(f, 1)
	s.now = base.now
	learn(s)
	f.world.OwnerTarget = &worldv1.CreatureState{Guid: enemyGUID, Present: true}
	for name, call := range map[string]func() error{
		"follow": func() error {
			_, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
			return err
		},
		"healing": func() error {
			_, err := s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
			return err
		},
		"assist": func() error {
			_, err := s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
			return err
		},
		"heal_once": func() error { _, err := s.HealOnce(context.Background(), &emptypb.Empty{}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("owner action accepted: %v", err)
			}
		})
	}
	s.Tick(context.Background())
	got := state(t, s)
	if got.Following || got.Healing || got.Assisting || got.CastPending || len(f.packets) != 0 {
		t.Fatal("independent mode acquired owner control", got, f.packets)
	}
	if s.lastWorld.Owner != nil || s.lastWorld.OwnerTarget != nil || f.world.Owner == nil || f.world.OwnerTarget == nil {
		t.Fatal("mixed-version snapshot did not isolate owner observations")
	}
	for _, toggle := range []func(context.Context, *actionsv1.ToggleRequest) (*actionsv1.State, error){s.Follow, s.Healing, s.Assist} {
		if _, err := toggle(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
			t.Fatalf("explicit disable unavailable: %v", err)
		}
	}
}

func TestIndependentModeRetainsBoundedOwnPractice(t *testing.T) {
	base, f, _ := practiceRig(t, 8)
	s := NewIndependentService(f, 1)
	s.now, s.planPath = base.now, base.planPath
	s.SetNavigationDataDir("/fixture/client-data")
	learnSpells(s, fireball)
	// Even a stale companion world image must not make Joel a combat target.
	f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, TargetGuid: enemyGUID, TargetKnown: true}
	f.world.OwnerTarget = f.world.NearbyCreatures[0]
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	got := state(t, s)
	if !got.Practicing || got.PracticePulls != 1 || lastCast(f) != fireball || got.LastCastTargetGuid != enemyGUID || got.Assisting {
		t.Fatal("own practice lost or owner assist enabled", got, f.packets)
	}
}

func TestIndependentModeRetainsOwnedQuestTravelAndMapBounds(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		base, f, _, request := travelRig(t)
		s := NewIndependentService(f, 1)
		s.now, s.planPath, s.approachDelay = base.now, base.planPath, base.approachDelay
		s.SetNavigationDataDir("/fixture/client-data")
		lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 30000})
		if err != nil {
			t.Fatal(err)
		}
		request.Token = lease.Token
		if unsafe {
			f.world.Self.MapId = 1
		}
		got, err := s.TravelQuest(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if unsafe {
			if countPackets(f, opcode.MSGMoveStartForward) != 0 || got.Traveling || got.Moving {
				t.Fatal("independent mode bypassed map boundary", got, f.packets)
			}
		} else if countPackets(f, opcode.MSGMoveStartForward) == 0 || countPackets(f, opcode.MSGMoveStop) != 1 || got.Traveling || got.Moving || got.InteractionOwner != "quests" {
			t.Fatal("own quest travel did not finish stopped under its lease", got, f.packets)
		}
	}
}

func TestIndependentActionStartupCanUseOwnerGUIDAsSelf(t *testing.T) {
	base, f, _ := rig(t)
	f.world.Self.Guid = 3
	f.world.Owner = nil
	s := NewIndependentService(f, 3)
	s.now = func() time.Time { return base.now() }
	got := state(t, s)
	if got.Position == nil || !got.Position.Known || !got.Position.MapKnown || s.lastWorld.Self.Guid != 3 {
		t.Fatal("actual self identity lost", got)
	}
}
