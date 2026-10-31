package buddyactions

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// These two bounded records describe observed bytes, never a cast decision.
// First rejection stays immutable even if later packets arrive. First own GO
// also survives, so a future explicitly supported login event remains auditable.
func (s *Service) roadCastObserverSnapshotLocked() *actionsv1.RoadCastObserverState {
	now := s.now()
	aged := func(event *actionsv1.RoadCastObservedEvent, at time.Time) *actionsv1.RoadCastObservedEvent {
		if event == nil {
			return nil
		}
		out := proto.Clone(event).(*actionsv1.RoadCastObservedEvent)
		if now.After(at) {
			out.AgeMs = uint64(now.Sub(at).Milliseconds())
		}
		return out
	}
	return &actionsv1.RoadCastObserverState{Known: s.roadCast.known, Busy: s.roadCast.busy, Poisoned: s.roadCast.poisoned,
		InitialSeen: s.roadInitialSeen, SpellbookKnown: s.roadSpellbookKnown, EpochKnown: s.roadEpochKnown,
		OutgoingSeen: s.roadOutgoingSeen, PendingSpellId: s.roadCast.spell, PendingTargetGuid: s.roadCast.target,
		Login_836Accepted: s.roadLogin836Accepted, AnyWriteAttempted: s.roadAnyWriteAttempted, OwnStartSeen: s.roadOwnStartSeen, OwnGoSeen: s.roadOwnGoSeen, InitialCount: s.roadInitialCount,
		FirstRejected: aged(s.roadFirstRejected, s.roadFirstRejectedAt), FirstOwnGo: aged(s.roadFirstOwnGo, s.roadFirstOwnGoAt)}
}

func (s *Service) roadCaptureRejectedLocked(op uint32, p []byte, reason string) {
	if s.roadFirstRejected == nil {
		s.roadObserverRevision++
		s.roadFirstRejected = roadObservedCastEvent(op, p, reason, s.roadObserverRevision)
		s.roadFirstRejectedAt = s.now()
	}
}

func (s *Service) roadCaptureOwnGoLocked(p []byte) {
	if s.roadFirstOwnGo != nil {
		return
	}
	e := roadObservedCastEvent(opcode.SMSGSpellGo, p, "own_go_observed", s.roadObserverRevision+1)
	if e.CasterKnown && e.CasterGuid == s.selfGUID {
		s.roadObserverRevision++
		s.roadFirstOwnGo, s.roadFirstOwnGoAt = e, s.now()
	}
}

func roadObservedCastEvent(op uint32, p []byte, reason string, revision uint64) *actionsv1.RoadCastObservedEvent {
	hash := sha256.Sum256(p)
	e := &actionsv1.RoadCastObservedEvent{Opcode: op, PayloadSize: uint32(len(p)), Payload: append([]byte(nil), p[:min(len(p), 256)]...), PayloadTruncated: len(p) > 256, PayloadSha256: hex.EncodeToString(hash[:]), Reason: reason, Revision: revision, GoEvent: op == opcode.SMSGSpellGo}
	if op != opcode.SMSGSpellStart && op != opcode.SMSGSpellGo {
		return e
	}
	item, n, err := readGUID(p)
	if err != nil {
		return e
	}
	caster, _, err := readGUID(p[n:])
	if err != nil {
		return e
	}
	e.CasterKnown, e.CastObjectGuid, e.CasterGuid = true, item, caster
	_, count, spell, offset, err := castEvent(p)
	if err != nil {
		return e
	}
	e.HeaderKnown, e.CastCount, e.SpellId = true, uint32(count), spell
	e.CastFlagsKnown, e.CastFlags = true, binary.LittleEndian.Uint32(p[offset:])
	pos := offset + 8
	if e.GoEvent {
		if pos >= len(p) {
			return e
		}
		hits := int(p[pos])
		e.HitCountKnown, e.HitCount = true, uint32(hits)
		pos++
		if hits > (len(p)-pos)/8 {
			return e
		}
		if hits > 0 {
			e.FirstHitKnown, e.FirstHitGuid = true, binary.LittleEndian.Uint64(p[pos:])
		}
		pos += hits * 8
		if pos >= len(p) {
			return e
		}
		misses := int(p[pos])
		e.MissCountKnown, e.MissCount = true, uint32(misses)
		pos++
		for i := 0; i < misses; i++ {
			if len(p)-pos < 9 {
				return e
			}
			reason := p[pos+8]
			if i == 0 {
				e.FirstMissKnown, e.FirstMissGuid, e.FirstMissReason = true, binary.LittleEndian.Uint64(p[pos:]), uint32(reason)
			}
			pos += 9
			if reason == 11 {
				if pos >= len(p) {
					return e
				}
				if i == 0 {
					e.FirstMissReflectKnown, e.FirstMissReflectResult = true, uint32(p[pos])
				}
				pos++
			}
		}
	}
	if len(p)-pos < 4 {
		return e
	}
	e.TargetFlagsKnown, e.TargetFlags = true, binary.LittleEndian.Uint32(p[pos:])
	if e.TargetFlags == 2 {
		target, _, err := readGUID(p[pos+4:])
		if err == nil {
			e.TargetGuidKnown, e.TargetGuid = true, target
			e.BodySupported = target != 0 && validCastTargetBody(p, offset, e.GoEvent, target)
		}
	}
	return e
}
