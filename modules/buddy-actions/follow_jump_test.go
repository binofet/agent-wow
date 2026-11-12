package buddyactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestFollowJumpUsesGroundRouteAndKeepsOtherGuards(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		s, f, advance := formationRig(t, 1, 5)
		f.world.Owner.X += 12
		f.world.Owner.Z += 1
		f.world.Owner.MovementFlags = 0x1001
		if blocked {
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				return nil, errors.New("no ground route")
			}
		}
		toggle(t, s, true)
		for i := 0; i < 5; i++ {
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
		}
		if !s.state.Following || (countPackets(f, opcode.MSGMoveStartForward) > 0) == blocked {
			t.Fatal(blocked, s.stateLocked())
		}
		if safePlayers(f.world, 1) == "" {
			t.Fatal("jump relaxed shared combat guard")
		}
		for _, bad := range []uint32{0x200, 0x200000, 0x1000000, 0x2000000} {
			f.world.Owner.MovementFlags = 0x1000 | bad
			if safeFollowPlayers(f.world, 1, true) == "" {
				t.Fatal("unsafe leader mode", bad)
			}
		}
		f.world.Owner.MovementFlags = 0x1000
		f.world.Owner.PositionAgeMs = 2001
		if safeFollowPlayers(f.world, 1, true) != "owner_moving_position_stale" {
			t.Fatal("stale airborne leader")
		}
		f.world.Owner.PositionAgeMs = 0
		f.world.Self.MovementFlags = 0x1000
		if safeFollowPlayers(f.world, 1, true) == "" {
			t.Fatal("airborne follower allowed")
		}
	}
}
