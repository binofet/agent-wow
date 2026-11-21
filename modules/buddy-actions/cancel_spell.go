package buddyactions

import (
	"context"
	"encoding/binary"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CancelSpell writes one normal cancellation for the explicitly selected
// current operation. Acknowledged transport is not an observed channel end.
func (s *Service) CancelSpell(ctx context.Context, r *actionsv1.CancelSpellRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetSpellId() == 0 || r.GetExpectedOperationRevision() == 0 || r.GetVisitId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body, exact spell operation revision and visit required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	token, supplied := modcb.IncomingPolicySpell(ctx)
	if supplied && token == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid cancellation token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	op := s.supportOp
	if op == nil || !s.state.CastPending || s.pendingSpell != op.view.SpellId || s.pendingTarget != op.view.TargetGuid || s.castCount != op.count ||
		op.view.SpellId != r.SpellId || op.view.OperationRevision != r.ExpectedOperationRevision || op.view.VisitId != r.VisitId {
		return nil, status.Error(codes.FailedPrecondition, "exact current pending operation required")
	}
	w, err := s.snapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId || supplied && !s.admitsPolicyEncounterLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "current operation visit and policy authority required")
	}
	if raw := w.GetSelf(); raw.GetChannelSpellKnown() && raw.GetChannelSpellId() != 0 && raw.GetChannelSpellId() != r.SpellId {
		return nil, status.Error(codes.FailedPrecondition, "a different current channel is observed")
	}
	if r.Channel {
		raw := w.GetSelf()
		active := op.view.ChannelStarted && !op.view.ChannelEnded || raw.GetChannelSpellKnown() && raw.GetChannelSpellId() == r.SpellId
		if !op.selfChannel || !active {
			return nil, status.Error(codes.FailedPrecondition, "observed current channel required")
		}
		catalog := s.spellData
		if catalog == nil {
			catalog = spellcatalog.Default()
		}
		if spell, known := catalog.Lookup(r.SpellId); known && spell.Attrs[0]&0x80000000 != 0 {
			return nil, status.Error(codes.FailedPrecondition, "current channel is marked uncancellable by client metadata")
		}
	} else if op.selfChannel && op.view.ChannelStarted && !op.view.ChannelEnded {
		return nil, status.Error(codes.FailedPrecondition, "active channel requires channel cancellation")
	}
	if op.view.CancelAttempted {
		return s.stateLocked(), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	wire := uint32(opcode.CMSGCancelCast)
	payload := []byte{s.cancelCastCount + 1}
	if r.Channel {
		wire = opcode.CMSGCancelChannelling
		payload = nil
	} else {
		s.cancelCastCount++
	}
	payload = binary.LittleEndian.AppendUint32(payload, r.SpellId)
	op.view.CancelAttempted = true
	// Use this cancellation invocation's token, never replace the original
	// cast's effect token or pending ledger binding.
	err = s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), wire, payload)
	if modcb.PacketWasNotDispatched(err) {
		op.view.CancelAttempted = false
		return nil, err
	}
	if err != nil {
		op.view.CancelOutcomeUnknown = true
		return s.stateLocked(), err
	}
	op.view.CancelTransportWritten = true
	return s.stateLocked(), nil
}
