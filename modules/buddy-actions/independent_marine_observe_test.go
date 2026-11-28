package buddyactions

import (
	"context"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"strings"
	"testing"
)

func TestIndependentEncounterPublishesOnlyStillCurrentOwnSurfaceWrite(t *testing.T) {
	for _, kind := range []string{"current", "superseded-by-real-own-input", "same-turn-still-UNKNOWN", "new-raw-position"} {
		t.Run(kind, func(t *testing.T) {
			s, f, _, _, _ := idleBodyFixture(t, true)
			seedSyntheticPortalWorldScope(f, 1)
			w := f.world
			w.Self.MovementFlagsKnown = true
			w.Self.MovementFlags = 0x200
			w.Self.PassengerMovement = &worldv1.PassengerMovementFacts{Revision: 3, VisitRevision: w.VisitRevision, Info: &worldv1.MovementInfoFacts{Flags: 0x200, Transport: &worldv1.MovementTransportFacts{Guid: independentShip, Seat: -1}}}
			at := strings.Repeat("b", 64)
			i := movementinfo.Info{Flags: movementinfo.SwimmingFlag | 1, PitchPresent: true, X: w.Self.X, Y: w.Self.Y, Z: w.Self.Z}
			wire, err := movementinfo.MoverFrame(s.selfGUID, i)
			if err != nil {
				t.Fatal(err)
			}
			f.ownMovementAttempt = at
			f.ownMovementReport = &modv1.WorldPacket{Opcode: opcode.MSGMoveStartSwim, Payload: wire, TransportScope: clonePortalScope(f.portalCoreScope)}
			if err := s.recordSurfaceFrameLocked(context.Background(), w, opcode.MSGMoveStartSwim, wire, at); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "superseded-by-real-own-input":
				f.ownMovementAttempt = strings.Repeat("c", 64)
				f.ownMovementReport.Opcode = opcode.MSGMoveStop
			case "same-turn-still-UNKNOWN":
				s.surfaceFollow.body.uncertain = true
			case "new-raw-position":
				w.Self.PositionRevision++
			}
			before := proto.Clone(w)
			v, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
			if err != nil || v.GetNavigation().GetMovingSurface() == nil {
				t.Fatal("normal observation", err, v.GetReason())
			}
			if !proto.Equal(before, w) || v.Navigation.MovingSurface.GetSelf().GetInfo().GetTransport().GetGuid() != independentShip {
				t.Fatal("received public source changed")
			}
			got := v.Navigation.MovingSurface.SelfWriteKnown
			if got != (kind == "current") {
				t.Fatalf("ordinary encounter published current write=%v, prior received hull=%x after %s", got, independentShip, kind)
			}
		})
	}
}
