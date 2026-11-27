package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func mountForTravel(t *testing.T, s *Service, p *worldv1.PlayerState, spell uint32) {
	t.Helper()
	p.UnitFlags |= movementflags.Mounted
	p.RunSpeed = 14
	_, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdateAll, Payload: buffAura(p.Guid, p.Guid, 7, spell, 0)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryGroundMountFollowAndStop(t *testing.T) {
	for _, who := range []string{"self", "owner", "both"} {
		t.Run(who, func(t *testing.T) {
			s, f, advance := formationRig(t, 1, 5)
			f.world.Owner.X += 15
			if who != "owner" {
				mountForTravel(t, s, f.world.Self, 23238)
			}
			if who != "self" {
				mountForTravel(t, s, f.world.Owner, 23240)
			}
			toggle(t, s, true)
			for i := 0; i < 4; i++ {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) == 0 {
				t.Fatal(s.stateLocked(), f.packets)
			}
			if _, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
				t.Fatal(err)
			}
			if s.state.Moving || s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(s.stateLocked())
			}
		})
	}
}

func TestOrdinaryGroundMountTravelTo(t *testing.T) {
	for _, tc := range []struct {
		name               string
		spell, move, other uint32
		goes               bool
	}{
		{"ordinary", 23238, 0, 0, true}, {"mount flag alone", 0, 0, 0, false},
		{"passenger mount", 60424, 0, 0, false}, {"flight mount", 32235, 0, 0, false},
		{"taxi", 23238, 0, 0x00100000, false}, {"transport", 23238, 0x200, 0, false},
		{"swimming", 23238, 0x200000, 0, false}, {"falling", 23238, 0x1000, 0, false}, {"combat", 23238, 0, 0x80000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, r := explorationRig(t)
			mountForTravel(t, s, f.world.Self, tc.spell)
			f.world.Self.MovementFlags = tc.move
			f.world.Self.UnitFlags |= tc.other
			got, err := s.TravelTo(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if (countPackets(f, opcode.MSGMoveStartForward) > 0) != tc.goes || tc.goes && (got.Reason != "exploration_within_distance" || got.Moving || got.MotionUnconfirmed) {
				t.Fatal(got, f.packets)
			}
		})
	}
}
