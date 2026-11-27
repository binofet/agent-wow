package buddyactions

import (
	"context"
	"encoding/binary"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"math"
	"testing"
	"time"
)

func explorationPacketPosition(t *testing.T, payload []byte) position {
	t.Helper()
	_, n, err := readGUID(payload)
	if err != nil || len(payload[n:]) != 30 {
		t.Fatal(err, payload)
	}
	b := payload[n:]
	return position{x: math.Float32frombits(binary.LittleEndian.Uint32(b[10:])), y: math.Float32frombits(binary.LittleEndian.Uint32(b[14:])), z: math.Float32frombits(binary.LittleEndian.Uint32(b[18:]))}
}
func TestExplorationUsesObservedBoostedOnFootSpeed(t *testing.T) {
	for _, speed := range []float32{4.9, 7, 7.5600004, 8.4, 14} {
		t.Run(time.Duration(speed*1000).String(), func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
			f.world.Self.RunSpeed = speed
			elapsed := time.Duration(0)
			packets := 0
			previous := s.pos
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				if d < time.Millisecond || d > explorationCheckpointDelay(float64(speed)) {
					t.Fatal("cadence ignored server speed", speed, d)
				}
				for ; packets < len(f.packets); packets++ {
					p := f.packets[packets]
					if p.Opcode != opcode.MSGMoveStartForward && p.Opcode != opcode.MSGMoveHeartbeat {
						continue
					}
					now := explorationPacketPosition(t, p.Payload)
					if distance(previous, now) > float64(speed)*elapsed.Seconds()+.002 {
						t.Fatal("step exceeds observed velocity", speed, elapsed, previous, now)
					}
					previous = now
				}
				elapsed = d
				advance(d)
				return ctx.Err()
			}
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got.Reason != "exploration_within_distance" || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStop) != 1 || distance(s.pos, position{x: r.X, y: r.Y, z: r.Z}) > 3 {
				t.Fatal(speed, got, err)
			}
		})
	}
}
func TestExplorationSupportedSpeedChangeDuringLegIsNotRetroactive(t *testing.T) {
	for _, speeds := range [][2]float32{{7, 7.5600004}, {7.5600004, 7}} {
		t.Run(time.Duration(speeds[0]*1000).String(), func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
			f.world.Self.RunSpeed = speeds[0]
			delays := 0
			previous := s.pos
			lastPacket := 0
			elapsed := time.Duration(0)
			previousSpeed := float64(speeds[0])
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				current := float64(f.world.Self.RunSpeed)
				allowed := math.Min(previousSpeed, current)*elapsed.Seconds() + .002
				for ; lastPacket < len(f.packets); lastPacket++ {
					p := f.packets[lastPacket]
					if p.Opcode != opcode.MSGMoveStartForward && p.Opcode != opcode.MSGMoveHeartbeat {
						continue
					}
					now := explorationPacketPosition(t, p.Payload)
					if distance(previous, now) > allowed {
						t.Fatal("new speed applied to past interval", speeds, previous, now, allowed)
					}
					previous = now
				}
				if delays > 0 && (d < time.Millisecond || d > explorationCheckpointDelay(current)) {
					t.Fatal(d, current)
				}
				elapsed = d
				previousSpeed = current
				advance(d)
				delays++
				if delays == 2 {
					f.world.Self.RunSpeed = speeds[1]
				}
				return ctx.Err()
			}
			got, err := s.TravelTo(context.Background(), r)
			if err != nil || got.Reason != "exploration_within_distance" || got.Moving || got.Traveling || delays < 3 || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(got, err, delays)
			}
		})
	}
}
func TestExplorationUnknownOrInvalidObservedSpeedStopsWithoutFurtherHeartbeat(t *testing.T) {
	for _, speed := range []float32{0, -1, 14.1, float32(math.NaN()), float32(math.Inf(1))} {
		s, f, advance, r := explorationRig(t)
		s.approachDelay = func(ctx context.Context, d time.Duration) error {
			advance(d)
			f.world.Self.RunSpeed = speed
			return ctx.Err()
		}
		got, err := s.TravelTo(context.Background(), r)
		if err != nil || got.Reason != "exploration_observed_run_speed_required" || got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
			t.Fatal(speed, got, err)
		}
	}
	s, f, advance, r := explorationRig(t)
	s.approachDelay = func(ctx context.Context, d time.Duration) error {
		advance(d)
		f.world.Self.SpeedsKnown = false
		return ctx.Err()
	}
	got, err := s.TravelTo(context.Background(), r)
	if err != nil || got.Reason != "exploration_observed_run_speed_required" || got.Moving || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err)
	}
}

func TestExplorationRefreshesSupportedSpeedAfterUnlockedPlanning(t *testing.T) {
	s, f, advance, r := explorationRig(t)
	planner := s.planMapPath
	s.planMapPath = func(ctx context.Context, root string, mapID uint32, from, to position) ([]position, error) {
		path, err := planner(ctx, root, mapID, from, to)
		f.world.Self.RunSpeed = 7.5600004
		return path, err
	}
	waits := 0
	s.approachDelay = func(ctx context.Context, d time.Duration) error {
		waits++
		if d < time.Millisecond || d > explorationCheckpointDelay(float64(f.world.Self.RunSpeed)) {
			t.Fatal("stale planning speed used for cadence", d)
		}
		advance(d)
		return ctx.Err()
	}
	got, err := s.TravelTo(context.Background(), r)
	if err != nil || got.Reason != "exploration_within_distance" || got.Moving || got.Traveling || waits == 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err)
	}
}
