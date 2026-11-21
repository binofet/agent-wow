package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestHostStandingHealingOwnsExactTargetAndRank(t *testing.T) {
	for _, host := range []bool{false, true} {
		s, f, _ := rig(t)
		s.sessionID = f.party.SessionId
		s.applySpellbookLocked(map[uint32]bool{2050: true, 2052: true})
		f.world.Self.Health, f.world.Owner.Health = 20, 35
		mira := triagePlayer(f, 4, 65)
		mira.X = 38 // Accessible to Lesser Heal but beyond the old native preference.
		triageMember(f, mira.Guid)
		f.world.NearbyPlayers = []*worldv1.PlayerState{mira}
		f.world.NearbyCreatures = []*worldv1.CreatureState{{Guid: 0xf130000001000001, Present: true, MapKnown: true,
			MapId: mira.MapId, AliveKnown: true, Alive: true, AttackVictimKnown: true, AttackVictimGuid: mira.Guid}}
		if host {
			s.UseHostStandingHealing()
		}
		ctx := context.Background()
		s.Tick(ctx)
		if countPackets(f, opcode.CMSGCastSpell) != 0 {
			t.Fatal("native standing selection competed with attached host", f.packets)
		}
		view, err := s.HealingOptions(ctx, &emptypb.Empty{})
		if err != nil || view.GetHostOwned() != host || !view.GetCaster().GetReady() || view.GetSessionId() != s.sessionID ||
			len(view.GetPatients()) != 3 || len(view.GetSpells()) != 2 || !view.Patients[2].UnderAttack || view.Patients[2].GetDistance() != 38 || view.Spells[0].GetMaxRange() != 40 {
			t.Fatalf("healing facts %+v, %v", view, err)
		}
		wire, err := protojson.Marshal(view)
		var object struct {
			Patients []map[string]any `json:"patients"`
			Spells   []map[string]any `json:"spells"`
		}
		if err != nil || json.Unmarshal(wire, &object) != nil || object.Patients[0]["guid"] != "1" ||
			object.Patients[0]["distance"] != float64(0) || object.Spells[0]["minRange"] != float64(0) {
			t.Fatalf("missing explicit zero facts or string identity in reflected JSON: %s (%v)", wire, err)
		}
		req := &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: mira.Guid, SpellId: 2050}
		st, err := s.CastHeal(ctx, req)
		if err != nil || !st.GetCastPending() || healingCastTarget(f) != mira.Guid || lastCast(f) != 2050 || countPackets(f, opcode.CMSGCastSpell) != 1 {
			t.Fatal("explicit heal was replaced or did not dispatch", st, err, f.packets)
		}
		goPacket := smiteEvent(true, s.castCount, mira.Guid)
		_, _, _, off, _ := castEvent(goPacket)
		binary.LittleEndian.PutUint32(goPacket[off-4:], 2050)
		_, _ = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: goPacket})
		st = state(t, s)
		if st.CastPending || st.ServerCastGo != 1 || mira.Health != 65 {
			t.Fatal("normal receipt not applied or invented HP change", st)
		}
	}
}

func TestExactHealRejectsObservationChangesWithoutSubstitution(t *testing.T) {
	for _, name := range []string{"membership lost", "out of range", "unknown rank", "new channel"} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := rig(t)
			s.sessionID = f.party.SessionId
			learn(s)
			s.UseHostStandingHealing()
			ctx := context.Background()
			f.world.Self.Health = 20
			s.refreshPositionLocked(f.world)
			s.state.Moving = true
			req := &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 2050}
			switch name {
			case "membership lost":
				f.onClock = func() { f.party.Group.Members[1].Online = false }
			case "out of range":
				f.onClock = func() { f.world.Owner.X = 41 }
			case "unknown rank":
				req.SpellId = 2052
			case "new channel":
				f.onClock = func() { f.world.Self.ChannelSpellId = 12051 }
			}
			if _, err := s.CastHeal(ctx, req); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("changed or unknown facts allowed a cast", err, f.packets)
			}
		})
	}
}
