package buddyactions

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestBodyLandingTransportAndFollowCustody(t *testing.T) {
	for _, mode := range []string{"follow", "lost_sight", "shore", "jump", "stop", "revoked", "changed_owner", "never_saw_owner", "no_safe_landing", "disconnected_pool"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, ctx, advance := targetMovementFixture(t)
			seedSyntheticPortalWorldScope(f, 9)
			s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
			toggle(t, s, true)
			f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = 10, 10, -35
			f.world.Owner.MovementFlags = swimMovementFlag
			if mode == "shore" {
				f.world.Owner.MovementFlags = 0
			}
			s.planSwimPath = func(_ context.Context, _ string, _ uint32, from, to buddynav.Point) ([]buddynav.SwimPoint, error) {
				if mode == "disconnected_pool" {
					return nil, errors.New("disconnected mixed route")
				}
				return []buddynav.SwimPoint{{Point: from, Swimming: true}, {Point: to, Swimming: mode != "shore"}}, nil
			}
			if mode == "never_saw_owner" {
				f.world.Owner.Present = false
			}
			if mode == "stop" {
				f.failSend = true
				f.failSendOpcode = opcode.MSGMoveStop
			}
			s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, start buddynav.Point, bearing, speed float64, _ time.Duration, jump bool) ([]buddynav.BodyMotionSample, error) {
				if mode == "no_safe_landing" {
					return nil, errors.New("first contact is solid ground outside the short landing envelope")
				}
				want := math.Pi / 4
				if jump {
					want = .5
				}
				if math.Abs(bearing-want) > .001 || speed != 7 {
					t.Fatal("not the selected input", bearing, speed)
				}
				return []buddynav.BodyMotionSample{{Point: start},
					{At: 50 * time.Millisecond, Point: buddynav.Point{X: .24, Y: .24}},
					{At: 1250 * time.Millisecond, FallTime: 1200 * time.Millisecond, Point: buddynav.Point{X: 6, Y: 6, Z: -13.9}},
					{At: 1950 * time.Millisecond, FallTime: 1900 * time.Millisecond, Swimming: true, Point: buddynav.Point{X: 9.4, Y: 9.4, Z: -34.8}}}, nil
			}
			s.bodyMotionWait = func(_ context.Context, dt time.Duration) error {
				advance(dt)
				if mode == "lost_sight" {
					f.world.Owner.Present = false
				}
				if mode == "revoked" {
					s.followRevision++
				}
				if mode == "changed_owner" {
					f.world.Owner.Guid++
				}
				s.Tick(context.Background())
				return nil
			}
			r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 18, DurationMs: 500, Landing: "follow_water"}
			if mode == "jump" {
				r.Landing = "ground_or_water"
				r.Jump = true
				r.BearingRadians = .5
			}
			out, err := s.MoveBody(ctx, r)
			if mode == "never_saw_owner" {
				if err == nil || len(f.packets) != 0 {
					t.Fatal("started blind", out, err, f.packets)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "no_safe_landing" || mode == "disconnected_pool" {
				if out.BodyMovement.Phase != "local_physics_refused" || out.BodyMovement.TransportWritten || len(f.packets) != 0 {
					t.Fatal("dry cliff wrote input", out, f.packets)
				}
				return
			}
			if mode == "revoked" || mode == "changed_owner" {
				if !out.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartSwim) != 0 {
					t.Fatal("continued revoked input", out, f.packets)
				}
				return
			}
			wantJumps := 0
			if r.Jump {
				wantJumps = 1
			}
			if countPackets(f, opcode.MSGMoveStartSwim) != 1 || countPackets(f, opcode.MSGMoveFallLand) != 0 || countPackets(f, opcode.MSGMoveJump) != wantJumps {
				t.Fatal("incorrect first contact or input framing", f.packets)
			}
			var flight, entry, stop bool
			for _, packet := range f.packets {
				if packet.TransportScope == nil || packet.TransportScope.VisitRevision != 9 {
					t.Fatal("frame escaped its source visit", packet)
				}
				_, n, _ := readGUID(packet.Payload)
				i, l, e := movementinfo.DecodePrefix(packet.Payload[n:])
				if e != nil || l != len(packet.Payload)-n {
					t.Fatal("partial movement report", i, e)
				}
				switch packet.Opcode {
				case opcode.MSGMoveHeartbeat:
					if i.JumpPresent && i.FallTime >= 1200 {
						initial := 0.
						if r.Jump {
							initial = buddynav.BodyJumpVelocity
						}
						if i.FallTime != 1200 || math.Abs(float64(i.Jump.Velocity)-initial) > .0001 || i.Jump.XYSpeed != 7 {
							t.Fatal("incorrect gravity tail", i)
						}
						flight = true
					}
				case opcode.MSGMoveStartSwim:
					entry = i.BodyWaterEntry(true)
				case opcode.MSGMoveStop:
					stop = i.BodyWaterEntry(false)
				}
			}
			if !flight || !entry || !s.swimming {
				t.Fatal("missing physical entry", out, f.packets)
			}
			if mode == "stop" {
				if !out.MotionUnconfirmed || out.BodyMovement.Phase != "body_scope_unconfirmed_no_retry" {
					t.Fatal("lost wet STOP claimed completion", out)
				}
				before := len(f.packets)
				if _, err := s.MoveBody(ctx, r); err == nil || len(f.packets) != before {
					t.Fatal("uncertain movement replayed", err)
				}
				return
			}
			if !stop || out.Moving || out.MotionUnconfirmed || out.BodyMovement.Phase != "body_input_written" || !out.BodyMovement.PositionEstimated || !out.Following {
				t.Fatal("water contact did not leave standing follow", out, f.packets)
			}
		})
	}
}

// Runs the production follow route, action and continuous pool navigation with
// a simulated session and copied map assets. No world connection is involved.
func TestMaraudonFollowWaterExecutionFromMeasuredLedge(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("copied client navigation assets not supplied")
	}
	for _, shore := range []bool{false, true} {
		s, f, _, ctx, advance := targetMovementFixture(t)
		own, leader := f.world.Self, f.world.Owner
		own.MapId, leader.MapId, s.encounterVisit.mapID = 349, 349, 349
		seedSyntheticPortalWorldScope(f, 9)
		s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
		own.X, own.Y, own.Z, own.Orientation = 475.6561, 50.766533, -96.19031, 3.13
		leader.X, leader.Y, leader.Z = 446.6661, 40.353012, -132.54245
		leader.MovementFlags = swimMovementFlag
		if shore {
			leader.X, leader.Y, leader.Z, leader.MovementFlags = 391, 27.2, -130.93, 0
		}
		s.posKnown = false
		s.navDataDir = root
		s.bodyMotionWait = func(_ context.Context, dt time.Duration) error { advance(dt); s.Tick(context.Background()); return nil }
		toggle(t, s, true)
		if !shore {
			s.Tick(context.Background())
		}
		if !shore && s.state.Reason != "swim_follow_route_unavailable" {
			t.Fatalf("normal pre-drop route did not yield: %s %s", s.state.Reason, s.state.NavReason)
		}
		o, err := s.EncounterObservation(ctx, &emptypb.Empty{})
		if err != nil || !o.GetNavigation().GetBodyLandingSupported() || o.GetNavigation().GetFollowOwner().GetSwimming() == shore {
			t.Fatal("follow missing measured capability or original swimmer", o, err)
		}
		out, err := s.MoveBody(ctx, &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 3, DurationMs: 500, Landing: "follow_water"})
		if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || !s.swimming || out.Moving || out.MotionUnconfirmed || math.Abs(float64(s.pos.z)+130.95) > .12 {
			t.Fatal("verified crossing failed", out, err, s.pos)
		}
		if countPackets(f, opcode.MSGMoveStartSwim) != 1 || countPackets(f, opcode.MSGMoveJump) != 0 {
			t.Fatal("wrong movement sequence", f.packets)
		}
		atContact := distance(s.pos, playerPosition(leader))
		for i := 0; i < 6; i++ {
			advance(200 * time.Millisecond)
			if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
				t.Fatal(err)
			}
			s.Tick(context.Background())
		}
		if !s.state.Following || s.state.MotionUnconfirmed || s.state.Reason != "following_mixed_swim_route" || distance(s.pos, playerPosition(leader)) >= atContact-.5 {
			t.Fatal("ordinary follow did not advance in the pool", s.stateLocked(), s.pos, atContact)
		}
	}
}
