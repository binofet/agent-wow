package buddyactions

import (
	"context"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"time"
)

// The ordinary owner/self rule remains available without a party. Defending
// another companion requires current server-observed membership in Joel's group.
func (s *Service) defendsVictimLocked(ctx context.Context, victim uint64) bool {
	if victim == ownerGUID || victim == s.selfGUID {
		return true
	}
	if victim != 1 && victim != 4 && victim != 5 || s.session == nil {
		return false
	}
	call, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	request, _ := anypb.New(&emptypb.Empty{})
	out, err := s.session.InvokeModule(call, &modv1.InvokeModuleRequest{Module: "buddy-party", Method: "state", Request: request})
	if err != nil || out == nil || out.Result == nil {
		return false
	}
	state := &partyv1.StateResponse{}
	if out.Result.UnmarshalTo(state) != nil {
		return false
	}
	group := state.Group
	if group == nil || !group.Known || !group.InGroup || len(group.Members) < 2 || len(group.Members) > 40 {
		return false
	}
	seen := map[uint64]bool{}
	own, owner, defended := false, false, false
	for _, m := range group.Members {
		if m == nil || m.Guid == 0 || seen[m.Guid] {
			return false
		}
		seen[m.Guid] = true
		if m.Guid == s.selfGUID && m.Self && m.Online {
			own = true
		}
		if m.Guid == ownerGUID && m.Name == "Joel" && m.Online {
			owner = true
		}
		if m.Guid == victim && m.Online {
			defended = true
		}
	}
	return own && owner && defended
}
