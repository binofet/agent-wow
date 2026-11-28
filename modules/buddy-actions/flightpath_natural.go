package buddyactions

import (
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/channelstate"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func (s *Service) ownsFlightpathQuery(r *actionsv1.FlightpathQueryRequest) bool {
	return s.ownsExploration(r.Token) || r.Mode == "arrival" && r.NaturalLandingRevision != 0 && s.hasExplorationToken(r.Token)
}

// This receive-only proof qualifies a free range-checked menu. It cannot
// establish arrival, change the character's position or resume ground input.
func (s *Service) naturalTaxiLandingLocked(activation, landing uint64, w *worldv1.SnapshotResponse) bool {
	if landing == 0 || !s.flightpathAcceptedCurrentLocked(activation) || !s.matchesCurrentEncounterVisitLocked(w) {
		return false
	}
	b := s.flightpathActivation.operation
	a, h, p := w.SelfMotionAuthority, w.SelfSplineFacts, w.Self
	if a == nil || h == nil || p == nil || b.CurrentSegment != nil || b.VisitId != w.VisitRevision ||
		!p.Present || !p.MapKnown || p.Guid != b.SelfGuid || !p.AliveKnown || !p.Alive ||
		!p.HealthKnown || p.Health == 0 || p.Health > p.MaxHealth || !channelstate.Idle(p) ||
		!p.UnitFlagsKnown || p.UnitFlags&unsafeQuestUnitFlags != 0 || !a.UnitFlagsKnown || p.UnitFlags != a.UnitFlags ||
		a.NaturalTaxiLandingRevision != landing || a.UnitFlagsRevision < landing || a.SelfGuid != p.Guid || a.VisitRevision != w.VisitRevision {
		return false
	}
	scope, accept := a.TransportScope, b.AcceptanceScope
	if !flightpathScopeBound(scope, p.Guid, p.MapId) || !scope.SplineActive || !scope.SplineIdKnown || scope.SplineSourceOpcode != opcode.SMSGMonsterMove ||
		!s.flightpathBookingSourceLocked(scope) ||
		accept.SplineRevision == math.MaxUint64 || scope.SplineRevision != accept.SplineRevision+1 ||
		a.TaxiOnMotionRevision <= accept.MotionRevision || a.UnitFlagsMotionRevision <= a.TaxiOnMotionRevision ||
		a.UnitFlagsMotionRevision <= scope.SplineSourceRevision || a.UnitFlagsMotionRevision > scope.MotionRevision ||
		h.SourceOpcode != opcode.SMSGMonsterMove || !h.Known || !h.IdKnown || !h.MapKnown || h.MapId != p.MapId ||
		h.SelfGuid != p.Guid || h.VisitRevision != w.VisitRevision || h.Revision != a.SplineRevision || h.SplineId != scope.SplineId ||
		!h.DestinationKnown || h.TransportRelative || h.Stopped || h.Cyclic {
		return false
	}
	// Keep an attempted/uncertain completion on its original retirement path.
	c := w.GetSplineCompletion().GetOperation()
	return c == nil || !sameFlightpathSource(c.TransportScope, scope) ||
		!c.Pending && !c.TransportWritten && !c.TransportUnknown && c.Phase == "not_dispatched"
}

func naturalTaxiReference(w *worldv1.SnapshotResponse) position {
	h := w.SelfSplineFacts
	raw, scope := w.SelfMovementFacts, w.SelfMotionAuthority.TransportScope
	if raw.GetKnown() && raw.GetInfo() != nil && sameFlightpathSource(raw.TransportScope, scope) &&
		raw.TransportScope.MotionRevision > scope.SplineSourceRevision && w.Self.PositionKnown && !w.Self.PositionInferred {
		return playerPosition(w.Self)
	}
	// Only a hint for choosing a visible NPC. The realm verifies actual range.
	return position{x: h.DestinationX, y: h.DestinationY, z: h.DestinationZ}
}

func naturalTaxiInteractionRange(w *worldv1.SnapshotResponse, npc *worldv1.CreatureState) float64 {
	// GetNPCIfCanInteractWith uses INTERACTION_DISTANCE plus each unit's
	// observed UNIT_FIELD_COMBATREACH. Omitted radii contribute nothing.
	r := 5.0
	if p := w.Self; p.CombatReachKnown {
		r += float64(p.CombatReach)
	}
	if npc.CombatReachKnown {
		r += float64(npc.CombatReach)
	}
	return r
}
