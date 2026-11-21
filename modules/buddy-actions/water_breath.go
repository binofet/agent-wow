package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func waterBreathPlayer(p *worldv1.PlayerState, mapID uint32) bool {
	if p.GetMovementFlags()&swimMovementFlag != 0 {
		if !swimFollowCompatible(p.GetMovementFlags(), p.GetExtraMovementFlags()) ||
			p.GetMovementFlags()&(swimAscendingFlag|swimDescendingFlag) != 0 && p.GetPositionAgeMs() > 2000 {
			return false
		}
		p = proto.Clone(p).(*worldv1.PlayerState)
		p.MovementFlags &^= swimMovementFlag | swimAscendingFlag | swimDescendingFlag
	}
	return safeHealingPlayer(p, mapID) && p.HealthKnown && p.Health > 0
}
