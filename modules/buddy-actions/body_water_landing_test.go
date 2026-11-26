package buddyactions

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
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

// Exercise both ordinary entry points, without a selected MoveBody or a Lua
// token. Only copied client map geometry and a simulated session are involved.
func TestMaraudonFollowWaterExecutionFromMeasuredLedge(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("copied client navigation assets not supplied")
	}
	for _, mode := range []string{"follow", "travel", "shore", "unsupported", "falling", "cannot_swim", "release", "last_seen", "visible_again", "offline", "formation"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, _, advance := targetMovementFixture(t)
			me, leader := f.world.Self, f.world.Owner
			me.MapId, leader.MapId, s.encounterVisit.mapID = 349, 349, 349
			seedSyntheticPortalWorldScope(f, 9)
			s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
			me.X, me.Y, me.Z, me.Orientation = 479.38, 49.15, -96.24, 3.13
			leader.X, leader.Y, leader.Z, leader.MovementFlags = 444.1598, 48.505165, -132.1898, swimMovementFlag
			if mode == "unsupported" || mode == "falling" {
				me.X, me.Y, me.Z, me.Orientation = 469.89444, 50.35286, -99.962814, 3.13
				if mode == "falling" {
					me.MovementFlags = 0x1000
					raw, e := movementinfo.Encode(movementinfo.Info{Flags: 0x1000, X: me.X, Y: me.Y, Z: me.Z, Orientation: me.Orientation, FallTime: 270, JumpPresent: true})
					if e != nil {
						t.Fatal(e)
					}
					f.world.SelfMovementFacts = &worldv1.OwnMovementFacts{WireImage: raw, Known: true, SelfGuid: me.Guid, VisitRevision: f.world.VisitRevision, Revision: 1, MapKnown: true, MapId: 349, Info: &worldv1.MovementInfoFacts{Flags: 0x1000, X: me.X, Y: me.Y, Z: me.Z, FallTime: 270, Jump: &worldv1.MovementJumpFacts{}}}
				}
			}
			if mode == "cannot_swim" {
				me.UnitFlags |= 0x4000
			}
			if mode == "shore" {
				leader.X, leader.Y, leader.Z, leader.MovementFlags = 391, 27.2, -130.93, 0
			}
			s.posKnown, s.navDataDir = false, dir
			s.planMapPath = planMapNavigation
			released := false
			s.bodyMotionWait = func(_ context.Context, dt time.Duration) error {
				advance(dt)
				if mode == "release" && !released && s.destinationFlight != nil && s.destinationFlight.airborne && s.pos.z < -101 {
					released = true
					toggle(t, s, false)
				}
				s.Tick(context.Background())
				return nil
			}
			s.approachDelay = func(ctx context.Context, dt time.Duration) error { advance(dt); return ctx.Err() }
			ctx := context.Background()
			if mode == "travel" {
				held, e := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
				if e != nil {
					t.Fatal(e)
				}
				dest := playerPosition(leader)
				f.world.Owner = nil
				out, e := s.TravelTo(ctx, &actionsv1.TravelToRequest{Token: held.Token, MapId: 349, X: dest.x, Y: dest.y, Z: dest.z})
				if e != nil || out.GetReason() != "exploration_within_distance" || out.GetMotionUnconfirmed() || distance(s.pos, dest) > 3 {
					t.Fatal("normal destination", out, e, s.pos)
				}
			} else {
				toggle(t, s, mode != "formation")
				if mode == "last_seen" || mode == "visible_again" || mode == "offline" {
					leader.X, leader.Y, leader.Z, leader.MovementFlags = 388.42874, 29.081429, -130.93112, 0
					s.mu.Lock()
					s.snapshot(ctx)
					s.mu.Unlock()
					leader.Present = false
					if mode == "offline" {
						f.party.Group.Members[1].Online = false
					}
				}
				for i := 0; i < 150; i++ {
					if mode == "formation" {
						host := encounterHostContext(testEncounterKey, 1)
						if _, e := s.RenewPolicyEncounterLease(host, wrapperspb.UInt64(1)); e != nil {
							t.Fatal(e)
						}
						if _, e := s.SetFollowFormation(host, &actionsv1.FollowFormationRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, FollowRevision: s.followRevision, OwnerGuid: leader.Guid, OwnerPositionRevision: leader.PositionRevision, X: leader.X, Y: leader.Y, Z: leader.Z, StartDistance: 2, StopDistance: 1, Ready: true}); e != nil {
							t.Fatal(e)
						}
					}
					if mode == "visible_again" && i == 9 {
						leader.Present = true
						leader.X, leader.Y, leader.Z, leader.MovementFlags = 444.1598, 48.505165, -132.1898, swimMovementFlag
					}
					advance(200 * time.Millisecond)
					s.runTick(ctx)
					if released || mode == "offline" && i == 2 || (mode != "last_seen" || s.state.Reason == "at_last_seen_follow_position") && countPackets(f, opcode.MSGMoveStartSwim) > 0 && distance(s.pos, playerPosition(leader)) < 3 {
						break
					}
					if s.state.MotionUnconfirmed {
						break
					}
				}
				if mode == "release" {
					if !released || s.state.Following || s.state.MotionUnconfirmed || s.state.Moving || s.pos.z >= -101 || countPackets(f, opcode.MSGMoveStop) == 0 {
						t.Fatal("released fall", s.state.Reason, s.state.NavReason, s.pos)
					}
					return
				}
				if mode == "last_seen" && s.state.Reason != "at_last_seen_follow_position" {
					t.Fatal("stale destination labelled as current", s.state.Reason, s.state.NavReason, s.pos)
				}
				if mode == "cannot_swim" || mode == "offline" {
					if len(f.packets) != 0 {
						t.Fatal("restricted swimmer was moved", len(f.packets))
					}
					return
				}
				if distance(s.pos, playerPosition(leader)) >= 3 || s.state.MotionUnconfirmed {
					t.Fatalf("ordinary follow: %v %v pos=%v remaining=%.2f", s.state.Reason, s.state.NavReason, s.pos, distance(s.pos, playerPosition(leader)))
				}
			}
			if countPackets(f, opcode.MSGMoveStartSwim) != 1 || countPackets(f, opcode.MSGMoveJump) != 0 {
				t.Fatal("wrong physical transitions", len(f.packets), s.state.Reason)
			}
		})
	}
}

// The package CI runs these normal owner/transport checks even without copied
// client assets. The planner's measured trajectory is supplied by a fixture.
func TestOrdinaryDestinationUsesMeasuredFallUnderExistingOwner(t *testing.T) {
	for _, mode := range []string{"follow", "travel", "no_entry", "cannot_swim", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, _, advance := targetMovementFixture(t)
			seedSyntheticPortalWorldScope(f, 9)
			s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
			me, dest := f.world.Self, f.world.Owner
			dest.X, dest.Y, dest.Z, dest.MovementFlags = me.X+8, me.Y, me.Z-2, swimMovementFlag
			s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				return nil, errors.New("navigation polygon path is partial")
			}
			s.planSwimPath = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.SwimPoint, error) {
				return nil, errors.New("disconnected mixed route")
			}
			s.planWaterEntry = func(_ context.Context, _ string, _ uint32, a, b buddynav.Point, speed float64) (buddynav.DestinationRoute, error) {
				if mode == "no_entry" {
					return buddynav.DestinationRoute{}, errors.New("solid first contact")
				}
				air, contact := a, a
				air.X, air.Z = a.X+.7, a.Z-.096
				contact.X, contact.Z = a.X+3.15, a.Z-1.95
				return buddynav.DestinationRoute{Approach: []buddynav.Point{a}, RunSpeed: speed, Fall: []buddynav.BodyMotionSample{{Point: a}, {Point: air, At: 100 * time.Millisecond, FallTime: 100 * time.Millisecond}, {Point: contact, At: 450 * time.Millisecond, FallTime: 450 * time.Millisecond, Swimming: true}}, Onward: []buddynav.SwimPoint{{Point: contact, Swimming: true}, {Point: b, Swimming: true}}}, nil
			}
			s.bodyMotionWait = func(ctx context.Context, dt time.Duration) error { advance(dt); return ctx.Err() }
			s.approachDelay = s.bodyMotionWait
			if mode == "cannot_swim" {
				me.UnitFlags |= 0x4000
			}
			if mode == "unknown" {
				f.failSend, f.failSendOpcode = true, opcode.MSGMoveStartSwim
			}
			ctx := context.Background()
			if mode == "travel" {
				held, e := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
				if e != nil {
					t.Fatal(e)
				}
				r := &actionsv1.TravelToRequest{Token: held.Token, MapId: me.MapId, X: dest.X, Y: dest.Y, Z: dest.Z}
				f.world.Owner = nil
				out, e := s.TravelTo(ctx, r)
				if e != nil || out.GetReason() != "exploration_within_distance" {
					t.Fatal(out, e)
				}
			} else {
				toggle(t, s, true)
				for i := 0; i < 8; i++ {
					advance(200 * time.Millisecond)
					s.runTick(ctx)
				}
			}
			switch mode {
			case "no_entry", "cannot_swim":
				if len(f.packets) != 0 {
					t.Fatal("unverified water route wrote packets", len(f.packets))
				}
			case "unknown":
				if !s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStartForward) != 1 {
					t.Fatal("ambiguous motion retried or cleared", s.stateLocked())
				}
			default:
				if countPackets(f, opcode.MSGMoveStartSwim) != 1 || countPackets(f, opcode.MSGMoveJump) != 0 || s.state.MotionUnconfirmed || !s.swimming {
					t.Fatal("ordinary route lost its verified water contact", s.stateLocked())
				}
			}
		})
	}
}

func TestDestinationGravityPinsOriginalFrameAcrossPlanning(t *testing.T) {
	s, f, _, _, _ := targetMovementFixture(t)
	w := f.world
	me := w.Self
	me.MovementFlags = 0x1000
	packet, e := movementinfo.Encode(movementinfo.Info{Flags: 0x1000, X: me.X, Y: me.Y, Z: me.Z, Orientation: me.Orientation, JumpPresent: true, FallTime: 150})
	if e != nil {
		t.Fatal(e)
	}
	w.SelfMovementFacts = &worldv1.OwnMovementFacts{Known: true, SelfGuid: s.selfGUID, VisitRevision: w.VisitRevision, MapKnown: true, MapId: me.MapId, Revision: 8, WireImage: packet, Info: &worldv1.MovementInfoFacts{Flags: 0x1000, X: me.X, Y: me.Y, Z: me.Z, FallTime: 150, Jump: &worldv1.MovementJumpFacts{}}}
	src := destinationGravitySource(w, playerPosition(me))
	w.SelfMovementFacts.AgeMs = 4000
	if !src.current(w) || destinationGravitySource(w, playerPosition(me)).known {
		t.Fatal("planning time replaced a pinned original or admitted a newly stale fall")
	}
	w.SelfMovementFacts.Revision++
	if src.current(w) {
		t.Fatal("replacement frame admitted")
	}
	w.SelfMovementFacts.Revision--
	w.SelfMovementFacts.WireImage[0] ^= 1
	if src.current(w) {
		t.Fatal("changed physical bytes admitted")
	}
}
