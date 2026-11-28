package buddyactions

import (
	"context"
	"strings"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestSurfaceDepartureUsesExactOwnFlagsWithoutFresheningWorld(t *testing.T) {
	for _, change := range []string{"current", "different attempt", "different bytes", "later own pose", "later visit", "uncertain write"} {
		t.Run(change, func(t *testing.T) {
			s, f, _, _, _ := idleBodyFixture(t, true)
			raw := f.world
			raw.Self.MovementFlags = passengerFlag
			attempt := strings.Repeat("b", 64)
			info := movementinfo.Info{Flags: swimMovementFlag | 1, X: raw.Self.X, Y: raw.Self.Y, Z: raw.Self.Z, PitchPresent: true}
			payload, err := movementinfo.MoverFrame(s.selfGUID, info)
			if err != nil {
				t.Fatal(err)
			}
			f.ownMovementAttempt = attempt
			f.ownMovementReport = &modv1.WorldPacket{Opcode: opcode.MSGMoveStartSwim, Payload: payload, TransportScope: clonePortalScope(f.portalCoreScope)}
			if err := s.recordSurfaceFrameLocked(context.Background(), raw, opcode.MSGMoveStartSwim, payload, attempt); err != nil {
				t.Fatal(err)
			}
			before := proto.Clone(raw)
			switch change {
			case "different attempt":
				f.ownMovementAttempt = strings.Repeat("c", 64)
			case "different bytes":
				f.ownMovementReport.Payload = append([]byte(nil), payload...)
				f.ownMovementReport.Payload[len(payload)-1] ^= 1
			case "later own pose":
				raw.Self.PositionRevision++
			case "later visit":
				raw.VisitRevision++
			case "uncertain write":
				s.surfaceFollow.body.uncertain = true
			}
			view := s.surfaceMovementWorldLocked(context.Background(), raw)
			if change == "current" {
				if view.Self.MovementFlags != swimMovementFlag|1 || !proto.Equal(raw, before) || view.Self.PositionRevision != raw.Self.PositionRevision || view.Self.PositionAgeMs != raw.Self.PositionAgeMs {
					t.Fatal("departure lost its exact native input or manufactured received freshness")
				}
			} else if view != raw || view.Self.MovementFlags != passengerFlag {
				t.Fatal("superseded or unknown input replaced the current World flags")
			}
		})
	}
}
