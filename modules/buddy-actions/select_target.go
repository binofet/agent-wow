package buddyactions

import (
	"context"
	"encoding/binary"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SelectTarget writes normal client selection, never an attack or tactical
// focus decision. It neither arms a native controller nor edits Lua intent.
func (s *Service) SelectTarget(ctx context.Context, r *actionsv1.SelectTargetRequest) (*actionsv1.SelectTargetResponse, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current session and exact body required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	w, err := s.readWorldSnapshot(ctx, r.TargetGuid)
	self := w.GetSelf()
	if err != nil || self.GetGuid() != s.selfGUID || !self.GetPresent() ||
		r.TargetGuid != 0 && (!self.GetMapKnown() || !observedSelectionTarget(w, r.TargetGuid, self.MapId)) {
		return nil, status.Error(codes.FailedPrecondition, "current body and observed same-map unit or corpse required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	out := &actionsv1.SelectTargetResponse{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: r.TargetGuid,
		Phase: "transport_unconfirmed", ObservedTargetKnown: self.TargetKnown, ObservedTargetGuid: self.TargetGuid}
	_, err = s.session.SendPacket(ctx, &modv1.SendPacketRequest{
		Opcode: opcode.CMSGSetSelection, Payload: binary.LittleEndian.AppendUint64(nil, r.TargetGuid),
	})
	if err != nil {
		// Do not turn a failed UI-selection write into a gameplay intent change.
		return out, nil
	}
	s.state.TransportWrites++
	out.TransportWritten, out.Phase = true, "transport_written"
	// The normal server may not have echoed selection by this read. Keep the
	// transport result separate and never synthesize a World target update.
	if after, err := s.readWorldSnapshot(ctx); err == nil && after.GetSelf().GetGuid() == s.selfGUID && after.GetSelf().GetPresent() {
		current := after.Self
		out.ObservedTargetKnown, out.ObservedTargetGuid = current.TargetKnown, current.TargetGuid
		out.SelectionObserved = current.TargetKnown && current.TargetGuid == r.TargetGuid &&
			(!self.TargetKnown || self.TargetGuid != r.TargetGuid)
		if out.SelectionObserved {
			out.Phase = "selection_observed"
		}
	}
	return out, nil
}

func observedSelectionTarget(w *worldv1.SnapshotResponse, target uint64, mapID uint32) bool {
	for _, p := range append([]*worldv1.PlayerState{w.GetSelf(), w.GetOwner()}, w.GetNearbyPlayers()...) {
		if p.GetGuid() == target && p.GetPresent() && p.GetMapKnown() && p.GetMapId() == mapID {
			return true
		}
	}
	for _, corpse := range append([]*worldv1.CorpseState{w.GetOwnCorpse()}, w.GetPlayerCorpses()...) {
		if corpse.GetGuid() == target && corpse.GetPresent() && corpse.GetMapKnown() && corpse.GetMapId() == mapID {
			return true
		}
	}
	for _, rows := range [][]*worldv1.CreatureState{
		{w.GetRequestedCreature(), w.GetOwnerTarget()}, w.GetNearbyCreatures(), w.GetRequestedCreatures(),
		w.GetQuestGivers(), w.GetTrainerCreatures(), w.GetLootableCorpses(), w.GetSkinnableCorpses(),
	} {
		for _, creature := range rows {
			if creature.GetGuid() == target && creature.GetPresent() && creature.GetMapKnown() && creature.GetMapId() == mapID {
				return true
			}
		}
	}
	return false
}
