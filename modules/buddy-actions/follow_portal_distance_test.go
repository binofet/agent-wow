package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func portalDistanceFixture(t *testing.T, offset position) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	s.navDataDir = "/client/portal-distance-fixture"
	// Both vertical endpoints remain in the same float32 binade, so the
	// exact 45-yard vertical boundary does not depend on rounded subtraction.
	portal := dungeonEntrances[2]
	self := portal.center
	self.x, self.y, self.z = self.x+offset.x, self.y+offset.y, self.z+offset.z
	putAtPortal(f.world.Self, portal.source, self)
	putAtPortal(f.world.Owner, portal.source, portal.center)
	toggle(t, s, true)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("fixture could not bind its actual source pose")
	}
	s.lastWorld = f.world
	s.observeDungeonPortalLocked(f.world, s.now())
	if s.followPortal.portal.trigger != portal.trigger || s.followPortal.witness == 0 {
		t.Fatal("fixture did not observe the owner at the actual entrance", s.followPortal)
	}
	return s, f, advance
}

func portalDistanceSource(t *testing.T, yards string) string {
	t.Helper()
	const declaration = "local portal_approach_yards = 45"
	if !strings.Contains(behaviors.FollowFormationSource, declaration) {
		t.Fatal("shared Lua distance declaration is missing")
	}
	return strings.Replace(behaviors.FollowFormationSource, declaration, "local portal_approach_yards = "+yards, 1)
}

func TestPortalDistanceDefaultSightingUsesExactThreeDimensionalBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		offset     position
		catchingUp bool
	}{
		{"horizontal exact", position{y: -45}, false},
		{"horizontal beyond", position{y: -45.01}, true},
		{"vertical exact", position{z: 45}, false},
		{"vertical beyond", position{z: 45.01}, true},
		{"combined exact", position{x: 27, z: 36}, false},
		{"combined beyond", position{x: 27, z: 36.01}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := portalDistanceFixture(t, tc.offset)
			if s.followPortal.catchingUp != tc.catchingUp || s.formation.runtime == nil ||
				s.formation.sourceRevision == "" || s.formation.remote != nil || s.hostEncounterKey != "" || len(f.packets) != 0 {
				t.Fatal("standalone sighting bypassed the actual default Lua 3D decision", s.followPortal, s.formation.sourceRevision, f.packets)
			}
		})
	}
}

func TestPortalDistanceEditReplacesModeWitnessAndClearsAdmittedGoal(t *testing.T) {
	s, f, req, ctx, advance := portalGoalFixture(t)
	// The fixture's disappearance is not observed by Actions yet. Keep the
	// existing owner-pose witness so only the edited mode can replace it.
	s.followPortal.lostAt = time.Time{}
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	if _, err := s.SetPortalCatchup(ctx, req); err != nil {
		t.Fatal("could not admit the existing exact catch-up proposal", err)
	}
	before, sid, follow := s.followPortal, s.sessionID, s.followRevision
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = true, true, true
	revision := host.reload(t, portalDistanceSource(t, "100"))
	s.observeDungeonPortalLocked(f.world, s.now())
	if s.followPortal.catchingUp || s.followPortal.witness <= before.witness || s.portalGoal.witness != 0 ||
		s.followPortal.ownerPoseRevision != before.ownerPoseRevision || s.followPortal.ground != before.ground ||
		s.formation.sourceRevision != revision || s.sessionID != sid || s.followRevision != follow || len(f.packets) != 0 {
		t.Fatal("same-body distance edit reused the old mode or admitted goal", before, s.followPortal, s.portalGoal, f.packets)
	}
	witness := s.followPortal.witness
	host.reload(t, portalDistanceSource(t, "60"))
	s.observeDungeonPortalLocked(f.world, s.now())
	if !s.followPortal.catchingUp || s.followPortal.witness <= witness {
		t.Fatal("a second real source edit failed to replace the classification", s.followPortal)
	}
	witness = s.followPortal.witness
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	s.observeDungeonPortalLocked(f.world, s.now())
	advance(time.Second)
	host.reload(t, portalDistanceSource(t, "1000"))
	s.observeDungeonPortalLocked(f.world, s.now())
	policy := s.portalPolicyLocked(f.world, s.now())
	if !s.followPortal.catchingUp || s.followPortal.witness != witness || !policy.Valid || !policy.Eligible || !policy.Ready || len(f.packets) != 0 {
		t.Fatal("an absent owner was silently reclassified by a later edit", s.followPortal, policy, f.packets)
	}
}

func TestPortalDistanceEditStopsAndResumesDirectRouteOnSameAbsentWitness(t *testing.T) {
	s, f, advance := portalDistanceFixture(t, position{y: -40})
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	s.observeDungeonPortalLocked(f.world, s.now())
	advance(time.Second)
	witness, sid, follow := s.followPortal.witness, s.sessionID, s.followRevision
	s.Tick(context.Background())
	if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
		t.Fatal("default direct range did not reach the verified route executor", s.stateLocked(), f.packets)
	}
	host.reload(t, portalDistanceSource(t, "35"))
	s.Tick(context.Background())
	if s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || !s.state.Following || s.portalSourceReadyLocked(f.world, s.now()) {
		t.Fatal("edited direct range did not stop the existing route", s.stateLocked(), f.packets)
	}
	revision := host.reload(t, portalDistanceSource(t, "45"))
	s.Tick(context.Background())
	if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 2 || countPackets(f, opcode.CMSGAreatrigger) != 0 ||
		s.followPortal.witness != witness || s.followPortal.catchingUp || s.sessionID != sid || s.followRevision != follow ||
		s.formation.sourceRevision != revision {
		t.Fatal("source restoration did not resume the same bounded direct witness", s.followPortal, s.stateLocked(), f.packets)
	}
}

func TestPortalDistanceFinalTriggerRejectsPoseChangedAfterLuaDecision(t *testing.T) {
	s, f, advance := portalDistanceFixture(t, position{y: -.5})
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	host.reload(t, portalDistanceSource(t, "1"))
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	s.observeDungeonPortalLocked(f.world, s.now())
	advance(time.Second)
	actualLua := s.formation.remote
	readyReads, injected := 0, false
	positionRevision := f.world.Self.PositionRevision
	s.formation.remote = func(input map[string]any) (bool, string, map[string]any, error) {
		available, revision, output, err := actualLua(input)
		if input["mode"] == "portal_follow" && err == nil && output["ready"] == true {
			readyReads++
			if readyReads == 2 {
				// Change only XYZ after the final Lua evaluation. The next
				// actual World read remains inside the entrance, but no longer
				// matches the one-yard policy fact that authorized this read.
				f.world.Self.Y = s.followPortal.ground.y - 2
				injected = true
			}
		}
		return available, revision, output, err
	}
	s.Tick(context.Background())
	if !injected || readyReads != 2 || f.world.Self.PositionRevision != positionRevision ||
		distance(playerPosition(f.world.Self), s.followPortal.portal.center) >= s.followPortal.portal.radius ||
		countPackets(f, opcode.CMSGAreatrigger) != 0 || s.followPortal.notified || len(f.packets) != 0 {
		t.Fatal("an earlier Lua pose authorized entry after the final observation changed", injected, readyReads, s.followPortal, f.packets)
	}
}

func TestPortalDistanceMalformedRemoteCannotReuseOldSightingOrEnter(t *testing.T) {
	for _, name := range []string{"missing catchingUp", "wrong catchingUp type", "host transport error"} {
		t.Run(name, func(t *testing.T) {
			s, f, advance := portalDistanceFixture(t, position{})
			host := newFollowPolicySession(t, s, f)
			s.UseHostFollowPolicy()
			if name == "host transport error" {
				host.bad = "transport"
			} else {
				decision := "{}"
				if name == "wrong catchingUp type" {
					decision = `{catchingUp="false"}`
				}
				source := strings.Replace(behaviors.FollowFormationSource, "return M", `
local original=M.decide
M.decide=function(input)
    if input.mode=="portal_sighting" then return `+decision+` end
    return original(input)
end
return M`, 1)
				host.reload(t, source)
			}
			s.observeDungeonPortalLocked(f.world, s.now())
			if s.followPortal.portal.trigger != 0 || s.followPortal.witness != 0 || s.portalGoal.witness != 0 {
				t.Fatal("malformed current policy retained an older valid classification", s.followPortal, s.portalGoal)
			}
			f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
			s.observeDungeonPortalLocked(f.world, s.now())
			advance(time.Second)
			s.Tick(context.Background())
			if len(f.packets) != 0 || countPackets(f, opcode.CMSGAreatrigger) != 0 || s.state.Moving {
				t.Fatal("invalid sighting acquired route or trigger authority", s.followPortal, f.packets)
			}
		})
	}
}

func TestPortalDistanceEditCannotEnlargeNativeRouteBudget(t *testing.T) {
	for _, catchingUp := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct witness", true: "catchup witness"}[catchingUp], func(t *testing.T) {
			offset := float32(-40)
			if catchingUp {
				offset = -80
			}
			s, f, advance := portalDistanceFixture(t, position{y: offset})
			host := newFollowPolicySession(t, s, f)
			s.UseHostFollowPolicy()
			f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
			s.observeDungeonPortalLocked(f.world, s.now())
			advance(time.Second)
			host.reload(t, portalDistanceSource(t, "1000"))
			beyond := s.followPortal.ground
			beyond.y -= float32(buddynav.MaxRouteLength + .25)
			putAtPortal(f.world.Self, s.followPortal.portal.source, beyond)
			f.world.Self.PositionRevision++
			if !s.refreshPositionLocked(f.world) {
				t.Fatal("could not observe the actual source correction")
			}
			policy := s.portalPolicyLocked(f.world, s.now())
			if !policy.Valid || !policy.Eligible || !policy.Ready || s.portalSourceCurrentLocked(f.world, s.now()) || s.portalSourceReadyLocked(f.world, s.now()) {
				t.Fatal("editable classification replaced the native physical budget", policy, s.followPortal)
			}
			s.Tick(context.Background())
			if s.followPortal.catchingUp != catchingUp || len(f.packets) != 0 || s.state.Moving {
				t.Fatal("distance edit authorized an over-budget route", s.followPortal, f.packets)
			}
		})
	}
}
