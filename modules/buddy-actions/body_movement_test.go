package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBodyMovementUsesIndependentProbeAndNormalJumpFrames(t *testing.T) {
	for _, mode := range []string{"step", "jump", "correction", "foreign_body", "foreign_visit"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, ctx, advance := targetMovementFixture(t)
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				return nil, errors.New("full route has unsupported ledge")
			}
			s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, start buddynav.Point, _, _ float64, _ time.Duration, jump bool) ([]buddynav.BodyMotionSample, error) {
				end := start
				end.X += 1.4
				return []buddynav.BodyMotionSample{{Point: start}, {At: 200 * time.Millisecond, Point: end}}, nil
			}
			s.bodyMotionWait = func(_ context.Context, d time.Duration) error {
				advance(d)
				if mode == "correction" {
					f.world.Self.PositionRevision++
					f.world.Self.X = 50
				}
				return nil
			}
			r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 7, DurationMs: 200, Jump: mode == "jump" || mode == "correction"}
			if mode == "foreign_body" {
				r.SelfGuid++
			}
			if mode == "foreign_visit" {
				r.VisitId++
			}
			out, err := s.MoveBody(ctx, r)
			if mode == "foreign_body" || mode == "foreign_visit" {
				if status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
					t.Fatal("foreign identity wrote", out, err, f.packets)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "correction" {
				if out.BodyMovement.Phase != "body_or_authoritative_position_changed" || s.pos.x != 50 || countPackets(f, opcode.MSGMoveFallLand) != 0 {
					t.Fatal("old trajectory overwrote actual correction", out, s.pos, f.packets)
				}
				return
			}
			if out.BodyMovement.Phase != "body_input_written" || !out.BodyMovement.TransportWritten || !out.BodyMovement.PositionEstimated || out.Moving || s.pos.x != 1.4 {
				t.Fatal("probe depended on failed full route or claimed a server effect", out, err, s.pos)
			}
			before := len(f.packets)
			if _, err := s.MoveBody(ctx, r); err != nil || len(f.packets) != before {
				t.Fatal("same probe replayed", err, f.packets)
			}
			if mode == "jump" {
				if countPackets(f, opcode.MSGMoveJump) != 1 || countPackets(f, opcode.MSGMoveFallLand) != 1 {
					t.Fatal("hop framing missing", f.packets)
				}
				for _, packet := range f.packets {
					if packet.Opcode == opcode.MSGMoveJump {
						_, n, _ := readGUID(packet.Payload)
						if len(packet.Payload) != n+46 || binary.LittleEndian.Uint32(packet.Payload[n:])&0x1000 == 0 {
							t.Fatal("jump lacks falling tail", packet.Payload)
						}
					}
				}
			}
		})
	}
}
