package buddyactions

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const meleeReceiptLimit = 3 * time.Second

type meleeAttempt struct {
	view  actionsv1.MeleeOperation
	token string
	until time.Time
}

// A policy swing outlives the receipt for its START. Until the matching STOP
// or a verified world entry, the native controller may neither adopt it nor
// start another attack, even if either transport outcome was ambiguous.
type encounterMeleeSwing struct {
	target, visit    uint64
	stopIssued       bool
	controlStopUntil time.Time
}

func (s *Service) stopEncounterMeleeLocked(ctx context.Context) {
	swing := &s.encounterSwing
	if swing.target == 0 || swing.stopIssued || !s.encounterVisit.bound || swing.visit != s.encounterVisit.revision || ctx.Err() != nil {
		return
	}
	swing.stopIssued = true
	s.endMeleeOperationLocked()
	s.autoStopping = true
	s.state.AutoAttackRequested = false
	s.state.AutoAttackUnconfirmed = true
	s.state.Reason = "encounter_melee_stop_waiting_for_server"
	if !s.sendLocked(ctx, opcode.CMSGAttackstop, nil) {
		s.state.Reason = "encounter_melee_stop_unconfirmed"
	}
}

func (s *Service) reserveMeleeOperationLocked(token string, target uint64, enabled bool) *meleeAttempt {
	op := &meleeAttempt{
		view: actionsv1.MeleeOperation{
			SelfGuid: s.selfGUID, TargetGuid: target, Enabled: enabled,
			Phase: "waiting_server", VisitId: s.encounterVisit.revision,
		},
		token: token, until: s.now().Add(meleeReceiptLimit),
	}
	s.meleeOp = op
	return op
}

func (s *Service) pendingMeleeOperationLocked() *meleeAttempt {
	op := s.meleeOp
	if op != nil && (op.view.Phase == "waiting_server" || op.view.Phase == "transport_unconfirmed_no_retry") {
		return op
	}
	return nil
}

func (s *Service) endMeleeOperationLocked() {
	if op := s.pendingMeleeOperationLocked(); op != nil {
		op.view.Phase = "outcome_unconfirmed_no_retry"
	}
}

func (s *Service) expireMeleeOperationLocked() {
	if op := s.pendingMeleeOperationLocked(); op != nil && !s.now().Before(op.until) {
		s.endMeleeOperationLocked()
	}
}

func (s *Service) meleeWorldBoundaryLocked(code uint32, p []byte) {
	switch code {
	case opcode.SMSGTransferPending:
		if len(p) == 4 || len(p) == 12 {
			s.endMeleeOperationLocked()
			s.meleeAcknowledgedVisit = 0
		}
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld:
		if validEncounterWorldEntry(p) {
			s.endMeleeOperationLocked()
			s.meleeAcknowledgedVisit = 0
			s.clearPreviousVisitMeleeLocked()
		}
	}
}

func (s *Service) clearPreviousVisitMeleeLocked() {
	op := s.meleeOp
	if s.encounterSwing.target != 0 && s.encounterSwing.visit == s.encounterVisit.revision {
		s.encounterSwing = encounterMeleeSwing{}
	} else if op == nil || op.view.VisitId != s.encounterVisit.revision || s.autoTarget != op.view.TargetGuid ||
		(s.state.AutoAttackTargetGuid != 0 && s.state.AutoAttackTargetGuid != op.view.TargetGuid) {
		return
	}
	// A validated new entry retires only the old visit's typed attack state.
	// Its historical receipt remains unverified unless previously confirmed.
	s.clearAutoAttackLocked()
}

func (s *Service) clearAutoAttackLocked() {
	s.meleeAcknowledgedVisit = 0
	s.autoTarget, s.autoStopping = 0, false
	s.state.AutoAttacking = false
	s.state.AutoAttackTargetGuid = 0
	s.state.AutoAttackRequested = false
	s.state.AutoAttackUnconfirmed = false
}

// Fear and confusion clear the server's melee bit with a victimless STOP,
// then take control of the mover. Neither packet acknowledges a typed write.
func (s *Service) settleControlMeleeStopLocked() {
	swing := s.encounterSwing
	if s.closed || !s.now().Before(swing.controlStopUntil) ||
		swing.target != s.autoTarget || swing.visit != s.encounterVisit.revision ||
		swing.stopIssued || s.autoStopping || s.state.AutoAttackUnconfirmed || !s.acknowledgedSwingActiveLocked() {
		return
	}
	s.encounterSwing = encounterMeleeSwing{}
	s.clearAutoAttackLocked()
}

// START and STOP carry no request ID. Bind the server observation to the one
// outstanding operation, its visit, direction and complete GUID pair.
// The caller has already checked the complete frame and the attacker GUID.
func (s *Service) acceptOwnMeleeAttackLocked(target uint64, enabled bool) bool {
	if enabled {
		s.encounterSwing.controlStopUntil = time.Time{}
	}
	swing := s.encounterSwing
	if swing.target != 0 {
		if target != swing.target || !s.encounterVisit.bound || swing.visit != s.encounterVisit.revision || enabled && swing.stopIssued {
			return false
		}
		if op := s.pendingMeleeOperationLocked(); !enabled && op != nil && op.view.Enabled {
			// A STOP proves the swing is no longer active but cannot prove an
			// earlier START ever landed.
			s.endMeleeOperationLocked()
		}
	}
	op := s.pendingMeleeOperationLocked()
	if op != nil {
		if target == 0 || target != op.view.TargetGuid || enabled != op.view.Enabled {
			return false
		}
		if s.closed || s.selfGUID == 0 || s.selfGUID != op.view.SelfGuid || !s.encounterVisit.bound ||
			op.view.VisitId == 0 || s.encounterVisit.revision != op.view.VisitId {
			s.endMeleeOperationLocked()
			return false
		}
		if enabled {
			op.view.Phase = "confirmed_attack_start"
		} else {
			op.view.Phase = "confirmed_attack_stop"
		}
	}
	if !enabled && swing.target != 0 {
		s.encounterSwing = encounterMeleeSwing{}
	}
	s.meleeAcknowledgedVisit = 0
	if enabled && !s.closed && s.encounterVisit.bound {
		s.meleeAcknowledgedVisit = s.encounterVisit.revision
	}
	return true
}

func (op *meleeAttempt) reportPolicyMeleeEffect(ctx context.Context) {
	switch op.view.Phase {
	case "confirmed_attack_start", "confirmed_attack_stop", "outcome_unconfirmed_no_retry":
		if modcb.ReportPolicyMeleeEffect(ctx, op.token, &op.view) {
			op.token = ""
		}
	}
}
