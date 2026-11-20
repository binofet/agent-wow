package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func exactMeleeFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.MeleeRequest, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	learnSpells(s, autoAttack)
	self := f.world.Self
	self.ChannelObjectKnown, self.ChannelSpellKnown = true, true
	self.BaseAttackTimeKnown, self.BaseAttackTimeMs = true, 2000
	self.CombatReachKnown, self.CombatReach = true, 1.5
	self.Mainhand = &worldv1.EquippedWeapon{
		SlotKnown: true, ItemGuid: 0x4000000000000042, MetadataKnown: true, Entry: 2092,
		DurabilityKnown: true, Durability: 10, MaxDurability: 10,
	}
	s.weaponQueries[2092] = true
	encounterTestPacket(t, s, opcode.SMSGItemQuerySingleResponse, weaponQueryFixture(2092, 15, 13))
	f.world.RequestedCreature = &worldv1.CreatureState{
		Guid: enemyGUID, Present: true, MapKnown: true, PositionKnown: true, X: 4.5,
		HealthKnown: true, Health: 50, MaxHealth: 100, AliveKnown: true, Alive: true, UnitFlagsKnown: true,
		AttackVictimKnown: true, AttackVictimGuid: ownerGUID, CombatReachKnown: true, CombatReach: 1.5,
	}
	encounterTestPacket(t, s, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
	f.world.VisitRevision = 1
	return s, f, &actionsv1.MeleeRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemyGUID, VisitId: 1}, advance
}

func exactMeleeContext() context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("agentwow-policy-spell", strings.Repeat("a", 64)))
}

func acknowledgeExactMeleeStart(t *testing.T, s *Service, target uint64) {
	t.Helper()
	p := binary.LittleEndian.AppendUint64(nil, s.selfGUID)
	encounterTestPacket(t, s, opcode.SMSGAttackstart, binary.LittleEndian.AppendUint64(p, target))
}

func TestExactMeleeStartsOnceAndStopsAcknowledgedOwnTargetAfterLoss(t *testing.T) {
	s, f, request, _ := exactMeleeFixture(t)
	ctx := exactMeleeContext()
	st, err := s.Melee(ctx, request)
	op := st.GetLastMeleeOperation()
	if err != nil || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAttackswing ||
		len(f.packets[0].Payload) != 8 || binary.LittleEndian.Uint64(f.packets[0].Payload) != enemyGUID ||
		!st.GetAutoAttackRequested() || st.GetAutoAttacking() || !op.GetEnabled() || op.GetVisitId() != request.VisitId ||
		!op.GetTransportWritten() || op.GetPhase() != "waiting_server" {
		t.Fatalf("start = %v, %v, packets %v", st, err, f.packets)
	}
	request.Stop = true
	if _, err := s.Melee(ctx, request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("unacknowledged swing authorized a stop", err, f.packets)
	}
	acknowledgeExactMeleeStart(t, s, enemyGUID)
	f.world.RequestedCreature = nil
	request.TargetGuid++
	if _, err := s.Melee(ctx, request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("an unrelated GUID stopped the acknowledged attack", err, f.packets)
	}
	request.TargetGuid = enemyGUID
	st, err = s.Melee(ctx, request)
	op = st.GetLastMeleeOperation()
	if err != nil || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGAttackstop || len(f.packets[1].Payload) != 0 ||
		!st.GetAutoAttacking() || !st.GetAutoAttackUnconfirmed() || op.GetEnabled() || op.GetTargetGuid() != enemyGUID ||
		!op.GetTransportWritten() || op.GetPhase() != "waiting_server" || len(f.lookups) != 1 {
		t.Fatalf("exact stop = %v, %v, packets %v; lookups %v", st, err, f.packets, f.lookups)
	}
}

func TestExactMeleeStartRejectsUnqualifiedRequestWithoutWrites(t *testing.T) {
	for _, name := range []string{
		"no token", "wrong visit", "wrong body", "unknown weapon", "unlearned", "unknown stance", "wrong stance", "control lost",
		"nonfinite reach", "oversized reach", "too far", "behind caster", "unsupported movement", "unsupported target trajectory", "protected",
	} {
		t.Run(name, func(t *testing.T) {
			s, f, request, _ := exactMeleeFixture(t)
			ctx := exactMeleeContext()
			self, target := f.world.Self, f.world.RequestedCreature
			want := codes.FailedPrecondition
			switch name {
			case "no token":
				ctx, want = context.Background(), codes.PermissionDenied
			case "wrong visit":
				f.world.VisitRevision++
			case "wrong body":
				request.SelfGuid++
			case "unknown weapon":
				self.Mainhand.DurabilityKnown = false
			case "unlearned":
				learnSpells(s, smite)
			case "unknown stance":
				self.ShapeshiftFormKnown = false
			case "wrong stance":
				self.ShapeshiftForm = 1
			case "control lost":
				s.clientControlLost = true
			case "nonfinite reach":
				target.CombatReach = float32(math.Inf(1))
			case "oversized reach":
				target.CombatReach = 51
			case "too far":
				target.X = 4.51
			case "behind caster":
				target.X = -3
			case "unsupported movement":
				self.MovementFlags = 0x2000
			case "unsupported target trajectory":
				target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, DestinationKnown: true, DestinationX: 3, TransportRelative: true}
			case "no attack":
				target.AttackVictimKnown = false
			case "foreign party":
				f.party.SessionId += "-old"
			case "protected":
				encounterTestPacket(t, s, opcode.SMSGAuraUpdate, sheepAura(enemyGUID, 9, 118, 20000))
			}
			if _, err := s.Melee(ctx, request); status.Code(err) != want || len(f.packets) != 0 {
				t.Fatalf("unqualified start: %v, packets %v", err, f.packets)
			}
		})
	}
}

func TestExactMeleeStopRequiresItsOwnVisitAndDoesNotRetry(t *testing.T) {
	t.Run("stale own ACK after transfer", func(t *testing.T) {
		s, f, request, _ := exactMeleeFixture(t)
		acknowledgeExactMeleeStart(t, s, enemyGUID)
		encounterTestPacket(t, s, opcode.SMSGNewWorld, make([]byte, 20))
		f.world.VisitRevision, request.VisitId, request.Stop = 2, 2, true
		if _, err := s.Melee(exactMeleeContext(), request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
			t.Fatal("old visit acknowledged a current stop", err, f.packets)
		}
	})
	for _, stop := range []bool{false, true} {
		name := "start"
		if stop {
			name = "stop"
		}
		t.Run("ambiguous "+name, func(t *testing.T) {
			s, f, request, advance := exactMeleeFixture(t)
			if stop {
				acknowledgeExactMeleeStart(t, s, enemyGUID)
			}
			request.Stop, f.failSend = stop, true
			st, err := s.Melee(exactMeleeContext(), request)
			op := st.GetLastMeleeOperation()
			if err != nil || len(f.packets) != 1 || op.GetTransportWritten() || op.GetPhase() != "transport_unconfirmed_no_retry" || !st.GetAutoAttackUnconfirmed() {
				t.Fatalf("ambiguous %s repeated or was confirmed: %v, %v, packets %v", name, st, err, f.packets)
			}
			advance(3 * time.Second)
			if _, err := s.Melee(exactMeleeContext(), request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
				t.Fatalf("ambiguous %s replayed after receipt expiry: %v, %v", name, err, f.packets)
			}
		})
	}
}

func TestExactMeleeWarriorWeaponStances(t *testing.T) {
	for _, form := range []uint32{17, 18, 19} {
		s, f, request, _ := exactMeleeFixture(t)
		f.world.Self.ClassId, f.world.Self.ShapeshiftForm = 1, form
		if _, err := s.Melee(exactMeleeContext(), request); err != nil || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAttackswing {
			t.Fatalf("observed Warrior form %d did not allow exact melee: %v, packets %v", form, err, f.packets)
		}
	}
	s, f, request, _ := exactMeleeFixture(t)
	f.world.Self.ShapeshiftForm = 17
	if _, err := s.Melee(exactMeleeContext(), request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
		t.Fatal("unrelated class acquired a Warrior stance", err, f.packets)
	}
}

func TestExactMeleeStartsWhileCasterAndTargetMoveWithoutStoppingEither(t *testing.T) {
	for _, age := range []uint64{500, 1500} {
		t.Run(encounterGUID(age), func(t *testing.T) {
			s, f, request, _ := exactMeleeFixture(t)
			s.state.Moving = true
			f.world.Self.MovementFlags = 1
			target := f.world.RequestedCreature
			target.X = 3
			target.MovementFlags = 0x08000001
			target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: age, DestinationKnown: true, DestinationX: 4}
			out, err := s.Melee(exactMeleeContext(), request)
			if err != nil || !out.GetMoving() || !out.GetAutoAttackRequested() || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAttackswing || !out.GetLastMeleeOperation().GetTransportWritten() {
				t.Fatalf("ordinary movement prevented exact swing: %+v %v %v", out, err, f.packets)
			}
		})
	}
}

func TestExactMeleeRejectsUnsupportedTrajectoryAndActualControlLoss(t *testing.T) {
	for _, mode := range []string{"cyclic", "transport", "unknown endpoint", "nonfinite endpoint", "self falling", "self swimming", "self control lost"} {
		t.Run(mode, func(t *testing.T) {
			s, f, request, _ := exactMeleeFixture(t)
			target := f.world.RequestedCreature
			target.MovementFlags = 0x08000001
			target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 500, DestinationKnown: true, DestinationX: 3}
			switch mode {
			case "cyclic":
				target.Trajectory.Cyclic = true
			case "transport":
				target.Trajectory.TransportRelative = true
			case "unknown endpoint":
				target.Trajectory.DestinationKnown = false
			case "nonfinite endpoint":
				target.Trajectory.DestinationX = float32(math.Inf(1))
			case "self falling":
				f.world.Self.MovementFlags = 0x2000
			case "self swimming":
				f.world.Self.MovementFlags = 0x200000
			case "self control lost":
				s.clientControlLost = true
			}
			if _, err := s.Melee(exactMeleeContext(), request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatalf("unsupported pose wrote: %v %v", err, f.packets)
			}
		})
	}
}

func TestEncounterMeleeReadinessUsesWrittenPoseDuringMovement(t *testing.T) {
	s, f, request, _ := exactMeleeFixture(t)
	target := f.world.RequestedCreature
	target.X = 13
	target.DynamicFlagsKnown = true
	f.world.NearbyCreatures = []*worldv1.CreatureState{target}
	s.pos = position{x: 10}
	s.posKnown = true
	s.posMap = f.world.Self.MapId
	s.positionRevision = f.world.Self.PositionRevision
	s.state.Moving = true
	f.world.Self.MovementFlags = 1
	f.world.Self.X = 0 // The server's unchanged own entry baseline.
	view := encounterTestRead(t, s, f).Targets[encounterGUID(request.TargetGuid)]
	if view == nil || !view.MeleeReady {
		t.Fatal("readiness used spawn instead of current written pose", view)
	}
	f.world.Self.PositionRevision++ // An actual correction invalidates the estimate.
	view = encounterTestRead(t, s, f).Targets[encounterGUID(request.TargetGuid)]
	if view == nil || view.MeleeReady {
		t.Fatal("stale written pose hid a server correction", view)
	}
}
