package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type supportAttempt struct {
	view  actionsv1.SpellOperation
	owner uint64
	count byte
	until time.Time
}
type resurrectionOffer struct {
	caster   uint64
	consumed bool
	until    time.Time
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
	if owner == s.selfGUID {
		return 0
	}
	p := observedHealingPlayer(w, owner)
	playerTeam := observedPlayerTeam(p)
	if team != 0 && p != nil && p.Present && (p.AliveKnown && p.Alive || playerTeam != 0 && playerTeam != team) {
		return 0
	}
	if (team == 0 || playerTeam == team) && p != nil && p.Present && p.AliveKnown && !p.Alive && p.HealthKnown && p.Health == 0 && p.PlayerFlags&ghostFlag == 0 && p.MapKnown && p.MapId == s.posMap && p.PositionKnown && finite(playerPosition(p)) && distance(s.pos, playerPosition(p)) <= r.MaxFriendly {
		return owner
	}
	var body *worldv1.CorpseState
	for _, c := range w.PlayerCorpses {
		if c != nil && c.OwnerGuid == owner && c.Present && c.Guid>>48 == 0xf101 && c.FlagsKnown && c.Flags&1 == 0 && c.MapKnown && c.MapId == s.posMap && c.PositionKnown && finite(position{c.X, c.Y, c.Z, c.Orientation}) {
			if body != nil {
				return 0
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
			return 0
		}
		pos := position{body.X, body.Y, body.Z, body.Orientation}
		if distance(s.pos, pos) <= r.MaxFriendly {
			return body.Guid
		}
	}
	return 0
}

func (s *Service) supportSnapshotLocked(w *worldv1.SnapshotResponse) {
	for owner := range s.resurrectionBindings {
		if p := observedHealingPlayer(w, owner); p != nil && p.Present && p.AliveKnown && p.Alive && p.HealthKnown && p.Health > 0 && p.PlayerFlags&0x10 == 0 {
			delete(s.resurrectionBindings, owner)
			if s.supportOp != nil && s.supportOp.owner == owner && s.supportOp.view.Capability == string(spellcatalog.Resurrect) {
				s.supportOp.view.EffectObserved = true
				if s.supportOp.view.ServerCastGo {
					s.supportOp.view.Phase = "confirmed_alive"
				}
			}
		}
	}
	if op := s.supportOp; op != nil && op.view.Phase == "waiting_server" && !s.now().Before(op.until) {
		op.view.Phase = "outcome_unconfirmed_no_retry"
		if s.pendingSpell == op.view.SpellId && s.castCount == op.count {
			s.state.CastPending = false
			s.pendingSpell = 0
			s.pendingTarget = 0
		}
	}
}

func (s *Service) resurrectionTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	s.supportSnapshotLocked(w)
	if !s.state.Healing || s.independent || s.closed || s.state.InteractionOwner != "" || s.state.CastPending || s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist) || safeSelfCombat(w, s.selfGUID) != "" || w.Self.UnitFlags&(0x80000|0x2000) != 0 || !s.refreshPositionLocked(w) || s.state.MotionUnconfirmed {
		return false
	}
	spells := s.candidatesLocked(spellcatalog.Resurrect)
	if len(spells) == 0 {
		return false
	}
	members := s.healingPartyLocked(ctx)
	owners := make([]uint64, 0, len(members))
	for guid, on := range members {
		if !on || guid == s.selfGUID {
			continue
		}
		p := observedHealingPlayer(w, guid)
		if p != nil && p.Present && p.UnitFlagsKnown && p.UnitFlags&0x80000 != 0 {
			return false
		}
		if !s.resurrectionBindings[guid] {
			owners = append(owners, guid)
		}
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	for _, r := range spells {
		if r.Power != 0 || !w.Self.PowerKnown || w.Self.PowerType != 0 || uint64(w.Self.Power) < spellCost(r, w.Self) || s.now().Before(s.spellReady[r.ID]) {
			continue
		}
		for _, owner := range owners {
			target := s.resurrectionTargetLocked(w, owner, r, 0)
			if target == 0 {
				continue
			}
			if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) {
				return true
			}
			fresh, err := s.snapshot(ctx)
			if err != nil || safeSelfCombat(fresh, s.selfGUID) != "" || fresh.Self.UnitFlags&(0x80000|0x2000) != 0 || !s.refreshPositionLocked(fresh) || !fresh.Self.PowerKnown || fresh.Self.PowerType != 0 || uint64(fresh.Self.Power) < spellCost(r, fresh.Self) || !s.healingPartyLocked(ctx)[owner] {
				return false
			}
			again := s.resurrectionTargetLocked(fresh, owner, r, 0)
			if again != target {
				return false
			}
			return s.castResurrectionLocked(ctx, r, target, owner)
		}
	}
	return false
}

func (s *Service) castResurrectionLocked(ctx context.Context, r spellcatalog.Spell, target, owner uint64) bool {
	if s.resurrectionBindings == nil {
		s.resurrectionBindings = map[uint64]bool{}
	}
	s.resurrectionBindings[owner] = true
	return s.castSupportLocked(ctx, r, target, owner)
}

func (s *Service) castSupportLocked(ctx context.Context, r spellcatalog.Spell, target, owner uint64) bool {
	s.castCount++
	s.reserveSpellCooldownLocked(r)
	until := s.now().Add(time.Duration(r.CastMs)*time.Millisecond + 3*time.Second)
	s.supportOp = &supportAttempt{view: actionsv1.SpellOperation{SpellId: r.ID, TargetGuid: target, Capability: string(r.Capability().Kind), Phase: "waiting_server"}, owner: owner, count: s.castCount, until: until}
	s.state.CastPending = true
	s.pendingSpell = r.ID
	s.pendingTarget = target
	s.castUntil = until
	s.nextHeal = s.now().Add(1500 * time.Millisecond)
	s.nextAssist = s.nextHeal
	s.state.LastCastSpellId = r.ID
	s.state.LastCastTargetGuid = target
	payload := spellPacket(s.castCount, r.ID, target)
	if target>>48 == 0xf101 {
		binary.LittleEndian.PutUint32(payload[6:], 0x8000)
	}
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, payload) {
		s.supportOp.view.Phase = "transport_unconfirmed_no_retry"
		return true
	}
	s.supportOp.view.TransportWritten = true
	s.state.Reason = "support_spell_waiting_for_server"
	return true
}

func (s *Service) supportPacketLocked(code uint32, p []byte) (bool, error) {
	bad := errors.New("malformed support spell event")
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
			s.resurrectionOffer = &resurrectionOffer{caster: caster, until: s.now().Add(60 * time.Second)}
		}
		return true, nil
	}
	op := s.supportOp
	if op == nil || op.view.Phase != "waiting_server" || !s.state.CastPending || s.pendingSpell != op.view.SpellId || s.castCount != op.count {
		return false, nil
	}
	rejected := false
	switch code {
	case opcode.SMSGCastFailed:
		if len(p) < 6 || p[0] != op.count || binary.LittleEndian.Uint32(p[1:]) != op.view.SpellId {
			return false, nil
		}
		if len(p) != 6 {
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
		if !validCastTargetBody(p, off, code == opcode.SMSGSpellGo, op.view.TargetGuid, s.implicitSelfSpellLocked(spell, op.view.TargetGuid)) {
			return true, bad
		}
		if code == opcode.SMSGSpellStart {
			return true, nil
		}
		op.view.ServerCastGo = true
		op.view.Phase = "server_cast_go_effect_unverified"
		if op.view.EffectObserved {
			if op.view.Capability == string(spellcatalog.Resurrect) {
				op.view.Phase = "confirmed_alive"
			} else {
				op.view.Phase = "confirmed_effect"
			}
		}
	default:
		return false, nil
	}
	if rejected {
		op.view.Phase = "server_rejected_no_replay"
		if op.view.Capability == string(spellcatalog.Resurrect) {
			delete(s.resurrectionBindings, op.owner)
			s.nextHeal = s.now().Add(10 * time.Second)
		}
	}
	s.state.CastPending = false
	s.pendingSpell = 0
	s.pendingTarget = 0
	return true, nil
}

// Accept a real current-party resurrection offer once, before corpse running.
func (s *Service) acceptResurrectionLocked(ctx context.Context) bool {
	offer := s.resurrectionOffer
	if offer == nil {
		return false
	}
	w, err := s.snapshot(ctx)
	if err != nil || w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.AliveKnown {
		return false
	}
	if w.Self.Alive && w.Self.HealthKnown && w.Self.Health > 0 && w.Self.PlayerFlags&0x10 == 0 {
		s.resurrectionOffer = nil
		return false
	}
	if !s.pendingResurrectionOfferLocked(offer, offer.caster) || !s.healingPartyLocked(ctx)[offer.caster] {
		return false
	}
	s.acceptResurrectionOfferLocked(ctx, offer)
	return true
}

func (s *Service) pendingResurrectionOfferLocked(offer *resurrectionOffer, caster uint64) bool {
	return offer != nil && offer.caster == caster && !offer.consumed && s.now().Before(offer.until)
}

func (s *Service) acceptResurrectionOfferLocked(ctx context.Context, offer *resurrectionOffer) {
	offer.consumed = true
	payload := binary.LittleEndian.AppendUint64(nil, offer.caster)
	payload = append(payload, 1)
	if s.sendLocked(ctx, opcode.CMSGResurrectResponse, payload) {
		s.state.Reason = "resurrection_accepted_waiting_for_alive_observation"
	}
}
