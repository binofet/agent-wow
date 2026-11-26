package behavior

import "errors"

// RejectAction records an unentered proposal for a normal not_dispatched
// receipt. It consumes the one-use decision but grants NO native write
// reservation. New ingress may fence dispatch without making the fact that
// this proposal was never sent unrecordable. Already admitted/UNKNOWN actions,
// other sessions, replacement epochs and newer action proposals stay fenced.
func (s *Session) RejectAction(decision Decision) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrSessionClosed
	}
	if s.swapping || s.fault != nil || decision.owner != s || decision.epoch != s.epoch || decision.serial == 0 || decision.serial != s.serial || decision.serial <= s.consumedSerial {
		return 0, ErrFenced
	}
	if len(s.pending) == maxPendingActions {
		return 0, ErrActionCapacity
	}
	if s.nextAction == ^uint64(0) {
		return 0, errors.New("behavior action sequence exhausted")
	}
	s.nextAction++
	s.admissible = 0
	s.consumedSerial = decision.serial
	s.pending[s.nextAction] = pendingAction{applied: ActionReserved, queued: ActionReserved, rejected: true}
	return s.nextAction, nil
}
