package buddyactions

import (
	"context"
	"encoding/binary"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"math"
	"testing"
	"time"
)

func TestStealthNeedsObservedCasterAuraStateAndActualOwnAura(t *testing.T) {
	s, f, _ := rogueRig(t)
	useStarterSemanticPolicy(t, s)
	s.setKnownSpell(1784, true)
	s.snapshot(context.Background())
	f.world.Self.AuraStateKnown = false
	s.snapshot(context.Background())
	if len(s.candidatesLocked(spellcatalog.Stealth)) != 0 {
		t.Fatal("unknown Faerie Fire state accepted")
	}
	f.world.Self.AuraStateKnown = true
	f.world.Self.AuraState = 1 << 11
	s.snapshot(context.Background())
	if len(s.candidatesLocked(spellcatalog.Stealth)) != 0 {
		t.Fatal("Faerie Fire ignored")
	}
	f.world.Self.AuraState = 0
	s.snapshot(context.Background())
	if len(s.candidatesLocked(spellcatalog.Stealth)) != 1 {
		t.Fatal("learned stealth not discovered")
	}
	if s.stealthedLocked() {
		t.Fatal("knowledge claimed stealth")
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(s.selfGUID, s.selfGUID, 2, 1784, 0)})
	if !s.stealthedLocked() {
		t.Fatal("actual stealth aura missing")
	}
	s.setKnownSpell(53, true)
	s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 15, inventory: 13, valid: true, damageKnown: true, damageMin: 20, damageMax: 30}
	s.pos, s.posKnown, s.posMap, s.positionRevision = position{x: 0}, true, 0, f.world.Self.PositionRevision
	target := &worldv1.CreatureState{Guid: enemyGUID, Present: true, MapKnown: true, PositionKnown: true, X: 2,
		HealthKnown: true, Health: 100, MaxHealth: 100, AliveKnown: true, Alive: true, UnitFlagsKnown: true}
	f.world.RequestedCreature = target
	choose := func() uint32 {
		point, _, _ := creaturePosition(target)
		decision := s.starterWeaponDecisionLocked(context.Background(), f.world, target, point)
		if !decision.valid {
			t.Fatal(s.starterSpells.failure())
		}
		return decision.spell.ID
	}
	if id := choose(); id != 53 {
		t.Fatal("behind dagger strike not selected", id)
	}
	target.Orientation = float32(math.Pi)
	if choose() == 53 {
		t.Fatal("Backstab from front")
	}
	target.Orientation = 0
	s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 4, inventory: 13, valid: true}
	if choose() == 53 {
		t.Fatal("Backstab without dagger")
	}
}
func TestFollowUsesActualSlowerStealthSpeed(t *testing.T) {
	s, f, advance, target := followNavRig(t)
	f.world.Self.RunSpeed = 4.9
	start := s.pos
	if !s.followFormationStepLocked(context.Background(), target, .8) {
		t.Fatal(s.state.Reason)
	}
	advance(200 * time.Millisecond)
	if !s.followFormationStepLocked(context.Background(), target, .8) || math.Abs(distance(start, s.pos)-.98) > .002 {
		t.Fatal("ignored observed reduced speed", s.pos, s.state.Reason)
	}
}
func TestImplicitSelfCastNormalizationRequiresActualSelfHitAndExactSpell(t *testing.T) {
	s, _, _ := buffRig(t, 8, 168)
	castBuff(t, s, s.selfGUID, 168)
	p := buffGo(s, s.selfGUID)
	_, _, _, off, _ := castEvent(p)
	targetOffset := off + 8 + 1 + 8 + 1
	binary.LittleEndian.PutUint32(p[targetOffset:], 2)
	p = p[:targetOffset+4]
	p = append(p, 0)
	if !validCastTargetBody(p, off, true, s.selfGUID, true) {
		t.Fatal("normalized implicit self rejected")
	}
	if validCastTargetBody(p, off, true, s.selfGUID, false) {
		t.Fatal("explicit target accepted zero GUID")
	}
	binary.LittleEndian.PutUint64(p[off+9:], 99)
	if validCastTargetBody(p, off, true, s.selfGUID, true) {
		t.Fatal("foreign hit granted implicit self")
	}
	for _, other := range []uint64{s.selfGUID, 99} {
		mixed := append([]byte(nil), p[:off+8]...)
		mixed = append(mixed, 2)
		mixed = binary.LittleEndian.AppendUint64(mixed, s.selfGUID)
		mixed = binary.LittleEndian.AppendUint64(mixed, other)
		mixed = append(mixed, 0)
		mixed = binary.LittleEndian.AppendUint32(mixed, 2)
		mixed = append(mixed, 0)
		if validCastTargetBody(mixed, off, true, s.selfGUID, true) {
			t.Fatal("mixed/duplicate hit list accepted")
		}
	}
}
