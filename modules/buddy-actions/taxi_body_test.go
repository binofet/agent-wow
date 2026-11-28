package buddyactions

import (
	"context"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNormalBodyTapSettlesElevatedTaxiEndpointThenJumps(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("copied client navigation not configured")
	}
	s, f, _, ctx, advance := targetMovementFixture(t)
	s.navDataDir = root
	p := f.world.Self
	p.X, p.Y, p.Z, p.MapId = -5421.1006, -2928.6516, 350.9113, 0
	p.PositionRevision++
	s.bodyMotionWait = func(_ context.Context, d time.Duration) error { advance(d); return nil }
	r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 7, DurationMs: 100}
	out, err := s.MoveBody(ctx, r)
	if err != nil || out.GetBodyMovement().GetPhase() != "body_settled_written" || countPackets(f, opcode.MSGMoveFallLand) != 1 ||
		countPackets(f, opcode.MSGMoveJump) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatalf("first tap: %v, %v", out.GetBodyMovement(), err)
	}
	for _, packet := range f.packets {
		i, ok := movementinfo.OwnFrame(packet.Payload, s.selfGUID)
		if !ok || i.X != p.X || i.Y != p.Y || i.Z < 346.8 || i.Z > p.Z {
			t.Fatalf("first contact changed horizontal position or exceeded floor: %#v", i)
		}
	}
	r.SequenceId++
	r.Jump = true
	out, err = s.MoveBody(ctx, r)
	if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || countPackets(f, opcode.MSGMoveJump) != 1 || countPackets(f, opcode.MSGMoveFallLand) != 2 {
		t.Fatalf("ground input after contact: %v %v", out.GetBodyMovement(), err)
	}
}
