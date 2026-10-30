package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const ownCorpseGUID = uint64(0xf101000000000001)

func recoveryRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	s, f, advance := rig(t)
	p := f.world.Self
	p.Alive = false
	p.Health = 0
	p.UnitFlags = 8
	f.world.OwnCorpse = &worldv1.CorpseState{Guid: ownCorpseGUID, OwnerGuid: 1, Present: true, MapKnown: true, PositionKnown: true, X: 60, PositionRevision: 1, FlagsKnown: true}
	s.navDataDir = "client-assets"
	s.planRecoveryPath = nil // Tests below inject short paths independently of static assets.
	s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
		path := []position{a}
		n := int(math.Ceil(distance(a, b)))
		for i := 1; i <= n; i++ {
			q := float32(i) / float32(n)
			path = append(path, position{x: a.x + (b.x-a.x)*q, y: a.y + (b.y-a.y)*q, z: a.z + (b.z-a.z)*q})
		}
		return path, nil
	}
	if _, err := s.Recovery(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return s, f, advance
}

func TestRecoveryProductionPlannerWiredAndMultipleVerifiedLegsReachActualCorpse(t *testing.T) {
	if NewService(nil, 1).planRecoveryPath == nil {
		t.Fatal("production multi-leg corpse planner is not wired")
	}
	s, f, advance := recoveryRig(t)
	const corpseX = float32(600)
	f.world.OwnCorpse.X = corpseX
	legs := 0
	s.planRecoveryPath = func(ctx context.Context, root string, mapID uint32, from, corpse position) ([]position, error) {
		if root != "client-assets" || mapID != 0 || corpse.x != corpseX {
			t.Fatal("planner was not bound to actual queried corpse", root, mapID, corpse)
		}
		legs++
		goal := corpse
		if goal.x-from.x > 100 {
			goal.x = from.x + 100
		}
		return s.planMapPath(ctx, root, mapID, from, goal)
	}
	s.Tick(context.Background())
	ghostAt(f, 0)
	s.Tick(context.Background())
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: corpseX}))
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
	for i := 0; i < 700 && countOpcode(f, opcode.CMSGReclaimCorpse) == 0; i++ {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if legs < 5 || int(s.recovery.state.VerifiedLegs) != legs || countOpcode(f, opcode.CMSGRepopRequest) != 1 || countOpcode(f, opcode.MSGCorpseQuery) != 1 || countOpcode(f, opcode.CMSGReclaimCorpse) != 1 || s.pos.x < corpseX-35 || s.recovery.state.AliveObserved {
		t.Fatal("multi-leg route/reclaim proof failed", legs, s.pos, s.stateLocked())
	}
	// Transport acceptance remains a pending reclaim, not resurrection.
	s.Tick(context.Background())
	if s.recovery.state.Rested || countOpcode(f, opcode.CMSGReclaimCorpse) != 1 {
		t.Fatal("transport became resurrection or retried", s.stateLocked())
	}
}
func corpseQueryFixture(mapID, actual uint32, p position) []byte {
	b := []byte{1}
	b = binary.LittleEndian.AppendUint32(b, mapID)
	for _, v := range []float32{p.x, p.y, p.z} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	b = binary.LittleEndian.AppendUint32(b, actual)
	return binary.LittleEndian.AppendUint32(b, 0)
}
func recoveryObserve(t *testing.T, s *Service, op uint32, p []byte) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p}); err != nil {
		t.Fatal(err)
	}
}
func ghostAt(f *fakeSession, x float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.world.Self
	p.Alive = false
	p.Health = 1
	p.PlayerFlags = ghostFlag
	p.MovementFlags = ghostWaterWalking
	p.RunSpeed = 8.75
	p.X = x
	p.PositionRevision++
}
func countOpcode(f *fakeSession, op uint32) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.packets {
		if p.Opcode == op {
			n++
		}
	}
	return n
}

func TestRecoveryNormalReleaseRouteReclaimNeedsActualAliveAndRest(t *testing.T) {
	s, f, advance := recoveryRig(t)
	s.state.Following, s.state.Healing, s.state.Assisting = true, true, true
	s.followUntil, s.healingUntil, s.assistUntil = s.now().Add(lease), s.now().Add(lease), s.now().Add(lease)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if countOpcode(f, opcode.CMSGRepopRequest) != 1 || !s.recovery.state.ReleaseWritten || s.state.InteractionOwner != "recovery" {
		t.Fatal(s.stateLocked())
	}
	ghostAt(f, 0)
	s.Tick(context.Background())
	if countOpcode(f, opcode.MSGCorpseQuery) != 1 {
		t.Fatal(f.packets)
	}
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 30000))
	for range 35 {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if s.pos.x < 25 || s.pos.x > 27 || !s.recovery.state.GhostObserved {
		t.Fatal(s.pos, s.stateLocked())
	}
	if countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
		t.Fatal("claimed before actual delay")
	}
	advance(30 * time.Second)
	s.Tick(context.Background())
	if countOpcode(f, opcode.CMSGReclaimCorpse) != 1 || s.recovery.state.AliveObserved {
		t.Fatal(s.stateLocked())
	}
	var reclaim *modv1.SendPacketRequest
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGReclaimCorpse {
			reclaim = p
		}
		if p.Opcode == opcode.MSGMoveStartForward || p.Opcode == opcode.MSGMoveHeartbeat || p.Opcode == opcode.MSGMoveStop {
			at := len(packedGUID(1))
			if binary.LittleEndian.Uint32(p.Payload[at:])&ghostWaterWalking == 0 {
				t.Fatal("lost observed ghostwaterwalking", p)
			}
		}
	}
	if reclaim == nil || len(reclaim.Payload) != 8 || binary.LittleEndian.Uint64(reclaim.Payload) != ownCorpseGUID {
		t.Fatal(reclaim)
	}
	s.Tick(context.Background())
	if countOpcode(f, opcode.CMSGReclaimCorpse) != 1 {
		t.Fatal("repeated reclaim")
	}
	f.mu.Lock()
	p := f.world.Self
	p.Alive = true
	p.Health = 50
	p.PlayerFlags = 0
	p.MovementFlags = 0
	p.RunSpeed = 7
	p.X = s.pos.x
	p.PositionRevision++
	f.world.OwnCorpse = nil
	f.mu.Unlock()
	s.Tick(context.Background())
	if !s.recovery.state.AliveObserved || !s.recovery.state.Active || s.recovery.state.Rested {
		t.Fatal(s.stateLocked())
	}
	f.mu.Lock()
	f.world.Self.Health = 100
	f.mu.Unlock()
	s.Tick(context.Background())
	if s.recovery.state.Active || !s.recovery.state.Rested || s.recovery.state.Phase != "complete" || s.state.InteractionOwner != "" || !s.state.Following || !s.state.Healing || !s.state.Assisting {
		t.Fatal(s.stateLocked())
	}
}

func TestRecoveryActualOwnCorpseAndSameMapTimerGates(t *testing.T) {
	for _, mode := range []string{"missing", "foreign", "bones", "wrong_guid", "different_map", "different_position", "unknown_flags", "query_other_map"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := recoveryRig(t)
			s.Tick(context.Background())
			ghostAt(f, 50)
			s.Tick(context.Background())
			mapID := uint32(0)
			if mode == "query_other_map" {
				mapID = 1
			}
			recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(mapID, mapID, position{x: 60}))
			recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
			advance(time.Second)
			f.mu.Lock()
			c := f.world.OwnCorpse
			switch mode {
			case "missing":
				f.world.OwnCorpse = nil
			case "foreign":
				c.OwnerGuid = 3
			case "bones":
				c.Flags = 1
			case "wrong_guid":
				c.Guid = 1
			case "different_map":
				c.MapId = 1
			case "different_position":
				c.X = 100
			case "unknown_flags":
				c.FlagsKnown = false
			}
			f.mu.Unlock()
			s.Tick(context.Background())
			if countOpcode(f, opcode.CMSGReclaimCorpse) != 0 {
				t.Fatal("invalid owncorpse reclaimed", mode)
			}
		})
	}
}

func TestRecoveryAmbiguousWritesCannotBeRepeatedOrResetByEnable(t *testing.T) {
	s, f, advance := recoveryRig(t)
	f.failSend = true
	s.Tick(context.Background())
	for range 5 {
		s.Recovery(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
		s.Tick(context.Background())
	}
	if countOpcode(f, opcode.CMSGRepopRequest) != 1 {
		t.Fatal("repeated ambiguous release")
	}
	f.failSend = false
	ghostAt(f, 50)
	s.Tick(context.Background())
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
	f.failSend = true
	s.Tick(context.Background())
	advance(11 * time.Second)
	s.Tick(context.Background())
	s.Recovery(context.Background(), &actionsv1.ToggleRequest{Enabled: true})
	s.Tick(context.Background())
	if countOpcode(f, opcode.CMSGReclaimCorpse) != 1 || s.recovery.state.Phase != "outcome_unconfirmed" {
		t.Fatal(s.stateLocked())
	}
}

func TestRecoveryNeverStealsOtherLeaseAndCancelStopsOwnedMovement(t *testing.T) {
	s, f, advance := recoveryRig(t)
	s.state.InteractionOwner = "professions"
	s.interactionToken = "unrelated"
	s.interactionUntil = s.now().Add(time.Hour)
	s.Tick(context.Background())
	if len(f.packets) != 0 || s.state.InteractionOwner != "professions" {
		t.Fatal("stole active item lease")
	}
	s.state.InteractionOwner = ""
	s.interactionToken = ""
	s.Tick(context.Background())
	ghostAt(f, 0)
	s.Tick(context.Background())
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !s.state.Moving {
		t.Fatal("fixture didn't start ghost movement")
	}
	before := countOpcode(f, opcode.MSGMoveStop)
	s.Recovery(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
	if s.state.Moving || s.state.InteractionOwner != "" || countOpcode(f, opcode.MSGMoveStop) != before+1 || s.recovery.state.Enabled {
		t.Fatal(s.stateLocked())
	}
}

func TestRecoveryFinalClockCorrectionStopsInsteadOfStaleGhostWrite(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.Tick(context.Background())
	ghostAt(f, 0)
	s.Tick(context.Background())
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	f.onClock = func() { f.world.Self.X = 200; f.world.Self.PositionRevision++ }
	s.Tick(context.Background())
	if countOpcode(f, opcode.MSGMoveStartForward) != 0 || s.recovery.state.Active {
		t.Fatal("stale correction wrote movement", s.stateLocked())
	}
}

func TestRecoveryBoundedUnavailableRouteAndFreshAgeIndependentCorpse(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.Tick(context.Background())
	ghostAt(f, 0)
	s.Tick(context.Background())
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 500}))
	s.Tick(context.Background())
	if s.recovery.state.Active || s.recovery.state.Reason != "corpse_navigation_unavailable" || countOpcode(f, opcode.MSGMoveStartForward) != 0 {
		t.Fatal(s.stateLocked())
	}
	a := &worldv1.CorpseState{Guid: ownCorpseGUID, OwnerGuid: 1, Present: true, MapKnown: true, PositionKnown: true, FlagsKnown: true, PositionRevision: 1}
	b := proto.Clone(a).(*worldv1.CorpseState)
	b.PositionAgeMs = 1000
	b.ObservationAgeMs = 1200
	if !sameRecoveryCorpse(a, b) {
		t.Fatal("age alone invalidated same actual corpse")
	}
	b.PositionRevision++
	if sameRecoveryCorpse(a, b) {
		t.Fatal("pose change not invalidated")
	}
}

func TestRecoveryProtocolStrictFramesAtomicAndUnsolicitedQueryIgnored(t *testing.T) {
	s, _, _ := recoveryRig(t)
	good := corpseQueryFixture(0, 0, position{x: 60})
	recoveryObserve(t, s, opcode.MSGCorpseQuery, good)
	if s.recovery.state.CorpseQueryKnown {
		t.Fatal("unsolicited query authorizes corpse route")
	}
	s.recovery.queryPending = true
	for n := range len(good) {
		recoveryObserve(t, s, opcode.MSGCorpseQuery, good[:n])
	}
	if s.recovery.state.CorpseQueryKnown || !s.recovery.queryPending {
		t.Fatal("truncated query mutated state")
	}
	bad := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(bad[5:], math.Float32bits(float32(math.NaN())))
	recoveryObserve(t, s, opcode.MSGCorpseQuery, bad)
	if s.recovery.state.CorpseQueryKnown {
		t.Fatal("NaN accepted")
	}
	recoveryObserve(t, s, opcode.MSGCorpseQuery, good)
	if !s.recovery.state.CorpseQueryKnown || !s.recovery.state.CorpseFound {
		t.Fatal(s.stateLocked())
	}
	old := s.recovery.delayUntil
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, []byte{0, 0})
	if s.recovery.delayUntil != old {
		t.Fatal("truncated timer mutated")
	}
}

func TestRecoveryPreservesKnownExpiredTimerAndRestCustody(t *testing.T) {
	s, f, advance := recoveryRig(t)
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
	advance(time.Second)
	s.Tick(context.Background())
	if !s.recovery.state.ReclaimDelayKnown || s.recovery.snapshot(s.now()).ReclaimDelayRemainingMs != 0 {
		t.Fatal("known zero timer replaced with guessed delay", s.stateLocked())
	}
	ghostAt(f, 50)
	s.Tick(context.Background())
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	s.Tick(context.Background())
	f.world.Self.Alive = true
	f.world.Self.Health = 50
	f.world.Self.PlayerFlags = 0
	f.world.Self.MovementFlags = 0
	f.world.Self.RunSpeed = 7
	for range 25 {
		advance(time.Second)
		s.Tick(context.Background())
		state(t, s)
	}
	if s.state.InteractionOwner != "recovery" || !s.recovery.state.Active {
		t.Fatal("rest custody expired", s.stateLocked())
	}
	s.state.InteractionOwner = "professions"
	s.interactionToken = "replacement"
	s.interactionUntil = s.now().Add(time.Hour)
	before := len(f.packets)
	s.Tick(context.Background())
	if s.state.InteractionOwner != "professions" || s.recovery.state.Active || len(f.packets) != before {
		t.Fatal("stole replacement custody", s.stateLocked())
	}
}

func TestRecoveryNewActualDeathDuringRestGetsNewSingleRequests(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.Tick(context.Background())
	ghostAt(f, 50)
	s.Tick(context.Background())
	recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
	recoveryObserve(t, s, opcode.SMSGCorpseReclaimDelay, binary.LittleEndian.AppendUint32(nil, 0))
	s.Tick(context.Background())
	f.world.Self.Alive = true
	f.world.Self.Health = 50
	f.world.Self.PlayerFlags = 0
	f.world.Self.MovementFlags = 0
	f.world.Self.RunSpeed = 7
	s.Tick(context.Background())
	f.world.Self.Alive = false
	f.world.Self.Health = 0
	s.Tick(context.Background())
	s.Tick(context.Background())
	if s.recovery.state.DeathEpoch != 2 || countOpcode(f, opcode.CMSGRepopRequest) != 2 || countOpcode(f, opcode.CMSGReclaimCorpse) != 1 {
		t.Fatal(s.stateLocked(), f.packets)
	}
}

func TestRecoverySnapshotLossNeverStopsReplacementLease(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.Tick(context.Background())
	s.state.InteractionOwner = "professions"
	s.interactionToken = "replacement"
	s.interactionUntil = s.now().Add(time.Hour)
	s.state.Moving = true
	f.failSnapshot = true
	before := len(f.packets)
	s.Tick(context.Background())
	if len(f.packets) != before || !s.state.Moving || s.state.InteractionOwner != "professions" || s.recovery.state.Active {
		t.Fatal("recovery stopped replacement on snapshot failure", s.stateLocked())
	}
}

func TestRecoveryMayReleaseActualDeathButNeedsFreshPoseToResolveOldMotion(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.state.MotionUnconfirmed = true
	s.state.Moving = true
	s.Tick(context.Background())
	if countOpcode(f, opcode.CMSGRepopRequest) != 1 || countOpcode(f, opcode.MSGMoveStartForward) != 0 {
		t.Fatal("actual dead release blocked by unrelated old motion uncertainty", s.stateLocked())
	}
	ghostAt(f, 50)
	s.Tick(context.Background())
	if s.state.MotionUnconfirmed || s.state.Moving || countOpcode(f, opcode.MSGCorpseQuery) != 1 || countOpcode(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("actual graveyard pose did not establish stopped ghost baseline", s.stateLocked())
	}
}

func TestRecoveryGhostFlagBeforeGraveyardPoseWaitsWithoutWrites(t *testing.T) {
	s, f, _ := recoveryRig(t)
	s.state.MotionUnconfirmed = true
	s.state.Moving = true
	s.Tick(context.Background())
	f.world.Self.Alive = false
	f.world.Self.Health = 1
	f.world.Self.PlayerFlags = ghostFlag
	f.world.Self.MovementFlags = ghostWaterWalking
	f.world.Self.RunSpeed = 8.75
	before := len(f.packets)
	s.Tick(context.Background())
	if len(f.packets) != before || !s.recovery.state.Active || s.recovery.state.Phase != "waiting_for_actual_graveyard_position_baseline" {
		t.Fatal("intermediate ghost prematurely stopped/retired", s.stateLocked())
	}
	ghostAt(f, 50)
	s.Tick(context.Background())
	if s.state.MotionUnconfirmed || countOpcode(f, opcode.MSGCorpseQuery) != 1 || !s.recovery.state.Active {
		t.Fatal(s.stateLocked())
	}
}

func TestRecoveryFinalClockStateMustStillBeActualSupportedGhost(t *testing.T) {
	for _, mode := range []string{"alive", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := recoveryRig(t)
			s.Tick(context.Background())
			ghostAt(f, 0)
			s.Tick(context.Background())
			recoveryObserve(t, s, opcode.MSGCorpseQuery, corpseQueryFixture(0, 0, position{x: 60}))
			f.onClock = func() {
				if mode == "alive" {
					f.world.Self.Alive = true
					f.world.Self.Health = 50
					f.world.Self.PlayerFlags = 0
				} else {
					f.world.Self.MovementFlags |= 0x2000000
				}
			}
			s.Tick(context.Background())
			if countOpcode(f, opcode.MSGMoveStartForward) != 0 || countOpcode(f, opcode.MSGMoveHeartbeat) != 0 || s.recovery.state.Active {
				t.Fatal("post-clock state change wrote ghost travel", s.stateLocked())
			}
		})
	}
}
