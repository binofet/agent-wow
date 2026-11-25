package buddyactions

import (
	"context"
	"encoding/binary"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

// One explicit death/query-bound normal entrance notification. Living exploration
// admission is unchanged. A written/unknown trigger is never resent in this death.
func (s *Service) enterCorpseInstanceLocked(ctx context.Context, q *actionsv1.CorpseApproachRequest, w *worldv1.SnapshotResponse, volume areatriggerreference.Trigger) (*actionsv1.State, error) {
	r := &s.recovery
	if r.state.CorpseEntryAttempted {
		return s.stateLocked(), nil
	}
	if r.state.ReclaimAttempted || s.resurrectionOffer != nil && s.resurrectionOffer.consumed || s.areaTrigger.operation.GetPending() || s.areaTrigger.operation.GetTransportUnknown() || s.followPortal.notified || !s.recoveryOwnsLocked() || !volume.Contains(w.Self.MapId, float64(s.pos.x), float64(s.pos.y), float64(s.pos.z), .03) {
		return nil, status.Error(codes.FailedPrecondition, "current owned ghost inside exact queried dungeon entrance required")
	}
	p := s.areaTriggerProposalLocked(w)
	epoch, query := r.state.DeathEpoch, r.state.CorpseQueryRevision
	current := areaTriggerAdmission(func(fresh *worldv1.SnapshotResponse, revision uint64, now time.Time) bool {
		return s.areaTriggerPhysicalPoseCurrentLocked(ctx, p, fresh, revision, now, ghostSelf(fresh, s.selfGUID)) &&
			s.recoveryOwnsLocked() && r.state.DeathEpoch == epoch && r.state.CorpseQueryRevision == query &&
			r.state.GhostMovementSupported && r.state.CorpseFound && r.state.CorpseLocation.GetMapKnown() &&
			r.state.CorpseLocation.MapId == volume.SourceMap && r.state.CorpseMapId == volume.DestinationMap &&
			!r.state.ReclaimAttempted && (s.resurrectionOffer == nil || !s.resurrectionOffer.consumed) &&
			!s.areaTrigger.operation.GetPending() && !s.areaTrigger.operation.GetTransportUnknown() && !s.followPortal.notified &&
			volume.Contains(p.mapID, float64(p.pose.x), float64(p.pose.y), float64(p.pose.z), .03)
	})
	r.state.CorpseEntryAttempted = true
	report, err := s.prepareAreaTriggerLocked(ctx, w, p, current, nil)
	if err != nil {
		// prepareAreaTrigger never sends AREA_TRIGGER; retain any STOP uncertainty.
		r.state.CorpseEntryAttempted = false
		return s.stateLocked(), status.Error(codes.FailedPrecondition, "ghost entrance not sent: "+err.Error())
	}
	r.entryOperation = &actionsv1.AreaTriggerOperation{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: p.visit, TriggerId: q.TriggerId, DestinationMapId: volume.DestinationMap, Pending: true, SourceScope: clonePortalScope(report.TransportScope)}
	r.path = nil
	r.state.Phase = "corpse_entry_requested"
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, q.TriggerId), report.TransportScope)
	if modcb.PacketWasNotDispatched(err) {
		r.state.CorpseEntryAttempted = false
		r.entryOperation = nil
		r.state.Phase = "corpse_entry_not_dispatched"
		return s.stateLocked(), err
	}
	r.state.CorpseEntryWritten = err == nil
	r.state.CorpseEntryUnknown = err != nil
	r.entryOperation.TransportWritten = err == nil
	r.entryOperation.TransportUnknown = err != nil
	r.state.Reason = "waiting_for_actual_life_or_world_entry_no_replay"
	return s.stateLocked(), err
}
