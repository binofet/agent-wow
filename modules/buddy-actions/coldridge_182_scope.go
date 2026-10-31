package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func (g coldridgePracticeGoal) usesTrollRegion() bool {
	return g.catchup && !g.leveling && g.questID == 0 || !g.catchup && (g.leveling && g.questID == 0 || !g.leveling && g.questID == 182)
}

func (g coldridgePracticeGoal) contains(p position) bool {
	if g.usesTrollRegion() {
		return finite(p) && buddynav.RegionColdridgeTrollCave.Contains(buddynav.Point{X: p.x, Y: p.y, Z: p.z})
	}
	if g.finite() {
		return false
	}
	switch g.questID {
	case 179, 170, 183:
		return coldridge(p)
	default:
		return false
	}
}

// The raw own baseline can remain at its last admitted valley observation
// while ordinary travel advances the client estimate. It grants no route or
// target authority outside the quest's separate combat region.
func (g coldridgePracticeGoal) baselineContains(p position) bool {
	return g.contains(p) || g.usesTrollRegion() && coldridge(p)
}

func (g coldridgePracticeGoal) positionEvidenceWithin(c *worldv1.CreatureState) bool {
	if !g.usesTrollRegion() {
		return true
	}
	if c == nil || !c.PositionKnown || !g.contains(position{x: c.X, y: c.Y, z: c.Z}) {
		return false
	}
	t := c.Trajectory
	return t == nil || !t.Known || !t.DestinationKnown || g.contains(position{x: t.DestinationX, y: t.DestinationY, z: t.DestinationZ})
}
