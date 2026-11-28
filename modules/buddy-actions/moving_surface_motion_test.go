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
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const surfaceTestShip uint64 = 0x1fc0000000000021

var surfaceTestDeck = buddynav.Point{X: 1.0764359, Y: 4.6327205, Z: 6.0980916}
var surfaceTestBow = buddynav.Point{X: 36.27977, Y: 2.2660532, Z: 6.094307}

// This synthetic socket enforces the normal route marker, exact Core scope
// and fresh per-frame attempt. It retains a physical report only on success.
type surfaceTestSession struct {
	*fakeSession
	t        *testing.T
	now      func() time.Time
	attempts map[string]bool
}

func (f *surfaceTestSession) GetClock(ctx context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*modv1.ClockResponse, error) {
	if f.onClock != nil {
		f.onClock()
	}
	return &modv1.ClockResponse{ClientTimeMs: uint32(f.now().UnixMilli())}, ctx.Err()
}

func (f *surfaceTestSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.t.Helper()
	md, _ := metadata.FromOutgoingContext(ctx)
	in := metadata.NewIncomingContext(ctx, md)
	id := modcb.IncomingMovementAttempt(in)
	followsOwner, ok, supplied := modcb.IncomingRouteBodyInput(in)
	ship, birth, seen, valid, marked := modcb.IncomingRouteHull(in)
	hull := movingHull(f.world, surfaceTestShip)
	if !valid || !marked || ship != surfaceTestShip || birth != hull.GetMovingTransport().GetObjectRevision() ||
		seen != f.world.ObservedPackets+f.world.IgnoredPackets+f.world.MalformedPackets {
		f.t.Fatalf("missing final World hull or packet sequence on opcode %d: %x, %d, %d", r.Opcode, ship, birth, seen)
	}
	if !ok || !supplied || followsOwner || !modcb.ValidMovementAttempt(id) || f.attempts[id] ||
		!proto.Equal(r.TransportScope, f.portalCoreScope) || !buddynav.RouteBodyFrame(r.Opcode, r.Payload, f.world.Self.Guid) {
		f.t.Fatalf("invalid ordinary surface frame or scope: opcode=%d attempt=%q marker=%v/%v/%v scope=%+v", r.Opcode, id, followsOwner, ok, supplied, r.TransportScope)
	}
	f.attempts[id] = true
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	if f.failSend && (f.failSendOpcode == 0 || f.failSendOpcode == r.Opcode) {
		return nil, errors.New("ambiguous synthetic surface write")
	}
	f.portalCoreScope.MotionRevision++
	f.ownMovementAttempt = id
	f.ownMovementReport = &modv1.WorldPacket{Opcode: r.Opcode, Payload: append([]byte(nil), r.Payload...), TransportScope: clonePortalScope(f.portalCoreScope)}
	return &emptypb.Empty{}, nil
}

type surfaceTestRig struct {
	*surfaceTestSession
	s        *Service
	ctx      context.Context
	request  *actionsv1.FollowFormationRequest
	tr       buddynav.SurfaceTransform
	progress uint32
	advance  func(time.Duration)
}

func newSurfaceTestRig(t *testing.T, progress uint32, own, owner buddynav.Point, onShip, ownerOnShip bool) *surfaceTestRig {
	t.Helper()
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_SURFACE_TEST_DATA_DIR for the copied client map, ship and ferry DBC")
	}
	s, f, g, ctx, advance := followGoalFixture(t)
	path, err := buddynav.LoadTransportPath(ctx, dir, 292, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := path.Sample(uint64(progress))
	if err != nil {
		t.Fatal(err)
	}
	tr := buddynav.SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}
	s.SetNavigationDataDir(dir)
	s.encounterVisit.mapID = sample.MapID
	f.world.Owner.MapId, f.world.Self.MapId = sample.MapID, sample.MapID
	f.world.ObservedPackets, f.world.IgnoredPackets, f.world.MalformedPackets = 11, 2, 1
	seedSyntheticPortalWorldScope(f, 9)
	r := &surfaceTestRig{surfaceTestSession: &surfaceTestSession{fakeSession: f, t: t, now: s.now, attempts: map[string]bool{}}, s: s, ctx: ctx, request: g, tr: tr, progress: progress, advance: advance}
	s.session = r.surfaceTestSession
	hull := &worldv1.GameObjectState{Guid: surfaceTestShip, Entry: 77, Present: true, MapKnown: true, MapId: sample.MapID, TypeKnown: true, GameObjectType: 15, PositionKnown: true,
		X: sample.Position.X, Y: sample.Position.Y, Z: sample.Position.Z, Orientation: sample.Yaw,
		MovingTransport: &worldv1.MovingTransportFacts{ObjectRevision: 4, PositionRevision: 7, MovementRevision: 7, SourceOpcode: opcode.SMSGUpdateObject, DisplayId: proto.Uint32(3015), Scale: proto.Float32(1), PeriodMs: proto.Uint32(path.Period()), PathProgressMs: proto.Uint32(progress),
			Template: &worldv1.MovingTransportTemplate{Guid: surfaceTestShip, Entry: 77, SelfGuid: s.selfGUID, VisitRevision: 1, Revision: 3, DisplayId: 3015, TaxiPathId: 292, MoveSpeed: 30, AccelerationRate: 3}}}
	f.world.MovingTransports = []*worldv1.GameObjectState{hull}
	r.player(f.world.Self, own, onShip)
	r.player(f.world.Owner, owner, ownerOnShip)
	g.TransportGuid = surfaceTestShip
	g.StartDistance, g.StopDistance = 3, 2
	r.ownerGoal()
	return r
}

func (r *surfaceTestRig) player(p *worldv1.PlayerState, local buddynav.Point, onShip bool) {
	q := r.tr.World(local)
	i := movementinfo.Info{Time: uint32(r.now().UnixMilli()), X: q.X, Y: q.Y, Z: q.Z, Orientation: r.tr.Yaw}
	if onShip {
		setPassengerFrame(&i, surfaceTestShip, r.tr, r.progress)
	}
	wire, err := movementinfo.Encode(i)
	if err != nil {
		r.t.Fatal(err)
	}
	p.PositionRevision++
	p.X, p.Y, p.Z, p.Orientation, p.MovementFlags = i.X, i.Y, i.Z, i.Orientation, i.Flags
	facts := &worldv1.MovementInfoFacts{X: i.X, Y: i.Y, Z: i.Z, Orientation: i.Orientation, Flags: i.Flags, Time: i.Time}
	if onShip {
		facts.Transport = &worldv1.MovementTransportFacts{Guid: i.Transport.GUID, X: i.Transport.X, Y: i.Transport.Y, Z: i.Transport.Z, Orientation: i.Transport.Orientation, Time: i.Transport.Time, Seat: -1}
	}
	p.PassengerMovement = &worldv1.PassengerMovementFacts{VisitRevision: r.world.VisitRevision, Revision: p.PositionRevision, Info: facts}
	if p == r.world.Self {
		r.world.SelfMovementFacts = &worldv1.OwnMovementFacts{Known: true, SelfGuid: p.Guid, VisitRevision: r.world.VisitRevision, MapKnown: true, MapId: p.MapId, Revision: p.PositionRevision, WireImage: wire, Info: facts, TransportScope: clonePortalScope(r.portalCoreScope)}
	}
}

func (r *surfaceTestRig) ownerGoal() {
	o := r.world.Owner
	r.request.OwnerPositionRevision, r.request.OwnerPassengerRevision = o.PositionRevision, o.PassengerMovement.Revision
	r.request.X, r.request.Y, r.request.Z, r.request.Orientation = o.X, o.Y, o.Z, o.Orientation
}

func (r *surfaceTestRig) set(mode string) {
	r.t.Helper()
	r.request.SurfaceMode = mode
	if _, err := r.s.SetFollowFormation(r.ctx, r.request); err != nil {
		r.t.Fatal("ordinary surface goal", mode, err)
	}
}

func (r *surfaceTestRig) tick(elapsed time.Duration) {
	r.t.Helper()
	r.advance(elapsed)
	r.world.MovingTransports[0].MovingTransport.MovementAgeMs += uint64(elapsed / time.Millisecond)
	for _, p := range []*worldv1.PlayerState{r.world.Owner, r.world.Self} {
		if p.PassengerMovement != nil {
			p.PassengerMovement.AgeMs += uint64(elapsed / time.Millisecond)
		}
	}
	if _, err := r.s.RenewPolicyEncounterLease(r.ctx, wrapperspb.UInt64(r.world.VisitRevision)); err != nil {
		r.t.Fatal(err)
	}
	r.s.Tick(context.Background())
}

func (r *surfaceTestRig) frame(n int) movementinfo.Info {
	r.t.Helper()
	if n < 0 || n >= len(r.packets) {
		r.t.Fatalf("absent frame %d; writes=%d phase=%s nav=%s", n, len(r.packets), r.s.state.Reason, r.s.state.NavReason)
	}
	i, ok := movementinfo.OwnFrame(r.packets[n].Payload, r.s.selfGUID)
	if !ok {
		r.t.Fatal("invalid written own frame", n)
	}
	return i
}

func TestSurfaceFollowMeasuredContactAndQuietRide(t *testing.T) {
	r := newSurfaceTestRig(t, 30000, surfaceTestDeck, surfaceTestDeck, false, true)
	r.set("board")
	r.tick(0)
	i := r.frame(0)
	if len(r.packets) != 1 || r.packets[0].Opcode != opcode.MSGMoveSetFacing || i.Flags != passengerFlag || i.Transport.GUID != surfaceTestShip || i.Transport.Seat != -1 || i.Transport.Time != 30000 || surfaceDistance(surfaceLocalPoint(i), surfaceTestDeck) > .002 {
		t.Fatalf("board %+v", i)
	}
	obs := r.s.surfaceObservationLocked(context.Background(), r.world)
	if obs.SelfAttachmentKnown || !obs.SelfWriteKnown || obs.SelfWrittenTransportGuid != surfaceTestShip || r.world.Self.PassengerMovement.Info.Transport != nil {
		t.Fatal("attachment provenance", obs)
	}
	r.set("ride")
	for range 4 {
		r.tick(100 * time.Millisecond)
		r.set("ride")
	}
	if len(r.packets) != 1 || r.s.surfaceFollow.phase != "surface_riding" {
		t.Fatal("stationary rider shuffled", len(r.packets), r.s.state.Reason, r.s.state.NavReason)
	}
}

func TestSurfaceFollowMovingDeckAndExplicitStop(t *testing.T) {
	r := newSurfaceTestRig(t, 10000, surfaceTestDeck, surfaceTestBow, true, true)
	r.set("ride")
	r.tick(0)
	start := r.frame(0)
	if r.packets[0].Opcode != opcode.MSGMoveStartForward || !start.TransportPresent {
		t.Fatal(start)
	}
	r.tick(325 * time.Millisecond)
	r.tick(time.Millisecond)
	r.tick(time.Millisecond)
	for n := 1; n < len(r.packets); n++ {
		i, prior := r.frame(n), r.frame(n-1)
		if !i.TransportPresent || i.Transport.GUID != surfaceTestShip || i.Transport.Seat != -1 || i.Transport.Time <= prior.Transport.Time ||
			i.Time-prior.Time < 95 || i.Time-prior.Time > 105 || math.Abs(float64(i.Time-prior.Time)-float64(i.Transport.Time-prior.Transport.Time)) > 1 {
			t.Fatalf("delayed host compressed movement or lost ship on step %d: %+v %+v", n, prior, i)
		}
	}
	if len(r.packets) != 4 {
		t.Fatalf("missing measured route steps %d: %s %s", len(r.packets), r.s.state.Reason, r.s.state.NavReason)
	}
	prior := r.frame(len(r.packets) - 1)
	toggle(t, r.s, false)
	end := r.frame(len(r.packets) - 1)
	if countPackets(r.fakeSession, opcode.MSGMoveStop) != 1 || end.Flags != passengerFlag || end.Transport.GUID != start.Transport.GUID || surfaceDistance(surfaceLocalPoint(end), surfaceLocalPoint(prior)) > .002 {
		t.Fatal("explicit relative stop", end)
	}
}

func TestSurfaceFollowMissingSourceAndUncertainWrite(t *testing.T) {
	for _, scenario := range []string{"missing own packet", "moving ship", "no hull floor", "uncertain attachment", "prewrite hull changed"} {
		t.Run(scenario, func(t *testing.T) {
			phase := uint32(30000)
			own := surfaceTestDeck
			if scenario == "moving ship" {
				phase = 10000
			}
			if scenario == "no hull floor" {
				own.Z += 150
			}
			r := newSurfaceTestRig(t, phase, own, surfaceTestDeck, false, true)
			r.set("board")
			switch scenario {
			case "missing own packet":
				r.world.SelfMovementFacts.WireImage = nil
			case "prewrite hull changed":
				r.onClock = func() { r.world.MovingTransports[0].MovingTransport.PositionRevision++ }
			case "uncertain attachment":
				r.failSend = true
			}
			r.tick(0)
			r.tick(100 * time.Millisecond)
			if scenario != "uncertain attachment" {
				if len(r.packets) != 0 {
					t.Fatal("unproven attachment wrote", len(r.packets), r.packets[0].Opcode, r.frame(0), r.s.state.Reason, r.s.state.NavReason)
				}
				return
			}
			if len(r.packets) != 1 || !r.s.state.MotionUnconfirmed || r.s.surfaceObservationLocked(context.Background(), r.world).SelfWriteKnown {
				t.Fatal("ambiguous attachment repeated or claimed")
			}
			id := r.s.surfaceFollow.lastAttempt
			r.portalCoreScope.MotionRevision++
			r.world.SelfMotionAuthority.TransportScope = clonePortalScope(r.portalCoreScope)
			r.player(r.world.Self, surfaceTestDeck, true)
			r.tick(0)
			if r.s.state.MotionUnconfirmed || len(r.packets) != 1 || r.s.surfaceFollow.lastAttempt != id || !r.s.surfaceFollow.body.uncertain {
				t.Fatal("independent passenger correction did not preserve uncertain attempt")
			}
		})
	}
}

func TestSurfaceFollowDockApproachAndMovingDeparture(t *testing.T) {
	// These points on the quay and deck were measured in the
	// copied model and terrain, in the local frame of path 292.
	lip := buddynav.Point{X: 1.5347, Y: 12.8191, Z: 6.0722}
	dock := buddynav.Point{X: 1.34863, Y: 19.14225, Z: 5.384591}
	t.Run("dock approach", func(t *testing.T) {
		r := newSurfaceTestRig(t, 30000, dock, surfaceTestDeck, false, true)
		r.set("board")
		for n := 0; n < 100; n++ {
			r.tick(50 * time.Millisecond)
			if b := r.s.surfaceFollow.body; b != nil && b.info.TransportPresent && !b.info.JumpPresent {
				m, _ := r.s.surfaceMeshLocked(r.ctx, r.world.MovingTransports[0])
				q := surfaceLocalPoint(b.info)
				foot, err := hullFoot(r.ctx, m.Collision(), q)
				if err != nil || !foot || len(r.packets) < 3 || r.frame(0).TransportPresent {
					t.Fatalf("first contact missing true intervening body movement: %+v %v", b, err)
				}
				return
			}
			r.request.Jump = r.s.surfaceFollow.jumpRequired
			r.set("board")
		}
		t.Fatal("no deck contact after actual dock approach", len(r.packets), r.s.state.Reason, r.s.state.NavReason, r.s.pos)
	})
	for _, scenario := range []string{"moving jump to water", "host replaced during a jump"} {
		t.Run(scenario, func(t *testing.T) {
			r := newSurfaceTestRig(t, 112000, lip, buddynav.Point{Y: 65}, true, false)
			r.request.Jump = true
			r.set("leave")
			r.tick(0)
			launch := r.frame(0)
			if r.packets[0].Opcode != opcode.MSGMoveJump || !launch.TransportPresent || !launch.JumpPresent {
				t.Fatal("expected physical launch from measured deck lip", launch)
			}
			curve := r.s.surfaceFollow.paths[surfaceTestShip]
			a, _ := curve.Sample(112000)
			b, _ := curve.Sample(112800)
			if a.Stopped || surfaceDistance(a.Position, b.Position) < 2 {
				t.Fatal("fixture requires a moving vessel")
			}
			var release bool
			for n := 0; n < 50; n++ {
				if scenario == "host replaced during a jump" && n == 4 {
					r.s.hostEncounterEpoch++
					release = true
				}
				r.tick(50 * time.Millisecond)
				if !release && r.s.surfaceFollow.planSource != nil {
					if b := r.s.surfaceFollow.body; b != nil && b.info.TransportPresent {
						r.set("leave")
					} else {
						r.set("")
					}
				}
				if r.s.surfaceFollow.planSource == nil && !r.s.state.Moving {
					break
				}
			}
			var wet, stop, vertical, detached bool
			for n, p := range r.packets {
				i := r.frame(n)
				if i.JumpPresent && i.Jump.XYSpeed == 0 && i.Flags&1 == 0 {
					vertical = true
				}
				if i.JumpPresent && !i.TransportPresent {
					detached = true
				}
				if p.Opcode == opcode.MSGMoveStartSwim {
					wet = i.PitchPresent && !i.TransportPresent
				}
				if p.Opcode == opcode.MSGMoveStop {
					stop = true
					if i.JumpPresent {
						t.Fatal("stopped an airborne body")
					}
				}
			}
			if !stop || !release && (!wet || !detached) || release && !vertical || r.s.state.MotionUnconfirmed {
				t.Fatalf("%s phase=%s reason=%s frames=%d wet=%v stop=%v vertical=%v state=%v", scenario, r.s.state.Reason, r.s.state.NavReason, len(r.packets), wet, stop, vertical, r.s.surfaceFollow.body)
			}
			if !release {
				last := r.frame(len(r.packets) - 1)
				if last.TransportPresent || !last.PitchPresent || surfaceDistance(surfaceInfoPoint(last), buddynav.Point{X: -4038.4773, Y: -4751.1055, Z: -.39675295}) > .035 {
					t.Fatal("terminal ordinary STOP differs from the measured map/liquid contact", last)
				}
			}
		})
	}
	t.Run("translating hull obstructs this exit", func(t *testing.T) {
		r := newSurfaceTestRig(t, 10000, lip, buddynav.Point{Y: 65}, true, false)
		r.request.Jump = true
		r.set("leave")
		r.tick(0)
		if len(r.packets) != 0 || r.s.surfaceFollow.phase != "surface_body_path_unavailable" {
			t.Fatal("unsafe launch", len(r.packets), r.s.state.Reason)
		}
	})
}

func TestSurfaceFollowRemeasuresMovingLaunchAndFencesStoppedHull(t *testing.T) {
	lip := buddynav.Point{X: 1.5347, Y: 12.8191, Z: 6.0722}
	r := newSurfaceTestRig(t, 112000, lip, buddynav.Point{Y: 65}, true, false)
	r.request.Jump = true
	r.set("leave")
	r.onClock = func() {
		r.onClock = nil
		r.advance(80 * time.Millisecond)
		r.world.Self.PassengerMovement.AgeMs += 80
		r.world.Owner.PassengerMovement.AgeMs += 80
		r.world.MovingTransports[0].MovingTransport.MovementAgeMs += 80
	}
	r.tick(0)
	i := r.frame(0)
	sample, _ := r.s.surfaceFollow.paths[surfaceTestShip].Sample(112080)
	tr := buddynav.SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}
	if i.Transport.Time != 112080 || surfaceDistance(surfaceInfoPoint(i), tr.World(lip)) > .005 || surfaceDistance(r.s.surfaceFollow.plan[0].Point, tr.World(lip)) > .005 {
		t.Fatal("launch or its measured flight retained the earlier world position", i)
	}
	r = newSurfaceTestRig(t, 10000, surfaceTestDeck, surfaceTestBow, true, true)
	r.set("ride")
	r.tick(0)
	r.onClock = func() { r.onClock = nil; r.world.MovingTransports = nil }
	toggle(t, r.s, false)
	if len(r.packets) != 1 || countPackets(r.fakeSession, opcode.MSGMoveStop) != 0 {
		t.Fatal("passenger STOP used a vanished hull", len(r.packets))
	}
}

func TestSurfaceFollowUsesFinalObservedHullAndPacketSequence(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		observed, ignored, malformed uint64
		writes                       int
	}{
		{"new World snapshot", 17, 6, 2, 1},
		{"missing sequence", 0, 0, 0, 0},
		{"saturated", math.MaxUint64 - 9, 8, 1, 0},
		{"overflow in ignored", math.MaxUint64 - 1, 3, 0, 0},
		{"overflow in malformed", math.MaxUint64 - 1, 0, 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSurfaceTestRig(t, 10000, surfaceTestDeck, surfaceTestBow, true, true)
			r.set("ride")
			r.onClock = func() {
				r.onClock = nil
				r.world.ObservedPackets, r.world.IgnoredPackets, r.world.MalformedPackets = tc.observed, tc.ignored, tc.malformed
			}
			r.tick(0)
			if len(r.packets) != tc.writes || r.s.state.MotionUnconfirmed {
				t.Fatalf("got %d writes, uncertain=%t: %s", len(r.packets), r.s.state.MotionUnconfirmed, r.s.state.NavReason)
			}
		})
	}
}
