package buddyactions

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Exact, strict writes and the two distinct physical read contracts. A parsed
// update can preserve the original report without changing its revision.
type flightmasterTrafficSession struct {
	sessionAPI
	f                 *fakeSession
	after             func(uint32)
	before            func(uint32) error
	changed, harmless bool
}

func (c *flightmasterTrafficSession) update(harmless bool) {
	c.changed, c.harmless = true, harmless
	c.f.portalCoreScope.MotionRevision++
	c.f.world.SelfMotionAuthority.TransportScope = clonePortalScope(c.f.portalCoreScope)
}

func (c *flightmasterTrafficSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if c.before != nil {
		if err := c.before(r.Opcode); err != nil {
			return nil, err
		}
	}
	out, err := c.sessionAPI.SendPacket(ctx, r, opts...)
	if err == nil && c.after != nil {
		c.after(r.Opcode)
	}
	return out, err
}

func (c *flightmasterTrafficSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if c.changed && r.Module == "session" && (r.Method == modcb.SelfBodyInputReportMethod || r.Method == modcb.SettledBodyInputReportMethod) {
		p := c.f.ownMovementReport
		if !c.harmless || p == nil || r.Method == modcb.SettledBodyInputReportMethod && p.Opcode != opcode.MSGMoveStop {
			return nil, status.Error(codes.FailedPrecondition, "physical source changed")
		}
		value, err := anypb.New(p)
		return &modv1.InvokeModuleResponse{Result: value}, err
	}
	return c.sessionAPI.InvokeModule(ctx, r, opts...)
}

func TestFlightmasterGroundApproachAcrossParsedTraffic(t *testing.T) {
	for _, mode := range []string{"before heartbeat", "before second heartbeat", "new checked approach", "superseded written STOP", "after START", "after STOP", "NPC moved", "late unwritten heartbeat", "unavailable then explicit STOP", "own correction", "same wire from a newer writer", "ambiguous heartbeat"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := instanceEntryIdleTravelFixture(t)
			const entry = 1572
			const guid = uint64(0xf130000624000021)
			self := f.world.Self
			self.FactionTemplate = 1
			r.X, r.Y, r.Z = self.X+9, self.Y, self.Z
			npc := &worldv1.CreatureState{Guid: guid, Entry: entry, NpcFlags: 0x2000, Present: true, MapKnown: true, MapId: r.MapId, PositionKnown: true, PositionRevision: 6, X: r.X, Y: r.Y, Z: r.Z, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, UnitFlagsKnown: true, FactionTemplate: 1, StationaryPositionKnown: true}
			f.world.RequestedCreatureStationaryPositionKnown = true
			f.lookupCreatures = map[uint64]*worldv1.CreatureState{guid: npc}
			f.world.NearbyCreatures = []*worldv1.CreatureState{npc}
			r.Flightmaster = &actionsv1.FlightmasterTarget{Guid: guid, PositionRevision: npc.PositionRevision, FollowRevision: s.followRevision}
			client := &flightmasterTrafficSession{sessionAPI: s.session, f: f}
			s.session = client
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				return []position{from, to}, nil
			}
			s.approachDelay = func(ctx context.Context, d time.Duration) error {
				advance(d)
				if !client.changed && len(f.packets) != 0 {
					switch mode {
					case "before heartbeat":
						client.update(true)
					case "before second heartbeat":
						if countPackets(f, opcode.MSGMoveHeartbeat) == 1 {
							client.update(true)
						}
					case "NPC moved":
						client.update(true)
						npc.PositionRevision++
					case "own correction", "unavailable then explicit STOP":
						client.update(false)
					case "same wire from a newer writer":
						client.update(true)
						f.ownMovementReport = proto.Clone(f.ownMovementReport).(*modv1.WorldPacket)
						f.ownMovementReport.TransportScope = clonePortalScope(f.portalCoreScope)
					}
				}
				return ctx.Err()
			}
			client.after = func(op uint32) {
				if mode == "superseded written STOP" && op == opcode.MSGMoveStop {
					client.update(true)
					f.ownMovementReport = proto.Clone(f.ownMovementReport).(*modv1.WorldPacket)
					f.ownMovementReport.TransportScope = clonePortalScope(f.portalCoreScope)
				}
				if !client.changed && (mode == "after START" && op == opcode.MSGMoveStartForward || (mode == "after STOP" || mode == "new checked approach") && op == opcode.MSGMoveStop) {
					client.update(true)
				}
			}
			client.before = func(op uint32) error {
				if op == opcode.MSGMoveHeartbeat && !client.changed {
					switch mode {
					case "late unwritten heartbeat":
						client.update(true) // socket will strictly refuse the already reserved old revision
					case "ambiguous heartbeat":
						return errors.New("unknown transport")
					}
				}
				return nil
			}
			out, err := s.TravelTo(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			start, beat, stop := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveStop)
			switch mode {
			case "superseded written STOP":
				if start != 1 || stop != 1 || !out.MotionUnconfirmed || s.observeIdleMotionStoppedLocked(context.Background()) {
					t.Fatal("a different identical STOP replaced the original physical write", out, f.packets)
				}
			case "own correction", "same wire from a newer writer", "ambiguous heartbeat", "unavailable then explicit STOP":
				if start != 1 || stop != 0 || !out.MotionUnconfirmed {
					t.Fatal("missing source supplied movement", out, f.packets)
				}
				if mode == "unavailable then explicit STOP" {
					client.harmless = true // classification for the unchanged original frame is now available
					stopped, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
					if err != nil || stopped.Moving || stopped.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStop) != 1 {
						t.Fatal("fresh explicit STOP left a proven old START active", err, stopped, f.packets)
					}
				}
			case "NPC moved", "late unwritten heartbeat":
				if start != 1 || beat != 0 || stop != 1 || out.Moving || out.MotionUnconfirmed {
					t.Fatal("definitely unwritten continuation stranded its prior START", out, f.packets)
				}
			case "new checked approach":
				if stop != 1 || out.MotionUnconfirmed || out.Moving {
					t.Fatal(out)
				}
				prior := s.pos
				client.update(true)
				again, err := s.TravelTo(context.Background(), r)
				if err != nil || again.GetMotionUnconfirmed() || again.GetMoving() || countPackets(f, opcode.MSGMoveStop) != 2 || distance(prior, s.pos) < 1 || distance(s.pos, position{r.X, r.Y, r.Z, 0}) > 5 {
					t.Fatal("fresh guarded leg failed to reach query range from its exact prior STOP", again, err)
				}
			default:
				if start != 1 || beat == 0 || stop != 1 || out.Moving || out.MotionUnconfirmed || out.Reason != "exploration_flightmaster_checkpoint_reached" {
					t.Fatal("grounded approach did not settle", out, f.packets)
				}
			}
		})
	}
}

func TestCopiedThelsamarIdleGroundTravel(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to the extracted client navigation assets")
	}
	for _, mode := range []string{"tagged mounted opportunity", "idle ground destination", "ordinary commanded destination"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := instanceEntryIdleTravelFixture(t)
			me := f.world.Self
			me.MapId, s.encounterVisit.mapID, r.MapId = 0, 0, 0
			me.X, me.Y, me.Z, me.FactionTemplate = -5430.846, -2939.1975, 348.41296, 1
			me.PositionAgeMs = 75000
			f.world.ObservedPackets = 12
			r.X, r.Y, r.Z, r.ArrivalDistance = -5424.85, -2929.87, 347.645, 2.5
			seedSyntheticPortalWorldScope(f, 9)
			s.SetNavigationDataDir(root)
			s.planMapPath, s.openTravel = planMapNavigation, openGroundTravel
			mountForTravel(t, s, me, 23238)
			ctx := context.Background()
			if mode == "ordinary commanded destination" {
				if _, err := s.ReleaseInteraction(ctx, &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
					t.Fatal(err)
				}
				lease, err := s.AcquireInteraction(ctx, &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 30000})
				if err != nil {
					t.Fatal(err)
				}
				r.Token = lease.Token
			}
			if mode == "tagged mounted opportunity" {
				const guid = uint64(0xf130000624000021)
				npc := &worldv1.CreatureState{Guid: guid, Entry: 1572, NpcFlags: 0x2000, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 6,
					X: r.X, Y: r.Y, Z: r.Z, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, UnitFlagsKnown: true, FactionTemplate: 1,
					StationaryPositionKnown: true, PositionAgeMs: 75000, ObservationAgeMs: 9500}
				f.world.RequestedCreatureStationaryPositionKnown = true
				f.lookupCreatures = map[uint64]*worldv1.CreatureState{guid: npc}
				f.world.NearbyCreatures = []*worldv1.CreatureState{npc}
				r.Flightmaster = &actionsv1.FlightmasterTarget{Guid: guid, PositionRevision: npc.PositionRevision, FollowRevision: s.followRevision}
			}
			c := &flightmasterTrafficSession{sessionAPI: s.session, f: f}
			s.session = c
			s.approachDelay = func(call context.Context, d time.Duration) error {
				advance(d)
				if len(f.packets) > 0 {
					c.update(true)
				}
				return call.Err()
			}
			goal := position{x: r.X, y: r.Y, z: r.Z}
			last := playerPosition(me)
			for leg := 1; leg <= 6; leg++ {
				out, err := s.TravelTo(ctx, r)
				if err != nil || out.GetMoving() || out.GetMotionUnconfirmed() || out.GetTraveling() || !out.GetNavAvailable() ||
					out.GetReason() != "exploration_within_distance" && out.GetReason() != "exploration_flightmaster_checkpoint_reached" ||
					distance(s.pos, last) < .05 || countPackets(f, opcode.MSGMoveStop) != leg {
					t.Fatal("dry trip did not settle its own input", leg, err, out)
				}
				last = s.pos
				gap := distance(s.pos, goal)
				if r.Flightmaster != nil && gap <= 5 || gap <= float64(r.ArrivalDistance) {
					if r.Flightmaster != nil {
						q := &actionsv1.FlightpathQueryRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Token: r.Token, NpcGuid: r.Flightmaster.Guid, Mode: "status", Opportunity: r.Flightmaster}
						query, err := s.QueryFlightpath(ctx, q)
						if err != nil || !query.GetOperation().GetTransportWritten() || countPackets(f, opcode.CMSGTaxinodeStatusQuery) != 1 {
							t.Fatal("the bounded mounted walk could not query from its actual terminal STOP", err, query)
						}
						if _, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGTaxinodeStatus, Payload: flightpathStatusPayload(q.NpcGuid, 0)}); err != nil {
							t.Fatal(err)
						}
						q.Mode, q.ExpectedRevision = "discover", query.Revision
						discover, err := s.QueryFlightpath(ctx, q)
						if err != nil || !discover.GetOperation().GetTransportWritten() || countPackets(f, opcode.CMSGTaxiqueryavailablenodes) != 1 {
							t.Fatal("unknown node did not support a single free discovery", err, discover)
						}
						for _, p := range []*modv1.WorldPacket{{Opcode: opcode.SMSGNewTaxiPath}, {Opcode: opcode.SMSGTaxinodeStatus, Payload: flightpathStatusPayload(q.NpcGuid, 1)}} {
							if _, err := s.OnPacket(ctx, p); err != nil {
								t.Fatal(err)
							}
						}
						learned, err := s.FlightpathObservation(ctx, &emptypb.Empty{})
						if err != nil || !learned.GetOperation().GetLearned() {
							t.Fatal("paired realm fixtures did not confirm learning", err, learned)
						}
					}
					t.Logf("map 0, %d trips, %d heartbeats, %d STOP, last XYZ %.3f %.3f %.3f, %.3f yards from observed master", leg, countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveStop), s.pos.x, s.pos.y, s.pos.z, gap)
					return
				}
			}
			t.Fatal("verified arrival missing", s.stateLocked())
		})
	}
}
