package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestIdleFlightmasterAfterDetachedGroundStop(t *testing.T) {
	for _, tc := range []string{"stale passenger", "fresh passenger", "later passenger", "later swim", "later fall", "unconfirmed", "lost receipt", "other position"} {
		t.Run(tc, func(t *testing.T) {
			s, f, _, ctx := groundedSurfaceInput(t)
			raw := f.world
			const npcGUID = uint64(0xf1300010e1000001)
			npc := &worldv1.CreatureState{Guid: npcGUID, Entry: 4321, NpcFlags: 0x2000, Present: true, MapKnown: true, MapId: raw.Self.MapId, PositionKnown: true, PositionRevision: 44, X: s.pos.x + 3, Y: s.pos.y, Z: s.pos.z, HealthKnown: true, Health: 100, MaxHealth: 100, AliveKnown: true, Alive: true, UnitFlagsKnown: true}
			raw.ObservedPackets = 53
			raw.RequestedCreatureStationaryPositionKnown = true
			f.lookupCreatures = map[uint64]*worldv1.CreatureState{npcGUID: npc}
			raw.Self.PositionAgeMs = 120000
			switch tc {
			case "fresh passenger":
				raw.Self.PositionAgeMs = 100
			case "later passenger":
				raw.Self.PositionRevision++
			case "later swim":
				raw.Self.PositionRevision++
				raw.Self.MovementFlags = swimMovementFlag
			case "later fall":
				raw.Self.PositionRevision++
				raw.Self.MovementFlags = 0x1000
			case "unconfirmed":
				s.state.MotionUnconfirmed = true
			case "lost receipt":
				f.ownMovementReport = nil
			case "other position":
				s.pos.x++
			}
			before := proto.Clone(raw.Self)
			n := len(f.packets)
			lease, err := s.AcquireIdleInteraction(ctx, &actionsv1.IdleInteractionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: raw.VisitRevision, Owner: "exploration", LeaseMs: 10000})
			if tc != "stale passenger" && tc != "fresh passenger" {
				if err == nil || lease != nil || len(f.packets) != n {
					t.Fatalf("unsafe lease=%v error=%v packets=%d", lease, err, len(f.packets)-n)
				}
				return
			}
			if err != nil || lease.GetToken() == "" || !lease.GetState().GetPosition().GetEstimated() || lease.State.Position.X != s.pos.x || len(f.packets) != n {
				t.Fatalf("detached idle lease: %v, %v", lease, err)
			}
			r := &actionsv1.FlightpathQueryRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: raw.VisitRevision, Token: lease.Token, NpcGuid: npcGUID, Mode: "status"}
			view, err := s.QueryFlightpath(context.Background(), r)
			if err != nil || !view.GetOperation().GetTransportWritten() || len(f.packets) != n+1 {
				t.Fatalf("flight status %v, %v", view, err)
			}
			p := f.packets[n]
			if p.Opcode != opcode.CMSGTaxinodeStatusQuery || binary.LittleEndian.Uint64(p.Payload) != npcGUID || !proto.Equal(p.TransportScope, f.ownMovementReport.TransportScope) || !proto.Equal(before, raw.Self) {
				t.Fatalf("query did not use original STOP/target or altered received evidence: %v", p)
			}
			if _, err := s.QueryFlightpath(context.Background(), r); err != nil || len(f.packets) != n+1 {
				t.Fatal("status replayed", err)
			}
		})
	}
}

func TestExplicitFreeFlightmasterUsesKnownGroundMount(t *testing.T) {
	x := newFlightpathRig(t)
	mountForTravel(t, x.s, x.f.world.Self, 23229)
	view, _ := x.query(t, "status")
	if !view.GetOperation().GetTransportWritten() {
		t.Fatal("explicit free query on mounted ground with no opportunity", view)
	}
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 0))
	view, _ = x.query(t, "discover")
	if !view.GetOperation().GetTransportWritten() {
		t.Fatal("explicit free discovery on mounted ground", view)
	}
}
