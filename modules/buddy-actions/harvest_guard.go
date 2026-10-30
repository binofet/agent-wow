package buddyactions

import (
	"context"
	"sort"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/harvestguard"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *Service) harvestPartyLocked(ctx context.Context) ([]uint64, string) {
	if s.session == nil {
		return nil, "harvest_current_party_unavailable"
	}
	call, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	request, _ := anypb.New(&emptypb.Empty{})
	out, err := s.session.InvokeModule(call, &modv1.InvokeModuleRequest{Module: "buddy-party", Method: "state", Request: request})
	state := &partyv1.StateResponse{}
	if err != nil || out == nil || out.Result == nil || out.Result.UnmarshalTo(state) != nil || len(state.SessionId) == 0 || len(state.SessionId) > 128 || state.Group == nil || !state.Group.Known {
		return nil, "harvest_current_party_unavailable"
	}
	g := state.Group
	protected := map[uint64]bool{s.selfGUID: true, ownerGUID: true}
	if !g.InGroup {
		if len(g.Members) != 0 {
			return nil, "harvest_current_party_inconsistent"
		}
	} else {
		if len(g.Members) < 2 || len(g.Members) > 40 {
			return nil, "harvest_current_party_inconsistent"
		}
		seen, self := map[uint64]bool{}, false
		for _, m := range g.Members {
			if m == nil || m.Guid == 0 || m.Guid>>48 != 0 || seen[m.Guid] || m.Self != (m.Guid == s.selfGUID) {
				return nil, "harvest_current_party_inconsistent"
			}
			seen[m.Guid] = true
			if m.Guid == s.selfGUID && m.Online {
				self = true
			}
			if m.Online {
				protected[m.Guid] = true
			}
		}
		if !self {
			return nil, "harvest_online_own_party_identity_required"
		}
	}
	guids := make([]uint64, 0, len(protected))
	for guid := range protected {
		guids = append(guids, guid)
	}
	sort.Slice(guids, func(i, j int) bool { return guids[i] < guids[j] })
	return guids, ""
}

// Party proof precedes the final world read. The same actual GUID set is
// returned with the lease for the profession's own subsequent LAST-world gate.
func (s *Service) harvestContextLocked(ctx context.Context) (*worldv1.SnapshotResponse, []uint64, string) {
	call, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	guids, reason := s.harvestPartyLocked(call)
	if reason != "" {
		return nil, nil, reason
	}
	w, err := s.snapshot(call)
	if err != nil {
		return nil, nil, "harvest_actual_world_unavailable"
	}
	if reason := harvestguard.WorldReason(w, s.selfGUID, guids); reason != "" {
		return w, guids, reason
	}
	return w, guids, ""
}

// Revocation only touches this exact guarded interaction. A failed STOP keeps
// its token and MotionUnconfirmed so no replacement lease can hide ambiguity.
// Preserve enabled defense intents even if generic STOP transport fails.
func (s *Service) revokeHarvestLocked(ctx context.Context, reason string) bool {
	if !s.state.InteractionHarvest || s.state.InteractionOwner == "" {
		return true
	}
	stopped := s.stopHarvestMovementLocked(ctx)
	if !stopped {
		s.state.Reason = "harvest_stop_unconfirmed_no_retry"
		return false
	}
	s.state.InteractionOwner = ""
	s.state.InteractionHarvest = false
	s.state.HarvestGuardGuids = nil
	s.interactionToken = ""
	s.lastStep = s.now()
	s.state.Reason = reason
	return true
}

func (s *Service) stopHarvestMovementLocked(ctx context.Context) bool {
	call, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	follow, heal, assist := s.state.Following, s.state.Healing, s.state.Assisting
	stopped := s.stopLocked(call)
	s.state.Following, s.state.Healing, s.state.Assisting = follow, heal, assist
	return stopped
}
