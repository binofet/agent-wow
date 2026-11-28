package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Received values at the Eastern Kingdoms -> Kalimdor ferry handoff. The
// passenger's serialized ship time and the hull's independent full clock are
// deliberately on different sides of the transfer.
const receivingTestShip uint64 = 2287828610704211972

func fieldReceivingFerry(t *testing.T, ambiguous bool) *surfaceTestRig {
	t.Helper()
	r := newSurfaceTestRig(t, 30000, surfaceTestDeck, surfaceTestBow, true, true)
	r.ship, r.request.TransportGuid = receivingTestShip, receivingTestShip
	h := r.world.MovingTransports[0]
	h.Guid, h.Entry = r.ship, 176231
	m := h.MovingTransport
	m.Template.Guid, m.Template.Entry, m.Template.AccelerationRate = r.ship, h.Entry, 1
	m.PeriodMs = proto.Uint32(230246)
	path, err := buddynav.LoadTransportPath(r.ctx, r.s.navDataDir, 292, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := path.Sample(30000)
	if err != nil || sample.MapID != 0 || path.Period() != *m.PeriodMs {
		t.Fatal("unexpected client ferry trajectory", sample, err)
	}
	r.tr = buddynav.SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}
	h.X, h.Y, h.Z, h.Orientation = sample.Position.X, sample.Position.Y, sample.Position.Z, sample.Yaw
	r.player(r.world.Self, surfaceTestDeck, true)
	r.player(r.world.Owner, surfaceTestBow, true)
	r.ownerGoal()
	// The field used the ordinary hosted formation with native Follow off.
	toggle(t, r.s, false)
	r.request.FollowRevision = r.s.followRevision
	r.failSend = ambiguous
	r.set("ride")
	r.tick(0)
	if len(r.packets) != 1 || r.packets[0].Opcode != opcode.MSGMoveStartForward ||
		r.s.state.MotionUnconfirmed != ambiguous || !ambiguous && !r.s.state.Moving {
		t.Fatal("initial physical ride", len(r.packets), r.s.state.Reason, r.s.state.NavReason)
	}
	r.failSend = false
	pending := binary.LittleEndian.AppendUint32(nil, 1)
	pending = binary.LittleEndian.AppendUint32(pending, h.Entry)
	pending = binary.LittleEndian.AppendUint32(pending, 0)
	entry := binary.LittleEndian.AppendUint32(nil, 1)
	for _, offset := range []float32{-5.9165373, 1.1584282, 6.097062, -2.267225} {
		entry = binary.LittleEndian.AppendUint32(entry, math.Float32bits(offset))
	}
	for _, packet := range []*modv1.WorldPacket{{Opcode: opcode.SMSGTransferPending, Payload: pending}, {Opcode: opcode.SMSGNewWorld, Payload: entry}} {
		if _, err := r.s.OnPacket(context.Background(), packet); err != nil {
			t.Fatal(err)
		}
		r.s.Tick(context.Background())
		if len(r.packets) != 1 {
			t.Fatal("an old route or stop was sent during the map handoff", packet.Opcode, r.packets)
		}
	}
	r.world.VisitRevision, r.world.Self.MapId, r.world.Owner.MapId, h.MapId = 2, 1, 1, 1
	r.world.Owner.Present, r.world.Owner.PositionKnown, r.world.Owner.PassengerMovement = false, false, nil
	r.world.ObservedPackets += 30
	seedSyntheticPortalWorldScope(r.fakeSession, 10)
	r.ctx = encounterHostContext(testEncounterKey, 2)
	r.progress = 423541282
	h.X, h.Y, h.Z, h.Orientation = -4262.9917, -4822.409, 0, 4.186925
	h.PositionRevision, m.PositionRevision, m.ObjectRevision, m.MovementRevision = 1, 1, 8831, 8831
	m.PathProgressMs, m.MovementAgeMs, m.SourceOpcode = proto.Uint32(r.progress), 23, opcode.SMSGCompressedUpdateObject
	m.Template.VisitRevision = 2
	r.tr = buddynav.SurfaceTransform{Origin: buddynav.Point{X: h.X, Y: h.Y, Z: h.Z}, Yaw: h.Orientation, Scale: 1}
	me := r.world.Self
	me.X, me.Y, me.Z, me.Orientation = -4259.7705, -4818.5938, 6.097062, 1.9661787
	me.MovementFlags, me.MovementFlagsKnown, me.PositionRevision, me.PositionInferred = passengerFlag, true, 3, false
	i := movementinfo.Info{X: me.X, Y: me.Y, Z: me.Z, Orientation: me.Orientation, Time: 423566013, Flags: passengerFlag, TransportPresent: true}
	i.Transport = movementinfo.Transport{GUID: r.ship, X: -5.9165373, Y: 1.1584282, Z: 6.097062, Orientation: -2.267225, Time: 89379, Seat: -1}
	wire, err := movementinfo.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	raw := &worldv1.MovementInfoFacts{Time: i.Time, Flags: i.Flags, X: i.X, Y: i.Y, Z: i.Z, Orientation: i.Orientation,
		Transport: &worldv1.MovementTransportFacts{Guid: r.ship, X: i.Transport.X, Y: i.Transport.Y, Z: i.Transport.Z, Orientation: i.Transport.Orientation, Time: i.Transport.Time, Seat: -1}}
	me.PassengerMovement = &worldv1.PassengerMovementFacts{VisitRevision: 2, Revision: 8891, Info: raw, SourceOpcode: opcode.SMSGCompressedUpdateObject}
	r.world.SelfMovementFacts = &worldv1.OwnMovementFacts{Known: true, SelfGuid: me.Guid, VisitRevision: 2, MapKnown: true, MapId: 1,
		Revision: 5, SourceOpcode: opcode.SMSGCompressedUpdateObject, Info: raw, WireImage: wire, TransportScope: clonePortalScope(r.portalCoreScope)}
	return r
}

func TestFerryTransferReceivingBodyAndFreshFollow(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		r := fieldReceivingFerry(t, ambiguous)
		previous, attempt := r.s.surfaceFollow.body, r.s.surfaceFollow.lastAttempt
		r.s.Tick(context.Background())
		if len(r.packets) != 1 || r.s.state.Moving || r.s.state.MotionUnconfirmed || r.s.state.Following ||
			r.s.surfaceFollow.body != previous || previous.uncertain != ambiguous || r.s.surfaceFollow.lastAttempt != attempt ||
			r.s.surfaceFollow.goal != nil || r.s.surfaceFollow.planSource != nil {
			t.Fatalf("stopped receiving body did not preserve original delivery: moving=%v unknown=%v writes=%d", r.s.state.Moving, r.s.state.MotionUnconfirmed, len(r.packets))
		}
		if _, err := r.s.SetFollowFormation(r.ctx, r.request); err == nil {
			t.Fatal("accepted old ride goal in the receiving visit")
		}
		if _, err := r.s.RenewPolicyEncounterLease(r.ctx, wrapperspb.UInt64(2)); err != nil {
			t.Fatal(err)
		}
		r.request.VisitId = 2
		if _, err := r.s.SetFollowFormation(r.ctx, r.request); err == nil {
			t.Fatal("accepted absent owner's former goal")
		}
		// A new current sighting of the owner in water at the field's last
		// observed coordinates is distinct from its earlier, absent cached row.
		owner := r.world.Owner
		owner.Present, owner.MapKnown, owner.PositionKnown = true, true, true
		water := buddynav.Point{X: -4221.1226, Y: -4704.1533, Z: -2.2322023}
		r.player(owner, r.tr.Local(water), false)
		owner.MovementFlags, owner.PassengerMovement.Info.Flags = swimMovementFlag, swimMovementFlag
		r.ownerGoal()
		r.request.Jump = true // The ordinary Lua leave decision for a swimming owner.
		r.set("leave")
		r.advanceSurface(0)
		r.s.runTick(context.Background())
		for i := 0; i < 180; i++ {
			if b := r.s.surfaceFollow.body; b != nil && (b.visit != r.world.VisitRevision || b.info.TransportPresent) {
				r.set("leave")
			} else {
				r.set("")
			}
			// A quiet controller performs its ordinary half-second encounter
			// poll. Active body samples use the scheduler's real default budget.
			interval := 100 * time.Millisecond
			if !r.s.state.Moving {
				interval = 500 * time.Millisecond
			}
			r.advanceSurface(interval)
			r.s.runTick(context.Background())
			if countPackets(r.fakeSession, opcode.MSGMoveStartSwim) == 1 && !r.s.state.Moving {
				break
			}
		}
		if len(r.packets) < 2 || r.packets[1].Opcode != opcode.MSGMoveStartForward || !r.frame(1).TransportPresent || r.frame(1).Transport.GUID != r.ship || r.frame(1).Transport.Time < r.progress || r.s.state.Following {
			t.Fatal("fresh receiving-side leave", r.s.state.Reason, r.s.state.NavReason, len(r.packets))
		}
		if countPackets(r.fakeSession, opcode.MSGMoveStartSwim) != 1 ||
			len(r.packets) < 4 || r.packets[len(r.packets)-1].Opcode != opcode.MSGMoveStop || r.frame(len(r.packets)-1).TransportPresent ||
			r.s.state.MotionUnconfirmed || r.s.state.Following || ambiguous && !r.s.state.LastTransportFailure.GetOutcomeUnknown() {
			t.Fatalf("receiving shore/water handoff failed: %s, %s, frames=%d, swim=%d", r.s.state.Reason, r.s.state.NavReason, len(r.packets), countPackets(r.fakeSession, opcode.MSGMoveStartSwim))
		}
	}
}

func TestFerryTransferInheritedPassengerClockMustMatchReceivingEvidence(t *testing.T) {
	r := fieldReceivingFerry(t, false)
	for _, test := range []struct {
		name string
		edit func(*worldv1.SnapshotResponse)
	}{
		{"receiving visit", func(*worldv1.SnapshotResponse) {}},
		{"wrong period", func(w *worldv1.SnapshotResponse) {
			w.MovingTransports[0].MovingTransport.PeriodMs = proto.Uint32(190656)
		}},
		{"wrong hull map", func(w *worldv1.SnapshotResponse) { w.MovingTransports[0].MapId = 0 }},
		{"wrong hull checkpoint", func(w *worldv1.SnapshotResponse) { w.MovingTransports[0].X++ }},
		{"hull recreated", func(w *worldv1.SnapshotResponse) { w.MovingTransports[0].MovingTransport.ObjectRevision = 8892 }},
		{"wrong passenger position", func(w *worldv1.SnapshotResponse) { w.Self.PassengerMovement.Info.X++ }},
		{"wrong passenger heading", func(w *worldv1.SnapshotResponse) { w.Self.PassengerMovement.Info.Orientation += .11 }},
		{"later ordinary heartbeat", func(w *worldv1.SnapshotResponse) { w.Self.PassengerMovement.SourceOpcode = opcode.MSGMoveHeartbeat }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := proto.Clone(r.world).(*worldv1.SnapshotResponse)
			test.edit(w)
			tr, clock, err := r.s.surfacePoseLocked(context.Background(), w, w.MovingTransports[0])
			if test.name == "receiving visit" {
				p, _ := r.s.surfaceFollow.paths[r.ship].Sample(uint64(r.progress + 23))
				if err != nil || clock != r.progress+23 || surfaceDistance(tr.Origin, p.Position) > .001 {
					t.Fatal("did not retain current full hull clock", clock, tr, err)
				}
			} else if err == nil {
				t.Fatal("accepted inconsistent receiving-side evidence", tr, clock)
			}
		})
	}
}

func TestFerryTransferKeepsUnresolvedPassengerForIncompleteReceivingBody(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*worldv1.SnapshotResponse)
	}{
		{"no raw movement", func(w *worldv1.SnapshotResponse) { w.SelfMovementFacts.WireImage = nil }},
		{"foreign body", func(w *worldv1.SnapshotResponse) { w.SelfMovementFacts.SelfGuid++ }},
		{"foreign connection", func(w *worldv1.SnapshotResponse) { w.SelfMovementFacts.TransportScope.ConnectionId = "replacement" }},
		{"replacement hull", func(w *worldv1.SnapshotResponse) {
			w.MovingTransports[0].MovingTransport.ObjectRevision = w.Self.PassengerMovement.Revision + 1
		}},
		{"contradictory passenger pair", func(w *worldv1.SnapshotResponse) { w.Self.PassengerMovement.Info.X++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := fieldReceivingFerry(t, true)
			b := r.s.surfaceFollow.body
			test.change(r.world)
			r.s.runTick(context.Background())
			if !r.s.state.MotionUnconfirmed || !b.uncertain || b != r.s.surfaceFollow.body || len(r.packets) != 1 || r.s.surfaceFollow.planSource != nil {
				t.Fatal("incomplete receiving evidence released or replayed prior passenger input", r.s.state.Reason, len(r.packets))
			}
		})
	}
}
