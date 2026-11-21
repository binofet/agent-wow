package buddyactions

import (
	"context"
	"encoding/binary"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"math"
	"testing"
	"time"
)

const enemyGUID = uint64(0xf130000033000001)

func assistRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	f.world.Self.ChannelSpellKnown, f.world.Self.ChannelObjectKnown = true, true
	learn(s)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: []byte{0x49, 2, 0, 0, 0, 0}})
	f.world.Owner.Health = 100
	f.world.Owner.UnitFlags = 0x80000
	f.world.Owner.TargetKnown = true
	f.world.Owner.TargetGuid = enemyGUID
	f.world.OwnerTarget = &worldv1.CreatureState{Guid: enemyGUID, Present: true, MapKnown: true, PositionKnown: true, X: 12, HealthKnown: true, Health: 100, MaxHealth: 100, AliveKnown: true, Alive: true, UnitFlagsKnown: true, DynamicFlagsKnown: true, AttackVictimKnown: true, AttackVictimGuid: 3}
	f.world.RequestedCreature = f.world.OwnerTarget
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
func TestHealingTakesPriorityAndSmiteSharesCooldown(t *testing.T) {
	s, f, advance := assistRig(t)
	f.world.Owner.Health = 50
	runLuaHealing(t, s, false)
	if state(t, s).HealAttempts != 1 || state(t, s).AssistAttempts != 0 {
		t.Fatal(state(t, s))
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: serverSpellPacket(true, 1)})
	f.world.Owner.Health = 100
	gcd := time.Duration(s.learnedContractsLocked()[lesserHeal].GcdMs) * time.Millisecond
	if gcd <= 0 || s.nextHeal != s.now().Add(gcd) || s.nextAssist != s.nextHeal {
		t.Fatal("healing and offense did not share the learned heal's real GCD", s.stateLocked())
	}
	advance(gcd - time.Millisecond)
	s.Tick(context.Background())
	if state(t, s).AssistAttempts != 0 {
		t.Fatal("ignored shared cooldown")
	}
	advance(time.Millisecond)
	if _, err := castSelectedForTest(s, smite, enemyGUID); err != nil {
		t.Fatal(err)
	}
	if state(t, s).AssistAttempts != 1 {
		t.Fatal(state(t, s))
	}
}
func TestRejectedSelectedOffenseDoesNotReplayAndHealingStillRuns(t *testing.T) {
	s, f, advance := assistRig(t)
	if _, err := castSelectedForTest(s, smite, enemyGUID); err != nil {
		t.Fatal(err)
	}
	p := []byte{1, 0x49, 2, 0, 0, 49}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: p})
	advance(6 * time.Second)
	s.Tick(context.Background())
	st := state(t, s)
	if !st.Assisting || st.CastPending || st.AssistAttempts != 1 || st.ServerAssistGo != 0 {
		t.Fatal(st)
	}
	f.world.Owner.Health = 40
	runLuaHealing(t, s, false)
	if lastCast(f) != lesserHeal || healingCastTarget(f) != ownerGUID {
		t.Fatal("offense failure stopped automatic healing", state(t, s), f.packets)
	}
}
func TestAssistStopsAfterLeaseExpiryAndCastTimeout(t *testing.T) {
	s, _, advance := assistRig(t)
	if _, err := castSelectedForTest(s, smite, enemyGUID); err != nil {
		t.Fatal(err)
	}
	advance(6 * time.Second)
	s.Tick(context.Background())
	if st := state(t, s); !st.Assisting || st.CastPending || st.ServerAssistGo != 0 || st.AssistAttempts != 1 {
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

func TestSelectedOffenseUsesElapsedAdvertisedEndpointWithoutInventingObservation(t *testing.T) {
	s, f, _ := assistRig(t)
	target := f.world.OwnerTarget
	target.PositionKnown = false
	target.X = 100
	target.MovementFlags = 0x08000000
	target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 1500, DestinationKnown: true, DestinationX: 12}
	if _, err := castSelectedForTest(s, smite, enemyGUID); err != nil {
		t.Fatal(err)
	}
	if state(t, s).AssistAttempts != 1 || target.X != 100 || target.PositionKnown {
		t.Fatal(state(t, s), target)
	}
	s, f, _ = assistRig(t)
	f.world.OwnerTarget.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 500, DestinationKnown: true, DestinationX: 12}
	if _, err := castSelectedForTest(s, smite, enemyGUID); err != nil {
		t.Fatal(err)
	}
	if state(t, s).AssistAttempts != 1 {
		t.Fatal("finite current moving spline was refused")
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
