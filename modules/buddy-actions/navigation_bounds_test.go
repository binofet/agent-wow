package buddyactions

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestNorthshireNavigationIncludesObservedEchoRidgeAndBoundedMine(t *testing.T) {
	for _, p := range []position{{x: -8648.084, y: -129.24467, z: 87.75133}, {x: -8420, y: -120, z: 65}, {x: -8400, y: 200, z: 40}} {
		if !northshire(p) {
			t.Fatal("supported mine position rejected", p)
		}
	}
	for _, p := range []position{{x: -8399.99, y: -120, z: 65}, {x: -9599.01, y: -120, z: 65}, {x: -8500, y: -700.01, z: 65}, {x: -8500, y: 200.01, z: 65}, {x: -8500, y: -120, z: 39.99}, {x: -8500, y: -120, z: 180.01}, {x: float32(math.NaN()), y: -120, z: 65}} {
		if northshire(p) {
			t.Fatal("unsupported position accepted", p)
		}
	}
	from, to := position{x: -8480, y: -120, z: 75}, position{x: -8420, y: -120, z: 65}
	if !validNavigationPath([]position{from, to}, from, to) {
		t.Fatal("bounded complete mine route rejected")
	}
	if validNavigationPath([]position{from, {x: -8399, y: -120, z: 65}, to}, from, to) {
		t.Fatal("out-of-area mine route accepted")
	}
}

func TestFormationFollowUsesMineNavigationWithElevenYardElevationChange(t *testing.T) {
	s, f, advance := formationRig(t, 1, 5)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8665, -129, 76
	f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = -8648.084, -129.24467, 87.75133
	toggle(t, s, true)
	for range 5 {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if !s.state.Moving || !s.state.NavAvailable || s.state.Reason != "following_formation" || s.pos.z <= 76 {
		t.Fatal("mine route fell through or retained old flat-ground/bounds gate", &s.state, s.pos)
	}
}

func TestConfiguredNavigationNeverFallsThroughOutsideMineArea(t *testing.T) {
	s, f, _ := formationRig(t, 1, 5)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8401, -120, 75
	f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = -8390, -120, 75
	toggle(t, s, true)
	s.Tick(context.Background())
	if s.state.Moving || len(f.packets) != 0 {
		t.Fatal("unsupported nav area fell through to direct movement", &s.state, f.packets)
	}
}

func TestAdjacentVineyardGoldshireCorridorRemainsBounded(t *testing.T) {
	for _, p := range []position{{x: -9460, y: -100, z: 60}, {x: -9100, y: -650, z: 80}, {x: -9599, y: -700, z: 40}} {
		if !northshire(p) {
			t.Fatal("supportedcorridorpositionrejected", p)
		}
	}
	from, to := position{x: -9360, y: -150, z: 60}, position{x: -9460, y: -100, z: 60}
	if !validNavigationPath([]position{from, to}, from, to) {
		t.Fatal("boundedGoldshirerouterejected")
	}
	if validNavigationPath([]position{from, {x: -9600, y: -100, z: 60}, to}, from, to) {
		t.Fatal("corridorescapesfixedtilecoverage")
	}
	if validNavigationPath([]position{from, {x: -9400, y: -750, z: 60}, to}, from, to) {
		t.Fatal("corridorescapessouthbound")
	}
}

func TestControllerNavigationAccepts240YardsButRejectsLongerRoutes(t *testing.T) {
	from, to := position{x: -8900, y: -100, z: 90}, position{x: -8660, y: -100, z: 90}
	if !validNavigationPath([]position{from, to}, from, to) {
		t.Fatal("complete 240 yard route rejected")
	}
	tooFar := to
	tooFar.x += .01
	if validNavigationPath([]position{from, tooFar}, from, tooFar) {
		t.Fatal("route beyond 240 yards accepted")
	}
	if validNavigationPath([]position{from, {x: -8780, y: -80, z: 90}, to}, from, to) {
		t.Fatal("over-budget detour accepted for nearby endpoints")
	}
}
