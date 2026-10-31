package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

func coldLevelingSetup(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold182Setup(t, 0, 4, 3)
	cold182GoalQuest(f.world).QuestId = 218
	f.world.Self.PositionRevision, s.positionRevision = 7, 7
	return s, f, advance
}

func coldLevelingRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := coldLevelingSetup(t)
	if _, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: "level5-fixture"}); err != nil {
		t.Fatal("synthetic explicit leveling fixture rejected", err)
	}
	return s, f, advance
}

func coldLevelingCastReceipt(t *testing.T, s *Service) {
	t.Helper()
	goPacket := smiteEvent(true, s.castCount, cold182GoalGUID)
	goPacket[1], goPacket[3] = 8, 8
	binary.LittleEndian.PutUint32(goPacket[5:], shadowBolt)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: goPacket})
	if state(t, s).CastPending || state(t, s).ServerAssistGo != 1 {
		t.Fatal("synthetic actual-shaped own cast receipt did not bind", state(t, s))
	}
}

type coldLevelingAmbiguousTransport struct {
	*fakeSession
	beforeCast func()
}

type coldLevelingAfterFacingTransport struct {
	*fakeSession
	afterFacing func()
}

func (f *coldLevelingAfterFacingTransport) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	result, err := f.fakeSession.SendPacket(ctx, r, opts...)
	if err == nil && r.Opcode == opcode.MSGMoveSetFacing && f.afterFacing != nil {
		after := f.afterFacing
		f.afterFacing = nil
		after()
	}
	return result, err
}

type coldLevelingMovementSnapshotTransport struct {
	*fakeSession
	afterSnapshot func()
}

func (f coldLevelingMovementSnapshotTransport) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	result, err := f.fakeSession.InvokeModule(ctx, r, opts...)
	if err == nil && r.Module == "buddy-world" && r.Method == "snapshot" && f.afterSnapshot != nil {
		f.afterSnapshot()
	}
	return result, err
}

func coldLevelingExpireOnMovement(s *Service, f *fakeSession, boundary string, expire func()) *bool {
	fired := false
	if boundary == "clock" {
		f.onClock = func() {
			f.onClock = nil
			fired = true
			expire()
		}
	} else {
		armed := false
		s.session = coldLevelingMovementSnapshotTransport{fakeSession: f, afterSnapshot: func() {
			if armed {
				armed, fired = false, true
				expire()
			}
		}}
		f.onClock = func() {
			f.onClock = nil
			armed = true
		}
	}
	return &fired
}

func (f coldLevelingAmbiguousTransport) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.Opcode == opcode.CMSGCastSpell && f.beforeCast != nil {
		f.beforeCast()
	}
	result, err := f.fakeSession.SendPacket(ctx, r, opts...)
	if r.Opcode == opcode.CMSGCastSpell {
		return nil, errors.New("cast may have reached transport")
	}
	return result, err
}

func TestColdridgeLevelingPracticePlansThenReservesOnePullWithoutCredit(t *testing.T) {
	s, f, _ := coldLevelingRig(t)
	s.Tick(context.Background())
	r := state(t, s).ColdridgeLeveling
	if s.practiceTarget != cold182GoalGUID || !state(t, s).NavAvailable || countPackets(f, opcode.CMSGCastSpell) != 0 || r.PullReserved || r.SourceGuid != 0 {
		t.Fatal("planning invented an owned leveling pull", state(t, s))
	}
	s.Tick(context.Background())
	got := state(t, s)
	r = got.ColdridgeLeveling
	if countPackets(f, opcode.CMSGCastSpell) != 1 || !s.practicePullStarted || got.PracticePulls != 1 || got.PracticeKills != 0 || got.ServerAssistGo != 0 || got.ShadowBoltDamageEvents != 0 || !r.PullReserved || r.SourceGuid != cold182GoalGUID || r.PositionRevision != 7 || r.ObservedLevel != 4 || r.LevelFiveObserved || s.coldridgeGoal.questID != 0 || !s.coldridgeGoal.leveling || coldLevelingQuest(f.world).ObjectiveCounts[0] != 0 || coldLevelingQuest(f.world).Complete {
		t.Fatal("cast invented a kill, level, quest credit or source reservation", got)
	}
	if len(f.lookups) == 0 || f.lookups[len(f.lookups)-1] != cold182GoalGUID {
		t.Fatal("first cast omitted its final exact requested-target snapshot", f.lookups)
	}
}

func coldLevelingDisprove(s *Service, f *fakeSession, mode string) {
	c, q := f.world.NearbyCreatures[0], coldLevelingQuest(f.world)
	switch mode {
	case "level5":
		f.world.Self.Level = 5
	case "unknown_level":
		f.world.Self.LevelKnown = false
	case "removed_with_old_quest":
		q.QuestId = 182
	case "complete":
		q.Complete = true
	case "false_kill_count":
		q.ObjectiveCounts[0] = 1
	case "claimed":
		c.TargetKnown, c.TargetGuid = true, 3
	case "wrong_profile":
		c.MaxHealth = 86
	case "raw_body_outside":
		c.Y = 524.99
	case "destination_outside":
		c.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: c.X, DestinationY: 524.99, DestinationZ: c.Z}
	case "revision":
		f.world.Self.PositionRevision++
	case "map":
		f.world.Self.MapId = 1
	case "health94":
		f.world.Self.Health = 92 // Max98: below the existing95% first-pull gate.
	case "mana59":
		f.world.Self.Power = uint32(manaBudget(f.world.Self, 60) - 1)
	case "unexpected_hostile":
		f.world.NearbyCreatures = append(f.world.NearbyCreatures, &worldv1.CreatureState{Guid: cold182GoalGUID + 1, Present: true, AliveKnown: true, Alive: true, AttackVictimKnown: true, AttackVictimGuid: 8})
	}
}

func TestColdridgeLevelingPracticeRechecksFirstPullAfterPlanAndFacing(t *testing.T) {
	for _, boundary := range []string{"after_plan", "during_facing"} {
		for _, mode := range []string{"level5", "unknown_level", "removed_with_old_quest", "complete", "false_kill_count", "claimed", "wrong_profile", "raw_body_outside", "destination_outside", "revision", "map", "health94", "mana59", "unexpected_hostile"} {
			t.Run(boundary+"/"+mode, func(t *testing.T) {
				s, f, _ := coldLevelingRig(t)
				s.Tick(context.Background())
				faced := false
				if boundary == "during_facing" {
					s.session = &coldLevelingAfterFacingTransport{fakeSession: f, afterFacing: func() {
						faced = true
						coldLevelingDisprove(s, f, mode)
					}}
				} else {
					coldLevelingDisprove(s, f, mode)
				}
				s.Tick(context.Background())
				got := state(t, s)
				if countPackets(f, opcode.CMSGCastSpell) != 0 || got.PracticePulls != 0 || got.ColdridgeLeveling.PullReserved || got.ColdridgeLeveling.SourceGuid != 0 {
					t.Fatal("newer disproof authorized a leveling cast or consumed a pull", got)
				}
				if boundary == "during_facing" && (!faced || countPackets(f, opcode.MSGMoveSetFacing) != 1) {
					t.Fatal("fixture missed the post-facing boundary", got)
				}
				if boundary == "during_facing" && (len(f.lookups) == 0 || f.lookups[len(f.lookups)-1] != cold182GoalGUID) {
					t.Fatal("post-facing disproof skipped the final requested-target read", f.lookups)
				}
				if mode == "level5" && (!got.ColdridgeLeveling.LevelFiveObserved || got.Practicing || got.ColdridgeLeveling.ObservedLevel != 5) {
					t.Fatal("actual level5 was not retained as a strict offense stop", got)
				}
			})
		}
	}
}

func TestColdridgeLevelingLevelFiveStopsEvenAnOwnedFightAtTickAndFinalRead(t *testing.T) {
	for _, boundary := range []string{"tick", "final_read"} {
		t.Run(boundary, func(t *testing.T) {
			s, f, advance := coldLevelingRig(t)
			s.Tick(context.Background())
			s.Tick(context.Background())
			coldLevelingCastReceipt(t, s)
			f.world.Self.UnitFlags = 0x80000
			c := f.world.NearbyCreatures[0]
			c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, 8, 100
			advance(3 * time.Second)
			faced := false
			if boundary == "final_read" {
				s.session = &coldLevelingAfterFacingTransport{fakeSession: f, afterFacing: func() {
					faced = true
					f.world.Self.Level = 5
				}}
			} else {
				f.world.Self.Level = 5
			}
			s.Tick(context.Background())
			got := state(t, s)
			if got.Practicing || countPackets(f, opcode.CMSGCastSpell) != 1 || got.PracticePulls != 1 || !got.ColdridgeLeveling.LevelFiveObserved || got.ColdridgeLeveling.SourceGuid != cold182GoalGUID || !got.ColdridgeLeveling.PullReserved {
				t.Fatal("level5 continued an owned fight or lost its exact cleanup source", got)
			}
			if boundary == "final_read" && (!faced || countPackets(f, opcode.MSGMoveSetFacing) != 2 || len(f.lookups) == 0 || f.lookups[len(f.lookups)-1] != cold182GoalGUID) {
				t.Fatal("fixture missed the next cast's successful-facing refresh")
			}
		})
	}
}

func TestColdridgeLevelingDeathStopsInsteadOfRetargetingAndOrdinaryPracticeCannotReset(t *testing.T) {
	s, f, _ := coldLevelingRig(t)
	until := s.practiceUntil
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || !s.practiceUntil.Equal(until) || !s.coldridgeGoal.leveling {
		t.Fatal("ordinary Practice replaced or renewed the active explicit goal", state(t, s), err)
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	coldLevelingCastReceipt(t, s)
	c := f.world.NearbyCreatures[0]
	c.Health, c.Alive, c.DynamicFlags = 0, false, 1
	other := cold182GoalCreature(3)
	other.Guid++
	other.PositionKnown, other.X, other.Y, other.Z = true, c.X, c.Y, c.Z
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, other)
	s.Tick(context.Background())
	s.Tick(context.Background())
	got := state(t, s)
	if got.Practicing || got.PracticeTargetGuid != 0 || countPackets(f, opcode.CMSGCastSpell) != 1 || got.PracticeKills != 0 || got.PracticePulls != 1 || got.ColdridgeLeveling.SourceGuid != c.Guid || !got.ColdridgeLeveling.PullReserved || !s.practiceUntil.Equal(until) {
		t.Fatal("observed death invented credit or started a second pull", got)
	}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil || state(t, s).ColdridgeLeveling.SourceGuid != c.Guid {
		t.Fatal("normal disable erased the retained source", err, state(t, s))
	}
}

func TestColdridgeLevelingAmbiguousCastConsumesPullAndDeadlineNeverExtends(t *testing.T) {
	s, f, advance := coldLevelingRig(t)
	reservedBeforeWrite := false
	s.session = coldLevelingAmbiguousTransport{fakeSession: f, beforeCast: func() {
		reservedBeforeWrite = s.coldridgeLeveling.pullReserved && s.coldridgeLeveling.sourceGUID == cold182GoalGUID && s.practicePullStarted && s.state.PracticePulls == 1
	}}
	s.Tick(context.Background())
	s.Tick(context.Background())
	got := state(t, s)
	if !reservedBeforeWrite || countPackets(f, opcode.CMSGCastSpell) != 1 || !got.ColdridgeLeveling.PullReserved || got.ColdridgeLeveling.SourceGuid != cold182GoalGUID || got.PracticePulls != 1 {
		t.Fatal("ambiguous cast made a second pull available", got)
	}
	s.Tick(context.Background())
	if state(t, s).Practicing || countPackets(f, opcode.CMSGCastSpell) != 1 || !state(t, s).ColdridgeLeveling.PullReserved {
		t.Fatal("uncertain first cast was retried or unreserved", state(t, s))
	}
	advance(3 * time.Minute)
	s.Tick(context.Background())
	if state(t, s).Practicing || state(t, s).ColdridgeLeveling.RemainingMs != 0 || state(t, s).ColdridgeLeveling.SourceGuid != cold182GoalGUID {
		t.Fatal("expired window renewed offense or erased cleanup history", state(t, s))
	}
}

func TestColdridgeLevelingFinalReadRejectsDeadlineCrossing(t *testing.T) {
	s, f, advance := coldLevelingRig(t)
	s.Tick(context.Background())
	faced := false
	s.session = &coldLevelingAfterFacingTransport{fakeSession: f, afterFacing: func() {
		faced = true
		advance(3 * time.Minute)
	}}
	s.Tick(context.Background())
	if !faced || countPackets(f, opcode.MSGMoveSetFacing) != 1 || state(t, s).Practicing || countPackets(f, opcode.CMSGCastSpell) != 0 || state(t, s).ColdridgeLeveling.PullReserved {
		t.Fatal("successful facing extended an expired offense window", state(t, s))
	}
}

func TestColdridgeLevelingDeadlineAfterReservationPreventsCastPacket(t *testing.T) {
	s, f, _ := coldLevelingRig(t)
	s.Tick(context.Background())
	if s.practiceTarget != cold182GoalGUID || s.coldridgeLeveling.pullReserved {
		t.Fatal("fixture missed the planned but unreserved first pull")
	}
	initialNow, until := s.now(), s.coldridgeLeveling.until
	crossed := false
	s.now = func() time.Time {
		if s.coldridgeLeveling.pullReserved {
			crossed = true
			return until
		}
		return initialNow
	}
	// All final observations and reservation see the original time. The first
	// clock observation after publishing that reservation reaches the deadline.
	s.Tick(context.Background())
	got := state(t, s)
	r := got.ColdridgeLeveling
	if !crossed || countPackets(f, opcode.CMSGCastSpell) != 0 || got.Practicing || got.CastPending || got.PracticePulls != 1 || r == nil || !r.PullReserved || r.SourceGuid != cold182GoalGUID || r.PositionRevision != 7 || r.RemainingMs != 0 {
		t.Fatal("post-reservation expiry sent offense, renewed budget or lost the source", got)
	}
	q := coldLevelingQuest(f.world)
	if got.PracticeKills != 0 || got.ServerAssistStarts != 0 || got.ServerAssistGo != 0 || got.ShadowBoltDamageEvents != 0 || r.ObservedLevel != 4 || r.LevelFiveObserved || f.world.Self.Level != 4 || q.Complete || q.ObjectiveCounts[0] != 0 {
		t.Fatal("a fenced cast invented server, kill, level or quest progress", got, q)
	}
	if len(f.lookups) == 0 || f.lookups[len(f.lookups)-1] != cold182GoalGUID {
		t.Fatal("reservation did not follow the final exact-target observation", f.lookups)
	}
	writes := len(f.packets)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if len(f.packets) != writes || state(t, s).Practicing || state(t, s).ColdridgeLeveling.RemainingMs != 0 {
		t.Fatal("a later tick retried the consumed, expired pull", state(t, s))
	}
}

func TestColdridgeLevelingCannotResumeAfterStatusLostOwnObservations(t *testing.T) {
	s, f, advance := coldLevelingRig(t)
	s.Tick(context.Background())
	s.Tick(context.Background())
	coldLevelingCastReceipt(t, s)
	f.failSnapshot = true
	if state(t, s).ColdridgeLeveling.RemainingMs != 0 {
		t.Fatal("lost own observations did not invalidate the retained run")
	}
	f.failSnapshot = false
	advance(3 * time.Second)
	s.Tick(context.Background())
	if state(t, s).Practicing || countPackets(f, opcode.CMSGCastSpell) != 1 || state(t, s).ColdridgeLeveling.RemainingMs != 0 {
		t.Fatal("a later valid snapshot resumed a disqualified run", state(t, s))
	}
}

func TestColdridgeLevelingMovingTargetCannotExtendThreePlanBudget(t *testing.T) {
	s, f, _ := coldLevelingRig(t)
	plans := 0
	s.planColdridge182Path = func(ctx context.Context, root string, from, to position) ([]position, error) {
		plans++
		return coldStraight(ctx, root, from, to)
	}
	for range 4 {
		s.Tick(context.Background())
		f.world.NearbyCreatures[0].X -= 3
	}
	if plans != 3 || s.practiceReplans != 3 || state(t, s).Practicing || countPackets(f, opcode.CMSGCastSpell) != 0 || state(t, s).ColdridgeLeveling.PullReserved {
		t.Fatal("moving706 extended the existing three-plan budget", plans, state(t, s))
	}
}

func TestColdridgeLevelingMovementDeadlineFencesStartHeartbeatAndFacing(t *testing.T) {
	for _, boundary := range []string{"clock", "fresh_snapshot"} {
		for _, movement := range []string{"start", "heartbeat", "facing"} {
			t.Run(boundary+"/"+movement, func(t *testing.T) {
				s, f, advance := coldLevelingRig(t)
				if movement != "facing" {
					// Synthetic30yd route inside the existing sealed region. It
					// requires actual navigation, rather than immediate cast facing.
					f.world.NearbyCreatures[0].X = s.pos.x - 30
				}
				s.Tick(context.Background())
				if s.practiceTarget != cold182GoalGUID || s.practiceReplans != 1 {
					t.Fatal("fixture did not plan its live706 route", state(t, s))
				}
				if movement == "heartbeat" {
					s.Tick(context.Background())
					if !state(t, s).Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
						t.Fatal("fixture did not begin a valid pre-deadline route", state(t, s))
					}
					advance(200 * time.Millisecond)
				}
				start, heartbeat, facing, stopped := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveSetFacing), countPackets(f, opcode.MSGMoveStop)
				fired := coldLevelingExpireOnMovement(s, f, boundary, func() { advance(s.coldridgeLeveling.until.Sub(s.now())) })
				s.Tick(context.Background())
				if state(t, s).Practicing {
					t.Fatal("a fenced movement or facing cycle did not stop practice")
				}
				s.Tick(context.Background())
				got := state(t, s)
				if !*fired || countPackets(f, opcode.MSGMoveStartForward) != start || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeat || countPackets(f, opcode.MSGMoveSetFacing) != facing || countPackets(f, opcode.CMSGCastSpell) != 0 || got.Practicing || got.Moving || got.ColdridgeLeveling.RemainingMs != 0 || got.ColdridgeLeveling.PullReserved {
					t.Fatal("movement dependency latency escaped the fixed leveling window", got, f.packets)
				}
				if movement == "heartbeat" && countPackets(f, opcode.MSGMoveStop) != stopped+1 {
					t.Fatal("expired movement prevented the one cleanup STOP", got, f.packets)
				}
			})
		}
	}
}

func coldLevelingDisproveFinalMovementWorld(f *fakeSession, mode string) {
	p := f.world.Self
	switch mode {
	case "dead":
		p.Alive, p.Health = false, 0
	case "alive_unknown":
		p.AliveKnown = false
	case "ground_unknown":
		p.MovementFlagsKnown = false
	case "non_ground_movement":
		p.MovementFlags = 0x200000
	case "extra_movement":
		p.ExtraMovementFlags = 1
	case "unsafe_unit_flag":
		p.UnitFlags = 0x4
	case "busy_unit_flag":
		p.UnitFlags = 0x2000
	case "channel_spell":
		p.ChannelSpellKnown, p.ChannelSpellId = true, 1
	case "channel_object":
		p.ChannelObjectKnown, p.ChannelObjectGuid = true, 1
	case "health_unknown":
		p.HealthKnown = false
	case "health_zero":
		// Keep Alive=true deliberately: the last health gate must also reject
		// an incoherent zero-health observation independently of AliveKnown.
		p.Health = 0
	case "speed_unknown":
		p.SpeedsKnown = false
	case "speed3.5":
		p.RunSpeed = 3.5
	case "speedNaN":
		p.RunSpeed = float32(math.NaN())
	case "speedInf":
		p.RunSpeed = float32(math.Inf(1))
	case "combat":
		p.UnitFlags = 0x80000
	}
}

func TestColdridgeLevelingFinalMovementWorldRejectsUnsafeOwnState(t *testing.T) {
	for _, movement := range []string{"start", "heartbeat", "facing"} {
		for _, mode := range []string{"dead", "alive_unknown", "ground_unknown", "non_ground_movement", "extra_movement", "unsafe_unit_flag", "busy_unit_flag", "channel_spell", "channel_object", "health_unknown", "health_zero", "combat", "speed_unknown", "speed3.5", "speedNaN", "speedInf"} {
			t.Run(movement+"/"+mode, func(t *testing.T) {
				s, f, advance := coldLevelingRig(t)
				if movement != "facing" {
					f.world.NearbyCreatures[0].X = s.pos.x - 30
				}
				s.Tick(context.Background())
				if movement == "heartbeat" {
					s.Tick(context.Background())
					if !state(t, s).Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
						t.Fatal("fixture did not start a safe pre-callback route")
					}
					advance(200 * time.Millisecond)
				}
				start, heartbeat, facing, stopped := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveSetFacing), countPackets(f, opcode.MSGMoveStop)
				fired := false
				f.onClock = func() {
					f.onClock = nil
					fired = true
					coldLevelingDisproveFinalMovementWorld(f, mode)
				}
				s.Tick(context.Background())
				got := state(t, s)
				if !fired || countPackets(f, opcode.MSGMoveStartForward) != start || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeat || countPackets(f, opcode.MSGMoveSetFacing) != facing || countPackets(f, opcode.CMSGCastSpell) != 0 || got.Practicing || got.Moving || got.ColdridgeLeveling.PullReserved {
					t.Fatal("last movement world disproof reused the earlier safe body", mode, got, f.packets)
				}
				if movement == "heartbeat" && countPackets(f, opcode.MSGMoveStop) != stopped+1 {
					t.Fatal("unsafe final world prevented the required cleanup STOP", mode, got, f.packets)
				}
			})
		}
	}
}

func TestColdridgeLevelingStartedOwnedFightRetainsCombatMovementException(t *testing.T) {
	s, f, _ := coldLevelingRig(t)
	s.Tick(context.Background())
	s.Tick(context.Background())
	coldLevelingCastReceipt(t, s)
	c := f.world.NearbyCreatures[0]
	c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, 8, 100
	f.world.Self.UnitFlags = 0x80000
	if !s.practicePullStarted || !s.coldridgeLeveling.pullReserved || s.coldridgeLeveling.sourceGUID != c.Guid || !s.coldridgeLevelingMovementAllowedLocked(f.world, s.pos) {
		t.Fatal("a genuine started own fight lost the existing combat exception", state(t, s))
	}
}
