package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

func selfDefenseRig(t *testing.T, class uint32) (*Service, *fakeSession, *worldv1.CreatureState, func(time.Duration)) {
	t.Helper()
	var s *Service
	var f *fakeSession
	var advance func(time.Duration)
	if class == 4 {
		s, f, advance = rogueRig(t)
		s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 15, inventory: 13, valid: true}
	} else {
		s, f, advance = assistRig(t)
	}
	target := proto.Clone(f.world.OwnerTarget).(*worldv1.CreatureState)
	f.world.Owner, f.world.OwnerTarget, f.party = nil, nil, nil
	f.world.Self.ClassId = class
	f.world.Self.Health = 100
	f.world.Self.UnitFlags = 0x80000
	f.world.Self.LevelKnown, f.world.Self.Level = true, 5
	target.LevelKnown, target.Level = true, 5
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, s.selfGUID, 100
	f.world.NearbyCreatures = []*worldv1.CreatureState{target}
	switch class {
	case 5:
		learnSpells(s, lesserHeal, smite)
	case 8:
		learnSpells(s, fireball)
	case 9:
		learnSpells(s, shadowBolt)
	case 4:
		learnSpells(s, sinisterStrike, autoAttack)
	}
	return s, f, target, advance
}
