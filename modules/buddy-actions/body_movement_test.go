package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestMountedBodyInputAtMenethil(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_SURFACE_TEST_DATA_DIR for the copied Menethil map")
	}
	s, f, r, ctx, _ := idleBodyFixture(t, false)
	s.navDataDir, s.bodyMotionPlan = dir, nil
	p := f.world.Self
	p.X, p.Y, p.Z, p.UnitFlags = -3841.9614, -745.749, 8.514998, 8
	mountForTravel(t, s, p, 23161)
	r.DurationMs, r.BearingRadians = 100, .25
	out, err := s.MoveBody(ctx, r)
	if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" ||
		countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatalf("mounted forward input: %v, %v, frames=%d", err, out.GetBodyMovement(), len(f.packets))
	}
	last, ok := movementinfo.OwnFrame(f.packets[len(f.packets)-1].Payload, s.selfGUID)
	bearing := float64(p.Orientation + r.BearingRadians)
	if !ok || last.TransportPresent || math.Abs(float64(last.Orientation)-bearing) > .001 ||
		math.Abs(float64(last.X-p.X)-1.4*math.Cos(bearing)) > .02 || math.Abs(float64(last.Y-p.Y)-1.4*math.Sin(bearing)) > .02 ||
		p.UnitFlags != 134217736 || out.MotionUnconfirmed {
		t.Fatal("measured mounted stride or original raw flags changed", last, p.UnitFlags, out.MotionUnconfirmed)
	}
}

func TestMountedBodyInputRechecksPaceBeforeGroundWrite(t *testing.T) {
	for _, change := range []string{"unchanged_lease", "slowed_after_start", "aura_removed", "stunned"} {
		t.Run(change, func(t *testing.T) {
			s, f, r, ctx, _ := idleBodyFixture(t, change == "unchanged_lease")
			mountForTravel(t, s, f.world.Self, 23161)
			checked := false
			f.onClock = func() {
				checked = true
				switch change {
				case "slowed_after_start":
					if len(f.packets) >= 2 {
						f.world.Self.RunSpeed = 7
					}
				case "aura_removed":
					delete(s.buffAuras, s.selfGUID)
				case "stunned":
					f.world.Self.UnitFlags |= 0x00040000
				}
			}
			out, err := s.MoveBody(ctx, r)
			if err != nil || !checked {
				t.Fatal("did not reach packet-boundary observation", err, out.GetBodyMovement())
			}
			if change == "unchanged_lease" {
				if out.GetBodyMovement().GetPhase() != "body_input_written" || countPackets(f, opcode.MSGMoveStop) != 1 || s.interactionToken != r.IdleToken {
					t.Fatal("mounted step lost its existing custody", out.GetBodyMovement())
				}
			} else if change == "slowed_after_start" {
				if len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStartForward || !out.MotionUnconfirmed {
					t.Fatal("continued at stale mounted speed or lost the outstanding START", out, f.packets)
				}
				_, _ = s.MoveBody(ctx, r)
				if len(f.packets) != 2 {
					t.Fatal("unresolved mounted movement replayed", f.packets)
				}
			} else if len(f.packets) != 0 || out.GetBodyMovement().GetTransportWritten() {
				t.Fatal("invalidated mounted input wrote movement", f.packets)
			}
		})
	}
}

func TestBodyMovementUsesIndependentProbeAndNormalJumpFrames(t *testing.T) {
	for _, mode := range []string{"step", "jump", "slow_plan_hop", "revoke", "correction", "foreign_body", "foreign_visit"} {
		t.Run(mode, func(t *testing.T) {
			s, f, target, ctx, advance := targetMovementFixture(t)
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				return nil, errors.New("full route has unsupported ledge")
			}
			s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, start buddynav.Point, _, _ float64, _ time.Duration, jump bool) ([]buddynav.BodyMotionSample, error) {
				duration := 200 * time.Millisecond
				if mode == "slow_plan_hop" {
					advance(800 * time.Millisecond)
					duration = 850 * time.Millisecond
				}
				end := start
				end.X += 1.4
				return []buddynav.BodyMotionSample{{Point: start}, {At: duration, Point: end}}, nil
			}
			s.bodyMotionWait = func(_ context.Context, d time.Duration) error {
				advance(d)
				if mode == "correction" {
					f.world.Self.PositionRevision++
					f.world.Self.X = 50
				}
				if mode == "revoke" {
					_, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(0))
					if err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 7, DurationMs: 200, Jump: mode != "step"}
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
			if mode == "correction" || mode == "revoke" {
				if out.BodyMovement.Phase != "body_or_authoritative_position_changed" || mode == "correction" && s.pos.x != 50 || countPackets(f, opcode.MSGMoveFallLand) != 0 {
					t.Fatal("old trajectory overwrote actual correction", out, s.pos, f.packets)
				}
				return
			}
			if mode == "slow_plan_hop" && (!s.hostEncounterActiveLocked() || s.lastStep != s.now()) {
				t.Fatal("bounded input lost its owner or resumed follow with a stale checkpoint", s.stateLocked())
			}
			if out.BodyMovement.Phase != "body_input_written" || !out.BodyMovement.TransportWritten || !out.BodyMovement.PositionEstimated || out.Moving || s.pos.x != 1.4 {
				t.Fatal("probe depended on failed full route or claimed a server effect", out, err, s.pos)
			}
			before := len(f.packets)
			if _, err := s.MoveBody(ctx, r); err != nil || len(f.packets) != before {
				t.Fatal("same probe replayed", err, f.packets)
			}
			if mode == "step" {
				serverRevision := f.world.Self.PositionRevision
				for _, sequence := range []uint64{8, 9} {
					r.SequenceId = sequence
					if _, err := s.MoveBody(ctx, r); err != nil {
						t.Fatal(err)
					}
					view, err := s.ApproachTarget(ctx, target)
					if err != nil || view.TargetMovement.Phase != "follow_navigation_unavailable" {
						t.Fatal("route unexpectedly approved probe", view, err)
					}
				}
				projection := &actionsv1.EncounterObservationResponse{Players: map[string]*actionsv1.EncounterPlayer{}}
				s.encounterPlayers(f.world, nil, projection)
				if projection.Players["1"].PositionRevision != serverRevision || f.world.Self.PositionRevision != serverRevision || math.Abs(float64(projection.Players["1"].X)-4.2) > .001 {
					t.Fatal("local probe publications changed authoritative revision", projection.Players["1"], f.world.Self)
				}
				for _, publication := range f.movementObservations {
					if publication.Valid && publication.ServerPositionRevision != serverRevision {
						t.Fatal("publication changed correction baseline", publication)
					}
				}
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
