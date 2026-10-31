package buddyactions

import (
	"context"
	"encoding/binary"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"math"
	"testing"
	"time"
)

func TestStealthNeedsObservedCasterAuraStateAndActualOwnAura(t *testing.T) {
	s, f, _ := rogueRig(t)
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
	s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 15, inventory: 13, valid: true}
	s.pos = position{x: 0}
	target := position{x: 2, o: 0}
	if r := s.weaponStrikeLocked(f.world.Self, target); r.ID != 53 {
		t.Fatal("behind dagger strike not selected", r)
	}
	target.o = float32(math.Pi)
	if r := s.weaponStrikeLocked(f.world.Self, target); r.ID == 53 {
		t.Fatal("Backstab from front")
	}
	target.o = 0
	s.weapons[2092] = weaponProof{entry: 2092, class: 2, subclass: 4, inventory: 13, valid: true}
	if r := s.weaponStrikeLocked(f.world.Self, target); r.ID == 53 {
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
	s.Tick(context.Background())
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
}
