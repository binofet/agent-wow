package buddyactions

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// This callback publishes only coordinates from an already written normal
// movement packet. It is discovery provenance, never a server pose correction.
// Callback failure cannot undo transport success or prevent a subsequent STOP.
func (s *Service) publishClientMovementLocked(ctx context.Context, w *worldv1.SnapshotResponse, p position, valid bool) {
	s.movementSequence++
	r := &worldv1.ClientMovementRequest{SelfGuid: s.selfGUID, Sequence: s.movementSequence}
	if valid && w != nil && w.Self != nil {
		r.Valid, r.MapId, r.ServerPositionRevision = true, w.Self.MapId, w.Self.PositionRevision
		r.X, r.Y, r.Z, r.Orientation = p.x, p.y, p.z, p.o
	}
	s.state.ClientMovementObservationConfirmed = false
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
	defer cancel()
	request, err := anypb.New(r)
	if err == nil && s.session != nil {
		var out *modv1.InvokeModuleResponse
		out, err = s.session.InvokeModule(call, &modv1.InvokeModuleRequest{Module: "buddy-world", Method: "observe_client_movement", Request: request})
		if err == nil && out != nil && out.Result != nil && out.Result.MessageIs(&emptypb.Empty{}) {
			s.state.ClientMovementObservationConfirmed = true
			return
		}
	}
	s.state.ClientMovementObservationFailures++
}
