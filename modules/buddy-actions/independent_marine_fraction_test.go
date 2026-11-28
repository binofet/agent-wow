package buddyactions

import (
	"context"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"math"
	"os"
	"testing"
)

func TestIndependentPostCreateFractionCanDisprovePredictedShipWhenNobodyIsAttached(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR for copied ferry clock/scalar evidence")
	}
	for _, row := range []struct {
		name        string
		observed    uint32
		fractionAge uint64
		refuse      bool
	}{{"fresh_mutually_consistent", 25000, 0, false}, {"old_original_scalar", 10000, 15000, false}, {"fresh_contradiction", 11000, 0, true}} {
		t.Run(row.name, func(t *testing.T) {
			const first = uint32(10000)
			s, f, _ := portalEntryRig(t)
			s.SetNavigationDataDir(dir)
			p, err := buddynav.LoadTransportPath(context.Background(), dir, 292, 30, 3)
			if err != nil {
				t.Fatal(err)
			}
			start, _ := p.Sample(uint64(first))
			f.world.Self.MapId = start.MapID
			f.world.Owner.PassengerMovement = nil
			f.world.Self.PassengerMovement = nil
			num := uint32(float32(row.observed)/float32(p.Period())*65535) & 65535
			h := &worldv1.GameObjectState{Guid: 0x1fc0000000000021, Entry: 77, MapId: start.MapID, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 7,
				PositionAgeMs: 15000, TypeKnown: true, GameObjectType: 15, X: start.Position.X, Y: start.Position.Y, Z: start.Position.Z, Orientation: start.Yaw}
			h.MovingTransport = &worldv1.MovingTransportFacts{DisplayId: proto.Uint32(3015), Scale: proto.Float32(1), PathProgressMs: proto.Uint32(first), PeriodMs: proto.Uint32(p.Period()), MovementAgeMs: 15000,
				PositionRevision: 7, MovementRevision: 7, ObjectRevision: 1, SourceOpcode: opcode.SMSGUpdateObject, PathFraction: proto.Uint32(num), PathFractionRevision: 8, PathFractionAgeMs: row.fractionAge, PathFractionSourceOpcode: opcode.SMSGUpdateObject,
				Template: &worldv1.MovingTransportTemplate{TaxiPathId: 292, MoveSpeed: 30, AccelerationRate: 3, DisplayId: 3015, Entry: 77, Guid: h.Guid, SelfGuid: s.selfGUID, VisitRevision: f.world.VisitRevision, Revision: 3}}
			f.world.MovingTransports = []*worldv1.GameObjectState{h}
			tr, wireClock, err := s.surfacePoseLocked(context.Background(), f.world, h)
			if !row.refuse {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				return
			}
			actual, _ := p.Sample(uint64(row.observed) + row.fractionAge)
			if delta := math.Hypot(float64(actual.Position.X-tr.Origin.X), float64(actual.Position.Y-tr.Origin.Y)); delta > 4 {
				t.Fatalf("new received dynamic high16=%d after the CREATE is separate phase evidence (%.3fms per unit); predicted clock %d conflicts by %.1fyd", num, float64(p.Period())/65535, wireClock, delta)
			}
		})
	}
}
