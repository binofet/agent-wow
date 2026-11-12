package buddyactions

import (
	"context"
	"errors"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
	"time"
)

func TestAssistDefendsCurrentObservedPartyCompanionsOnly(t *testing.T) {
	for _, mode := range []string{"valid", "fresh_party_only", "stale_party_only", "unavailable", "offline", "missing_owner", "wrong_owner", "duplicate", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := assistRig(t)
			f.world.OwnerTarget.AttackVictimGuid = 4
			f.party = &partyv1.StateResponse{Group: &partyv1.Group{Known: true, InGroup: true, Members: []*partyv1.Member{
				{Guid: 1, Self: true, Online: true, Name: "Arlen"}, {Guid: 3, Online: true, Name: "Joel"}, {Guid: 4, Online: true, Name: "Mira"},
			}}}
			switch mode {
			case "fresh_party_only":
				f.world.Owner.UnitFlags = 0
			case "stale_party_only":
				f.world.Owner.UnitFlags = 0
				f.world.OwnerTarget.AttackAgeMs = 5001
			case "unavailable":
				f.party = nil
			case "offline":
				f.party.Group.Members[2].Online = false
			case "missing_owner":
				f.party.Group.Members = f.party.Group.Members[2:]
			case "wrong_owner":
				f.party.Group.Members[1].Name = "Other"
			case "duplicate":
				f.party.Group.Members = append(f.party.Group.Members, f.party.Group.Members[2])
			case "unrelated":
				f.world.OwnerTarget.AttackVictimGuid = 99
			}
			s.Tick(context.Background())
			if (lastCast(f) != 0) != (mode == "valid" || mode == "fresh_party_only") {
				t.Fatal(mode, state(t, s), f.packets)
			}
		})
	}
}

func TestConfiguredMeleeApproachFollowsVerifiedCornersAndCachesOwnRoute(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		s, f, advance := rogueRig(t)
		f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8900, -140, 82
		f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = -8890, -140, 82
		f.world.OwnerTarget.X, f.world.OwnerTarget.Y, f.world.OwnerTarget.Z = -8885, -140, 82
		verifyWeapon(t, s, f)
		s.SetNavigationDataDir("/verified/client/assets")
		plans := 0
		s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
			plans++
			if blocked {
				return nil, errors.New("verified mesh blocked")
			}
			a, b := from, to
			a.y += 5
			b.y += 5
			return []position{from, a, b, to}, nil
		}
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
		if blocked {
			if countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || s.pos.x != -8900 {
				t.Fatal("failed mesh used direct approach", state(t, s))
			}
		} else if !s.state.Moving || s.pos.x != -8900 || s.pos.y <= -140 || plans != 1 || s.state.Reason != "rogue_approaching_engaged_target_nav" {
			t.Fatal("melee did not retain and follow verified corner", plans, state(t, s))
		}
	}
}
