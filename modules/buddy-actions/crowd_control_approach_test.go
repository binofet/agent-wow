package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPolymorphApproachStopsAtVisibleRangeAndWaitsForExactCommit(t *testing.T) {
	ctx := context.Background()
	s, f, advance, req := polymorphFixture(t)
	f.lookupCreatures[sheepTarget].X = 40
	s.crowdControlSight = func(_ context.Context, _ string, _ uint32, from, _ position) (bool, error) {
		return from.x >= 19, nil // Turn the corner before gaining spell LOS.
	}
	req.Approach = true
	st, err := s.Polymorph(ctx, req)
	if err != nil || !st.GetCrowdControlApproachSupported() || !st.GetCrowdControl().GetActive() || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatalf("prepare: %v %v", st, err)
	}
	for range 30 {
		advance(200 * time.Millisecond)
		s.Tick(ctx)
		if s.crowdControl.phase == "ready" {
			break
		}
	}
	if s.crowdControl.phase != "ready" || s.pos.x < 19 || s.pos.x >= 23 || s.state.Moving ||
		countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatalf("did not stop after the corner without casting: %v, pos %v", s.stateLocked(), s.pos)
	}
	req.Approach = false
	st, err = s.Polymorph(ctx, req)
	if err != nil || st.GetCrowdControl().GetPhase() != "cast_dispatched" || st.GetCrowdControl().GetActive() ||
		!st.GetLastSpellOperation().GetTransportWritten() || st.GetLastSpellOperation().GetEffectObserved() {
		t.Fatalf("commit: %v %v", st, err)
	}
	_, _ = s.Polymorph(ctx, req)
	if countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.MSGMoveSetFacing) != 1 {
		t.Fatalf("commit was duplicated or caster did not face target: %v", f.packets)
	}
}

func TestPolymorphApproachFindsClearStandOffWithoutRouteToCreature(t *testing.T) {
	ctx := context.Background()
	s, f, advance, req := polymorphFixture(t)
	f.lookupCreatures[sheepTarget].X = 40
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if to.x >= 35 {
			return nil, errors.New("no walkable route to the creature")
		}
		return []position{from, to}, nil
	}
	s.crowdControlSight = func(_ context.Context, _ string, _ uint32, from, _ position) (bool, error) {
		return from.y > 4, nil // Only one side of the pillar has LOS.
	}
	req.Approach = true
	if _, err := s.Polymorph(ctx, req); err != nil {
		t.Fatal(err)
	}
	for range 35 {
		advance(200 * time.Millisecond)
		s.Tick(ctx)
		if s.crowdControl.phase == "ready" {
			break
		}
	}
	if s.crowdControl.phase != "ready" || s.pos.y <= 4 || s.pos.x >= 35 || s.state.Moving ||
		countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatalf("did not reach a clear stand-off position: %v, pos %v", s.stateLocked(), s.pos)
	}
	req.Approach = false
	st, err := s.Polymorph(ctx, req)
	if err != nil || st.GetCrowdControl().GetPhase() != "cast_dispatched" || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatalf("commit: %v %v", st, err)
	}
}

func TestSapApproachStealthFlanksMovingTargetAndCastsFromRear(t *testing.T) {
	ctx := context.Background()
	s, f, req := sapFixture(t)
	now := s.now()
	s.now = func() time.Time { return now }
	target := f.lookupCreatures[sheepTarget]
	target.X = 20
	target.Trajectory = &worldv1.CreatureTrajectory{
		Known: true, DurationMs: 1400, AgeMs: 50, DestinationKnown: true, DestinationX: 14,
	}
	f.world.Self.RunSpeed = 4.9
	s.controlTemplates = map[uint32]controlTemplate{555: {creatureType: 7}}
	s.setKnownSpell(1784, true)
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(1, 1, 1, 0, 0))
	f.world.Self.ShapeshiftForm = 0
	s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return true, nil }
	req.Approach = true
	if _, err := s.Sap(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.Tick(ctx)
	if s.crowdControl.phase != "waiting_for_stealth" || len(f.packets) != 1 || binary.LittleEndian.Uint32(f.packets[0].Payload[1:]) != 1784 {
		t.Fatalf("did not request normal self Stealth: %v", s.stateLocked())
	}
	goPacket := polymorphGo(s.castCount, 1, 1, true)
	binary.LittleEndian.PutUint32(goPacket[2*len(packedGUID(1))+1:], 1784)
	emitPolymorph(s, opcode.SMSGSpellGo, goPacket)
	s.Tick(ctx)
	if countPackets(f, opcode.MSGMoveStartForward) != 0 {
		t.Fatal("moved before Stealth aura")
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(1, 1, 1, 1784, 20000))
	f.world.Self.ShapeshiftForm = 30
	s.Tick(ctx)
	if len(s.crowdControl.path) < 4 || !sapRearPathValid(position{}, s.crowdControl.plannedTarget, s.crowdControl.path) ||
		countPackets(f, opcode.MSGMoveStartForward) != 1 || s.crowdControl.phase != "approaching" {
		t.Fatalf("did not flank during observed moving spline: %v / %v", s.stateLocked(), s.crowdControl.path)
	}
	// A newer stop supplies the final current heading and position. The old
	// spline start at x=20 is outside range of the final rear position.
	target.X, target.Orientation = 14, 3.1415927
	target.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true}
	for range 60 {
		now = now.Add(200 * time.Millisecond)
		s.Tick(ctx)
		if s.crowdControl.phase == "ready" {
			break
		}
	}
	if s.crowdControl.phase != "ready" || !behindEnemy(s.pos, position{x: 14, o: 3.1415927}) || s.state.Moving ||
		countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatalf("no verified rear pose: %v, pos %v", s.stateLocked(), s.pos)
	}
	req.Approach = false
	st, err := s.Sap(ctx, req)
	if err != nil || st.GetCrowdControl().GetPhase() != "cast_dispatched" || st.GetLastSpellOperation().GetCapability() != "sap" ||
		countPackets(f, opcode.CMSGCastSpell) != 2 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatalf("exact Sap commit: %v %v", st, err)
	}
}

func TestExpiredPreparedCrowdControlDoesNotFallThroughToDirectCast(t *testing.T) {
	ctx := context.Background()
	s, f, advance, req := polymorphFixture(t)
	req.Approach = true
	if _, err := s.Polymorph(ctx, req); err != nil {
		t.Fatal(err)
	}
	advance(crowdControlLease)
	s.Tick(ctx)
	req.Approach = false
	st, err := s.Polymorph(ctx, req)
	if err != nil || st.GetCrowdControl().GetPhase() != "expired" || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatalf("expired exact preparation could cast: %v %v", st, err)
	}
}

func TestPreparedCrowdControlExpiresWithUnconfirmedMovement(t *testing.T) {
	ctx := context.Background()
	s, f, advance, req := polymorphFixture(t)
	req.Approach = true
	if _, err := s.Polymorph(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.state.MotionUnconfirmed = true
	advance(crowdControlLease)
	s.Tick(ctx)
	if got := state(t, s).GetCrowdControl(); got.GetActive() || got.GetPhase() != "expired" {
		t.Fatalf("unconfirmed movement held crowd control past expiry: %v", got)
	}
	if countPackets(f, opcode.MSGMoveStop) != 0 {
		t.Fatal("lease expiry retried an unconfirmed stop")
	}
}

func TestSapApproachReportsWhoEnteredCombatBeforeCast(t *testing.T) {
	ctx := context.Background()
	for _, phase := range []string{"sap_caster_in_combat", "sap_target_in_combat"} {
		t.Run(phase, func(t *testing.T) {
			s, f, req := sapFixture(t)
			req.Approach = true
			if _, err := s.Sap(ctx, req); err != nil {
				t.Fatal(err)
			}
			if phase == "sap_caster_in_combat" {
				f.world.Self.UnitFlags |= 0x80000
			} else {
				f.lookupCreatures[sheepTarget].UnitFlags |= 0x80000
			}
			s.Tick(ctx)
			got := state(t, s).GetCrowdControl()
			if got.GetPhase() != phase || got.GetActive() || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatalf("Sap left an incorrect result after combat started: %v", got)
			}
		})
	}
}
