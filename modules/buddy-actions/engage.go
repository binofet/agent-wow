package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type engageIntent struct {
	active, executing bool
	target            uint64
	until             time.Time
	phase             string
	mapID             uint32
	revision          uint64
}

func (e *engageIntent) snapshot(now time.Time) *actionsv1.EngageNPCState {
	out := &actionsv1.EngageNPCState{Active: e.active, TargetGuid: e.target, Phase: e.phase}
	if e.active && now.Before(e.until) {
		out.LeaseRemainingMs = uint64(e.until.Sub(now).Milliseconds())
	}
	return out
}

// The caller selects one observed NPC. This is target intent, not permission
// to bypass ordinary movement, equipped-weapon or learned-spell validation.
func (s *Service) EngageNPC(ctx context.Context, r *actionsv1.EngageNPCRequest) (*actionsv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "engage request required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if !r.Enabled {
		s.engage.active, s.engage.phase = false, "disabled"
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		return s.stateLocked(), nil
	}
	if r.TargetGuid == 0 || s.state.InteractionOwner != "" || s.state.Practicing || s.fargodeep.active || s.state.Traveling || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "engage requires one target and confirmed movement custody")
	}
	w, err := s.snapshot(ctx, r.TargetGuid)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) || !s.engageTargetValidLocked(w, r.TargetGuid) {
		return nil, status.Error(codes.FailedPrecondition, "living observed NPC unavailable within normal melee approach bounds")
	}
	s.engage = engageIntent{active: true, target: r.TargetGuid, until: s.now().Add(lease), phase: "selected", mapID: w.Self.MapId, revision: w.Self.PositionRevision}
	return s.stateLocked(), nil
}
func (s *Service) engageTargetValidLocked(w *worldv1.SnapshotResponse, target uint64) bool {
	c := partyDefenseCreature(w, target)
	if c == nil || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || !c.UnitFlagsKnown || c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || c.UnitFlags_2&1 != 0 || c.DynamicFlags&0x20 != 0 {
		return false
	}
	if kind := c.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		return false
	}
	p, known, _ := creaturePosition(c)
	return known && distance(s.pos, p) <= 35 && math.Abs(float64(p.z-s.pos.z)) <= 12
}
func (s *Service) engageCurrentLocked(ctx context.Context) bool {
	if !s.engage.active || !s.now().Before(s.engage.until) || s.state.InteractionOwner != "" || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.CastPending {
		return false
	}
	w, err := s.snapshot(ctx, s.engage.target)
	return err == nil && safeSelfCombat(w, s.selfGUID) == "" && w.Self.MapId == s.engage.mapID && w.Self.PositionRevision == s.engage.revision && s.engageTargetValidLocked(w, s.engage.target)
}
func (s *Service) engageOffenseAllowedLocked(ctx context.Context, target uint64, spell uint32) bool {
	if target != s.engage.target || !s.engageCurrentLocked(ctx) || !s.defenseOffenseAllowedLocked(s.lastWorld, target, spell) {
		s.state.Reason = "engage_npc_observation_changed_before_attack"
		return false
	}
	return true
}
func (s *Service) engageTickLocked(ctx context.Context) bool {
	if !s.engage.active {
		return false
	}
	if !s.now().Before(s.engage.until) {
		s.engage.active, s.engage.phase = false, "lease_expired"
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		return true
	}
	w, err := s.snapshot(ctx, s.engage.target)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		s.engage.phase = "observations_unavailable"
		s.stopLocked(ctx)
		return true
	}
	c := partyDefenseCreature(w, s.engage.target)
	if c != nil && c.Present && c.MapKnown && c.MapId == w.Self.MapId && c.AliveKnown && !c.Alive && c.HealthKnown && c.Health == 0 {
		s.engage.active, s.engage.phase = false, "observed_dead"
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		return true
	}
	if !s.engageTargetValidLocked(w, s.engage.target) {
		s.engage.phase = "target_not_observed_or_outside_approach"
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		return true
	}
	s.engage.mapID, s.engage.revision = w.Self.MapId, w.Self.PositionRevision
	s.engage.executing = true
	defer func() { s.engage.executing = false; s.engage.phase = s.state.Reason }()
	s.assistObservedTargetLocked(ctx, w, c, true)
	return true
}
