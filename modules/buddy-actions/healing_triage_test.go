package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func triagePlayer(f *fakeSession, guid uint64, health uint32) *worldv1.PlayerState {
	p := proto.Clone(f.world.Owner).(*worldv1.PlayerState)
	p.Guid, p.Health, p.X = guid, health, 15
	return p
}

func triageMember(f *fakeSession, guid uint64) {
	f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: guid, Online: true})
}

func healingCastTarget(f *fakeSession) uint64 {
	for i := len(f.packets) - 1; i >= 0; i-- {
		p := f.packets[i]
		if p.Opcode == opcode.CMSGCastSpell && len(p.Payload) > 10 {
			guid, _, _ := readGUID(p.Payload[10:])
			return guid
		}
	}
	return 0
}

func enableTriage(t *testing.T, s *Service) {
	t.Helper()
	learn(s)
	if _, err := s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestHealingTriageSelfWorksWithoutOwnerOrPartyAndDoesNotInventHealth(t *testing.T) {
	s, f, _ := rig(t)
	f.world.Self.Health = 30
	f.world.Self.PlayerFlags = 2 // AFK does not forfeit actual self-preservation.
	f.world.Owner, f.party = nil, nil
	enableTriage(t, s)
	s.Tick(context.Background())
	if healingCastTarget(f) != 1 || state(t, s).LastCastTargetGuid != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("missing independent self heal", state(t, s), f.packets)
	}
	makeGo := func(target uint64) []byte {
		p := smiteEvent(true, 1, target)
		_, _, _, off, _ := castEvent(p)
		binary.LittleEndian.PutUint32(p[off-4:], lesserHeal)
		return p
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: makeGo(ownerGUID)})
	if !state(t, s).CastPending || state(t, s).ServerCastGo != 0 {
		t.Fatal("Joel-target event consumed self cast")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: makeGo(1)})
	if state(t, s).CastPending || state(t, s).ServerCastGo != 1 || f.world.Self.Health != 30 {
		t.Fatal("receipt changed observed health", state(t, s), f.world.Self)
	}
}

func TestHealingTriageUrgentSelfThenLowestObservedRatioAndJoelTie(t *testing.T) {
	for _, mode := range []string{"urgent", "party", "ratio", "tie", "owner_absent", "self_only"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			mira := triagePlayer(f, 4, 15)
			f.world.NearbyPlayers = []*worldv1.PlayerState{mira}
			triageMember(f, 4)
			f.world.Self.Health, f.world.Owner.Health = 60, 20
			want := uint64(4)
			switch mode {
			case "urgent":
				f.world.Self.Health, mira.Health = 49, 1
				want = 1
			case "ratio":
				f.world.Owner.Health, f.world.Owner.MaxHealth = 25, 200
				want = 3
			case "tie":
				f.world.Owner.Health, mira.Health = 50, 50
				want = 3
			case "owner_absent":
				f.world.Owner = nil
			case "self_only":
				f.world.Owner, f.party = nil, nil
				want = 1
			}
			enableTriage(t, s)
			s.Tick(context.Background())
			if healingCastTarget(f) != want {
				t.Fatal(mode, want, state(t, s), f.packets)
			}
		})
	}
}

func TestHealingTriageRequiresActualCurrentOnlinePartyAndPublicTarget(t *testing.T) {
	for _, mode := range []string{"not_member", "offline", "party_unavailable", "unknown_group", "not_grouped", "missing_self", "foreign_self", "duplicate_member", "duplicate_observation", "unobserved", "dead", "health_unknown", "health_overflow", "different_map", "stale_moving", "far", "ghost", "afk", "npc_guid", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			f.world.Owner.Health = 100
			p := triagePlayer(f, 4, 10)
			f.world.NearbyPlayers = []*worldv1.PlayerState{p}
			triageMember(f, 4)
			switch mode {
			case "not_member":
				f.party.Group.Members = f.party.Group.Members[:2]
			case "offline":
				f.party.Group.Members[2].Online = false
			case "party_unavailable":
				f.party = nil
			case "unknown_group":
				f.party.Group.Known = false
			case "not_grouped":
				f.party.Group.InGroup = false
			case "missing_self":
				f.party.Group.Members = f.party.Group.Members[1:]
			case "foreign_self":
				f.party.Group.Members[2].Self = true
			case "duplicate_member":
				f.party.Group.Members = append(f.party.Group.Members, f.party.Group.Members[2])
			case "duplicate_observation":
				f.world.NearbyPlayers = append(f.world.NearbyPlayers, proto.Clone(p).(*worldv1.PlayerState))
			case "unobserved":
				f.world.NearbyPlayers = nil
			case "dead":
				p.Alive = false
			case "health_unknown":
				p.HealthKnown = false
			case "health_overflow":
				p.Health = p.MaxHealth + 1
			case "different_map":
				p.MapId = 1
			case "stale_moving":
				p.MovementFlags, p.PositionAgeMs = 1, 2001
			case "far":
				p.X = 36
			case "ghost":
				p.PlayerFlags = 0x10
			case "afk":
				p.PlayerFlags = 2
			case "npc_guid":
				p.Guid = 0xf130000001000004
			case "oversized":
				for len(f.world.NearbyPlayers) <= 64 {
					f.world.NearbyPlayers = append(f.world.NearbyPlayers, p)
				}
			}
			enableTriage(t, s)
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal(mode, state(t, s), f.packets)
			}
		})
	}
}

func TestHealingTriagePartyLossAndUrgentSelfDuringStopAreRechecked(t *testing.T) {
	for _, mode := range []string{"leave", "range", "mana_unknown", "self_urgent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			learn(s)
			toggle(t, s, true)
			s.Tick(context.Background())
			f.onClock = func() {
				switch mode {
				case "leave":
					f.party.Group.Members[1].Online = false
				case "range":
					f.world.Owner.X = 100
				case "mana_unknown":
					f.world.Self.BaseManaKnown, f.world.Self.BaseMana, f.world.Self.MaxPower = false, 0, 0
				case "self_urgent":
					f.world.Self.Health = 20
				}
			}
			s.HealOnce(context.Background(), &emptypb.Empty{})
			if mode == "self_urgent" {
				if healingCastTarget(f) != 1 {
					t.Fatal("urgent self did not replace ally selection", state(t, s))
				}
			} else if countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("stale party/range authorized cast", mode, state(t, s))
			}
		})
	}
}

func TestHealingTriageAutomaticThresholdManualInjuryAndActualRankCost(t *testing.T) {
	for _, mode := range []string{"automatic85", "automatic84", "manual99", "rank_short", "rank_exact"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			f.world.Owner.Health = 85
			enableTriage(t, s)
			want := false
			switch mode {
			case "automatic84":
				f.world.Owner.Health, want = 84, true
			case "manual99":
				f.world.Owner.Health, want = 99, true
			case "rank_short", "rank_exact":
				f.world.Self.Health, f.world.Self.BaseManaKnown, f.world.Self.BaseMana = 20, true, 100
				f.world.Owner = nil
				rankLearn(s, 2052)
				f.world.Self.Power = 20
				if mode == "rank_exact" {
					f.world.Self.Power, want = 21, true
				}
			}
			if mode == "manual99" {
				s.HealOnce(context.Background(), &emptypb.Empty{})
			} else {
				s.Tick(context.Background())
			}
			if (countPackets(f, opcode.CMSGCastSpell) == 1) != want {
				t.Fatal(mode, state(t, s), f.packets)
			}
			if mode == "rank_exact" && (lastCast(f) != 2052 || healingCastTarget(f) != 1) {
				t.Fatal("actual rank/self binding lost", state(t, s))
			}
		})
	}
}

func TestHealingTriageKeepsLeasePendingCooldownAndNoRetryGates(t *testing.T) {
	s, f, advance := rig(t)
	f.world.Self.Health, f.world.Owner = 20, nil
	enableTriage(t, s)
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "training"})
	if err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	s.HealOnce(context.Background(), &emptypb.Empty{})
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("active interaction bypassed")
	}
	s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "training", Token: lease.Token})
	s.Tick(context.Background())
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("pending heal repeated")
	}
	advance(6 * time.Second)
	s.Tick(context.Background())
	if state(t, s).Healing || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("timeout silently retried", state(t, s))
	}
	s, f, _ = rig(t)
	f.world.Self.Health, f.world.Owner, f.failSend = 20, nil, true
	enableTriage(t, s)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if state(t, s).Healing || countPackets(f, opcode.CMSGCastSpell) != 1 || state(t, s).ServerCastGo != 0 {
		t.Fatal("ambiguous write replayed or confirmed", state(t, s))
	}
}
