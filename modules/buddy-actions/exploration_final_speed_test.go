package buddyactions

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
	"time"
)

func TestExplorationFinalClockSpeedChangeRefusesStaleCheckpoint(t *testing.T) {
	for _, kind := range []string{"slowdown", "unknown", "invalid", "increase"} {
		t.Run(kind, func(t *testing.T) {
			s, f, advance, r := explorationRig(t)
			s.approachDelay = func(ctx context.Context, _ time.Duration) error {
				advance(850 * time.Millisecond)
				f.onClock = func() {
					f.onClock = nil
					switch kind {
					case "slowdown":
						f.world.Self.RunSpeed = 3.5
					case "unknown":
						f.world.Self.SpeedsKnown = false
					case "invalid":
						f.world.Self.RunSpeed = 0
					case "increase":
						f.world.Self.RunSpeed = 14
					}
				}
				return ctx.Err()
			}
			out, err := s.TravelTo(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "increase" {
				if out.Reason != "exploration_observed_run_speed_changed" || out.MotionUnconfirmed || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
					t.Fatal(out, f.packets)
				}
			} else {
				var prev position
				first := true
				for _, pkt := range f.packets {
					p := explorationPacketPosition(t, pkt.Payload)
					if pkt.Opcode == opcode.MSGMoveHeartbeat && first {
						if distance(prev, p) > 7*.6+.01 {
							t.Fatal("faster fresh speed applied retroactively", prev, p)
						}
						first = false
					}
					prev = p
				}
				if first {
					t.Fatal("no bounded heartbeat")
				}
			}
		})
	}
}

func TestExplorationFinalScopedReadSpeedRefusalKeepsOneCleanupStop(t *testing.T) {
	s, f, r, ctx, advance := idleBodyFixture(t, true)
	s.refreshPositionLocked(f.world)
	scope, err := s.newIdleMotionScopeLocked(ctx, r.IdleToken, f.world, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
	if !s.movementLocked(ctx, opcode.MSGMoveStartForward, s.pos, true) {
		t.Fatal(s.stateLocked())
	}
	advance(850 * time.Millisecond)
	// First snapshot is the generic movement check; the second is the final
	// scoped transport admission. Change speed only at that final existing read.
	reads := 0
	s.session = &thermapluggTestSession{fakeSession: f, onSnapshot: func() {
		reads++
		if reads == 2 {
			f.world.Self.RunSpeed = 3.5
		}
	}}
	path := []position{s.pos, {x: s.pos.x + 10, y: s.pos.y, z: s.pos.z}}
	index := 0
	ok, reason := s.navigationStepResultLocked(ctx, path, &index, finite, 7, true)
	if ok || reason != "observed_run_speed_changed" || reads != 2 || scope.broken || s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
		t.Fatal(ok, reason, reads, s.stateLocked(), f.packets)
	}
	if !s.stopLocked(ctx) || countPackets(f, opcode.MSGMoveStop) != 1 || s.state.MotionUnconfirmed {
		t.Fatal("known prewrite refusal lost safe scoped STOP", s.stateLocked(), f.packets)
	}
}
