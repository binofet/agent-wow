package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) buffCasterAvailableLocked(w *worldv1.SnapshotResponse, spell spellcatalog.Spell) bool {
	self := w.GetSelf()
	return !s.closed && self.GetGuid() == s.selfGUID && waterBreathPlayer(self, self.GetMapId()) &&
		self.UnitFlags&(0x2000|0x00040000|0x00400000|0x00800000) == 0 && self.ChannelSpellKnown && self.ChannelSpellId == 0 &&
		!s.clientControlLost && !s.clientControlRestoring && !s.state.CastPending &&
		!s.state.MotionUnconfirmed && s.state.InteractionOwner == "" &&
		(spell.CastMs == 0 || !s.state.Moving && self.MovementFlags&0xff == 0) &&
		(spell.GcdMs == 0 || !s.now().Before(s.nextHeal) && !s.now().Before(s.nextAssist)) &&
		(s.buffOp == nil || s.buffOp.view.Phase != "waiting_server")
}
func (s *Service) buffRankAvailableLocked(self *worldv1.PlayerState, spell spellcatalog.Spell) bool {
	return spell.Capability().Kind == spellcatalog.Buff && s.knownSpells[spell.ID] &&
		s.encounterRankResourcesAvailable(spell, self)
}

// CastBuff executes exactly the selected learned friendly aura spell. Lua owns
// recipient, rank, usefulness, stronger-aura, mana-reserve and refresh decisions.
func (s *Service) CastBuff(ctx context.Context, r *actionsv1.CastBuffRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetTargetGuid() == 0 || r.GetSpellId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body, exact friendly target and learned spell required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	token, supplied := modcb.IncomingPolicySpell(ctx)
	if supplied && token == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid policy spell token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	s.buffTimeoutLocked()
	s.expireInteractionLocked()
	online := s.healingPartyLocked(ctx)
	w, err := s.snapshot(ctx)
	spell, learned := s.learnedContractsLocked()[r.SpellId]
	if err != nil || !s.spellbookKnown || !learned || !s.buffCasterAvailableLocked(w, spell) ||
		!s.buffRankAvailableLocked(w.GetSelf(), spell) || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current controllable caster and exact ready learned buff required")
	}
	target := observedHealingPlayer(w, r.TargetGuid)
	sameTeam := observedPlayerTeam(w.Self) != 0 && observedPlayerTeam(w.Self) == observedPlayerTeam(target)
	if !waterBreathPlayer(target, w.Self.MapId) || r.TargetGuid != s.selfGUID &&
		(spell.Capability().Target != "ally" || !online[r.TargetGuid] && !sameTeam) {
		return nil, status.Error(codes.FailedPrecondition, "current living observed friendly target required")
	}
	d := distance(s.pos, playerPosition(target))
	if r.TargetGuid != s.selfGUID && (d < spell.MinFriendly || d > spell.MaxFriendly) {
		return nil, status.Error(codes.FailedPrecondition, "selected buff target is outside spell range")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if supplied && !s.admitsPolicyEncounterLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "active policy lease for current visit required")
	}
	s.startBuffLocked(ctx, spell, r.TargetGuid)
	return s.stateLocked(), nil
}
