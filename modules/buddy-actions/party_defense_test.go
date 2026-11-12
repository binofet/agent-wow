package buddyactions

import (
	"context"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func partyDefenseRig(t *testing.T, class uint32) (*Service, *fakeSession, *worldv1.CreatureState, func(time.Duration)) {
	t.Helper()
	s, f, c, advance := selfDefenseRig(t, class)
	own := uint64(1)
	if class == 8 {
		own = 4
	}
	if class == 4 {
		own = 5
	}
	s.selfGUID, f.world.Self.Guid = own, own
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -9460, 20, 57 // server startup pose
	victim := proto.Clone(f.world.Self).(*worldv1.PlayerState)
	victim.Guid = 1
	if own == 1 {
		victim.Guid = 4
	}
	victim.X, victim.Y, victim.Z = -9830, 125, 5
	f.world.NearbyPlayers = []*worldv1.PlayerState{victim}
	s.pos, s.posKnown, s.posMap, s.positionRevision = position{-9832, 125, 5, 0}, true, 0, f.world.Self.PositionRevision
	c.X, c.Y, c.Z = -9829, 125, 5
	c.AttackVictimGuid, c.AttackAgeMs = victim.Guid, 90000
	c.TargetKnown, c.TargetGuid = true, victim.Guid
	c.UnitFlagsKnown, c.UnitFlags = true, 0x80000
	f.party = &partyv1.StateResponse{SessionId: "current-party-body", Group: &partyv1.Group{Known: true, InGroup: true, Members: []*partyv1.Member{
		{Guid: 1, Online: true, Self: own == 1, Name: "Arlen"}, {Guid: 4, Online: true, Self: own == 4, Name: "Mira"}, {Guid: 5, Online: true, Self: own == 5, Name: "Rook"}, {Guid: 3, Online: false, Name: "Joel"},
	}}}
	return s, f, c, advance
}

func TestPartyDefenseOfflineOwnerUsesActualCurrentFightAndClientPositionForEveryRole(t *testing.T) {
	for class, spell := range map[uint32]uint32{5: smite, 8: fireball, 4: sinisterStrike} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, c, _ := partyDefenseRig(t, class)
			s.Tick(context.Background())
			if lastCast(f) != spell || s.state.LastCastTargetGuid != c.Guid || s.state.AssistAttempts != 1 || s.state.AssistDamageEvents != 0 || s.state.ServerAssistGo != 0 {
				t.Fatal(s.stateLocked(), f.packets)
			}
			if f.world.Self.X != -9460 || len(f.movementObservations) == 0 || f.movementObservations[0].X != -9832 {
				t.Fatal("lost client/server pose distinction", f.world.Self, f.movementObservations)
			}
		})
	}
}

func TestPartyDefenseRejectsLostOrForeignCombatMembershipAndCurrentTargetEvidence(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession, *worldv1.CreatureState){
		"attack stopped":                  func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.AttackVictimKnown = false },
		"target cleared":                  func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.TargetGuid = 0 },
		"other target":                    func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.TargetGuid = 99 },
		"foreign victim":                  func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.AttackVictimGuid = 99; c.TargetGuid = 99 },
		"no target for old attack":        func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.TargetKnown = false },
		"no NPC combat for old attack":    func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.UnitFlags = 0 },
		"no victim combat for old attack": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.NearbyPlayers[0].UnitFlags = 0 },
		"offline victim":                  func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.party.Group.Members[0].Online = false },
		"offline own":                     func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.party.Group.Members[1].Online = false },
		"no party":                        func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.party = nil },
		"foreign map":                     func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.MapId = 1 },
		"unobserved victim":               func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.NearbyPlayers = nil },
		"dead victim":                     func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.NearbyPlayers[0].Alive = false },
		"independent":                     func(s *Service, f *fakeSession, c *worldv1.CreatureState) { s.independent = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, c, _ := partyDefenseRig(t, 8)
			change(s, f, c)
			s.Tick(context.Background())
			if lastCast(f) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal(name, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPartyDefenseChecksProofAgainAfterFacingOrBeforeMeleeMovement(t *testing.T) {
	for _, mode := range []string{"cast_target_clear", "cast_offline", "cast_range", "melee_target_clear", "melee_offline", "melee_range"} {
		t.Run(mode, func(t *testing.T) {
			class := uint32(8)
			if mode == "melee_target_clear" || mode == "melee_offline" || mode == "melee_range" {
				class = 4
			}
			s, f, c, advance := partyDefenseRig(t, class)
			if class == 4 {
				c.X = -9820
				s.SetNavigationDataDir("/verified/test/assets")
				advance(200 * time.Millisecond)
			}
			f.onClock = func() {
				if mode == "cast_range" || mode == "melee_range" {
					c.X = -9700
				} else if mode == "cast_offline" || mode == "melee_offline" {
					f.party.Group.Members[0].Online = false
				} else {
					c.TargetGuid = 0
				}
			}
			s.Tick(context.Background())
			if lastCast(f) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 || s.state.AutoAttackUnconfirmed {
				t.Fatal(mode, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPartyDefenseRogueUsesVerifiedNavWithoutOwnerAndStopsOnProofLoss(t *testing.T) {
	s, f, c, advance := partyDefenseRig(t, 4)
	c.X = -9820
	s.SetNavigationDataDir("/verified/test/assets")
	advance(200 * time.Millisecond)
	plans := 0
	s.planMapPath = func(_ context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
		plans++
		corner := from
		corner.y += 2
		to.y += 2
		return []position{from, corner, to, position{c.X, c.Y, c.Z, 0}}, nil
	}
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if plans != 1 || !s.state.Moving || s.pos.y <= 125 || s.pos.x != -9832 || countPackets(f, opcode.MSGMoveStartForward) != 1 || lastCast(f) != 0 {
		t.Fatal(s.stateLocked(), f.packets)
	}
	c.AttackVictimKnown = false
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal(s.stateLocked(), f.packets)
	}
}

func TestPartyDefenseKeepsHealingPriorityAndBoundedClassMana(t *testing.T) {
	s, f, _, _ := partyDefenseRig(t, 5)
	f.world.NearbyPlayers[0].Health = 40
	if _, err := s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if s.state.HealAttempts != 1 || s.state.AssistAttempts != 0 || s.state.LastCastTargetGuid != 4 {
		t.Fatal(s.stateLocked(), f.packets)
	}
	s, f, _, _ = partyDefenseRig(t, 5)
	f.world.Self.Power = 32
	s.Tick(context.Background())
	if lastCast(f) != 0 || s.state.AssistAttempts != 0 {
		t.Fatal(s.stateLocked())
	}
}

func TestOngoingSelfDefenseBeyondThirtySecondsRequiresCurrentTargetAndBothCombatFlags(t *testing.T) {
	s, f, c, _ := selfDefenseRig(t, 8)
	c.AttackAgeMs = 90000
	c.TargetKnown, c.TargetGuid = true, s.selfGUID
	c.UnitFlagsKnown, c.UnitFlags = true, 0x80000
	s.Tick(context.Background())
	if lastCast(f) != fireball {
		t.Fatal(s.stateLocked(), f.packets)
	}
}

func TestPartyDefenseRefreshesActualResourcesLevelFlagsAndWeaponAfterFacing(t *testing.T) {
	for _, mode := range []string{"mana_drained", "mana_unknown", "power_type", "silenced", "level_changed", "immune", "energy_drained", "energy_unknown", "disarmed", "weapon_removed"} {
		t.Run(mode, func(t *testing.T) {
			class := uint32(8)
			if mode == "energy_drained" || mode == "energy_unknown" || mode == "disarmed" || mode == "weapon_removed" {
				class = 4
			}
			s, f, c, _ := partyDefenseRig(t, class)
			f.onClock = func() {
				switch mode {
				case "mana_drained":
					f.world.Self.Power = 0
				case "mana_unknown", "energy_unknown":
					f.world.Self.PowerKnown = false
				case "power_type":
					f.world.Self.PowerType = 3
				case "silenced":
					f.world.Self.UnitFlags |= 0x2000
				case "level_changed":
					c.Level = 9
				case "immune":
					c.UnitFlags |= 0x100
				case "energy_drained":
					f.world.Self.Power = 44
				case "disarmed":
					f.world.Self.UnitFlags |= 0x200000
				case "weapon_removed":
					f.world.Self.Mainhand = nil
				}
			}
			s.Tick(context.Background())
			allowedAuto := mode == "energy_drained" || mode == "energy_unknown"
			if lastCast(f) != 0 || (countPackets(f, opcode.CMSGAttackswing) != 0) != allowedAuto || s.state.CastPending || s.state.AssistAttempts != 0 || allowedAuto && countPackets(f, opcode.CMSGAttackstop) != 0 {
				t.Fatal(mode, s.stateLocked(), f.packets)
			}
		})
	}
}
