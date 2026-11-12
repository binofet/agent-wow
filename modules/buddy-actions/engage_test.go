package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestEngageNPCExplicitObservedTargetUsesGenericMeleeAndStops(t *testing.T) {
	for _, mode := range []string{"death", "disable", "expiry", "target_removed_after_facing", "defense_priority"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, advance := selfDefenseRig(t, 4)
			f.world.Self.ClassId = 2
			learnSpells(s, autoAttack)
			c.AttackVictimKnown = false // Explicit selection permits an initial attack.
			c.UnitFlagsKnown = true
			s.state.Assisting = false
			got, err := s.EngageNPC(context.Background(), &actionsv1.EngageNPCRequest{Enabled: true, TargetGuid: c.Guid})
			if err != nil || !got.EngageNpc.Active || countPackets(f, opcode.CMSGAttackswing) != 0 {
				t.Fatal(got, err, f.packets)
			}
			expected := c.Guid
			if mode == "target_removed_after_facing" {
				f.onClock = func() { c.Present = false }
			}
			if mode == "defense_priority" {
				threat := proto.Clone(c).(*worldv1.CreatureState)
				threat.Guid++
				threat.AttackVictimKnown = true
				threat.AttackVictimGuid = s.selfGUID
				threat.AttackAgeMs = 0
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, threat)
				expected = threat.Guid
			}
			s.Tick(context.Background())
			want := 1
			if mode == "target_removed_after_facing" {
				want = 0
			}
			if countPackets(f, opcode.CMSGAttackswing) != want || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("explicit melee target failed", s.stateLocked(), f.packets)
			}
			if want == 0 {
				return
			}
			for _, p := range f.packets {
				if p.Opcode == opcode.CMSGAttackswing && binary.LittleEndian.Uint64(p.Payload) != expected {
					t.Fatal("wrong bound target", p)
				}
			}
			if mode == "defense_priority" {
				if !s.engage.active || s.engage.target != c.Guid {
					t.Fatal("defense rewrote explicit intent")
				}
				return
			}
			switch mode {
			case "death":
				c.Alive, c.Health = false, 0
			case "disable":
				_, err = s.EngageNPC(context.Background(), &actionsv1.EngageNPCRequest{})
				if err != nil {
					t.Fatal(err)
				}
			case "expiry":
				advance(lease + time.Second)
			}
			s.Tick(context.Background())
			if s.engage.active || countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGAttackstop) != 1 || s.state.PracticeKills != 0 {
				t.Fatal("engagement did not stop honestly", s.stateLocked(), f.packets)
			}
		})
	}
	s, f, c, _ := selfDefenseRig(t, 4)
	c.Present = false
	if _, err := s.EngageNPC(context.Background(), &actionsv1.EngageNPCRequest{Enabled: true, TargetGuid: c.Guid}); err == nil || len(f.packets) != 0 {
		t.Fatal("unobserved target accepted", err, f.packets)
	}
}
