package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func walkedToFlightmaster(t *testing.T) *flightpathRig {
	t.Helper()
	x := newFlightpathRig(t)
	w := x.f.world
	seedSyntheticPortalWorldScope(x.f, 9)
	w.Self.X, w.Self.Y, w.Self.Z = -4898.58, -954.08, 501.46
	w.Self.PositionAgeMs = 276000
	if !x.s.refreshPositionLocked(w) {
		t.Fatal("raw login position unavailable")
	}
	x.s.pos = position{-4820.5, -1154.19, 502.21, 1}
	npc := w.NearbyCreatures[0]
	npc.X, npc.Y, npc.Z = x.s.pos.x+1.9, x.s.pos.y, x.s.pos.z
	npc.PositionAgeMs, npc.ObservationAgeMs = 105000, 105000
	w.RequestedCreatureStationaryPositionKnown = true
	x.f.lookupCreatures = map[uint64]*worldv1.CreatureState{x.npc: npc}
	w.ObservedPackets = 12
	x.f.retainSyntheticOwnMovementReport(&modv1.SendPacketRequest{Opcode: opcode.MSGMoveStop,
		Payload: movementPacket(x.s.selfGUID, 41000, x.s.pos, false)})
	return x
}

func TestFlightpathStatusAndMenuAtActuallyWrittenGroundArrival(t *testing.T) {
	x := walkedToFlightmaster(t)
	view, _ := x.query(t, "status")
	if c := view.Operation; !c.TransportWritten || c.StatusKnown || len(x.f.packets) != 1 ||
		!proto.Equal(x.f.packets[0].TransportScope, x.f.ownMovementReport.TransportScope) {
		t.Fatalf("expected one exact scoped status query, got %v", c)
	}
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	view, _ = x.query(t, "discover")
	if !view.Operation.TransportWritten || view.Operation.MenuKnown || len(x.f.packets) != 2 {
		t.Fatal(view.Operation)
	}
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 6, 6, 12))
	if menu := x.observation(t).Operation; !menu.MenuKnown || menu.SourceNode != 6 || menu.Pending {
		t.Fatal(menu)
	}
}

func TestFlightpathGroundArrivalRequiresCurrentStopAndRequestedStationarySpawn(t *testing.T) {
	for _, change := range []string{"missing STOP", "new START", "wrong mover", "other XYZ", "other map", "other physical visit",
		"unknown stationarity", "moving spline", "missing exact lookup", "despawn after STOP read", "channel after STOP read",
		"out of range", "zero packet sequence", "counter overflow"} {
		t.Run(change, func(t *testing.T) {
			x := walkedToFlightmaster(t)
			guid, self := x.npc, x.s.selfGUID
			w, npc, stop := x.f.world, x.f.lookupCreatures[x.npc], x.f.ownMovementReport
			switch change {
			case "missing STOP":
				x.f.ownMovementReport = nil
			case "new START":
				stop.Opcode = opcode.MSGMoveStartForward
			case "wrong mover":
				stop.Payload = movementPacket(self+1, 1, x.s.pos, false)
			case "other XYZ":
				stop.Payload = movementPacket(self, 1, playerPosition(w.Self), false)
			case "other map":
				w.SelfMotionAuthority.TransportScope.MapId++
			case "other physical visit":
				w.SelfMotionAuthority.TransportScope.VisitRevision++
			case "unknown stationarity":
				w.RequestedCreatureStationaryPositionKnown = false
			case "moving spline":
				npc.Trajectory = &worldv1.CreatureTrajectory{Known: true}
			case "missing exact lookup":
				delete(x.f.lookupCreatures, guid)
			case "despawn after STOP read":
				x.f.onOwnMovementReport = func(*modv1.WorldPacket) { delete(x.f.lookupCreatures, guid) }
			case "channel after STOP read":
				x.f.onOwnMovementReport = func(*modv1.WorldPacket) { w.Self.ChannelSpellId = 10 }
			case "out of range":
				npc.Z += 3
			case "zero packet sequence":
				w.ObservedPackets = 0
			case "counter overflow":
				w.IgnoredPackets = math.MaxUint64
			}
			view, _ := x.query(t, "status")
			if view.Operation.Phase != "not_dispatched" || view.Operation.Pending || len(x.f.packets) != 0 {
				t.Fatal(change, view.Operation, x.f.packets)
			}
		})
	}
}

func TestFlightpathGroundArrivalPreservesUnknownAndRequiresCurrentStopForBooking(t *testing.T) {
	x := walkedToFlightmaster(t)
	x.f.failSend = true
	v, request := x.query(t, "status")
	if !v.Operation.Pending || !v.Operation.TransportUnknown || v.Operation.TransportWritten {
		t.Fatal(v.Operation)
	}
	x.f.failSend = false
	retry, err := x.s.QueryFlightpath(context.Background(), request)
	if err != nil || !proto.Equal(retry, v) || len(x.f.packets) != 1 {
		t.Fatal(retry, err)
	}
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	x.query(t, "discover")
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 12, 2, 12))
	x.f.ownMovementReport = nil
	activate := flightpathActivationRequest(t, x)
	assertFlightpathActivationNotDispatched(t, flightpathActivate(t, x, activate), activate)
	if len(x.f.packets) != 2 {
		t.Fatal("an older ground arrival escaped the paid guard", x.f.packets)
	}
}

func TestFlightpathPaidBookingAfterOrdinaryGroundTravel(t *testing.T) {
	s, f, advance, travel := instanceEntryIdleTravelFixture(t)
	arrival, err := s.TravelTo(context.Background(), travel)
	if err != nil || arrival.GetReason() != "exploration_within_distance" || arrival.GetMotionUnconfirmed() ||
		countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(arrival, err)
	}
	const guid = uint64(0xf130000625000001)
	npc := &worldv1.CreatureState{Guid: guid, Entry: 1573, NpcFlags: 0x2000, Present: true, MapKnown: true, MapId: travel.MapId,
		PositionKnown: true, PositionRevision: 4, X: travel.X, Y: travel.Y, Z: travel.Z, PositionAgeMs: 60000, ObservationAgeMs: 60000,
		AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, UnitFlagsKnown: true}
	w := f.world
	w.Self.PositionAgeMs, w.ObservedPackets = 60000, 51
	w.RequestedCreatureStationaryPositionKnown = true
	w.SelfCopperKnown, w.SelfCopper, w.SelfCopperRevision = true, 1500, 8
	w.NearbyCreatures = []*worldv1.CreatureState{npc}
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{guid: npc}
	client := &flightpathSession{fakeSession: f}
	s.session = client
	x := &flightpathRig{s: s, f: f, client: client, advance: advance, npc: guid, token: travel.Token}
	x.query(t, "status")
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(guid, 1))
	x.query(t, "discover")
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(guid, 6, 6, 8))
	if !x.observation(t).GetOperation().GetMenuKnown() {
		t.Fatal("verified walk and ordinary status did not produce a menu")
	}
	r := flightpathActivationRequest(t, x)
	r.SourceNode, r.DestinationNode = 6, 8
	got := flightpathActivate(t, x, r)
	if op := got.Activation; !op.TransportWritten || !op.Pending || op.ActivationAccepted || !op.MoneyBeforeKnown || op.MoneyBeforeCopper != 1500 {
		t.Fatal("booking after proven stationary arrival", op)
	}
	p := f.packets[len(f.packets)-1]
	if p.Opcode != opcode.CMSGActivatetaxi || len(p.Payload) != 16 || binary.LittleEndian.Uint64(p.Payload) != guid ||
		binary.LittleEndian.Uint32(p.Payload[8:]) != 6 || binary.LittleEndian.Uint32(p.Payload[12:]) != 8 ||
		!proto.Equal(p.TransportScope, f.ownMovementReport.TransportScope) {
		t.Fatal(p)
	}
	assertFlightpathActivationNoInventedEffects(t, got.Activation)
	again := flightpathActivate(t, x, r)
	if !proto.Equal(again, got) || countPackets(f, opcode.CMSGActivatetaxi) != 1 {
		t.Fatal("same booking repeated", again)
	}
}

func TestFlightpathGroundBookingUsesCurrentWalletBodyAndFlightmaster(t *testing.T) {
	for _, change := range []string{"wallet", "lease", "visit", "NPC despawn", "channel", "unconfirmed movement", "unknown write"} {
		t.Run(change, func(t *testing.T) {
			x := walkedToFlightmaster(t)
			x.query(t, "status")
			x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
			x.query(t, "discover")
			x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 12, 2, 12))
			r := flightpathActivationRequest(t, x)
			x.f.onOwnMovementReport = func(*modv1.WorldPacket) {
				switch change {
				case "wallet":
					x.f.world.SelfCopperRevision++
				case "lease":
					x.s.interactionToken = "replaced"
				case "visit":
					x.f.world.VisitRevision++
				case "NPC despawn":
					delete(x.f.lookupCreatures, x.npc)
				case "channel":
					x.f.world.Self.ChannelSpellId = 10
				case "unconfirmed movement":
					x.s.state.MotionUnconfirmed = true
				case "unknown write":
					x.f.failSend = true
				}
			}
			got := flightpathActivate(t, x, r)
			writes := countPackets(x.f, opcode.CMSGActivatetaxi)
			if change == "unknown write" {
				if !got.Activation.Pending || !got.Activation.TransportUnknown || got.Activation.TransportWritten || writes != 1 {
					t.Fatal(got)
				}
			} else {
				assertFlightpathActivationNotDispatched(t, got, r)
				if writes != 0 {
					t.Fatal("revoked departure sent a booking")
				}
			}
			if again := flightpathActivate(t, x, r); !proto.Equal(again, got) || countPackets(x.f, opcode.CMSGActivatetaxi) != writes {
				t.Fatal("retained disposition changed on replay", again)
			}
		})
	}
}

func TestFlightpathGroundArrivalLearningStillNeedsNoticeAndExactNodeReply(t *testing.T) {
	x := walkedToFlightmaster(t)
	x.unknownBaseline(t)
	x.query(t, "discover")
	x.packet(t, opcode.SMSGNewTaxiPath, nil)
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc+1, 1))
	if op := x.observation(t).Operation; !op.Pending || op.Learned || op.MenuKnown {
		t.Fatal(op)
	}
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	if op := x.observation(t).Operation; op.Pending || !op.Learned || op.MenuKnown {
		t.Fatal(op)
	}
	x.query(t, "discover")
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 12, 2, 12))
	if op := x.observation(t).Operation; op.Pending || !op.MenuKnown || op.SourceNode != 12 || len(x.f.packets) != 3 {
		t.Fatal(op, x.f.packets)
	}
}
