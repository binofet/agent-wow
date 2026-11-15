package buddyactions

import (
	"context"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestDungeonInterruptedOffenseReselectsLiveAttackerAfterNormalGCD(t *testing.T) {
	for _, tc := range []struct {
		name string
		code byte
	}{
		{"continue same attacker", 40},
		{"move to next attacker", 41},
		{"lost attacker", 40},
		{"unrelated receipt", 41},
		{"retain overworld cooldown", 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, attacker, advance := partyDefenseRig(t, 8)
			ctx := context.Background()
			self, victim := f.world.Self, f.world.NearbyPlayers[0]
			self.ClassId = 9
			if tc.name != "retain overworld cooldown" {
				self.MapId, victim.MapId, attacker.MapId, s.posMap = 33, 33, 33, 33
			}
			learnSpells(s, 705)
			s.Tick(ctx)
			if lastCast(f) != 705 || !s.state.CastPending || s.pendingTarget != attacker.Guid {
				t.Fatal("initial Shadow Bolt did not start", s.stateLocked())
			}
			gcd, firstCount := s.nextAssist, s.castCount
			receipt := healingRejectionPacket(opcode.SMSGCastFailed, s.selfGUID, firstCount, s.pendingSpell)
			receipt.Payload[5] = tc.code
			if tc.name == "unrelated receipt" {
				receipt.Payload[0]++
			}
			s.OnPacket(ctx, receipt)
			if tc.name == "retain overworld cooldown" {
				gcd = s.now().Add(5 * time.Second)
			}
			if s.nextAssist != gcd || s.state.CastPending != (tc.name == "unrelated receipt") {
				t.Fatal("interruption changed the normal GCD or settled an unrelated cast", s.stateLocked())
			}
			advance(time.Second)
			s.Tick(ctx)
			if countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("cast repeated before the normal GCD", s.stateLocked())
			}
			want := attacker.Guid
			switch tc.name {
			case "move to next attacker":
				second := proto.Clone(attacker).(*worldv1.CreatureState)
				second.Guid++
				attacker.Alive, attacker.Health = false, 0
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, second)
				want = second.Guid
			case "lost attacker":
				attacker.AttackVictimKnown, attacker.TargetKnown, attacker.UnitFlags = false, false, 0
				want = 0
			case "unrelated receipt", "retain overworld cooldown":
				want = 0
			}
			advance(time.Second)
			s.Tick(ctx)
			if want == 0 {
				if countPackets(f, opcode.CMSGCastSpell) != 1 {
					t.Fatal("cast repeated without the confirmed interruption and a live attacker", s.stateLocked())
				}
				return
			}
			if countPackets(f, opcode.CMSGCastSpell) != 2 || s.castCount != firstCount+1 || s.pendingTarget != want {
				t.Fatal("no fresh cast on the current attacker within the normal GCD", s.stateLocked(), f.packets)
			}
		})
	}
}
