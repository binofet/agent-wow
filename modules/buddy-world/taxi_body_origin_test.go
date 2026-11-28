package buddyworld

import (
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestPaidTaxiBodyUsesMatureOwnSplineThenYieldsToNewReceivedPose(t *testing.T) {
	for _, event := range []string{"mature", "early clear", "expired alone", "foreign clear", "damaged batch", "after clear correction", "new own spline", "same map reentry"} {
		t.Run(event, func(t *testing.T) {
			x := newCompletionRig(t)
			raw := movementinfo.Info{X: -10, Y: -20, Z: 5, Orientation: 1}
			wire, err := movementinfo.Encode(raw)
			if err != nil {
				t.Fatal(err)
			}
			during := x.deliver(t, opcode.MSGMoveHeartbeat, ownMovementMessage(1, wire))
			if during.Self.PositionKnown || !during.SelfMovementFacts.Known || during.SelfMovementFacts.Info.X != raw.X {
				t.Fatal("departing raw leaked into physical position", during.Self, during.SelfMovementFacts)
			}
			if event != "early clear" {
				x.now = x.now.Add(time.Second)
			}
			if event == "damaged batch" {
				x.deliver(t, opcode.SMSGUpdateObject, []byte{1})
			}
			if event == "new own spline" {
				x.start(t, completionFlyingHeader(4, 16), 18)
			}
			if event == "same map reentry" {
				x.deliver(t, opcode.SMSGNewWorld, login())
			}
			guid := uint64(1)
			if event == "foreign clear" {
				guid = 2
			}
			if event != "expired alone" {
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(guid, map[int]uint32{unitFlags: 8}))
			}
			got := ownMovementSnapshot(t, x.s)
			switch event {
			case "mature", "after clear correction":
				if !got.Self.PositionKnown || !got.Self.PositionInferred || !got.Self.MovementFlagsKnown || got.Self.X != 14 || got.Self.Y != 24 || got.Self.Z != 34 ||
					!proto.Equal(got.SelfMovementFacts.Info, during.SelfMovementFacts.Info) || got.SelfMovementFacts.Revision != during.SelfMovementFacts.Revision || got.SplineCompletion.Operation != nil {
					t.Fatal("separate current projected motion and raw departure facts required", got.Self, got.SelfMotionAuthority)
				}
				if event == "after clear correction" {
					raw.X = 22
					wire, _ = movementinfo.Encode(raw)
					got = x.deliver(t, opcode.MSGMoveHeartbeat, ownMovementMessage(1, wire))
					if !got.Self.PositionKnown || got.Self.PositionInferred || got.Self.X != 22 || got.SelfMovementFacts.Info.X != 22 || x.s.taxiBody.origin != x.scope.MotionRevision {
						t.Fatal("new realm pose lost", got.Self)
					}
				}
			default:
				if got.Self.PositionInferred || x.s.taxiBody.origin != 0 {
					t.Fatal("uncertain taxi source certified a motion origin", got.Self, event)
				}
			}
		})
	}
}
