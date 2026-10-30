package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func cryptRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ExitCryptRequest, cryptExitPlanner) {
	t.Helper()
	clockService, f, advance := rig(t)
	s := NewIndependentService(f, 6)
	s.now = clockService.now
	p := f.world.Self
	p.Guid, p.ClassId, p.LevelKnown, p.Level = 6, 8, true, 1
	p.X, p.Y, p.Z = 1676.1973, 1671.4958, 136.40988
	f.world.Owner = nil
	f.world.SelfQuestLogKnown = true
	for i := uint32(0); i < 25; i++ {
		f.world.SelfQuestLog = append(f.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: i})
	}
	f.world.SelfQuestLog[0].QuestId, f.world.SelfQuestLog[0].Complete, f.world.SelfQuestLog[0].State = 363, true, 1
	s.SetNavigationDataDir("/fixture/deathknell-client-data")
	s.approachDelay = func(ctx context.Context, d time.Duration) error { advance(d); return ctx.Err() }
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 30000})
	if err != nil {
		t.Fatal(err)
	}
	plan := func(_ context.Context, _ string, from, to position) ([]position, error) {
		// A tiny flat fixture near the endpoint; production always calls the
		// physical client-asset planner, never this synthetic geometry.
		n := int(math.Ceil(distance(from, to)))
		path := make([]position, n+1)
		for i := range path {
			t := float32(i) / float32(n)
			path[i] = position{x: from.x + (to.x-from.x)*t, y: from.y + (to.y-from.y)*t, z: from.z + (to.z-from.z)*t}
		}
		return path, nil
	}
	return s, f, advance, &actionsv1.ExitCryptRequest{Token: lease.Token}, plan
}

func TestCryptExitUsesOwnCompleteQuestAndStopsAtEstimatedFixedLandmark(t *testing.T) {
	s, f, _, r, plan := cryptRig(t)
	got, err := s.exitCrypt(context.Background(), r, plan)
	if err != nil || got.Reason != "crypt_exit_route_completed_client_estimate_requires_observation" || got.Moving || got.Traveling || got.MotionUnconfirmed || !got.NavAvailable || !got.Position.Estimated || got.InteractionOwner != "quests" || got.TravelNpcGuid != 0 {
		t.Fatal(got, err)
	}
	if distance(s.pos, cryptExitLandmark()) > 0.05 || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || len(f.world.QuestGivers) != 0 {
		t.Fatal(s.pos, f.packets, f.world.QuestGivers)
	}
	previous, previousClock := playerPosition(f.world.Self), uint32(0)
	for _, packet := range f.packets {
		_, n, err := readGUID(packet.Payload)
		if err != nil || len(packet.Payload[n:]) != 30 {
			t.Fatal("not an ordinary movement packet", packet)
		}
		body := packet.Payload[n:]
		p := position{x: math.Float32frombits(binary.LittleEndian.Uint32(body[10:])), y: math.Float32frombits(binary.LittleEndian.Uint32(body[14:])), z: math.Float32frombits(binary.LittleEndian.Uint32(body[18:]))}
		clock := binary.LittleEndian.Uint32(body[6:])
		if !deathknellCrypt(p) || clock <= previousClock || distance(previous, p) > 1.401 || math.Abs(float64(p.z-previous.z)) > 2 {
			t.Fatal("movement broke region, timing or speed caps", p, previous, clock, previousClock)
		}
		previous, previousClock = p, clock
	}
}

func TestCryptExitRejectsUnsafeSelfModeAndLogBeforePlanning(t *testing.T) {
	for _, mode := range []string{"companion", "wrongtoken", "shortlease", "map", "outside", "class", "level", "dead", "combat", "speed", "unknownhealth", "unknownflags", "unknownlog", "shortlog", "duplicateslot", "missingquest", "failedquest", "duplicatequest", "cast", "practice", "motion", "attack", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r, _ := cryptRig(t)
			p := f.world.Self
			switch mode {
			case "companion":
				s.independent = false
			case "wrongtoken":
				r.Token = "different-token"
			case "shortlease":
				advance(5 * time.Second)
			case "map":
				p.MapId = 1
			case "outside":
				p.X = 1843.32
			case "class":
				p.ClassId = 5
			case "level":
				p.Level = 6
			case "dead":
				p.Alive = false
			case "combat":
				p.UnitFlags |= 0x80000
			case "speed":
				p.RunSpeed = float32(math.NaN())
			case "unknownhealth":
				p.HealthKnown = false
			case "unknownflags":
				p.MovementFlagsKnown = false
			case "unknownlog":
				f.world.SelfQuestLogKnown = false
			case "shortlog":
				f.world.SelfQuestLog = f.world.SelfQuestLog[:24]
			case "duplicateslot":
				f.world.SelfQuestLog[24].Slot = 0
			case "missingquest":
				f.world.SelfQuestLog[0].QuestId = 0
			case "failedquest":
				f.world.SelfQuestLog[0].Failed = true
			case "duplicatequest":
				f.world.SelfQuestLog[1].QuestId = 363
			case "cast":
				s.state.CastPending = true
			case "practice":
				s.state.Practicing = true
			case "motion":
				s.state.MotionUnconfirmed = true
			case "attack":
				s.autoTarget = 123
			case "concurrent":
				s.questApproaching = true
			}
			got, _ := s.exitCrypt(context.Background(), r, func(context.Context, string, position, position) ([]position, error) {
				t.Fatal("unsafe operation reached planner")
				return nil, nil
			})
			if len(f.packets) != 0 || got != nil && got.Moving {
				t.Fatal("unsafe operation wrote movement", got, f.packets)
			}
		})
	}
}

func TestCryptExitStopsOnCancelExpiryCombatCorrectionAndExplicitStop(t *testing.T) {
	for _, mode := range []string{"cancel", "expiry", "combat", "correction", "explicitstop", "ticklate", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r, plan := cryptRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				advance(d)
				switch mode {
				case "cancel":
					cancel()
				case "expiry":
					advance(31 * time.Second)
				case "combat":
					f.world.Self.UnitFlags |= 0x80000
				case "correction":
					f.world.Self.X, f.world.Self.PositionRevision = 1672, 2
				case "explicitstop":
					_, _ = s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: false})
				case "ticklate":
					advance(time.Second)
				case "snapshot":
					f.failSnapshot = true
				}
				return ctx.Err()
			}
			got, err := s.exitCrypt(ctx, r, plan)
			if mode == "cancel" && status.Code(err) != codes.Canceled {
				t.Fatal(got, err)
			}
			if got.Moving || got.Traveling || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal("operation restarted or failed to stop", got, err, f.packets)
			}
		})
	}
}

func TestCryptExitPlanningRejectsRevocationReplacementStopAndCorrection(t *testing.T) {
	for _, mode := range []string{"revoke", "replace", "stop", "correction", "concurrent", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r, plan := cryptRig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got, err := s.exitCrypt(ctx, r, func(ctx context.Context, root string, from, to position) ([]position, error) {
				switch mode {
				case "revoke", "replace":
					if _, err := s.ReleaseInteraction(ctx, &actionsv1.InteractionRelease{Owner: "quests", Token: r.Token}); err != nil {
						t.Fatal(err)
					}
					if mode == "replace" {
						if _, err := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 30000}); err != nil {
							t.Fatal(err)
						}
						s.state.Reason = "replacement_owner_reason"
					}
				case "stop":
					if _, err := s.Follow(ctx, &actionsv1.ToggleRequest{Enabled: false}); err != nil {
						t.Fatal(err)
					}
				case "correction":
					f.world.Self.PositionRevision++
				case "concurrent":
					if _, err := s.ExitCrypt(ctx, r); status.Code(err) != codes.FailedPrecondition {
						t.Fatal("concurrent operation admitted", err)
					}
					cancel()
				case "cancel":
					cancel()
				}
				return plan(ctx, root, from, to)
			})
			if len(f.packets) != 0 || got.Moving || got.Traveling {
				t.Fatal("planning invalidation wrote movement", got, err, f.packets)
			}
			if mode == "replace" && got.Reason != "replacement_owner_reason" {
				t.Fatal("old operation overwrote replacement owner state", got)
			}
		})
	}
}

type cryptSetupStopContext struct {
	context.Context
	once sync.Once
	stop func()
}

func (c *cryptSetupStopContext) Deadline() (time.Time, bool) {
	// WithTimeout checks the parent after the initial reservation was released
	// and before the first observation lock. This makes that scheduling gap
	// deterministic without adding a production test hook or a goroutine.
	c.once.Do(c.stop)
	return c.Context.Deadline()
}

func TestCryptExitInitialReservationHonorsExplicitStop(t *testing.T) {
	s, f, _, r, _ := cryptRig(t)
	ctx := &cryptSetupStopContext{Context: context.Background(), stop: func() {
		if _, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); err != nil {
			t.Fatal(err)
		}
	}}
	got, err := s.exitCrypt(ctx, r, func(context.Context, string, position, position) ([]position, error) {
		t.Fatal("a stop after reservation was overwritten before planning")
		return nil, nil
	})
	if err != nil || got.Reason != "crypt_exit_interrupted_by_controller_change" || len(f.packets) != 0 || got.Moving || got.Traveling {
		t.Fatal(got, err, f.packets)
	}
}

func TestCryptExitInitialReservationDoesNotOverwriteReplacementOwner(t *testing.T) {
	s, f, _, r, _ := cryptRig(t)
	ctx := &cryptSetupStopContext{Context: context.Background(), stop: func() {
		if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "quests", Token: r.Token}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 30000}); err != nil {
			t.Fatal(err)
		}
		s.state.Reason = "replacement_owner_reason"
	}}
	got, err := s.exitCrypt(ctx, r, func(context.Context, string, position, position) ([]position, error) {
		t.Fatal("old operation planned after its lease was replaced")
		return nil, nil
	})
	if status.Code(err) != codes.FailedPrecondition || got.Reason != "replacement_owner_reason" || len(f.packets) != 0 || got.Traveling || got.Moving || s.interactionToken == r.Token {
		t.Fatal(got, err, f.packets)
	}
}

func TestCryptExitFailedStopIsNotAutomaticallyRetried(t *testing.T) {
	s, f, advance, r, plan := cryptRig(t)
	s.approachDelay = func(ctx context.Context, d time.Duration) error {
		advance(d)
		f.world.Self.UnitFlags |= 0x80000
		f.failSend = true
		return nil
	}
	got, err := s.exitCrypt(context.Background(), r, plan)
	if err != nil || !got.MotionUnconfirmed || got.Moving || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(got, err, f.packets)
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("ambiguous STOP retried automatically", f.packets)
	}
}

func TestCryptExitRejectsMissingPartialAndEscapingRoutes(t *testing.T) {
	for _, mode := range []string{"missing", "onepoint", "outside", "vertical", "partial", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, r, plan := cryptRig(t)
			got, err := s.exitCrypt(context.Background(), r, func(ctx context.Context, root string, from, to position) ([]position, error) {
				path, _ := plan(ctx, root, from, to)
				switch mode {
				case "missing":
					return nil, errors.New("missing crypt data")
				case "onepoint":
					return path[:1], nil
				case "outside":
					path[1].x = 1843
				case "vertical":
					path[1].z += 1.1
				case "partial":
					path = path[:2]
				case "deadline":
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return path, nil
			})
			if err != nil || got.NavAvailable || got.Reason != "crypt_exit_navigation_unavailable" || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}

func TestPhysicalCryptExitControllerUsesMeasuredStairsAndStops(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_DEATHKNELL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("independently copied public Deathknell client assets required")
	}
	s, f, _, r, _ := cryptRig(t)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = 1676.71, 1678.31, 121.67
	f.world.Self.PositionRevision++
	s.SetNavigationDataDir(dir)
	got, err := s.ExitCrypt(context.Background(), r)
	if err != nil || got.Reason != "crypt_exit_route_completed_client_estimate_requires_observation" || got.Moving || got.Traveling || got.MotionUnconfirmed || !got.Position.Estimated || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		body, _ := json.Marshal(got)
		t.Fatal(string(body), err, len(f.packets))
	}
	if distance(s.pos, cryptExitLandmark()) > 0.05 || countPackets(f, opcode.MSGMoveHeartbeat) < 100 {
		t.Fatal("controller skipped the physical stair route", s.pos, len(f.packets))
	}
}
