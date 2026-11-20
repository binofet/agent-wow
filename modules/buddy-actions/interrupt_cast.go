package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InterruptCast executes exactly the selected learned spell on exactly the
// selected active cast. Lua, rather than this harness, prioritizes enemy heals.
func (s *Service) InterruptCast(ctx context.Context, r *actionsv1.InterruptCastRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetTargetGuid() == 0 || r.GetSpellId() == 0 || r.GetCastRevision() == 0 {
		return nil, status.Error(codes.InvalidArgument, "session, caster, enemy, learned spell and active cast revision required")
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
	if s.closed || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current caster session required")
	}
	if !supplied && s.hostEncounterActiveLocked() {
		return nil, status.Error(codes.FailedPrecondition, "current Lua encounter owns hostile casts")
	}
	w, err := s.snapshot(ctx, r.TargetGuid)
	if err != nil || !s.interruptCasterAvailableLocked(w) || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current controllable living caster required")
	}
	enemy := w.RequestedCreature
	rank, learned := s.learnedContractsLocked()[r.SpellId]
	if !s.spellbookKnown || !learned || !s.interruptTargetObservedLocked(w, enemy) || enemy.ActiveCast.Revision != r.CastRevision ||
		!s.interruptRankAvailableLocked(rank, w.Self) || !s.interruptAvailableLocked(rank, w.Self, enemy) {
		return nil, status.Error(codes.FailedPrecondition, "exact active enemy cast and ready learned in-range interrupt required")
	}
	pos, _, _ := creaturePosition(enemy)
	if !s.stopLocked(ctx) || rank.Facing != 0 && math.Abs(math.Remainder(float64(s.pos.o-followFacing(s.pos, pos)), 2*math.Pi)) > math.Pi/2 && !s.faceEnemyLocked(ctx, pos) {
		return nil, status.Error(codes.FailedPrecondition, "caster could not stop and face selected cast")
	}
	// Turning can consume the end of a cast. Spend the selected interrupt
	// only if this exact cast revision still exists after movement settles.
	fresh, err := s.snapshot(ctx, r.TargetGuid)
	if err != nil || !s.interruptCasterAvailableLocked(fresh) || !s.refreshPositionLocked(fresh) {
		return nil, status.Error(codes.FailedPrecondition, "caster changed while facing selected cast")
	}
	enemy = fresh.RequestedCreature
	if !s.interruptTargetObservedLocked(fresh, enemy) || enemy.ActiveCast.Revision != r.CastRevision || !s.interruptAvailableLocked(rank, fresh.Self, enemy) {
		return nil, status.Error(codes.FailedPrecondition, "selected enemy cast ended or moved out of range")
	}
	if supplied && !s.admitsPolicyEncounterLocked(ctx, fresh) {
		return nil, status.Error(codes.FailedPrecondition, "active policy lease for current encounter visit required")
	}
	s.lastInterrupt = enemyCastIdentity{guid: enemy.Guid, revision: r.CastRevision}
	s.castCount++
	s.pendingSpell, s.pendingTarget = rank.ID, enemy.Guid
	s.castUntil = s.now().Add(2 * time.Second)
	s.state.CastPending = true
	s.state.LastCastSpellId, s.state.LastCastTargetGuid, s.state.LastCastFailure = rank.ID, enemy.Guid, 0
	s.reserveSpellCooldownLocked(rank)
	op := s.reserveExplicitCastLocked(token, rank.ID, enemy.Guid, "interrupt", false)
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, rank.ID, enemy.Guid)) {
		op.view.Phase = "transport_unconfirmed_no_retry"
		return s.stateLocked(), nil
	}
	op.view.TransportWritten = true
	s.state.Reason = "interrupt_cast_waiting_for_server"
	return s.stateLocked(), nil
}
