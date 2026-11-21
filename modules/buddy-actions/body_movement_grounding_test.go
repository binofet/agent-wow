package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestBodyMovementSettlesBeforeStepOrHop(t *testing.T) {
	for _, jump := range []bool{false, true} {
		t.Run(map[bool]string{false: "tap", true: "jump"}[jump], func(t *testing.T) {
			s, f, _, ctx, advance := targetMovementFixture(t)
			originalRevision := f.world.Self.PositionRevision
			first := true
			s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, _, _ float64, _ time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
				if first {
					first = false
					mid, end := from, from
					mid.Z -= .1
					end.Z -= 1
					return []buddynav.BodyMotionSample{{Point: from, Settling: true}, {At: 100 * time.Millisecond, Point: mid, Settling: true}, {At: 350 * time.Millisecond, Point: end, Settling: true}}, nil
				}
				end := from
				end.X += 1
				return []buddynav.BodyMotionSample{{Point: from}, {At: 850 * time.Millisecond, Point: end}}, nil
			}
			s.bodyMotionWait = func(_ context.Context, d time.Duration) error { advance(d); return nil }
			r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 21, DurationMs: 1, Jump: jump}
			out, err := s.MoveBody(ctx, r)
			if err != nil || out.GetBodyMovement().GetPhase() != "body_settled_written" || !out.BodyMovement.TransportWritten || s.state.Moving || s.state.MotionUnconfirmed {
				t.Fatal("airborne input did not settle normally", out, err)
			}
			if countPackets(f, opcode.MSGMoveJump) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveFallLand) != 1 {
				t.Fatal("unsupported body tried to step or jump before landing", f.packets)
			}
			for _, packet := range f.packets {
				_, n, _ := readGUID(packet.Payload)
				flags := binary.LittleEndian.Uint32(packet.Payload[n:])
				if packet.Opcode == opcode.MSGMoveHeartbeat {
					if flags&0x1001 != 0x1000 || len(packet.Payload) != n+46 || math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[n+30:])) != 0 || math.Float32frombits(binary.LittleEndian.Uint32(packet.Payload[n+42:])) != 0 {
						t.Fatal("ordinary gravity was framed as forward motion or a jump", packet)
					}
				} else if flags&0x1000 != 0 {
					t.Fatal("landing or stop retained falling", packet)
				}
			}
			r.SequenceId++
			r.Jump = true
			out, err = s.MoveBody(ctx, r)
			if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || countPackets(f, opcode.MSGMoveJump) != 1 || countPackets(f, opcode.MSGMoveFallLand) != 2 {
				t.Fatal("subsequent supported hop failed", out, err, f.packets)
			}
			if f.world.Self.PositionRevision != originalRevision {
				t.Fatal("gravity replaced the authoritative position", f.world.Self)
			}
		})
	}
}

func TestBodyMovementPersistsExactPhysicalRefusal(t *testing.T) {
	s, f, _, ctx, _ := targetMovementFixture(t)
	s.bodyMotionPlan = func(context.Context, string, uint32, buddynav.Point, float64, float64, time.Duration, bool) ([]buddynav.BodyMotionSample, error) {
		return nil, errors.New("measured support above body: start_z=9.38 floor_z=10.25")
	}
	r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 31, DurationMs: 1}
	out, err := s.MoveBody(ctx, r)
	if err != nil || out.GetBodyMovement().GetPhase() != "local_physics_refused" || out.BodyMovement.Detail != "measured support above body: start_z=9.38 floor_z=10.25" || out.BodyMovement.TransportWritten || len(f.packets) != 0 {
		t.Fatal("known physical refusal lost its reason or wrote", out, err, f.packets)
	}
	if s.stateLocked().BodyMovement.Detail != out.BodyMovement.Detail {
		t.Fatal("later state discarded the refusal reason")
	}
}
