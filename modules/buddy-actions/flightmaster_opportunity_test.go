package buddyactions

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestFlightmasterOpportunityRequiresCurrentSpawnAndOwnPhysicalArrival(t *testing.T) {
	for _, mode := range []string{"qualified", "ground_mount", "vehicle", "flying_mount", "swimming", "unbound_mount", "lost_stationarity", "new_follow_intent", "different_faction", "replaced_spawn_pose", "missing_ground_stop"} {
		t.Run(mode, func(t *testing.T) {
			x := walkedToFlightmaster(t)
			npc := x.f.lookupCreatures[x.npc]
			x.f.world.Self.FactionTemplate, npc.FactionTemplate = 1, 1
			r := x.request(t, "status")
			r.Opportunity = &actionsv1.FlightmasterTarget{Guid: npc.Guid, PositionRevision: npc.PositionRevision, FollowRevision: x.s.followRevision}
			switch mode {
			case "ground_mount", "vehicle", "flying_mount", "swimming", "unbound_mount":
				spell := uint32(23238)
				if mode == "vehicle" {
					spell = 60424
				}
				if mode == "flying_mount" {
					spell = 32235
				}
				mountForTravel(t, x.s, x.f.world.Self, spell)
				if mode == "swimming" {
					x.f.world.Self.MovementFlags = 0x200000
				}
				if mode == "unbound_mount" {
					r.Opportunity = nil
				}
			case "lost_stationarity":
				x.f.world.RequestedCreatureStationaryPositionKnown = false
			case "new_follow_intent":
				x.s.followRevision++
			case "different_faction":
				npc.FactionTemplate = 4
			case "replaced_spawn_pose":
				npc.PositionRevision++
			case "missing_ground_stop":
				x.f.ownMovementReport = nil
			}
			view, err := x.s.QueryFlightpath(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			want := mode == "qualified" || mode == "ground_mount"
			if view.Operation.TransportWritten != want || (len(x.f.packets) == 1) != want || !want && view.Operation.Phase != "not_dispatched" {
				t.Fatal(view, x.f.packets)
			}
			if want && (x.f.packets[0].Opcode != opcode.CMSGTaxinodeStatusQuery || x.f.packets[0].TransportScope == nil) {
				t.Fatal(x.f.packets[0])
			}
			if mode == "ground_mount" {
				x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 0))
				next := x.request(t, "discover")
				next.Opportunity = r.Opportunity
				menu, err := x.s.QueryFlightpath(context.Background(), next)
				if err != nil || !menu.GetOperation().GetTransportWritten() || len(x.f.packets) != 2 || x.f.packets[1].Opcode != opcode.CMSGTaxiqueryavailablenodes {
					t.Fatal(menu, err, x.f.packets)
				}
				x.packet(t, opcode.SMSGNewTaxiPath, nil)
				x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
				if !x.observation(t).GetOperation().GetLearned() || x.f.world.Self.UnitFlags&movementflags.Mounted == 0 {
					t.Fatal(x.observation(t))
				}
			}
		})
	}
}

func TestFlightmasterApproachFollowsShortCheckedGroundLegAndStops(t *testing.T) {
	for _, mode := range []string{"short_leg", "mounted_short_leg", "unrelated_destination", "diverted_before_input", "lost_after_start", "lost_at_heartbeat_clock", "long_detour", "no_safe_geometry"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, r := instanceEntryIdleTravelFixture(t)
			self := f.world.Self
			self.FactionTemplate = 1
			if mode == "mounted_short_leg" {
				mountForTravel(t, s, self, 23238)
			}
			const guid = uint64(0xf130000625000001)
			r.X, r.Y, r.Z = self.X+9, self.Y, self.Z
			npc := &worldv1.CreatureState{Guid: guid, Entry: 1573, NpcFlags: 0x2000, Present: true, MapKnown: true, MapId: r.MapId, PositionKnown: true, PositionRevision: 6, X: r.X, Y: r.Y, Z: r.Z,
				AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, UnitFlagsKnown: true, FactionTemplate: 1, StationaryPositionKnown: true}
			f.lookupCreatures = map[uint64]*worldv1.CreatureState{guid: npc}
			f.world.NearbyCreatures = []*worldv1.CreatureState{npc}
			r.Flightmaster = &actionsv1.FlightmasterTarget{Guid: guid, PositionRevision: 6, FollowRevision: s.followRevision}
			s.planMapPath = func(ctx context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				switch mode {
				case "long_detour":
					return []position{from, {x: from.x, y: from.y + 16, z: from.z}, to}, nil
				case "no_safe_geometry":
					return nil, context.DeadlineExceeded
				case "diverted_before_input":
					s.followRevision++
				}
				return []position{from, to}, nil
			}
			if mode == "unrelated_destination" {
				r.X++
			}
			if mode == "lost_after_start" {
				s.approachDelay = func(ctx context.Context, d time.Duration) error {
					advance(d)
					delete(f.lookupCreatures, guid)
					return ctx.Err()
				}
			}
			if mode == "lost_at_heartbeat_clock" {
				f.onClock = func() {
					if len(f.packets) > 0 {
						delete(f.lookupCreatures, guid)
					}
				}
			}
			out, err := s.TravelTo(context.Background(), r)
			if mode == "no_safe_geometry" && err != nil && len(f.packets) == 0 {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, packet := range f.packets {
				if packet.TransportScope == nil || packet.TransportScope.VisitRevision != 9 {
					t.Fatal("unscoped move", packet)
				}
			}
			if out.Moving || out.Traveling || out.MotionUnconfirmed {
				t.Fatal(out)
			}
			switch mode {
			case "short_leg", "mounted_short_leg":
				if out.Reason != "exploration_flightmaster_checkpoint_reached" || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 ||
					distance(playerPosition(self), s.pos) <= 0 || distance(playerPosition(self), s.pos) > 3 {
					t.Fatal(out, f.packets, s.pos)
				}
			case "lost_after_start", "lost_at_heartbeat_clock":
				if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
					t.Fatal(out, f.packets)
				}
			default:
				if len(f.packets) != 0 {
					t.Fatal("ineligible target or route wrote movement", out, f.packets)
				}
			}
		})
	}
}

func TestFlightmasterOpportunityRereadsTheFollowedOwnerAtGroundArrival(t *testing.T) {
	for _, mode := range []string{"current", "disappeared", "separated", "combat", "different_owner", "new_map"} {
		t.Run(mode, func(t *testing.T) {
			x := walkedToFlightmaster(t)
			w := x.f.world
			w.Owner = proto.Clone(w.Self).(*worldv1.PlayerState)
			w.Owner.Guid, w.Owner.X, w.Owner.Y, w.Owner.Z = 33, x.s.pos.x, x.s.pos.y, x.s.pos.z
			x.s.state.Following = true
			w.Self.FactionTemplate, x.f.lookupCreatures[x.npc].FactionTemplate = 1, 1
			r := x.request(t, "status")
			r.Opportunity = &actionsv1.FlightmasterTarget{Guid: x.npc, PositionRevision: x.f.lookupCreatures[x.npc].PositionRevision, FollowRevision: x.s.followRevision, FollowOwnerGuid: 33, OwnerRangeYards: 18}
			x.f.onOwnMovementReport = func(*modv1.WorldPacket) {
				switch mode {
				case "disappeared":
					w.Owner = nil
				case "separated":
					w.Owner.X += 40
				case "combat":
					w.Owner.UnitFlags |= 0x80000
				case "different_owner":
					w.Owner.Guid++
				case "new_map":
					w.Owner.MapId++
				}
			}
			view, err := x.s.QueryFlightpath(context.Background(), r)
			if err != nil || view.Operation.TransportWritten != (mode == "current") || (len(x.f.packets) == 1) != (mode == "current") {
				t.Fatal(view, err, x.f.packets)
			}
		})
	}
}

func TestFlightmasterFactionTemplatesReadExactWrathRows(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dbc"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "dbc", "FactionTemplate.dbc")
	rows := [][14]uint32{{1, 1, 0, 2, 2, 4}, {55, 47, 0, 2, 2, 4}, {2, 2, 0, 4, 4, 2}, {300, 70, 0, 0, 0, 0, 47}, {301, 71, 0, 0, 0, 0, 0, 0, 0, 0, 47}}
	raw := []byte("WDBC")
	for _, n := range []uint32{uint32(len(rows)), 14, 56, 1} {
		raw = binary.LittleEndian.AppendUint32(raw, n)
	}
	for _, row := range rows {
		for _, n := range row {
			raw = binary.LittleEndian.AppendUint32(raw, n)
		}
	}
	raw = append(raw, 0)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Service{navDataDir: root}
	for _, pair := range []struct {
		self, npc uint32
		want      bool
	}{{1, 55, true}, {2, 55, false}, {300, 55, false}, {301, 55, true}, {0, 55, false}, {1, 999, false}} {
		if got := s.flightmasterFriendlyLocked(pair.self, pair.npc); got != pair.want {
			t.Fatal(pair, got)
		}
	}
	if err := os.WriteFile(path, raw[:len(raw)-1], 0600); err != nil {
		t.Fatal(err)
	}
	if readFlightFactionTemplates(root) != nil {
		t.Fatal("malformed DBC admitted")
	}
}
