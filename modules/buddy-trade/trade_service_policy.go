package buddytrade

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Preparation has its own native reservation. An accepted read-only policy
// choice is not a packet capability or an ordinary ACCEPT reservation.
type tradeServiceAdmission struct {
	lease      *tradeConsentAdmission
	allocator  byte
	worldFacts map[string]any
	selfXYZ    [3]float32
	spellFacts map[string]any
	required   uint32
	castTime   time.Duration
	reserveAt  time.Time
}

func tradeServiceItemFacts(item tradeServiceItem) map[string]any {
	return map[string]any{"entry": float64(item.entry), "count": float64(item.count),
		"wrapped": item.wrapped, "lockId": float64(item.lockID)}
}

func tradeServiceRemaining(now, until time.Time) (bool, float64) {
	if until.IsZero() {
		return false, 0
	}
	remaining := until.Sub(now).Milliseconds()
	if remaining < 0 {
		remaining = 0
	}
	return true, float64(remaining)
}

// Validate coverage separately from the raw Known flag; never turn unknown
// skills into a negative ability observation. Only the actual633 row is sent.
func tradeServiceSkills(w *worldv1.SnapshotResponse) (map[string]any, *worldv1.SkillState) {
	complete := len(w.GetSelfSkills()) == 128
	var slots [128]bool
	ids := map[uint32]bool{}
	var selected *worldv1.SkillState
	for _, row := range w.GetSelfSkills() {
		if row == nil || row.Slot >= 128 || slots[row.Slot] || row.SkillId > 65535 || row.Value > row.MaxValue || row.MaxValue > 65535 ||
			row.SkillId == 0 && (row.Step != 0 || row.Value != 0 || row.MaxValue != 0 || row.TemporaryBonus != 0 || row.PermanentBonus != 0) ||
			row.SkillId != 0 && ids[row.SkillId] {
			complete = false
			continue
		}
		slots[row.Slot] = true
		if row.SkillId != 0 {
			ids[row.SkillId] = true
		}
		if row.SkillId == lockpickingSkill {
			selected = row
		}
	}
	rows := []any{}
	if complete && selected != nil {
		rows = append(rows, map[string]any{"slot": float64(selected.Slot), "skillId": float64(selected.SkillId),
			"value": float64(selected.Value), "maxValue": float64(selected.MaxValue),
			"temporaryBonus": float64(selected.TemporaryBonus), "permanentBonus": float64(selected.PermanentBonus)})
	} else {
		selected = nil
	}
	return map[string]any{"known": w.GetSelfSkillsKnown(), "complete": complete,
		"revision": strconv.FormatUint(w.GetSelfSkillsRevision(), 10), "rows": rows}, selected
}

func tradeServiceWorldFacts(w *worldv1.SnapshotResponse) map[string]any {
	p, o := w.GetSelf(), w.GetOwner()
	skills, _ := tradeServiceSkills(w)
	return map[string]any{"visitId": strconv.FormatUint(w.GetVisitRevision(), 10), "mapKnown": p.GetMapKnown(), "mapId": float64(p.GetMapId()),
		"self": map[string]any{"guid": strconv.FormatUint(p.GetGuid(), 10), "present": p.GetPresent(), "aliveKnown": p.GetAliveKnown(), "alive": p.GetAlive(),
			"mapKnown": p.GetMapKnown(), "mapId": float64(p.GetMapId()), "positionKnown": p.GetPositionKnown(), "positionRevision": strconv.FormatUint(p.GetPositionRevision(), 10),
			"classKnown": p.GetClassKnown(), "classId": float64(p.GetClassId()), "unitFlagsKnown": p.GetUnitFlagsKnown(), "unitFlags": float64(p.GetUnitFlags()),
			"movementFlagsKnown": p.GetMovementFlagsKnown(), "movementFlags": float64(p.GetMovementFlags())},
		"owner": map[string]any{"guid": strconv.FormatUint(o.GetGuid(), 10), "present": o.GetPresent(), "aliveKnown": o.GetAliveKnown(), "alive": o.GetAlive(),
			"mapKnown": o.GetMapKnown(), "mapId": float64(o.GetMapId()), "positionKnown": o.GetPositionKnown(), "positionRevision": strconv.FormatUint(o.GetPositionRevision(), 10),
			"unitFlagsKnown": o.GetUnitFlagsKnown(), "unitFlags": float64(o.GetUnitFlags()), "x": float64(o.GetX()), "y": float64(o.GetY()), "z": float64(o.GetZ())},
		"skills": skills}
}

func tradeServiceSpellFacts(toolkit *actionsv1.SpellToolkitState) (map[string]any, *actionsv1.KnownSpell) {
	var spell *actionsv1.KnownSpell
	for _, row := range toolkit.GetSpells() {
		if row.GetSpellId() == pickLockSpell {
			if spell != nil { // A contradictory duplicate does not establish a row.
				spell = nil
				break
			}
			spell = row
		}
	}
	return map[string]any{"sessionId": toolkit.GetSessionId(), "selfGuid": strconv.FormatUint(toolkit.GetSelfGuid(), 10),
		"spellbookKnown": toolkit.GetSpellbookKnown(), "spellbookRevision": strconv.FormatUint(toolkit.GetSpellbookRevision(), 10),
		"present": spell != nil, "spellId": float64(spell.GetSpellId()), "metadataKnown": spell.GetMetadataKnown(), "castTimeMs": float64(spell.GetCastTimeMs())}, spell
}

func (s *Service) serviceToolkit(ctx context.Context) (*actionsv1.SpellToolkitState, error) {
	toolkit := &actionsv1.SpellToolkitState{}
	err := s.invoke(ctx, "buddy-actions", "spell_toolkit", &emptypb.Empty{}, toolkit)
	return toolkit, err
}

func (s *Service) servicePolicyWait(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Phase == "OPEN" && !s.pendingCancel {
		s.state.Reason = reason
	}
}

func tradeServiceSlotMatches(frame *tradev1.State, item tradeServiceItem) bool {
	for _, row := range frame.GetPartnerItems() {
		if row.GetSlot() == nonTradedSlot {
			return row.GetEntry() == item.entry && row.GetCount() == item.count
		}
	}
	return false
}

func (s *Service) requestTradeService(ctx context.Context, before *worldv1.SnapshotResponse, toolkit *actionsv1.SpellToolkitState) (*tradeServiceAdmission, string) {
	const unavailable = "trade_service_policy_unavailable"
	if ctx.Err() != nil || s.session == nil || s.selfGUID == 0 {
		return nil, unavailable
	}
	s.mu.Lock()
	now := s.now()
	frame, view, err := tradeConsentFrame(s.state)
	lease := &tradeConsentAdmission{frame: frame, token: s.token, deadline: s.deadline, service: s.serviceItem,
		partnerSpell: s.partnerSpell, pickLock: s.pickLock, mapKnown: before.GetSelf().GetMapKnown(), mapID: before.GetSelf().GetMapId()}
	admission := &tradeServiceAdmission{lease: lease, allocator: s.pickLockCount, worldFacts: tradeServiceWorldFacts(before),
		selfXYZ: [3]float32{before.GetSelf().GetX(), before.GetSelf().GetY(), before.GetSelf().GetZ()}}
	windowCurrent := !s.independent && !s.pendingCancel && !s.pendingBegin && s.state.Phase == "OPEN" &&
		s.state.PartnerGuid == ownerGUID && !s.state.AcceptTransportWritten && lease.token != "" &&
		!lease.deadline.IsZero() && now.Before(lease.deadline)
	service := tradeServiceItemFacts(lease.service)
	service["slot"], service["partnerSpell"] = float64(nonTradedSlot), float64(lease.partnerSpell)
	attemptKnown, attemptRemaining := tradeServiceRemaining(now, lease.pickLock.until)
	windowKnown, windowRemaining := tradeServiceRemaining(now, lease.deadline)
	attempt := map[string]any{"item": tradeServiceItemFacts(lease.pickLock.item), "castCount": float64(lease.pickLock.count),
		"queued": lease.pickLock.queued, "accepted": lease.pickLock.accepted, "castGo": lease.pickLock.castGo, "retry": lease.pickLock.retry,
		"deadlineKnown": attemptKnown, "remainingMs": attemptRemaining}
	s.mu.Unlock()
	if err != nil || !windowCurrent {
		return nil, unavailable
	}
	admission.spellFacts, _ = tradeServiceSpellFacts(toolkit)
	required, known := gamecatalog.LockpickingRequirement(lease.service.lockID)
	admission.required = required
	input := map[string]any{"kind": "prepare_trade_service", "sessionId": frame.SessionId, "selfGuid": strconv.FormatUint(s.selfGUID, 10),
		"trade": view, "service": service, "attempt": attempt, "world": admission.worldFacts, "spell": admission.spellFacts,
		"reference": map[string]any{"catalog": "client_build_12340_lockpicking", "lockId": float64(lease.service.lockID), "known": known, "requiredSkillRank": float64(required)},
		"window":    map[string]any{"deadlineKnown": windowKnown, "remainingMs": windowRemaining}}
	available, revision, decision, err := modcb.TradeConsentDecision(ctx, s.session, frame.SessionId, s.selfGUID, input)
	reason, reasonKnown := decision["reason"].(string)
	if err != nil || ctx.Err() != nil || !available || revision == "" || len(decision) != 2 || !reasonKnown || len(reason) == 0 || len(reason) > 128 || !utf8.ValidString(reason) {
		return nil, unavailable
	}
	if decision["kind"] != "prepare" {
		if decision["kind"] == "wait" {
			return nil, "trade_service_wait:" + reason
		}
		return nil, unavailable
	}
	_, spell := tradeServiceSpellFacts(toolkit)
	if !known || lease.service.entry == 0 || lease.service.count != 1 || lease.service.wrapped || lease.partnerSpell != 0 ||
		lease.pickLock.castGo || lease.pickLock.queued || lease.pickLock.item == lease.service && !lease.pickLock.retry ||
		!tradeServiceSlotMatches(frame, lease.service) || !s.tradeServiceWorldSafe(before, required) || !toolkit.GetSpellbookKnown() || toolkit.GetSpellbookRevision() == 0 ||
		toolkit.GetSessionId() != frame.SessionId || toolkit.GetSelfGuid() != s.selfGUID || spell == nil || !spell.MetadataKnown || spell.CastTimeMs == 0 {
		return nil, "trade_service_capability_unconfirmed"
	}
	lease.sourceRevision = revision
	admission.castTime = time.Duration(spell.CastTimeMs) * time.Millisecond
	return admission, ""
}

func (s *Service) tradeServiceWorldSafe(w *worldv1.SnapshotResponse, required uint32) bool {
	p, o := w.GetSelf(), w.GetOwner()
	finite := func(v float32) bool {
		return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) && math.Abs(float64(v)) < 40000
	}
	return w.GetVisitRevision() != 0 && p.GetGuid() == s.selfGUID && p.GetPresent() && p.GetAliveKnown() && p.GetAlive() &&
		p.GetMapKnown() && p.GetPositionKnown() && p.GetPositionRevision() != 0 && o.GetGuid() == ownerGUID && o.GetPresent() &&
		o.GetAliveKnown() && o.GetAlive() && o.GetMapKnown() && o.GetMapId() == p.GetMapId() && o.GetPositionKnown() && o.GetPositionRevision() != 0 &&
		finite(p.GetX()) && finite(p.GetY()) && finite(p.GetZ()) && finite(o.GetX()) && finite(o.GetY()) && finite(o.GetZ()) &&
		(!p.GetMovementFlagsKnown() || p.GetMovementFlags()&0xff == 0) && canPickTradeLock(w, required)
}

// The last renewal's detached Actions pose supplies the ordinary trade-range
// check without another dependency after final World. It is still an estimate.
func (p *tradeServiceAdmission) readyLocked(ctx context.Context, s *Service, world *worldv1.SnapshotResponse) bool {
	now := s.now()
	lease := p.lease
	frame, _, err := tradeConsentFrame(s.state)
	item, prior := lease.service, lease.pickLock
	if err != nil || s.independent || s.pendingCancel || s.pendingBegin || s.state.Phase != "OPEN" || s.state.AcceptTransportWritten ||
		!proto.Equal(frame, lease.frame) || s.serviceItem != item || s.partnerSpell != lease.partnerSpell || s.pickLock != prior ||
		s.pickLockCount != p.allocator || !s.deadline.Equal(lease.deadline) || lease.sourceRevision == "" || s.state.PartnerGuid != ownerGUID ||
		item.entry == 0 || item.count != 1 || item.wrapped || lease.partnerSpell != 0 || prior.castGo || prior.queued || prior.item == item && !prior.retry ||
		!tradeServiceSlotMatches(frame, item) ||
		!reflect.DeepEqual(tradeServiceWorldFacts(world), p.worldFacts) || !s.tradeServiceWorldSafe(world, p.required) ||
		[3]float32{world.GetSelf().GetX(), world.GetSelf().GetY(), world.GetSelf().GetZ()} != p.selfXYZ {
		return false
	}
	position, owner := lease.lastLeasePosition, world.GetOwner()
	if position == nil || !position.Known || !position.MapKnown || position.MapId != owner.GetMapId() {
		return false
	}
	dx, dy, dz := float64(position.X-owner.X), float64(position.Y-owner.Y), float64(position.Z-owner.Z)
	distance := dx*dx + dy*dy + dz*dz
	if math.IsNaN(distance) || math.IsInf(distance, 0) || distance > 100 {
		return false
	}
	ready := ctx.Err() == nil && lease.token != "" && s.token == lease.token && now.Before(lease.until) &&
		!lease.deadline.IsZero() && now.Before(lease.deadline)
	if ready {
		p.reserveAt = now
	}
	return ready
}

func (p *tradeServiceAdmission) checkToolkit(toolkit *actionsv1.SpellToolkitState) error {
	facts, _ := tradeServiceSpellFacts(toolkit)
	if !reflect.DeepEqual(facts, p.spellFacts) {
		return errors.New("trade_service_spellbook_changed")
	}
	return nil
}
