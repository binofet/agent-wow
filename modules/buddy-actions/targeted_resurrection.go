package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func playerTeamForRace(race uint32) uint8 {
	switch race {
	case 1, 3, 4, 7, 11: // Human, dwarf, night elf, gnome, draenei.
		return 1
	case 2, 5, 6, 8, 10: // Orc, undead, tauren, troll, blood elf.
		return 2
	default:
		return 0
	}
}

func observedPlayerTeam(p *worldv1.PlayerState) uint8 {
	if p != nil && p.Present && p.RaceKnown {
		return playerTeamForRace(p.RaceId)
	}
	return 0
}

// ResurrectOnce uses a learned spell on one observed friendly death. Unlike
// automatic support, the explicitly chosen player can be outside the party.
func (s *Service) ResurrectOnce(ctx context.Context, r *actionsv1.ResurrectOnceRequest) (*actionsv1.State, error) {
	if r == nil || r.SessionId == "" || r.SelfGuid == 0 || r.TargetGuid == 0 || r.TargetGuid>>48 != 0 || r.TargetGuid == r.SelfGuid {
		return nil, status.Error(codes.InvalidArgument, "current session, caster and a distinct player GUID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current caster session required")
	}
	w, err := s.snapshot(ctx)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current living caster observation required")
	}
	s.supportSnapshotLocked(w)
	p := w.Self
	if s.state.CastPending || s.state.Moving || s.state.MotionUnconfirmed || s.state.AutoAttacking || s.state.AutoAttackRequested ||
		s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.autoStopping || s.state.InteractionOwner != "" ||
		p.MovementFlags&0xff != 0 || p.ChannelSpellId != 0 || p.UnitFlags&(0x80000|0x2000) != 0 || s.selfAttackerLocked(w) != nil {
		return nil, status.Error(codes.FailedPrecondition, "stationary caster out of combat required")
	}
	team := observedPlayerTeam(p)
	if team == 0 || !s.spellbookKnown || !p.PowerKnown || p.PowerType != 0 {
		return nil, status.Error(codes.FailedPrecondition, "observed caster faction, spellbook and mana required")
	}
	if s.resurrectionBindings[r.TargetGuid] {
		return nil, status.Error(codes.FailedPrecondition, "resurrection already attempted for this death")
	}
	for _, spell := range s.candidatesLocked(spellcatalog.Resurrect) {
		if r.SpellId != 0 && r.SpellId != spell.ID || spell.Power != 0 || uint64(p.Power) < spellCost(spell, p) || s.now().Before(s.spellReady[spell.ID]) {
			continue
		}
		target := s.resurrectionTargetLocked(w, r.TargetGuid, spell, team)
		if target == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		s.castResurrectionLocked(ctx, spell, target, r.TargetGuid)
		return s.stateLocked(), nil
	}
	return nil, status.Error(codes.FailedPrecondition, "ready learned resurrection and observed dead same-faction target in range required")
}

// AcceptResurrection answers a genuine offer from the exact requested player.
// A released ghost can receive it outside the caster's visibility, so the
// server's offer supplies the binding when the caster cannot be observed.
func (s *Service) AcceptResurrection(ctx context.Context, r *actionsv1.AcceptResurrectionRequest) (*actionsv1.State, error) {
	if r == nil || r.SessionId == "" || r.SelfGuid == 0 || r.CasterGuid == 0 || r.CasterGuid>>48 != 0 || r.CasterGuid == r.SelfGuid {
		return nil, status.Error(codes.InvalidArgument, "current session, recipient and a distinct caster GUID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	offer := s.resurrectionOffer
	if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || !s.pendingResurrectionOfferLocked(offer, r.CasterGuid) {
		return nil, status.Error(codes.FailedPrecondition, "current recipient session and pending offer from that caster required")
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "current dead recipient observation required")
	}
	p := w.GetSelf()
	if p == nil || p.Guid != s.selfGUID || !p.Present || !p.AliveKnown || p.Alive || p.PlayerFlags&ghostFlag == 0 && (!p.HealthKnown || p.Health != 0) {
		return nil, status.Error(codes.FailedPrecondition, "current dead recipient observation required")
	}
	if casterTeam := observedPlayerTeam(observedHealingPlayer(w, offer.caster)); casterTeam != 0 && casterTeam != observedPlayerTeam(p) {
		return nil, status.Error(codes.FailedPrecondition, "same-faction resurrection offer required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.acceptResurrectionOfferLocked(ctx, offer)
	return s.stateLocked(), nil
}
