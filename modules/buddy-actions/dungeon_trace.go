package buddyactions

import (
	"context"
	"errors"
	"strconv"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	contract "github.com/agent-wow/agent-wow/pkg/behaviorcontract"
	"github.com/agent-wow/agent-wow/pkg/dungeontrace"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// EnableDungeonTrace binds the recorder to Arlen's own companion session.
func (s *Service) EnableDungeonTrace(directory string, mapID uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.independent || s.selfGUID != 1 || s.closed || s.dungeonTrace != nil {
		return errors.New("dungeon trace requires Arlen's single current companion session")
	}
	trace, err := dungeontrace.New(directory, s.sessionID, mapID)
	if err != nil {
		return err
	}
	s.dungeonTrace = trace
	return nil
}

func (s *Service) FlushDungeonTrace(ctx context.Context) {
	s.mu.Lock()
	trace := s.dungeonTrace
	if trace != nil {
		trace.Close(s.now(), "observer_shutdown")
	}
	s.mu.Unlock()
	if trace != nil {
		select {
		case <-ctx.Done():
		case <-trace.Done():
		}
	}
}

func traceRosterGUID(guid uint64) bool {
	switch guid {
	case 1, ownerGUID, 4, 5, 8:
		return true
	}
	return false
}

func traceContractInScope(in contract.Input) bool {
	allowed := func(value string) bool {
		if value == "" || value == "0" {
			return true
		}
		guid, err := strconv.ParseUint(value, 10, 64)
		return err == nil && (traceRosterGUID(guid) || guid>>48 == 0xf130 || guid>>48 == 0xf150)
	}
	if !allowed(in.Self.Guid) || !allowed(in.Target.Guid) || !allowed(in.DefaultTargetGuid) {
		return false
	}
	for _, target := range in.Targets {
		if !allowed(target.Guid) {
			return false
		}
	}
	return true
}

func (s *Service) observeDungeonWorld(world *worldv1.SnapshotResponse) {
	if s.dungeonTrace == nil {
		return
	}
	s.traceLastWorld = s.now()
	s.traceWorld(world, s.traceLastWorld)
	s.tracePreviousWorld = world
}

// Existing action reads record their snapshots directly. The small fallback
// read keeps instance boundaries observable if action intents are idle.
func (s *Service) sampleDungeonTraceLocked(ctx context.Context) {
	phase := s.dungeonTrace.Status().Phase
	if s.closed || ctx.Err() != nil || (phase != "armed" && phase != "recording") {
		return
	}
	at := s.now()
	if at.Sub(s.traceLastWorld) >= 500*time.Millisecond {
		call, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
		world, _ := s.readWorldSnapshot(call)
		cancel()
		s.observeDungeonWorld(world)
	}
	if s.session == nil || !s.dungeonTrace.Active() || at.Sub(s.traceLastParty) < 5*time.Second {
		return
	}
	s.traceLastParty = at
	call, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
	defer cancel()
	request, _ := anypb.New(&emptypb.Empty{})
	reply, err := s.session.InvokeModule(call, &modv1.InvokeModuleRequest{
		Module: "buddy-party", Method: "state", Request: request,
	})
	state := &partyv1.StateResponse{}
	if err != nil || reply == nil || reply.Result == nil || reply.Result.UnmarshalTo(state) != nil ||
		state.Group == nil || !state.Group.Known {
		s.dungeonTrace.Record("observation_gap", s.now(), map[string]any{"stream": "party", "reason": "group_unobserved"})
		return
	}
	members := make([]uint64, 0, 5)
	for _, member := range state.Group.Members {
		if member != nil && member.Online && traceRosterGUID(member.Guid) {
			members = append(members, member.Guid)
		}
	}
	s.dungeonTrace.Record("party_observed", s.now(), map[string]any{
		"in_group": state.Group.InGroup, "known_online_roster": members,
	})
}
