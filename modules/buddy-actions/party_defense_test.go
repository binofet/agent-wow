package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
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

func scarletSentryDefenseRig(t *testing.T, class uint32) (*Service, *fakeSession, *worldv1.CreatureState, func(time.Duration)) {
	t.Helper()
	s, f, sentry, advance := partyDefenseRig(t, class)
	arlen := f.world.NearbyPlayers[0]
	f.world.Owner = proto.Clone(arlen).(*worldv1.PlayerState)
	f.world.Owner.Guid = ownerGUID
	f.world.Owner.PlayerFlags = 2 // AFK
	f.world.Owner.UnitFlags &^= 0x80000
	f.world.Owner.TargetKnown, f.world.Owner.TargetGuid = true, 0
	f.party.Group.Members[3].Online = true
	f.world.Self.Level, arlen.Level, f.world.Owner.Level = 25, 25, 25
	sentry.Entry, sentry.Level = 4283, 31 // Scarlet Sentry
	if _, err := s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	return s, f, sentry, advance
}
