package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const enemyGUID = uint64(0xf130000033000001)

func assistRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	s, f, advance := rig(t)
	learn(s)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: []byte{0x49, 2, 0, 0}})
	f.world.Owner.Health = 100
	f.world.Owner.UnitFlags = 0x80000
	f.world.Owner.TargetKnown = true
	f.world.Owner.TargetGuid = enemyGUID
	f.world.OwnerTarget = &worldv1.CreatureState{Guid: enemyGUID, Present: true, MapKnown: true, PositionKnown: true, X: 12, HealthKnown: true, Health: 100, MaxHealth: 100, AliveKnown: true, Alive: true, AttackVictimKnown: true, AttackVictimGuid: 3}
	_, _ = s.Assist(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	return s, f, advance
}
func smiteEvent(goEvent bool, count byte, target uint64) []byte {
	p := append(packedGUID(1), packedGUID(1)...)
	p = append(p, count)
	header := make([]byte, 12)
	binary.LittleEndian.PutUint32(header, smite)
	p = append(p, header...)
	if goEvent {
		p = append(p, 1)
		p = binary.LittleEndian.AppendUint64(p, target)
		p = append(p, 0)
	}
	p = append(p, 2, 0, 0, 0)
	return append(p, packedGUID(target)...)
}
func TestSmiteFacesObservedEnemyAndWaitsForMatchingServerCast(t *testing.T) {
	s, f, _ := assistRig(t)
	s.Tick(context.Background())
	st := state(t, s)
	if !st.Assisting || !st.KnownSmite || !st.CastPending || st.AssistAttempts != 1 || st.ServerAssistGo != 0 || st.HealAttempts != 0 || len(f.packets) != 2 || f.packets[0].Opcode != opcode.MSGMoveSetFacing || f.packets[1].Opcode != opcode.CMSGCastSpell {
		t.Fatal(st, f.packets)
	}
	if binary.LittleEndian.Uint32(f.packets[1].Payload[1:]) != 585 {
		t.Fatal("wrong offensive spell")
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: smiteEvent(true, 1, ownerGUID)})
	if !state(t, s).CastPending {
		t.Fatal("wrong target consumed cast")
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellStart, Payload: smiteEvent(false, 1, enemyGUID)})
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: smiteEvent(true, 1, enemyGUID)})
	st = state(t, s)
	if st.CastPending || st.ServerAssistStarts != 1 || st.ServerAssistGo != 1 || st.ServerCastGo != 0 {
		t.Fatal(st)
	}
}
func TestSmiteRequiresCombatProofHealthyOwnerManaAndAttackableTarget(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession){
		"no spell":       func(s *Service, f *fakeSession) { s.state.KnownSmite = false },
		"low health":     func(s *Service, f *fakeSession) { f.world.Owner.Health = 84 },
		"unknown health": func(s *Service, f *fakeSession) { f.world.Owner.HealthKnown = false },
		"reserve":        func(s *Service, f *fakeSession) { f.world.Self.Power = 32 }, // ceil9%128+ceil16%128=33
		"no victim":      func(s *Service, f *fakeSession) { f.world.OwnerTarget.AttackVictimKnown = false },
		"selected target only": func(s *Service, f *fakeSession) {
			f.world.OwnerTarget.AttackVictimKnown = false
			f.world.OwnerTarget.TargetKnown = true
			f.world.OwnerTarget.TargetGuid = 3
		},
		"other victim":  func(s *Service, f *fakeSession) { f.world.OwnerTarget.AttackVictimGuid = 99 },
		"no combat":     func(s *Service, f *fakeSession) { f.world.Owner.UnitFlags = 0 },
		"old combat":    func(s *Service, f *fakeSession) { f.world.OwnerTarget.AttackAgeMs = 30001 },
		"dead":          func(s *Service, f *fakeSession) { f.world.OwnerTarget.Alive = false },
		"wrong target":  func(s *Service, f *fakeSession) { f.world.Owner.TargetGuid++ },
		"far":           func(s *Service, f *fakeSession) { f.world.OwnerTarget.X = 29 },
		"active spline": func(s *Service, f *fakeSession) { f.world.OwnerTarget.MovementFlags = 0x08000000 },
		"immune":        func(s *Service, f *fakeSession) { f.world.OwnerTarget.UnitFlags = 0x100 },
		"feign death":   func(s *Service, f *fakeSession) { f.world.OwnerTarget.UnitFlags_2 = 1 },
		"silenced":      func(s *Service, f *fakeSession) { f.world.Self.UnitFlags = 0x2000 },
		"AFK":           func(s *Service, f *fakeSession) { f.world.Owner.PlayerFlags = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := assistRig(t)
			change(s, f)
			s.Tick(context.Background())
			if state(t, s).AssistAttempts != 0 || len(f.packets) != 0 {
				t.Fatal("unsafe offensive cast", state(t, s))
			}
		})
	}
}
func TestHealingTakesPriorityAndSmiteSharesCooldown(t *testing.T) {
	s, f, advance := assistRig(t)
	f.world.Owner.Health = 50
	_, _ = s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	s.Tick(context.Background())
	if state(t, s).HealAttempts != 1 || state(t, s).AssistAttempts != 0 {
		t.Fatal(state(t, s))
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: serverSpellPacket(true, 1)})
	f.world.Owner.Health = 100
	advance(1600 * time.Millisecond)
	s.Tick(context.Background())
	if state(t, s).AssistAttempts != 0 {
		t.Fatal("ignored shared cooldown")
	}
	advance(1500 * time.Millisecond)
	s.Tick(context.Background())
	if state(t, s).AssistAttempts != 1 {
		t.Fatal(state(t, s))
	}
}
func TestSmiteFailurePausesSameTargetWithoutDisablingHealing(t *testing.T) {
	s, _, advance := assistRig(t)
	_, _ = s.Healing(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	s.Tick(context.Background())
	p := []byte{1, 0x49, 2, 0, 0, 49}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: p})
	advance(6 * time.Second)
	s.Tick(context.Background())
	st := state(t, s)
	if !st.Healing || !st.Assisting || st.CastPending || st.AssistAttempts != 1 || st.ServerAssistGo != 0 {
		t.Fatal(st)
	}
}
func TestAssistStopsAfterLeaseExpiryAndCastTimeout(t *testing.T) {
	s, _, advance := assistRig(t)
	s.Tick(context.Background())
	advance(6 * time.Second)
	s.Tick(context.Background())
	if st := state(t, s); st.Assisting || st.CastPending || st.ServerAssistGo != 0 {
		t.Fatal(st)
	}
	s, _, advance = assistRig(t)
	advance(4*time.Hour + time.Second)
	s.Tick(context.Background())
	if state(t, s).Assisting {
		t.Fatal("assist intent never expired")
	}
}

func TestUnsafeTickStopsAtLatestAuthoritativeCorrection(t *testing.T) {
	s, f, advance := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	f.world.Self.X = 100
	f.world.Self.PositionRevision++
	f.world.Self.MovementFlags = 0x1000
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	p := f.packets[len(f.packets)-1]
	_, n, err := readGUID(p.Payload)
	if err != nil || p.Opcode != opcode.MSGMoveStop || math.Float32frombits(binary.LittleEndian.Uint32(p.Payload[n+10:])) != 100 {
		t.Fatal("unsafe pause wrote superseded coordinates", p)
	}
}

func TestSmiteUsesElapsedAdvertisedEndpointWithoutInventingObservation(t *testing.T) {
	s, f, _ := assistRig(t)
	target := f.world.OwnerTarget
	target.PositionKnown = false
	target.X = 100
	target.MovementFlags = 0x08000000
	target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 1500, DestinationKnown: true, DestinationX: 12}
	s.Tick(context.Background())
	if state(t, s).AssistAttempts != 1 || target.X != 100 || target.PositionKnown {
		t.Fatal(state(t, s), target)
	}
	s, f, _ = assistRig(t)
	f.world.OwnerTarget.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 500, DestinationKnown: true, DestinationX: 12}
	s.Tick(context.Background())
	if state(t, s).AssistAttempts != 0 {
		t.Fatal("cast on an unarrived path")
	}
}

func TestOwnSmiteDamageCounterRequiresActualServerDamage(t *testing.T) {
	s, _, _ := assistRig(t)
	packet := append(packedGUID(enemyGUID), packedGUID(1)...)
	tail := make([]byte, 32)
	binary.LittleEndian.PutUint32(tail, smite)
	binary.LittleEndian.PutUint32(tail[4:], 15)
	tail[12] = 2
	packet = append(packet, tail...)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: packet})
	if st := state(t, s); st.SmiteDamageEvents != 1 || st.SmiteDamageTotal != 15 || st.ServerAssistGo != 0 {
		t.Fatal(st)
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: packet[:len(packet)-1]})
	if st := state(t, s); st.SmiteDamageEvents != 1 || st.MalformedPackets != 1 {
		t.Fatal(st)
	}
}

func TestExpiredApproachLeaseStopsBeforeIntentResumes(t *testing.T) {
	s, f, advance := rig(t)
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s.state.Moving = true
	advance(1100 * time.Millisecond)
	if st := state(t, s); st.InteractionLeaseRemainingMs != 0 || st.InteractionOwner != "loot" {
		t.Fatal("status discarded moving lease", st)
	}
	s.Tick(context.Background())
	if state(t, s).Moving || state(t, s).InteractionOwner != "" || len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStop {
		t.Fatal(lease, state(t, s), f.packets)
	}
}
