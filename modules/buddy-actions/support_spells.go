package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type supportAttempt struct {
	view               actionsv1.SpellOperation
	owner              uint64
	count              byte
	until              time.Time
	controlAura        observedBuffAura
	controlSlot        uint8
	creation           *itemCreation
	selfChannel        bool
	effectLogEligible  bool
	cleanseAura        uint32
	cleanseBaseline    map[uint8]observedBuffAura
	cleanseEligible    map[uint32]bool
	cleanseLogs        map[uint32]bool
	cleanseLogObserved bool
	policySpellToken   string
	breakAuraRevision  uint64
	breakAuraSlot      uint32
	breakAura          observedBuffAura
	breakItem          uint64
}
type resurrectionOffer struct {
	caster, revision, epoch uint64
	consumed, written       bool
	unknown                 bool
	observedAt              time.Time
	sickness                bool
	reclaimDelay            *uint32
}

func (s *Service) spellKindLocked(id uint32) spellcatalog.Kind {
	r, ok := s.learnedContractsLocked()[id]
	if !ok {
		return ""
	}
	return r.Capability().Kind
}

// Only observed dead players or actual owned corpse objects can grant a target.
func (s *Service) resurrectionTargetLocked(w *worldv1.SnapshotResponse, owner uint64, r spellcatalog.Spell, team uint8) uint64 {
	target, pos, ok := s.resurrectionBodyLocked(w, owner, team)
	if ok && distance(s.pos, pos) >= r.MinFriendly && distance(s.pos, pos) <= r.MaxFriendly {
		return target
	}
	return 0
}

// Resolve the real player/body independently of spell range. Lua can use the
// resulting observed body to approach before requesting an exact resurrection.
func (s *Service) resurrectionBodyLocked(w *worldv1.SnapshotResponse, owner uint64, team uint8) (uint64, position, bool) {
	if owner == s.selfGUID || w.GetSelf() == nil {
		return 0, position{}, false
	}
	p := observedHealingPlayer(w, owner)
	playerTeam := observedPlayerTeam(p)
	if team != 0 && p != nil && p.Present && (p.AliveKnown && p.Alive || playerTeam != 0 && playerTeam != team) {
		return 0, position{}, false
	}
	if (team == 0 || playerTeam == team) && p != nil && p.Present && p.AliveKnown && !p.Alive && p.HealthKnown && p.Health == 0 && p.PlayerFlags&ghostFlag == 0 && p.MapKnown && p.MapId == w.Self.MapId && p.PositionKnown && finite(playerPosition(p)) {
		return owner, playerPosition(p), true
	}
	var body *worldv1.CorpseState
	for _, c := range w.PlayerCorpses {
		if c != nil && c.OwnerGuid == owner && c.Present && c.Guid>>48 == 0xf101 && c.FlagsKnown && c.Flags&1 == 0 && c.MapKnown && c.MapId == w.Self.MapId && c.PositionKnown && finite(position{c.X, c.Y, c.Z, c.Orientation}) {
			if body != nil {
				return 0, position{}, false
			}
			body = c
		}
	}
	if body != nil {
		corpseTeam := uint8(0)
		if body.RaceKnown {
			corpseTeam = playerTeamForRace(body.RaceId)
		}
		if team != 0 && (corpseTeam != 0 && corpseTeam != team || corpseTeam == 0 && playerTeam != team) {
			return 0, position{}, false
		}
		return body.Guid, position{body.X, body.Y, body.Z, body.Orientation}, true
	}
	return 0, position{}, false
}

func (s *Service) supportSnapshotLocked(w *worldv1.SnapshotResponse) {
	for owner := range s.resurrectionBindings {
		if p := observedHealingPlayer(w, owner); p != nil && p.Present && p.AliveKnown && p.Alive && p.HealthKnown && p.Health > 0 && p.PlayerFlags&0x10 == 0 {
			delete(s.resurrectionBindings, owner)
			if s.supportOp != nil && s.supportOp.owner == owner && s.supportOp.view.Capability == string(spellcatalog.Resurrect) {
				// Actual observed life resolves this death even if the original
				// cast write had an unknown transport outcome. Do not clear a
				// newer cast which happens to share the patient.
				if s.pendingSpell == s.supportOp.view.SpellId && s.castCount == s.supportOp.count {
					s.state.CastPending = false
					s.pendingSpell, s.pendingTarget = 0, 0
				}
				if s.supportOp.view.ServerCastGo {
					s.supportOp.view.EffectObserved = true
					s.supportOp.view.Phase = "confirmed_alive"
				} else {
					// Another player or the server may have revived this patient.
					// Retire custody without attributing an unconfirmed own cast.
					s.supportOp.view.EffectObserved = false
					s.supportOp.view.Phase = "observed_alive_without_cast_confirmation"
				}
			}
		}
	}
	if op := s.supportOp; op != nil && (op.view.Phase == "waiting_server" || op.view.Phase == "waiting_cleanse_effect" || op.view.Capability == fearBreakCapability && op.view.Phase == "transport_unconfirmed_no_retry") && !s.now().Before(op.until) {
		op.view.Phase = "outcome_unconfirmed_no_retry"
		if s.pendingSpell == op.view.SpellId && s.castCount == op.count {
			s.state.CastPending = false
			s.pendingSpell = 0
			s.pendingTarget = 0
		}
	}
}

func (s *Service) castResurrectionLocked(ctx context.Context, r spellcatalog.Spell, target, owner uint64) bool {
	if s.resurrectionBindings == nil {
		s.resurrectionBindings = map[uint64]bool{}
	}
	s.resurrectionBindings[owner] = true
	return s.castSupportLocked(ctx, r, target, owner)
}

func (s *Service) castSupportLocked(ctx context.Context, r spellcatalog.Spell, target, owner uint64) bool {
	return s.castTargetedSupportLocked(ctx, r, target, owner, string(r.Capability().Kind))
}

func (s *Service) castTargetedSupportLocked(ctx context.Context, r spellcatalog.Spell, target, owner uint64, capability string) bool {
	return s.castSupportPacketLocked(ctx, r, target, owner, capability, nil)
}

func (s *Service) castSupportPacketLocked(ctx context.Context, r spellcatalog.Spell, target, owner uint64, capability string, item *worldv1.EquipmentSlotState) bool {
	s.castCount++
	s.reserveSpellCooldownLocked(r)
	until := s.now().Add(time.Duration(r.CastMs)*time.Millisecond + 3*time.Second)
	s.supportOp = &supportAttempt{view: actionsv1.SpellOperation{SpellId: r.ID, TargetGuid: target, Capability: capability, Phase: "waiting_server"}, owner: owner, count: s.castCount, until: until}
	if item == nil {
		s.supportOp.policySpellToken = modcb.PolicySpellToken(ctx)
	}
	s.state.CastPending = true
	s.pendingSpell = r.ID
	s.pendingTarget = target
	s.castUntil = until
	if capability == fearBreakCapability || capability == string(spellcatalog.Cleanse) {
		s.state.LastCastFailure = 0
		ready := s.now().Add(time.Duration(r.GcdMs) * time.Millisecond)
		if s.nextHeal.Before(ready) {
			s.nextHeal = ready
		}
		if s.nextAssist.Before(ready) {
			s.nextAssist = ready
		}
	} else {
		s.nextHeal = s.now().Add(1500 * time.Millisecond)
		s.nextAssist = s.nextHeal
	}
	s.state.LastCastSpellId = r.ID
	s.state.LastCastTargetGuid = target
	payload := spellPacket(s.castCount, r.ID, target)
	if target>>48 == 0xf101 {
		binary.LittleEndian.PutUint32(payload[6:], 0x8000)
	} else if target>>48 == 0xf110 {
		binary.LittleEndian.PutUint32(payload[6:], 0x800)
	}
	wire := uint32(opcode.CMSGCastSpell)
	if item != nil {
		wire, payload = opcode.CMSGUseItem, fearBreakItemPacket(item, s.castCount, r.ID)
	}
	if !s.sendLocked(ctx, wire, payload) {
		s.supportOp.view.Phase = "transport_unconfirmed_no_retry"
		return true
	}
	s.supportOp.view.TransportWritten = true
	s.state.Reason = "support_spell_waiting_for_server"
	if capability == fearBreakCapability {
		s.state.Reason = "fear_break_waiting_for_server"
	}
	return true
}

// A callback reports only outcomes established by the native parser. Spell GO
// during a pending operation does not by itself settle its effect.
func (op *supportAttempt) reportPolicySpellEffect(ctx context.Context, castPending bool) {
	switch op.view.Phase {
	case "confirmed_aura", "confirmed_heal", "confirmed_effect", "confirmed_alive", "confirmed_loot_window", "confirmed_item":
		if !op.view.ServerCastGo || !op.view.EffectObserved {
			return
		}
	case "server_cast_go_effect_unverified":
		if castPending {
			return
		}
	case "server_rejected_no_replay", "server_cast_go_no_target_hit",
		"outcome_unconfirmed_no_retry", "channel_ended_effect_unverified", "world_transfer_outcome_unconfirmed",
		"map_changed_outcome_unconfirmed", "target_lost_outcome_unconfirmed", "observed_alive_without_cast_confirmation", "observed_control_clear_effect_unverified":
	default:
		return
	}
	if modcb.ReportPolicySpellEffect(ctx, op.policySpellToken, &op.view) {
		op.policySpellToken = ""
	}
}

func (s *Service) supportPacketLocked(code uint32, p []byte) (bool, error) {
	bad := errors.New("malformed support spell event")
	if code == opcode.MSGChannelStart || code == opcode.MSGChannelUpdate {
		return true, s.observeSelfChannelLocked(code, p)
	}
	if code == opcode.SMSGSpelldispellog {
		return true, s.observeCleanseLocked(p)
	}
	if code == opcode.SMSGLootResponse {
		s.observePickLockLootLocked(p)
		return true, nil
	}
	if code == opcode.SMSGItemPushResult {
		return true, s.observeCreatedItemPushLocked(p)
	}
	if code == opcode.SMSGResurrectRequest {
		if len(p) < 14 {
			return true, bad
		}
		caster := binary.LittleEndian.Uint64(p)
		if caster == 0 || caster>>48 != 0 || binary.LittleEndian.Uint32(p[8:]) != 1 || p[12] != 0 {
			return true, bad
		}
		// AzerothCore serializes the empty player name as a C string, then
		// writes another NUL before the sickness byte. Some realm versions
		// send a single NUL. Both can include a four-byte timer.
		sickness := 13
		switch len(p) - sickness {
		case 2, 6:
			if p[sickness] != 0 {
				return true, bad
			}
			sickness++
		case 1, 5:
		default:
			return true, bad
		}
		if p[sickness] > 1 {
			return true, bad
		}
		if s.resurrectionOffer == nil || s.resurrectionOffer.caster != caster {
			s.resurrectionOfferSequence++
			epoch := s.recovery.state.DeathEpoch
			if !s.recovery.lifeKnown || s.recovery.lastObservedAlive {
				epoch++
			}
			offer := &resurrectionOffer{caster: caster, revision: s.resurrectionOfferSequence, epoch: epoch,
				observedAt: s.now(), sickness: p[sickness] == 1}
			if len(p)-sickness == 5 {
				delay := binary.LittleEndian.Uint32(p[sickness+1:])
				offer.reclaimDelay = &delay
			}
			s.resurrectionOffer = offer
		}
		return true, nil
	}
	op := s.supportOp
	if op == nil || !s.state.CastPending || s.pendingSpell != op.view.SpellId || s.castCount != op.count {
		return false, nil
	}
	if op.view.Phase != "waiting_server" && op.view.Phase != "channeling" &&
		!((op.view.Capability == selfCastCapability || op.view.Capability == fearBreakCapability || op.view.Capability == string(spellcatalog.Cleanse)) && op.view.Phase == "transport_unconfirmed_no_retry") {
		return false, nil
	}
	rejected := false
	switch code {
	case opcode.SMSGCastFailed:
		if len(p) < 6 || p[0] != op.count || binary.LittleEndian.Uint32(p[1:]) != op.view.SpellId {
			return false, nil
		}
		if len(p) != 6 && op.view.Capability != pickLockCapability {
			return true, bad
		}
		rejected = true
	case opcode.SMSGSpellFailure, opcode.SMSGSpellFailedOther:
		guid, n, e := readGUID(p)
		if e != nil || len(p) < n+6 || guid != s.selfGUID || p[n] != op.count || binary.LittleEndian.Uint32(p[n+1:]) != op.view.SpellId {
			return false, nil
		}
		if len(p) != n+6 {
			return true, bad
		}
		rejected = true
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		guid, count, spell, off, e := castEvent(p)
		if e != nil || guid != s.selfGUID || count != op.count || spell != op.view.SpellId {
			return false, nil
		}
		if op.view.Capability == fearBreakCapability {
			source, _, _ := readGUID(p)
			expected := s.selfGUID
			if op.breakItem != 0 {
				expected = op.breakItem
			}
			if source != expected {
				return false, nil
			}
		}
		if op.view.Capability == selfCastCapability {
			source, _, _ := readGUID(p)
			if source != s.selfGUID {
				return false, nil
			}
		}
		if !validCastTargetBody(p, off, code == opcode.SMSGSpellGo, op.view.TargetGuid, op.view.Capability == selfCastCapability || op.view.Capability == fearBreakCapability || s.implicitSelfSpellLocked(spell, op.view.TargetGuid)) {
			return true, bad
		}
		if code == opcode.SMSGSpellStart {
			return true, nil
		}
		if spellcatalog.Kind(op.view.Capability).BreaksOnDamage() {
			source, _, _ := readGUID(p)
			if source != s.selfGUID {
				return false, nil
			}
			op.view.ServerCastGo = true
			// The parser validated the complete target body. Direct
			// single-unit crowd control must also hit that exact unit.
			hits := off + 8
			if len(p) < hits+10 || p[hits] != 1 || binary.LittleEndian.Uint64(p[hits+1:]) != op.view.TargetGuid || p[hits+9] != 0 {
				s.finishCrowdControlLocked("server_cast_go_no_target_hit")
			} else {
				s.confirmCrowdControlLocked()
			}
			return true, nil
		}
		op.view.ServerCastGo = true
		if op.selfChannel {
			if op.view.ChannelEnded {
				s.finishSelfChannelLocked("channel_ended_effect_unverified")
			}
			return true, nil
		}
		op.view.Phase = "server_cast_go_effect_unverified"
		if op.cleanseAura != 0 {
			op.view.Phase = "waiting_cleanse_effect"
		}
		if op.creation != nil {
			op.view.Phase = "waiting_created_item"
			return true, nil
		}
		if op.view.EffectObserved {
			if op.view.Capability == string(spellcatalog.Resurrect) {
				op.view.Phase = "confirmed_alive"
			} else if op.view.Capability == pickLockCapability {
				op.view.Phase = "confirmed_loot_window"
			} else {
				op.view.Phase = "confirmed_effect"
			}
		}
	default:
		return false, nil
	}
	if rejected {
		if spellcatalog.Kind(op.view.Capability).BreaksOnDamage() {
			s.finishCrowdControlLocked("server_rejected_no_replay")
			return true, nil
		}
		op.view.Phase = "server_rejected_no_replay"
		if (op.view.Capability == pickLockCapability || op.view.Capability == fearBreakCapability || op.view.Capability == string(spellcatalog.Cleanse)) && code == opcode.SMSGCastFailed {
			s.state.LastCastFailure = uint32(p[5])
		}
		if op.view.Capability == string(spellcatalog.Resurrect) {
			delete(s.resurrectionBindings, op.owner)
		}
		if op.view.Capability == fearBreakCapability {
			delete(s.controlBreakBindings, op.breakAuraSlot)
		}
	}
	s.state.CastPending = false
	s.pendingSpell = 0
	s.pendingTarget = 0
	return true, nil
}

func (s *Service) pendingResurrectionOfferLocked(offer *resurrectionOffer, caster uint64) bool {
	return offer != nil && offer.caster == caster && !offer.consumed
}

func (s *Service) acceptResurrectionOfferLocked(ctx context.Context, offer *resurrectionOffer) error {
	offer.consumed = true
	payload := binary.LittleEndian.AppendUint64(nil, offer.caster)
	payload = append(payload, 1)
	err := s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGResurrectResponse, payload)
	if modcb.PacketWasNotDispatched(err) {
		offer.consumed = false
		return err
	}
	offer.written, offer.unknown = err == nil, err != nil
	if err == nil {
		s.state.Reason = "resurrection_accepted_waiting_for_alive_observation"
	} else {
		s.state.Reason = "resurrection_acceptance_transport_unconfirmed_no_retry"
	}
	return nil
}
