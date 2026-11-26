package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type delayedExplorationSession struct {
	*fakeSession
	advance         func(time.Duration)
	now             func() time.Time
	epoch           time.Time
	kind            string
	times           []time.Time
	refuseHeartbeat bool
}

func (f *delayedExplorationSession) GetClock(ctx context.Context, r *emptypb.Empty, o ...grpc.CallOption) (*modv1.ClockResponse, error) {
	if f.kind == "clock" {
		f.advance(650 * time.Millisecond)
	}
	out, err := f.fakeSession.GetClock(ctx, r, o...)
	if out != nil {
		out.ClientTimeMs = uint32(f.now().Sub(f.epoch) / time.Millisecond)
	}
	return out, err
}
func (f *delayedExplorationSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, o ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && (f.kind == "snapshot" && r.Method == "snapshot" || f.kind == "publication" && r.Method == "observe_client_movement") {
		f.advance(650 * time.Millisecond)
	}
	return f.fakeSession.InvokeModule(ctx, r, o...)
}
func (f *delayedExplorationSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, o ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.refuseHeartbeat && r.Opcode == opcode.MSGMoveHeartbeat {
		return nil, modcb.PacketNotDispatchedError("fixture current transport refused")
	}
	f.times = append(f.times, f.now())
	return f.fakeSession.SendPacket(ctx, r, o...)
}

func TestExplorationDelayedCallbacksKeepBoundedVerifiedProgress(t *testing.T) {
	for _, kind := range []string{"snapshot", "clock", "publication", "scheduler"} {
		t.Run(kind, func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
			f.world.Self.MapId = 349
			r.MapId = 349
			slow := &delayedExplorationSession{fakeSession: f, advance: advance, now: s.now, epoch: s.now(), kind: kind}
			s.session = slow
			bounded := false
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				bounded = bounded || s.state.Reason == "exploration_checkpoint_delay_bounded"
				if kind == "scheduler" {
					d += 650 * time.Millisecond
				}
				advance(d)
				return ctx.Err()
			}
			start := playerPosition(f.world.Self)
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got.Reason != "exploration_within_distance" || got.Moving || got.Traveling || got.MotionUnconfirmed {
				t.Fatal(got, err)
			}
			if kind != "clock" && !bounded {
				t.Fatal("fixture did not exercise bounded scheduling gap")
			}
			if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal("duplicate input or STOP", f.packets)
			}
			previous := start
			var stamp uint32
			for i, pkt := range f.packets {
				p := explorationPacketPosition(t, pkt.Payload)
				_, n, _ := readGUID(pkt.Payload)
				currentStamp := binary.LittleEndian.Uint32(pkt.Payload[n+6:])
				if pkt.Opcode == opcode.MSGMoveHeartbeat {
					d := distance(previous, p)
					if d > 7*.6+.01 || d > 7*slow.times[i].Sub(slow.times[i-1]).Seconds()+.01 || d > 7*float64(currentStamp-stamp)/1000+.01 {
						t.Fatal("late backlog became excess motion", d, pkt)
					}
					if p.x > 1500 && p.x < 1515 && p.y != 1506 {
						t.Fatal("late checkpoint cut navmesh corner", p)
					}
				}
				previous, stamp = p, currentStamp
			}
		})
	}
}

func TestExplorationTwoShortLegsAfterSlowSnapshotStopAtFreshPose(t *testing.T) {
	s, f, advance, r := explorationRig(t)
	f.world.Self.MapId = 349
	r.MapId = 349
	s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
		return []position{a, b}, nil
	}
	slow := &delayedExplorationSession{fakeSession: f, advance: advance, now: s.now, epoch: s.now(), kind: "snapshot"}
	s.session = slow
	for range 2 {
		from := s.pos
		if !s.posKnown {
			from = playerPosition(f.world.Self)
		}
		r.X, r.Y, r.Z = from.x+6.5, from.y, from.z
		got, err := s.TravelTo(context.Background(), r)
		if err != nil || got.Reason != "exploration_within_distance" || got.Moving || got.MotionUnconfirmed || distance(from, s.pos) == 0 {
			t.Fatal("short observed leg made no progress", got, err)
		}
		// Independently observed next pose; do not silently convert an estimate.
		f.world.Self.X, f.world.Self.Y, f.world.Self.Z = s.pos.x, s.pos.y, s.pos.z
		f.world.Self.PositionRevision++
	}
	if countPackets(f, opcode.MSGMoveStartForward) != 2 || countPackets(f, opcode.MSGMoveStop) != 2 {
		t.Fatal(f.packets)
	}
}

func TestExplorationCheckpointFailuresKeepCauseAndNoUnknownReplay(t *testing.T) {
	for _, kind := range []string{"clock_regressed", "geometry", "geometry_stop_unknown", "heartbeat_unknown", "heartbeat_not_dispatched", "correction"} {
		t.Run(kind, func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
			s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				return []position{a, b}, nil
			}
			if kind == "geometry" || kind == "geometry_stop_unknown" {
				r.X = 1510
				r.Z = 90
			}
			if kind == "geometry_stop_unknown" {
				f.failSend = true
				f.failSendOpcode = opcode.MSGMoveStop
			}
			if kind == "heartbeat_unknown" {
				f.failSend = true
				f.failSendOpcode = opcode.MSGMoveHeartbeat
			}
			slow := &delayedExplorationSession{fakeSession: f, advance: advance, now: s.now, epoch: s.now(), refuseHeartbeat: kind == "heartbeat_not_dispatched"}
			s.session = slow
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				if kind == "clock_regressed" {
					advance(-time.Second)
				} else {
					advance(850 * time.Millisecond)
				}
				if kind == "correction" {
					f.onClock = func() { f.onClock = nil; f.world.Self.X += 40; f.world.Self.PositionRevision++ }
				}
				return ctx.Err()
			}
			got, err := s.TravelTo(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{"clock_regressed": "exploration_clock_regressed", "geometry": "exploration_step_geometry_invalid", "geometry_stop_unknown": "exploration_step_geometry_invalid_stop_unconfirmed", "heartbeat_unknown": "exploration_movement_transport_unconfirmed", "heartbeat_not_dispatched": "exploration_movement_not_dispatched", "correction": "exploration_movement_prewrite_refused"}[kind]
			if got.Reason != expected || got.Traveling {
				t.Fatal("failure cause conflated", kind, got)
			}
			if kind == "geometry_stop_unknown" || kind == "heartbeat_unknown" {
				if !got.MotionUnconfirmed {
					t.Fatal("ambiguous effect cleared", got)
				}
				count := len(f.packets)
				_, _ = s.TravelTo(context.Background(), r)
				if len(f.packets) != count {
					t.Fatal("unknown transport replayed", f.packets)
				}
			}
			stops := countPackets(f, opcode.MSGMoveStop)
			if kind == "heartbeat_unknown" {
				if stops != 0 {
					t.Fatal("blind STOP after ambiguous input", stops)
				}
			} else if stops != 1 {
				t.Fatal("STOP duplicated or omitted", stops, got)
			}
			if kind == "correction" && s.pos.x != 1540 {
				t.Fatal("STOP did not rebase actual correction", s.pos)
			}
		})
	}
}

func TestLegacyNavigationLateCheckpointStillStops(t *testing.T) {
	s, f, advance, _ := explorationRig(t)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("missing initial position")
	}
	path := []position{s.pos, {x: s.pos.x + 20, y: s.pos.y, z: s.pos.z}}
	index := 0
	if !s.navigationStepAtSpeedLocked(context.Background(), path, &index, finite, 7) {
		t.Fatal("start refused")
	}
	advance(850 * time.Millisecond)
	if s.navigationStepAtSpeedLocked(context.Background(), path, &index, finite, 7) || s.state.Moving || s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
		t.Fatal("exploration delay policy escaped into legacy caller", s.stateLocked(), f.packets)
	}
}
