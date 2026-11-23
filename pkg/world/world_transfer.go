package world

import (
	"context"
	"encoding/binary"
	"math"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// A transfer edge records adjacent observations on this connection. It does not
// identify a taxi booking, prove server acceptance of an ACK, or grant movement.
type worldTransferState struct {
	revision  uint64
	exhausted bool
	waiting   bool
	eligible  bool
	entry     *modv1.TransportScope
}

// Retain only the source tuple, never an older edge nested inside a new edge.
func transferSource(scope *modv1.TransportScope) *modv1.TransportScope {
	if scope == nil {
		return nil
	}
	out := cloneTransportScope(scope)
	out.WorldTransfer = nil
	return out
}

func sameTransferVisit(a, b *modv1.TransportScope) bool {
	return a != nil && b != nil && a.ConnectionId != "" && a.ConnectionId == b.ConnectionId &&
		a.SelfGuid != 0 && a.SelfGuid == b.SelfGuid && a.VisitBound && b.VisitBound &&
		a.VisitRevision != 0 && a.VisitRevision == b.VisitRevision && a.MapId == b.MapId
}

func sameTransferSource(a, b *modv1.TransportScope) bool {
	return sameTransferVisit(a, b) && a.SplineActive && b.SplineActive && a.SplineIdKnown && b.SplineIdKnown &&
		a.SplineId == b.SplineId && a.SplineRevision != 0 && a.SplineRevision == b.SplineRevision &&
		a.SplineSourceRevision != 0 && a.SplineSourceRevision == b.SplineSourceRevision &&
		a.SplineSourceOpcode == b.SplineSourceOpcode && a.MotionRevision <= b.MotionRevision
}

func (t *transportScopeState) reviseTransfer() bool {
	f := &t.transfer
	if f.exhausted || f.revision == math.MaxUint64 {
		f.exhausted, f.eligible = true, false
		if t.scope.WorldTransfer != nil {
			t.scope.WorldTransfer.Revision = f.revision
			t.scope.WorldTransfer.Known = false
			t.scope.WorldTransfer.Phase = "incomplete"
			t.scope.WorldTransfer.Reason = "transfer_revision_exhausted"
		}
		return false
	}
	f.revision++
	t.scope.WorldTransfer.Revision = f.revision
	return true
}

func (t *transportScopeState) supersedePendingTransfer(reason string) {
	if t.scope == nil || !t.transfer.waiting || !t.transfer.eligible {
		return
	}
	t.transfer.eligible = false
	e := t.scope.WorldTransfer
	e.Known, e.Phase, e.Reason = false, "incomplete", reason
	t.reviseTransfer()
}

func (t *transportScopeState) observeTransferBefore(op uint16, body []byte) {
	if t.scope == nil {
		return
	}
	if op == opcode.SMSGTransferPending {
		if t.transfer.waiting {
			t.supersedePendingTransfer("overlapping_transfer_pending")
			return
		}
		from := transferSource(t.scope)
		e := &modv1.ObservedWorldTransfer{
			Phase: "pending", ConnectionId: from.ConnectionId, SelfGuid: from.SelfGuid,
			FromVisitRevision: from.VisitRevision, FromMapId: from.MapId, FromMotionRevision: from.MotionRevision,
			FromSplineIdKnown: from.SplineIdKnown, FromSplineId: from.SplineId, FromSplineRevision: from.SplineRevision,
			FromSplineSourceRevision: from.SplineSourceRevision, FromSplineSourceOpcode: from.SplineSourceOpcode,
			FromSplineActive: from.SplineActive,
		}
		t.scope.WorldTransfer = e
		t.transfer.waiting, t.transfer.eligible, t.transfer.entry = true, true, nil
		if len(body) == 4 {
			// Held AzerothCore b8c2e189 Player.cpp TeleportTo emits map:u32,
			// optionally followed by transport-entry:u32 and old-map:u32.
			// That 12-byte variant has transport-relative NEW_WORLD coordinates;
			// this first fact contract deliberately certifies only the plain form.
			e.TargetMapKnown, e.TargetMapId = true, binary.LittleEndian.Uint32(body)
		} else {
			e.Phase, e.Reason, t.transfer.eligible = "incomplete", "unsupported_transfer_pending_payload", false
		}
		if !from.VisitBound || from.VisitRevision == 0 || from.ConnectionId == "" || from.SelfGuid == 0 || t.exhausted {
			e.Phase, e.Reason, t.transfer.eligible = "incomplete", "transfer_source_unbound", false
		}
		if sameTransferSource(t.completionReport, from) {
			e.CompletionReportWritten, e.CompletionReportMotionRevision = true, t.completionReport.MotionRevision
		}
		t.reviseTransfer()
		return
	}
	if op == opcode.SMSGLoginVerifyWorld {
		t.supersedePendingTransfer("login_replaced_transfer")
		if t.scope.WorldTransfer != nil && !t.transfer.waiting {
			e := t.scope.WorldTransfer
			e.Known, e.Phase, e.Reason = false, "incomplete", "login_replaced_transfer"
			t.reviseTransfer()
		}
		t.transfer.waiting = false
		return
	}
	if op == opcode.SMSGTransferAborted {
		t.supersedePendingTransfer("transfer_aborted")
		return
	}
	if op != opcode.SMSGNewWorld && transportMotionBarrier(uint32(op)) && !transportClearlyForeign(op, body, t.scope.SelfGuid) {
		t.supersedePendingTransfer("intervening_motion_superseded_transfer")
	}
}

func (t *transportScopeState) observeTransferAfter(op uint16, body []byte) {
	if op == opcode.SMSGTransferPending && t.scope.WorldTransfer != nil {
		if t.scope.WorldTransfer.PendingMotionRevision == 0 {
			t.scope.WorldTransfer.PendingMotionRevision = t.scope.MotionRevision
		}
		return
	}
	if op != opcode.SMSGNewWorld {
		return
	}
	e := t.scope.WorldTransfer
	if !t.transfer.waiting || e == nil {
		e = &modv1.ObservedWorldTransfer{Phase: "incomplete", ConnectionId: t.scope.ConnectionId, SelfGuid: t.scope.SelfGuid,
			Reason: "new_world_without_transfer_pending"}
		t.scope.WorldTransfer = e
		t.transfer.eligible = false
	}
	e.ToVisitRevision, e.ToMapId, e.EntryMotionRevision = t.scope.VisitRevision, t.scope.MapId, t.scope.MotionRevision
	mapID, valid := scopeWorldEntry(body)
	if !valid || !t.scope.VisitBound || t.scope.VisitRevision == 0 {
		t.transfer.eligible, e.Phase, e.Reason = false, "incomplete", "invalid_new_world"
	} else if t.transfer.eligible && (t.scope.ConnectionId != e.ConnectionId || t.scope.SelfGuid != e.SelfGuid ||
		t.scope.VisitRevision <= e.FromVisitRevision || !e.TargetMapKnown || mapID != e.TargetMapId) {
		t.transfer.eligible, e.Phase, e.Reason = false, "incomplete", "new_world_does_not_match_pending"
	}
	if t.transfer.eligible {
		e.Phase = "entered"
	}
	t.transfer.entry = transferSource(t.scope)
	t.reviseTransfer()
}

func (t *transportScopeState) worldportAckUnavailable() {
	if t.scope == nil || t.scope.WorldTransfer == nil {
		return
	}
	e := t.scope.WorldTransfer
	e.Known, e.Phase = false, "incomplete"
	if e.Reason == "" {
		e.Reason = "worldport_ack_unavailable"
	}
	t.transfer.eligible, t.transfer.waiting = false, false
	t.reviseTransfer()
}

func (t *transportScopeState) worldportAckOutcome(outcome GameplayWriteOutcome, self uint64) {
	if t.scope == nil || t.scope.WorldTransfer == nil {
		return
	}
	e, entry := t.scope.WorldTransfer, t.transfer.entry
	e.WorldportAckWritten, e.WorldportAckUnknown = outcome == GameplayWritten, outcome == GameplayWriteFailed
	if outcome == GameplayWritten {
		e.WorldportAckMotionRevision = t.scope.MotionRevision
		current := t.scope
		if t.transfer.eligible && entry != nil && self == e.SelfGuid && sameTransferVisit(entry, current) &&
			entry.MotionRevision != math.MaxUint64 && current.MotionRevision == entry.MotionRevision+1 &&
			!current.SplineIdKnown && !current.SplineActive && current.SplineRevision == entry.SplineRevision {
			e.Known, e.Phase, e.Reason = true, "ack_written", ""
		} else if t.transfer.eligible {
			e.Known, e.Phase, e.Reason = false, "incomplete", "worldport_ack_scope_superseded"
		}
	} else {
		e.Known, e.Phase = false, "incomplete"
		if outcome == GameplayWriteFailed {
			e.Phase, e.Reason = "ack_unknown", "worldport_ack_transport_unknown"
		} else {
			e.Reason = "worldport_ack_not_written"
		}
	}
	t.transfer.eligible, t.transfer.waiting = false, false
	t.reviseTransfer()
}

func (e *engine) writeWorldportAcknowledgement(op uint32, body []byte) error {
	outcome, _, err := e.writeContext(context.Background(), op, body)
	e.transport.worldportAckOutcome(outcome, e.s.state.Character.GUID)
	e.publishTransportScope()
	return err
}
