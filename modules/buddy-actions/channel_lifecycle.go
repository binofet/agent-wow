package buddyactions

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"math"
	"time"
)

// Every operation is identified within the authenticated body, independently
// of its self/unit/object/ground target and the byte-sized wire cast counter.
func (s *Service) initializeSpellLifecycleLocked(op *supportAttempt, channel bool, visit uint64) error {
	if s.spellOperationEpoch == 0 {
		entropy := s.spellOperationEntropy
		if entropy == nil {
			entropy = rand.Reader
		}
		var epoch [8]byte
		if _, err := io.ReadFull(entropy, epoch[:]); err != nil {
			return status.Error(codes.Unavailable, "spell operation identity entropy unavailable")
		}
		s.spellOperationEpoch = binary.LittleEndian.Uint64(epoch[:])
		if s.spellOperationEpoch == 0 {
			return status.Error(codes.Unavailable, "nonzero spell operation identity required")
		}
	}
	// Random instance identity prevents a same-SID module recreation from
	// restarting the exact cancellation revision at one. Never wrap/reuse it.
	if s.spellOperationSequence == math.MaxUint64 || s.spellOperationEpoch > math.MaxUint64-(s.spellOperationSequence+1) {
		return status.Error(codes.ResourceExhausted, "spell operation revision exhausted")
	}
	s.spellOperationSequence++
	op.view.OperationRevision = s.spellOperationEpoch + s.spellOperationSequence
	op.view.VisitId = visit
	op.selfChannel = channel
	return nil
}

// The legacy helper name remains internal compatibility. Channels belong to
// the caster; target shape does not select a different lifecycle handler.
func (s *Service) pendingSpellLifecycleLocked() *supportAttempt {
	op := s.supportOp
	if op != nil && s.state.CastPending && s.pendingSpell == op.view.SpellId &&
		s.pendingTarget == op.view.TargetGuid && s.castCount == op.count {
		return op
	}
	return nil
}

func (s *Service) pendingSelfChannelLocked() *supportAttempt {
	if op := s.pendingSpellLifecycleLocked(); op != nil && op.selfChannel {
		return op
	}
	return nil
}

// An actual world transfer retires this visit's cast custody without making
// a claim about a spell effect or an observed channel end in the old world.
func (s *Service) finishSpellLifecycleLocked(phase string) {
	if op := s.pendingSpellLifecycleLocked(); op != nil && (op.genericSpell || op.view.Capability == selfCastCapability) {
		op.view.Phase = phase
		s.pendingSpell, s.pendingTarget, s.state.CastPending = 0, 0, false
	}
}

func (s *Service) finishSelfChannelLocked(phase string) {
	if op := s.pendingSelfChannelLocked(); op != nil {
		op.view.Phase = phase
		s.pendingSpell, s.pendingTarget, s.state.CastPending = 0, 0, false
	}
}

func (s *Service) expireSelfChannelLocked() {
	if op := s.pendingSpellLifecycleLocked(); op != nil && (op.selfChannel || op.genericSpell && op.genericChannelUnknown) && !s.now().Before(op.until) {
		op.view.Phase = "outcome_unconfirmed_no_retry"
		s.pendingSpell, s.pendingTarget, s.state.CastPending = 0, 0, false
	}
}

func (s *Service) observeSelfChannelLocked(code uint32, p []byte) error {
	s.expireSelfChannelLocked()
	op := s.pendingSpellLifecycleLocked()
	if op == nil {
		return nil
	}
	actor, n, err := readGUID(p)
	if err != nil {
		return err
	}
	if actor != s.selfGUID {
		return nil
	}
	if code == opcode.MSGChannelStart {
		if len(p) != n+8 {
			return errors.New("malformed channel start")
		}
		if binary.LittleEndian.Uint32(p[n:]) != op.view.SpellId || op.view.ChannelStarted {
			return nil
		}
		n += 4
		// A matching server START is authoritative even when this learned spell
		// had no catalog channel metadata when its generic request was admitted.
		op.selfChannel = true
		op.view.ChannelStarted, op.view.Phase = true, "channeling"
	} else if len(p) != n+4 {
		return errors.New("malformed channel update")
	} else if !op.selfChannel || !op.view.ChannelStarted || op.view.ChannelEnded {
		return nil
	}
	op.view.ChannelRemainingMs = binary.LittleEndian.Uint32(p[n:])
	if op.view.ChannelRemainingMs == 0 {
		op.view.ChannelEnded = true
		// An actual end retires channel custody even if GO was lost. It never
		// confirms damage/healing or changes the recorded launch observation.
		s.finishSelfChannelLocked("channel_ended_effect_unverified")
		return nil
	}
	// Channel duration is provided by the server after spell modifiers.
	op.until = s.now().Add(time.Duration(op.view.ChannelRemainingMs)*time.Millisecond + 3*time.Second)
	s.castUntil = op.until
	return nil
}
