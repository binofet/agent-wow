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
	for _, name := range []string{"membership lost", "out of range", "unknown rank", "new channel", "rank unlearned", "visit changed", "party session changed", "patient recovered", "patient died"} {
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
			case "rank unlearned":
				f.onClock = func() { s.setKnownSpell(2050, false) }
			case "visit changed":
				f.onClock = func() { f.world.VisitRevision++ }
			case "party session changed":
				f.onClock = func() { f.party.SessionId = "different-body" }
			case "patient recovered":
				f.onClock = func() { f.world.Owner.Health = f.world.Owner.MaxHealth }
			case "patient died":
				f.onClock = func() { f.world.Owner.Alive, f.world.Owner.Health = false, 0 }
			}
			if _, err := s.CastHeal(ctx, req); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("changed or unknown facts allowed a cast", err, f.packets)
			}
		})
	}
}

func TestHealingUsesIdentityAndVitalsWithoutInferringRange(t *testing.T) {
	for _, mode := range []string{"unknown caster pose", "unknown patient pose", "old moving patient", "inferred caster", "self without pose", "stationary swimmers"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			s.sessionID = f.party.SessionId
			learn(s)
			self, ally := f.world.Self, f.world.Owner
			s.refreshPositionLocked(f.world) // A previously retained position must not revive an invalidated one.
			target, known := ally.Guid, false
			switch mode {
			case "unknown caster pose":
				self.PositionKnown, self.MovementFlagsKnown = false, false
			case "unknown patient pose":
				ally.PositionKnown, ally.MovementFlagsKnown = false, false
			case "old moving patient":
				ally.MovementFlags, ally.PositionAgeMs, ally.X = 1, 3000, 100
			case "inferred caster":
				self.PositionInferred = true
			case "self without pose":
				self.PositionKnown, self.MovementFlagsKnown = false, false
				self.Health, ally.Health, target, known = 20, ally.MaxHealth, self.Guid, true
			case "stationary swimmers":
				self.MovementFlags, ally.MovementFlags, known = swimMovementFlag, swimMovementFlag, true
			}
			view, err := s.HealingOptions(context.Background(), &emptypb.Empty{})
			if err != nil || !view.GetCaster().GetReady() || view.Caster.GetMana() != self.Power || len(view.Spells) != 1 ||
				len(view.Patients) != 1 || view.Patients[0].Guid != target || (view.Patients[0].Distance != nil) != known {
				t.Fatal("pose changed factual healing eligibility", view, err)
			}
			action := runLuaHealing(t, s, false)
			if action["needsApproach"] != nil || lastCast(f) != 2050 || healingCastTarget(f) != target || len(f.packets) != 1 {
				t.Fatal("ordinary triage did not write one exact targeted heal", action, f.packets)
			}
		})
	}
}

func TestMissingCasterPoseDoesNotOverrideHealCustody(t *testing.T) {
	for _, mode := range []string{"stunned", "lost control", "motion owns body", "previous movement", "unknown movement write", "pending cast", "silence", "channel", "opposing faction"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			s.sessionID = f.party.SessionId
			learn(s)
			f.world.Self.PositionKnown, f.world.Self.MovementFlagsKnown = false, false
			switch mode {
			case "stunned":
				f.world.Self.UnitFlags |= 0x40000
			case "lost control":
				s.clientControlLost = true
			case "motion owns body":
				s.bodyMoving = true
			case "previous movement":
				s.state.Moving = true
			case "unknown movement write":
				s.state.MotionUnconfirmed = true
			case "pending cast":
				s.state.CastPending = true
			case "silence":
				f.world.Self.UnitFlags |= 0x2000
			case "channel":
				f.world.Self.ChannelObjectGuid = 9
			case "opposing faction":
				f.party.Group.InGroup = false
				f.world.Self.RaceKnown, f.world.Self.RaceId = true, 1
				f.world.Owner.RaceKnown, f.world.Owner.RaceId = true, 2
			}
			action := luaHealingAction(t, s, healingVM(t, s, ""), false)
			_, err := s.CastHeal(context.Background(), &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 2050})
			if action != nil || status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatal("invalid targeted cast or competing movement", action, err, f.packets)
			}
		})
	}
}
