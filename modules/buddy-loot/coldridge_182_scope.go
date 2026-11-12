package buddyloot

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
)

func coldridge182Point(x, y, z float32) bool {
	return buddynav.RegionColdridgeTrollCave.Contains(buddynav.Point{X: x, Y: y, Z: z})
}

func coldridge182PositionEvidenceWithin(c *worldv1.CreatureState) bool {
	if c == nil || !c.PositionKnown || !coldridge182Point(c.X, c.Y, c.Z) {
		return false
	}
	t := c.Trajectory
	return t == nil || !t.Known || !t.DestinationKnown || coldridge182Point(t.DestinationX, t.DestinationY, t.DestinationZ)
}
