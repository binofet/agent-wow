package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
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
	learnSpells(s, lesserHeal, smite, fireball, shadowBolt, sinisterStrike, autoAttack)
	return s, f, target, advance
}

func TestSelfDefenseWithoutOwnerUsesOnlyActualSelfAttackerAndKnownClassAbility(t *testing.T) {
	for class, expected := range map[uint32]uint32{4: sinisterStrike, 5: smite, 8: fireball, 9: shadowBolt} {
		t.Run(fmt.Sprint(class), func(t *testing.T) {
			s, f, target, _ := selfDefenseRig(t, class)
			s.Tick(context.Background())
			got := state(t, s)
			if !got.Assisting || !got.CastPending || got.AssistAttempts != 1 || lastCast(f) != expected || got.LastCastTargetGuid != target.Guid || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(got, f.packets)
			}
			if got.ServerAssistGo != 0 || got.AssistDamageEvents != 0 || got.MeleeDamageEvents != 0 {
				t.Fatal("defensive transport fabricated a server result", got)
			}
		})
	}
}

func TestSelfDefenseChoosesNearestAttackerThenStableGuidAndPrioritizesOwnThreat(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		s, f, target, _ := selfDefenseRig(t, 8)
		far := proto.Clone(target).(*worldv1.CreatureState)
		far.Guid, far.X = enemyGUID+10, 8
		left := proto.Clone(target).(*worldv1.CreatureState)
		left.Guid, left.X = enemyGUID+2, -4
		right := proto.Clone(target).(*worldv1.CreatureState)
		right.Guid, right.X = enemyGUID+3, 4
		selectedOnly := proto.Clone(target).(*worldv1.CreatureState)
		selectedOnly.Guid, selectedOnly.X, selectedOnly.AttackVictimKnown = enemyGUID+1, 1, false
		selectedOnly.TargetKnown, selectedOnly.TargetGuid = true, s.selfGUID
		f.world.NearbyCreatures = []*worldv1.CreatureState{far, right, selectedOnly, left}
		if reverse {
			f.world.NearbyCreatures = []*worldv1.CreatureState{left, selectedOnly, right, far}
		}
		f.world.Owner = &worldv1.PlayerState{Guid: ownerGUID, Present: false, TargetKnown: true, TargetGuid: far.Guid}
		f.world.OwnerTarget = far
		s.Tick(context.Background())
		if got := state(t, s); got.LastCastTargetGuid != left.Guid || got.AssistAttempts != 1 {
			t.Fatal(reverse, got, f.packets)
		}
	}
}

func TestSelfDefenseVictimBindingUsesThisActualBodyRatherThanFixedCompanion(t *testing.T) {
	for _, tc := range []struct{ self, victim uint64 }{{1, 1}, {4, 4}, {5, 5}, {8, 8}, {5, 1}, {8, 3}} {
		base, f, target, _ := selfDefenseRig(t, 5)
		f.world.Self.Guid, target.AttackVictimGuid = tc.self, tc.victim
		s := NewService(f, tc.self)
		s.now = base.now
		learnSpells(s, lesserHeal, smite)
		s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
		s.Tick(context.Background())
		want := uint64(0)
		if tc.self == tc.victim {
			want = 1
		}
		if got := state(t, s); got.AssistAttempts != want || want != 0 && got.LastCastTargetGuid != target.Guid {
			t.Fatal(tc, got, f.packets)
		}
	}
}

func TestSelfDefenseRejectsAnOverboundCreatureObservationList(t *testing.T) {
	s, f, target, _ := selfDefenseRig(t, 8)
	for len(f.world.NearbyCreatures) <= 32 {
		next := proto.Clone(target).(*worldv1.CreatureState)
		next.Guid += uint64(len(f.world.NearbyCreatures))
		f.world.NearbyCreatures = append(f.world.NearbyCreatures, next)
	}
	s.Tick(context.Background())
	if got := state(t, s); got.AssistAttempts != 0 || len(f.packets) != 0 {
		t.Fatal(got, f.packets)
	}
}

func TestSelfDefenseRejectsMissingStaleForeignUnsafeOrUnattackableEvidence(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession, *worldv1.CreatureState){
		"selected target only": func(s *Service, f *fakeSession, c *worldv1.CreatureState) {
			c.AttackVictimKnown, c.TargetKnown, c.TargetGuid = false, true, s.selfGUID
		},
		"other victim": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.AttackVictimGuid = 3 },
		"stale attack": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.AttackAgeMs = 30001 },
		"noncombat stale": func(s *Service, f *fakeSession, c *worldv1.CreatureState) {
			f.world.Self.UnitFlags, c.AttackAgeMs = 0, 5001
		},
		"foreign map":        func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.MapId = 1 },
		"unknown map":        func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.MapKnown = false },
		"absent":             func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.Present = false },
		"dead":               func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.Alive = false },
		"zero health":        func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.Health = 0 },
		"unknown health":     func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.HealthKnown = false },
		"player":             func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.Guid = 3 },
		"unknown position":   func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.PositionKnown = false },
		"active spline":      func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.MovementFlags = 0x08000000 },
		"immune":             func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.UnitFlags = 0x100 },
		"feign death":        func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.UnitFlags_2 = 1 },
		"known high level":   func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.Level = 9 },
		"no spell":           func(s *Service, f *fakeSession, c *worldv1.CreatureState) { learnSpells(s, lesserHeal) },
		"silenced":           func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.UnitFlags |= 0x2000 },
		"heal mana reserve":  func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.Power = 32 },
		"own dead":           func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.Alive = false },
		"own unknown health": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.HealthKnown = false },
		"own foreign guid":   func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.Guid = 4 },
		"own unknown map":    func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.MapKnown = false },
		"own falling":        func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.MovementFlags = 0x1000 },
		"own unsafe flags":   func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.Self.UnitFlags |= 0x40000 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, target, _ := selfDefenseRig(t, 5)
			change(s, f, target)
			s.Tick(context.Background())
			if got := state(t, s); got.AssistAttempts != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || got.Moving {
				t.Fatal(got, f.packets)
			}
		})
	}
}

func TestSelfDefenseFreshAttackCanPrecedeOwnCombatFlagButOngoingFightIsBounded(t *testing.T) {
	for _, tc := range []struct {
		combat bool
		age    uint64
		casts  uint64
	}{{false, 5000, 1}, {false, 5001, 0}, {true, 30000, 1}, {true, 30001, 0}} {
		s, f, target, _ := selfDefenseRig(t, 8)
		if !tc.combat {
			f.world.Self.UnitFlags = 0
		}
		target.AttackAgeMs = tc.age
		s.Tick(context.Background())
		if got := state(t, s); got.AssistAttempts != tc.casts {
			t.Fatal(tc, got, f.packets)
		}
	}
}

func TestSelfDefenseNeverChasesAndRetainsConservativeClassRanges(t *testing.T) {
	for _, tc := range []struct {
		class uint32
		x     float32
	}{{4, 10}, {5, 29}, {8, 34}, {9, 29}} {
		s, f, target, _ := selfDefenseRig(t, tc.class)
		target.X = tc.x
		s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
			t.Fatal("self-defense started a chase")
			return nil, nil
		}
		s.Tick(context.Background())
		if got := state(t, s); got.AssistAttempts != 0 || got.Moving || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
			t.Fatal(tc, got, f.packets)
		}
	}
}

func TestSelfDefenseRetainsExplicitDisableLeaseAndExpiryPriority(t *testing.T) {
	for _, mode := range []string{"disabled", "interaction", "expired"} {
		s, f, _, advance := selfDefenseRig(t, 5)
		switch mode {
		case "disabled":
			s.Assist(context.Background(), &actionsv1.ToggleRequest{})
		case "interaction":
			if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 15000}); err != nil {
				t.Fatal(err)
			}
		case "expired":
			advance(4*time.Hour + time.Second)
		}
		s.Tick(context.Background())
		if got := state(t, s); got.AssistAttempts != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
			t.Fatal(mode, got, f.packets)
		}
	}
}

func TestSelfDefenseLostRogueAttackProofStopsOnceAndWaitsForServer(t *testing.T) {
	s, f, target, _ := selfDefenseRig(t, 4)
	f.world.Self.Power = 0 // Isolate normal autoattack lifecycle.
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal("defense did not start the observed melee target", state(t, s))
	}
	target.AttackVictimKnown = false
	s.Tick(context.Background())
	s.Tick(context.Background())
	if got := state(t, s); !got.AutoAttackUnconfirmed || countPackets(f, opcode.CMSGAttackstop) != 1 || countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal(got, f.packets)
	}
	ack := append(packedGUID(s.selfGUID), packedGUID(target.Guid)...)
	ack = binary.LittleEndian.AppendUint32(ack, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackstop, Payload: ack})
	if got := state(t, s); got.AutoAttackUnconfirmed || got.AutoAttacking || got.AssistDamageEvents != 0 {
		t.Fatal(got)
	}
}

func TestSelfDefenseRogueTargetChangeWaitsForExactOldAttackStop(t *testing.T) {
	s, f, target, _ := selfDefenseRig(t, 4)
	f.world.Self.Power = 0
	s.Tick(context.Background())
	closer := proto.Clone(target).(*worldv1.CreatureState)
	closer.Guid, closer.X = target.Guid+1, 2
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, closer)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackstop) != 1 || countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal(state(t, s), f.packets)
	}
	ack := append(packedGUID(s.selfGUID), packedGUID(target.Guid)...)
	ack = binary.LittleEndian.AppendUint32(ack, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackstop, Payload: ack})
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackswing) != 2 || s.autoTarget != closer.Guid {
		t.Fatal(state(t, s), f.packets)
	}
}

func TestSelfDefenseCastResultAndFailureRemainBoundToActualTarget(t *testing.T) {
	s, f, target, advance := selfDefenseRig(t, 5)
	s.Tick(context.Background())
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: smiteEvent(true, 1, target.Guid+1)})
	if !state(t, s).CastPending {
		t.Fatal("foreign target completed defensive cast")
	}
	failure := []byte{s.castCount}
	failure = binary.LittleEndian.AppendUint32(failure, smite)
	failure = append(failure, 49)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: failure})
	target.AttackVictimKnown = false // A settled rejection alone cannot authorize a new defensive cast.
	advance(6 * time.Second)
	s.Tick(context.Background())
	if got := state(t, s); !got.Assisting || got.AssistAttempts != 1 || got.CastPending || got.ServerAssistGo != 0 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal(got, f.packets)
	}
}
