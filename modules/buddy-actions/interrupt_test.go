package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestKickPreemptsDamageOnObservedEnemyHeal(t *testing.T) {
	s, f, _ := rig(t)
	s.setKnownSpell(1766, true)
	f.world.Self.ClassId, f.world.Self.PowerType, f.world.Self.Power = 4, 3, 50
	f.world.Self.UnitFlags = 0x80000
	f.world.Owner.UnitFlags = 0x80000
	enemy := &worldv1.CreatureState{
		Guid: 0xf130000000000001, Present: true, MapKnown: true, X: 3, PositionKnown: true,
		AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, UnitFlagsKnown: true, UnitFlags: 0x80000,
		AttackVictimKnown: true, AttackVictimGuid: f.world.Owner.Guid,
		// The NPC is healing itself, so its current target isn't the party victim.
		TargetKnown: true, TargetGuid: 0xf130000000000001,
		ActiveCast: &worldv1.CreatureCast{SpellId: 12039, RemainingMs: 1500, Revision: 1},
	}
	f.world.NearbyCreatures = []*worldv1.CreatureState{enemy}
	toggle(t, s, true)
	s.nextAssist = s.now().Add(time.Second) // Kick is off the ordinary damage GCD.
	s.Tick(context.Background())

	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell ||
		binary.LittleEndian.Uint32(f.packets[0].Payload[1:]) != 1766 ||
		s.pendingTarget != enemy.Guid || s.state.Reason != "interrupting_enemy_heal" {
		t.Fatalf("expected Kick first, got packets %v and reason %s", f.packets, s.state.Reason)
	}
	s.castFailedLocked(5, "")
	if s.failedAssistTarget != 0 || s.failedAssistCastTarget != 0 {
		t.Fatal("a failed Kick blocked ordinary attacks on the enemy")
	}
	s.Tick(context.Background())
	for _, packet := range f.packets[1:] {
		if packet.Opcode == opcode.CMSGCastSpell && binary.LittleEndian.Uint32(packet.Payload[1:]) == 1766 {
			t.Fatal("replayed an interrupted cast")
		}
	}
}

func TestSkipUnrelatedNonHealingDistantOrEndedCast(t *testing.T) {
	for name, change := range map[string]func(*worldv1.CreatureState){
		"unrelated NPC": func(c *worldv1.CreatureState) { c.AttackVictimGuid = 700 },
		"non-heal":      func(c *worldv1.CreatureState) { c.ActiveCast.SpellId = 133 },
		"distant heal":  func(c *worldv1.CreatureState) { c.X = 30 },
		"ended heal":    func(c *worldv1.CreatureState) { c.ActiveCast.RemainingMs = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := rig(t)
			s.setKnownSpell(1766, true)
			f.world.Self.PowerType = 3
			c := &worldv1.CreatureState{
				Guid: 0xf130000000000001, X: 3, Present: true, PositionKnown: true, MapKnown: true,
				AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, UnitFlagsKnown: true,
				AttackVictimKnown: true, AttackVictimGuid: f.world.Self.Guid,
				ActiveCast: &worldv1.CreatureCast{SpellId: 12039, RemainingMs: 1000, Revision: 1},
			}
			change(c)
			f.world.NearbyCreatures = []*worldv1.CreatureState{c}
			ctx := context.Background()
			w, err := s.snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if s.interruptHealLocked(ctx, w) || len(f.packets) != 0 {
				t.Fatal("interrupt wrote a packet for an ineligible cast")
			}
		})
	}
}
