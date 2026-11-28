package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"math"
	"os"
	"strings"
	"testing"
)

func TestIndependentShipKeepsMappedPhysicalVisit(t *testing.T) {
	for _, core := range []uint64{1, 9} {
		s, f, _ := portalEntryRig(t)
		seedSyntheticPortalWorldScope(f, core)
		f.world.Self.PositionRevision = 11
		f.world.MovingTransports = []*worldv1.GameObjectState{{Guid: 0x1fc0000000000123, Entry: 77, MapId: f.world.Self.MapId, Present: true, MapKnown: true, TypeKnown: true, GameObjectType: 15, PositionKnown: true, PositionRevision: 4, MovingTransport: &worldv1.MovingTransportFacts{ObjectRevision: 4, PositionRevision: 4}}}
		frame, err := movementinfo.MoverFrame(s.selfGUID, movementinfo.Info{Flags: 0x201, X: 7, Y: 8, Z: 9, TransportPresent: true, Transport: movementinfo.Transport{GUID: 0x1fc0000000000123, X: 2, Seat: -1}})
		if err != nil {
			t.Fatal(err)
		}
		if portalObservedWorldScope(f.world, s.selfGUID) == nil || !s.matchesCurrentEncounterVisitLocked(f.world) {
			t.Fatal("broken fixture")
		}
		if err := s.recordSurfaceFrameLocked(context.Background(), f.world, opcode.MSGMoveStartForward, frame, strings.Repeat("a", 64)); err != nil {
			t.Errorf("World %d mapped to Core %d but proved physical write lost: %v", f.world.VisitRevision, core, err)
			continue
		}
		if s.surfaceFollow.body.visit != 1 || s.surfaceFollow.body.packet.TransportScope.VisitRevision != core {
			t.Fatal("logical/physical source not retained")
		}
	}
}

const independentShip = 0x1fc0000000000123

func TestIndependentDeclinedNormalFormationKeepsSelectedShipGoal(t *testing.T) {
	s, f, req, ctx, _ := followGoalFixture(t)
	f.world.Self.MovementFlagsKnown = true
	f.world.Owner.MovementFlagsKnown = true
	f.world.Self.PassengerMovement = &worldv1.PassengerMovementFacts{Revision: 3, VisitRevision: f.world.VisitRevision, Info: &worldv1.MovementInfoFacts{}}
	f.world.Owner.PassengerMovement = &worldv1.PassengerMovementFacts{Revision: 7, VisitRevision: f.world.VisitRevision, Info: &worldv1.MovementInfoFacts{Flags: 0x200, Transport: &worldv1.MovementTransportFacts{Guid: independentShip, Seat: -1}}}
	f.world.MovingTransports = []*worldv1.GameObjectState{{Guid: independentShip, MapId: f.world.Self.MapId, Present: true, MapKnown: true, TypeKnown: true, GameObjectType: 15, MovingTransport: &worldv1.MovingTransportFacts{}}}
	ship := proto.Clone(req).(*actionsv1.FollowFormationRequest)
	ship.SurfaceMode = "board"
	ship.TransportGuid = independentShip
	ship.OwnerPassengerRevision = 7
	if _, err := s.SetFollowFormation(ctx, ship); err != nil {
		t.Fatal("real surface setter", err)
	}
	req.Phase = "bad-phase"
	if _, err := s.SetFollowFormation(ctx, req); err == nil {
		t.Fatal("invalid ground setter passed")
	}
	if !proto.Equal(s.surfaceFollow.goal, ship) {
		t.Error("definitively rejected ordinary formation erased current board goal")
	}
}
func TestIndependentShipClockEvidence(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR for copied ferry-path evidence")
	}
	path, err := buddynav.LoadTransportPath(context.Background(), dir, 292, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name  string
		clock uint32
		age   uint64
	}{{"uint32wrap", math.MaxUint32 - 10, 20}, {"contradictory_received_passenger", 10000, 15000}, {"new_matching_passenger", 10000, 15000}} {
		t.Run(row.name, func(t *testing.T) {
			s, f, _ := portalEntryRig(t)
			s.SetNavigationDataDir(dir)
			sample, err := path.Sample(uint64(row.clock))
			if err != nil {
				t.Fatal(err)
			}
			f.world.Self.MapId = sample.MapID
			f.world.Owner.MapId = sample.MapID
			h := &worldv1.GameObjectState{Guid: independentShip, Entry: 176310, MapId: sample.MapID, MapKnown: true, Present: true, PositionKnown: true, PositionRevision: 1, TypeKnown: true, GameObjectType: 15, X: sample.Position.X, Y: sample.Position.Y, Z: sample.Position.Z, Orientation: sample.Yaw,
				MovingTransport: &worldv1.MovingTransportFacts{MovementAgeMs: row.age, MovementRevision: 3, PositionRevision: 1, ObjectRevision: 1, PeriodMs: proto.Uint32(path.Period()), PathProgressMs: proto.Uint32(row.clock), Scale: proto.Float32(1), DisplayId: proto.Uint32(476),
					Template: &worldv1.MovingTransportTemplate{TaxiPathId: 292, MoveSpeed: 30, AccelerationRate: 3, Guid: independentShip, Entry: 176310, SelfGuid: s.selfGUID, VisitRevision: f.world.VisitRevision, Revision: 4, DisplayId: 476}}}
			if row.name != "uint32wrap" {
				witness := sample
				curr := row.clock
				if row.name == "new_matching_passenger" {
					witness, _ = path.Sample(uint64(row.clock) + row.age)
					curr += uint32(row.age)
				}
				f.world.Owner.Present = true
				f.world.Owner.AliveKnown = true
				f.world.Owner.Alive = true
				f.world.Owner.MovementFlagsKnown = true
				f.world.Owner.MapKnown = true
				f.world.Owner.PositionKnown = true
				f.world.Owner.PositionRevision = 9
				f.world.Owner.PositionAgeMs = 0
				f.world.Owner.MovementFlags = 0x200
				f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z, f.world.Owner.Orientation = witness.Position.X, witness.Position.Y, witness.Position.Z, witness.Yaw
				f.world.Owner.PassengerMovement = &worldv1.PassengerMovementFacts{Revision: 9, AgeMs: 0, VisitRevision: f.world.VisitRevision, SourceOpcode: opcode.MSGMoveHeartbeat, Info: &worldv1.MovementInfoFacts{
					X: witness.Position.X, Y: witness.Position.Y, Z: witness.Position.Z, Orientation: witness.Yaw, Flags: 0x200,
					Transport: &worldv1.MovementTransportFacts{Guid: independentShip, Seat: -1, Time: curr}}}
				frac := uint32(float32(curr)/float32(path.Period())*65535) & 0xffff
				h.MovingTransport.PathFraction = proto.Uint32(frac)
				h.MovingTransport.PathFractionRevision = 8
				h.MovingTransport.PathFractionSourceOpcode = opcode.SMSGUpdateObject
			}
			h.MovingTransport.SourceOpcode = opcode.SMSGUpdateObject
			h.PositionAgeMs = row.age
			f.world.MovingTransports = []*worldv1.GameObjectState{h}
			got, clock, err := s.surfacePoseLocked(context.Background(), f.world, h)
			if row.name == "new_matching_passenger" {
				if err != nil {
					t.Fatal("independently matching current hull", err)
				}
				return
			}
			if err != nil {
				return
			}
			if row.name == "uint32wrap" {
				wrapped, _ := path.Sample(uint64(clock))
				if wrapped.MapID != h.MapId || got.Origin != wrapped.Position {
					t.Fatalf("returned wire clock %d but inferred transform %+v on map %d; that actual wrapped clock has %+v on map %d", clock, got.Origin, h.MapId, wrapped.Position, wrapped.MapID)
				}
				return
			}
			o := f.world.Owner.PassengerMovement.Info
			delta := math.Hypot(float64(got.Origin.X-o.X), float64(got.Origin.Y-o.Y))
			if delta > .025 {
				t.Fatalf("fresh same-hull paired zero-local offset and received scalar are contradicted by inferred origin: %.2f yards at wire clock %d", delta, clock)
			}
		})
	}
}
