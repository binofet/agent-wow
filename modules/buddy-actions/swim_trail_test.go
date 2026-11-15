package buddyactions

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func TestSwimTrailDoesNotJoinAcrossWorldChangeOrTeleport(t *testing.T) {
	_, f, _ := rig(t)
	w := f.world
	w.Owner.MovementFlags = swimMovementFlag
	var trail swimTrail
	trail.observe(w, 1)
	w.Owner.X += 2
	w.Owner.PositionRevision++
	trail.observe(w, 1)
	if len(trail.points) != 2 {
		t.Fatalf("expected two observed swim samples, got %v", trail.points)
	}
	w.Owner.X += buddynav.MaxSwimTrailStep + 1
	w.Owner.PositionRevision++
	trail.observe(w, 1)
	if len(trail.points) != 1 {
		t.Fatal("joined swim observations across a teleport")
	}
	w.Self.MapId = 48
	trail.observe(w, 1)
	if len(trail.points) != 0 {
		t.Fatal("kept swimming observations from another map")
	}
}

func TestFollowDivesThroughBlackfathomSubmergedPassage(t *testing.T) {
	assets, trace := os.Getenv("BUDDY_NAV_BFD_TEST_DATA_DIR"), os.Getenv("BUDDY_NAV_BFD_DIVE_TEST_FILE")
	if assets == "" || trace == "" {
		t.Skip("set the installed Blackfathom assets and observed swim trail")
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var frames []struct {
		buddynav.Point
		Flags uint32
	}
	if err := json.Unmarshal(data, &frames); err != nil {
		t.Fatal(err)
	}
	s, f, advance := rig(t)
	s.SetNavigationDataDir(assets)
	self, leader := f.world.Self, f.world.Owner
	self.MapId, leader.MapId = 48, 48
	self.X, self.Y, self.Z = -368.66, 280.57, -56.56
	self.MovementFlags = swimMovementFlag
	ctx := context.Background()
	toggle(t, s, true)
	for _, frame := range frames {
		if frame.Flags&swimMovementFlag == 0 {
			break
		}
		leader.X, leader.Y, leader.Z = frame.X, frame.Y, frame.Z
		leader.MovementFlags = frame.Flags
		leader.PositionRevision++
		s.swimTrail.observe(f.world, 1)
	}
	goal := playerPosition(leader)
	start := playerPosition(self)
	if _, err := buddynav.PlanSwimContext(ctx, assets, 48, swimPoint(start), swimPoint(goal)); !disconnectedSwimRoute(err) {
		t.Fatalf("test requires disconnected surface polygons, got %v", err)
	}
	lowest := start.z
	for step := 0; step < 250; step++ {
		advance(200 * time.Millisecond)
		s.Tick(ctx)
		lowest = min(lowest, s.pos.z)
		if distance(s.pos, goal) <= .05 && !s.state.Moving {
			break
		}
	}
	if lowest > -70 || distance(s.pos, goal) > .05 {
		t.Fatalf("expected a dive below -70 and arrival at %+v, got low %.3f, position %+v, %s / %s", goal, lowest, s.pos, s.state.Reason, s.state.NavReason)
	}
}
