package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
)

func portalTimingFixture(t *testing.T, catchingUp bool) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, _, _, advance := portalGoalFixture(t)
	if !catchingUp {
		near := s.followPortal.portal.center
		near.y -= float32(s.followPortal.portal.radius + 2)
		putAtPortal(f.world.Self, s.followPortal.portal.source, near)
		f.world.Self.PositionRevision++
		if !s.refreshPositionLocked(f.world) {
			t.Fatal("could not observe the near-entrance source pose")
		}
		f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = true, true, true
		s.observeDungeonPortalLocked(f.world, s.now())
		f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	}
	s.followPortal.seenAt, s.followPortal.lostAt = s.now(), s.now()
	if s.followPortal.catchingUp != catchingUp {
		t.Fatal("fixture did not preserve actual distance classification", s.followPortal)
	}
	return s, f, advance
}

// Timing semantics use the actual shared source independently of the real
// default-budget watchdog tests. Seed it before any sighting is classified.
func healthyPortalTimingFixture(t *testing.T, catchingUp bool) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	runtime, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Reload(behaviors.FollowFormationSource, ""); err != nil {
		t.Fatal(err)
	}
	s.formation.runtime = runtime
	s.sessionID, s.navDataDir = f.party.SessionId, "/client/portal-fixture"
	portal := dungeonEntrances[0]
	start := portal.center
	start.y -= 80
	if !catchingUp {
		start.y = portal.center.y - float32(portal.radius+2)
	}
	putAtPortal(f.world.Self, portal.source, start)
	putAtPortal(f.world.Owner, portal.source, portal.center)
	activateEncounterWriter(t, s, f)
	seedSyntheticPortalWorldScope(f, 9)
	toggle(t, s, true)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("fixture could not observe its current pose")
	}
	s.lastWorld = f.world
	s.observeDungeonPortalLocked(f.world, s.now())
	if s.followPortal.portal.trigger != portal.trigger || s.followPortal.witness == 0 ||
		s.followPortal.sourceVisit != f.world.VisitRevision || s.followPortal.followRevision != s.followRevision ||
		s.followPortal.catchingUp != catchingUp {
		t.Fatal("timing fixture did not establish its actual bound entrance sighting", s.followPortal, s.formation.lastFailure, runtime.Status())
	}
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	s.followPortal.seenAt, s.followPortal.lostAt = s.now(), s.now()
	return s, f, advance
}

func TestPortalFollowPolicyOwnsExactFreshnessAndWaitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		catchingUp bool
		sighting   time.Duration
		lost       time.Duration
		eligible   bool
		ready      bool
	}{
		{"first disappearance", false, 0, 0, true, false},
		{"before half second", false, time.Second, 499 * time.Millisecond, true, false},
		{"half second", false, time.Second, 500 * time.Millisecond, true, true},
		{"near freshness boundary", false, 20 * time.Second, time.Second, true, true},
		{"near expired", false, 20*time.Second + time.Millisecond, time.Second, false, false},
		{"catchup still fresh", true, 20*time.Second + time.Millisecond, time.Second, true, true},
		{"catchup freshness boundary", true, 75 * time.Second, time.Second, true, true},
		{"catchup expired", true, 75*time.Second + time.Millisecond, time.Second, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := healthyPortalTimingFixture(t, tc.catchingUp)
			s.followPortal.seenAt, s.followPortal.lostAt = s.now().Add(-tc.sighting), s.now().Add(-tc.lost)
			got := s.portalPolicyLocked(f.world, s.now())
			if !got.Valid || got.Eligible != tc.eligible || got.Ready != tc.ready || len(f.packets) != 0 {
				t.Fatal("shared Lua timing boundary changed", got, f.packets, s.followPortal, s.formation.lastFailure)
			}
			view := s.portalObservationLocked(f.world)
			if !view.GetSightingAgeKnown() || view.GetSightingAgeMs() != uint64(tc.sighting.Milliseconds()) ||
				!view.GetDisappearanceAgeKnown() || view.GetDisappearanceAgeMs() != uint64(tc.lost.Milliseconds()) {
				t.Fatal("raw observation concealed or changed the timing facts", view)
			}
		})
	}
}

func TestPortalFollowPolicyDoesNotInventUnknownOrFutureAges(t *testing.T) {
	t.Run("disappearance clock starts at observation", func(t *testing.T) {
		s, f, advance := healthyPortalTimingFixture(t, false)
		s.followPortal.lostAt = time.Time{}
		first := s.now()
		s.observeDungeonPortalLocked(f.world, first)
		advance(500 * time.Millisecond)
		s.observeDungeonPortalLocked(f.world, s.now())
		got := s.portalPolicyLocked(f.world, s.now())
		if s.followPortal.lostAt != first || !got.Valid || !got.Eligible || !got.Ready || len(f.packets) != 0 {
			t.Fatal("delayed follow execution restarted the actually observed disappearance clock", s.followPortal, got, f.packets, s.formation.lastFailure)
		}
	})
	for _, name := range []string{"unknown sighting", "future sighting", "unknown disappearance", "future disappearance", "legacy zero visit"} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := healthyPortalTimingFixture(t, false)
			s.followPortal.lostAt = s.now().Add(-time.Second)
			sightingKnown, lostKnown, eligible, ready := true, true, true, true
			switch name {
			case "unknown sighting":
				s.followPortal.seenAt = time.Time{}
				sightingKnown, eligible, ready = false, false, false
			case "future sighting":
				s.followPortal.seenAt = s.now().Add(time.Millisecond)
				sightingKnown, eligible, ready = false, false, false
			case "unknown disappearance":
				s.followPortal.lostAt = time.Time{}
				lostKnown, ready = false, false
			case "future disappearance":
				s.followPortal.lostAt = s.now().Add(time.Millisecond)
				lostKnown, ready = false, false
			case "legacy zero visit":
				s.followPortal.sourceVisit, f.world.VisitRevision, s.encounterVisit.revision = 0, 0, 0
			}
			got := s.portalPolicyLocked(f.world, s.now())
			if !got.Valid || got.Eligible != eligible || got.Ready != ready || len(f.packets) != 0 {
				t.Fatal("unknown timing granted entry or matching legacy visits were lost", got, f.packets, s.followPortal, s.formation.lastFailure)
			}
			if name != "legacy zero visit" {
				view := s.portalObservationLocked(f.world)
				if view.GetSightingAgeKnown() != sightingKnown || view.GetDisappearanceAgeKnown() != lostKnown {
					t.Fatal("raw knownness disagreed with the witnessed clock", view)
				}
			}
		})
	}
}

func portalTimingSource(t *testing.T, wait, sighting string) string {
	t.Helper()
	const declaration = "portal_wait_ms, portal_seen_ms, portal_catchup_ms = 500,20000,75000"
	source := strings.Replace(behaviors.FollowFormationSource, declaration,
		"portal_wait_ms, portal_seen_ms, portal_catchup_ms = "+wait+","+sighting+",75000", 1)
	if source == behaviors.FollowFormationSource {
		t.Fatal("test did not edit the actual shared Lua timing declaration")
	}
	return source
}

func TestPortalFollowPolicySourceEditChangesWaitAndFreshnessInSameBody(t *testing.T) {
	s, f, _ := portalTimingFixture(t, false)
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	s.followPortal.lostAt = s.now().Add(-750 * time.Millisecond)
	if before := s.portalPolicyLocked(f.world, s.now()); !before.Valid || !before.Eligible || !before.Ready {
		t.Fatal("default timing fixture was not ready", before)
	}
	sid, follow, witness := s.sessionID, s.followRevision, s.followPortal.witness
	revision := host.reload(t, portalTimingSource(t, "1500", "20000"))
	if slower := s.portalPolicyLocked(f.world, s.now()); !slower.Valid || !slower.Eligible || slower.Ready || s.formation.sourceRevision != revision {
		t.Fatal("current edited wait did not replace the old native timing", slower, s.formation.sourceRevision)
	}
	s.followPortal.seenAt = s.now().Add(-25 * time.Second)
	if stale := s.portalPolicyLocked(f.world, s.now()); !stale.Valid || stale.Eligible || stale.Ready {
		t.Fatal("default short sighting window did not expire", stale)
	}
	host.reload(t, portalTimingSource(t, "1500", "30000"))
	if fresher := s.portalPolicyLocked(f.world, s.now()); !fresher.Valid || !fresher.Eligible || fresher.Ready {
		t.Fatal("edited sighting window did not apply independently of wait", fresher)
	}
	revision = host.reload(t, portalTimingSource(t, "500", "30000"))
	if ready := s.portalPolicyLocked(f.world, s.now()); !ready.Valid || !ready.Eligible || !ready.Ready || s.formation.sourceRevision != revision ||
		s.sessionID != sid || s.followRevision != follow || s.followPortal.witness != witness || len(f.packets) != 0 {
		t.Fatal("timing edit reset custody or required a different body", ready, s.followPortal, f.packets)
	}
}

func TestPortalFollowPolicyReadyGatesVerifiedMovement(t *testing.T) {
	s, f, advance := portalTimingFixture(t, false)
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	revision := host.reload(t, portalTimingSource(t, "1000", "20000"))
	plans := 0
	s.planMapPath = func(call context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
		plans++
		deadline, known := call.Deadline()
		if !known || time.Until(deadline) > 251*time.Millisecond || mapID != f.world.Self.MapId || to != s.followPortal.ground {
			t.Fatal("ready policy bypassed the bounded exact route", deadline, mapID, to)
		}
		return []position{from, to}, nil
	}
	for range 3 {
		advance(250 * time.Millisecond)
		s.Tick(context.Background())
		if plans != 0 || len(f.packets) != 0 || s.state.Moving || !s.state.Following {
			t.Fatal("native route ran before the current Lua wait elapsed", plans, s.stateLocked(), f.packets)
		}
	}
	advance(250 * time.Millisecond)
	s.Tick(context.Background())
	if plans != 1 || countPackets(f, opcode.MSGMoveStartForward) != 1 || !s.state.Moving || !s.state.Following || s.formation.sourceRevision != revision || countPackets(f, opcode.CMSGAreatrigger) != 0 {
		t.Fatal("ready timing did not reach the existing physical executor", plans, s.stateLocked(), f.packets)
	}
}

type portalTimingWorldHook struct {
	*followPolicySession
	beforeWorld func()
}

func (s *portalTimingWorldHook) InvokeModule(ctx context.Context, req *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if req.Module == "buddy-world" && req.Method == "snapshot" && s.beforeWorld != nil {
		s.beforeWorld()
	}
	return s.followPolicySession.InvokeModule(ctx, req, opts...)
}

func TestPortalFollowPolicyChangedBeforeFinalTriggerReadBlocksEntry(t *testing.T) {
	s, f, _ := portalTimingFixture(t, false)
	putAtPortal(f.world.Self, s.followPortal.portal.source, s.followPortal.portal.center)
	f.world.Self.PositionRevision++
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("could not observe the actual source correction")
	}
	s.followPortal.lostAt = s.now().Add(-750 * time.Millisecond)
	host := newFollowPolicySession(t, s, f)
	injected := false
	hook := &portalTimingWorldHook{followPolicySession: host}
	hook.beforeWorld = func() {
		if injected {
			return
		}
		for _, input := range host.inputs {
			if input["mode"] == "portal_follow" {
				host.reload(t, portalTimingSource(t, "2000", "20000"))
				injected = true
				return
			}
		}
	}
	s.session = hook
	s.UseHostFollowPolicy()
	s.Tick(context.Background())
	if !injected || countPackets(f, opcode.CMSGAreatrigger) != 0 || s.followPortal.notified || countPackets(f, opcode.MSGMoveStop) != 1 || len(f.packets) != 1 || s.formation.sourceRevision != host.runtime.Status().Revision {
		t.Fatal("a superseded ready decision authorized the irreversible trigger", injected, s.followPortal, f.packets)
	}
}

func TestPortalFollowPolicyReloadDoesNotReplayUnknownTrigger(t *testing.T) {
	s, f, advance := portalTimingFixture(t, false)
	putAtPortal(f.world.Self, s.followPortal.portal.source, s.followPortal.portal.center)
	f.world.Self.PositionRevision++
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("could not observe the actual source correction")
	}
	s.followPortal.lostAt = s.now().Add(-time.Second)
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	f.failSendOpcode = opcode.CMSGAreatrigger
	f.failSend = true
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAreatrigger) != 1 || !s.followPortal.notified {
		t.Fatal("ambiguous source entry was not reserved before the transport attempt", s.followPortal, f.packets)
	}
	host.reload(t, portalTimingSource(t, "0", "30000"))
	advance(2 * time.Second)
	f.failSend = false
	toggle(t, s, true)
	for range 4 {
		s.Tick(context.Background())
		advance(250 * time.Millisecond)
	}
	if countPackets(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("a new timing source or expired goal replayed unknown entry", f.packets)
	}
}
