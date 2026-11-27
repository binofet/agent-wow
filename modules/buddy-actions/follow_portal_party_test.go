package buddyactions

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestInstanceEntryLuaUsesGroundedNearSideFromExterior(t *testing.T) {
	p := newInstanceEntryPolicyFixture(t)
	park := position{x: -6071.37, y: -2955.16, z: 209.782}
	putAtPortal(p.f.world.Self, 0, park)
	putAtPortal(p.f.world.Owner, 0, park)
	p.f.world.Self.PositionRevision++
	p.s.encounterVisit.mapID = 0
	seedSyntheticPortalWorldScope(p.f, 9)
	p.command(map[string]any{"task": "enter_instance", "id": "near-side", "ownerGuid": "3", "destinationMapId": 70, "allowedTriggerIds": []any{286}, "triggerId": 286})
	for _, capability := range []string{"instance_entry_status", "area_trigger_reference", "area_trigger_observation", "acquire_idle_interaction"} {
		p.dispatch(capability)
	}
	a := p.action("plan_route_leg")
	goal := buddynav.Point{X: float32(a["x"].(float64)), Y: float32(a["y"].(float64)), Z: float32(a["z"].(float64))}
	entrance, inside := witnessedPortal(0, position{x: goal.X, y: goal.Y, z: goal.Z})
	if !inside || entrance.trigger != 286 || goal.Z != park.z || goal.X >= entrance.center.x-1 {
		t.Fatal("Lua did not choose the near side in the actual approach plane", a)
	}
	if root := os.Getenv("BUDDY_NAV_TEST_DATA"); root != "" {
		points, err := buddynav.PlanLegContext(context.Background(), root, 0, buddynav.Point{X: park.x, Y: park.y, Z: park.z}, goal)
		if err != nil || len(points) < 2 {
			t.Fatal("entry leg", err, points)
		}
		end := points[len(points)-1]
		if !entrance.contains(0, position{x: end.X, y: end.Y, z: end.Z}, .25) {
			t.Fatal("mesh misses the accepted volume", end)
		}
	}
}

func TestOrdinaryFollowFromOutsideEntranceRequiresPartyDestination(t *testing.T) {
	for _, tc := range []struct {
		name        string
		known       bool
		destination uint32
		enter       bool
	}{
		{"party inside", true, 70, true}, {"missing", false, 0, false}, {"exterior only", true, 0, false}, {"other instance", true, 90, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, advance := portalEntryRig(t)
			s.navDataDir = "fixture"
			if root := os.Getenv("BUDDY_NAV_TEST_DATA"); root != "" {
				s.navDataDir = root
				s.planMapPath = planMapNavigation
			}
			park := position{x: -6071.37, y: -2955.16, z: 209.782}
			putAtPortal(f.world.Self, 0, park)
			putAtPortal(f.world.Owner, 0, park)
			f.party.Group.GroupGuid = 99
			s.encounterVisit.mapID = 0
			seedSyntheticPortalWorldScope(f, 9)
			ctx := context.Background()
			toggle(t, s, true)
			s.Tick(ctx)
			f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
			for range 4 {
				advance(250 * time.Millisecond)
				s.Tick(ctx)
			}
			if countPackets(f, opcode.CMSGRequestPartyMemberStats) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 0 {
				t.Fatalf("lost sight: %s, queries=%d, entries=%d", s.state.Reason, countPackets(f, opcode.CMSGRequestPartyMemberStats), countPackets(f, opcode.CMSGAreatrigger))
			}
			f.world.Owner.MapKnown, f.world.Owner.MapId = tc.known, tc.destination
			for range 30 {
				advance(250 * time.Millisecond)
				s.Tick(ctx)
			}
			count := 0
			var prior uint32
			for _, p := range f.packets {
				if p.Opcode == opcode.CMSGRequestPartyMemberStats && (len(p.Payload) != 8 || binary.LittleEndian.Uint64(p.Payload) != ownerGUID) {
					t.Fatalf("unbound group query: %x", p.Payload)
				}
				if p.Opcode == opcode.CMSGAreatrigger {
					count++
					if binary.LittleEndian.Uint32(p.Payload) != 286 || prior != opcode.MSGMoveStop {
						t.Fatalf("trigger without exact STOP %x previous %x", p.Payload, prior)
					}
				}
				prior = p.Opcode
			}
			if (count == 1) != tc.enter || count > 1 || !s.state.Following || countPackets(f, opcode.CMSGRequestPartyMemberStats) != 1 {
				t.Fatalf("physical outcome %d following=%t reason=%s, position=%+v", count, s.state.Following, s.state.Reason, s.pos)
			}
		})
	}
}
