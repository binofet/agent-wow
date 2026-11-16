package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
)

func TestStandingPartyRestoresDutiesAndResumesAfterFollowStop(t *testing.T) {
	ctx := context.Background()
	s, f, advance := rig(t)
	s.UseStandingPartyRole(true)
	learn(s)
	f.world.Owner.Health = f.world.Owner.MaxHealth
	s.Tick(ctx)
	if view := state(t, s); !view.Following || !view.Healing || !view.Assisting {
		t.Fatalf("duties not armed at first tick: %v", view)
	}

	disabled := &actionsv1.ToggleRequest{Enabled: false}
	s.Follow(ctx, disabled)
	s.Healing(ctx, disabled)
	s.Assist(ctx, disabled)
	s.Tick(ctx)
	if view := state(t, s); view.Following || !view.Healing || !view.Assisting {
		t.Fatalf("short movement hold must not disable support: %v", view)
	}

	advance(3 * time.Second)
	s.Tick(ctx)
	if view := state(t, s); !view.Following || !view.Healing || !view.Assisting {
		t.Fatalf("movement hold did not expire: %v", view)
	}

	f.party = nil
	advance(3 * time.Second)
	s.Tick(ctx)
	if view := state(t, s); view.Following || view.Assisting || !view.Healing {
		t.Fatalf("healer stopped serving nearby allies after leaving party: %v", view)
	}
}

func TestStandingPartyHealsWhenInCombatWithoutChatOrAnEnableCall(t *testing.T) {
	ctx := context.Background()
	s, f, _ := rig(t)
	s.UseStandingPartyRole(true)
	learn(s)
	f.world.Self.UnitFlags |= 0x80000
	f.world.Owner.UnitFlags |= 0x80000
	f.world.Owner.Health = 25
	s.Tick(ctx)
	if got := healingCastTarget(f); got != ownerGUID {
		t.Fatalf("healer did not cast for the injured ally in combat: target=%d, state=%v", got, state(t, s))
	}
}
