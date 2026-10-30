package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func (g coldridgePracticeGoal) contains(p position) bool {
	switch g.questID {
	case 179, 170, 183:
		return coldridge(p)
	case 182:
		return finite(p) && buddynav.RegionColdridgeTrollCave.Contains(buddynav.Point{X: p.x, Y: p.y, Z: p.z})
	default:
		return false
	}
}

// The raw own baseline can remain at its last admitted valley observation
// while ordinary travel advances the client estimate. It grants no route or
// target authority outside the quest's separate combat region.
func (g coldridgePracticeGoal) baselineContains(p position) bool {
	return g.contains(p) || g.questID == 182 && coldridge(p)
}

func (g coldridgePracticeGoal) positionEvidenceWithin(c *worldv1.CreatureState) bool {
	if g.questID != 182 {
		return true
	}
	if c == nil || !c.PositionKnown || !g.contains(position{x: c.X, y: c.Y, z: c.Z}) {
		return false
	}
	t := c.Trajectory
	return t == nil || !t.Known || !t.DestinationKnown || g.contains(position{x: t.DestinationX, y: t.DestinationY, z: t.DestinationZ})
}
