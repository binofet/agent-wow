package buddyquests

import (
	"context"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Public gameobject_queststarter/ender:55 ends37/starts45;56 ends45/starts71.
// This is an ordinary gameobject gossip interaction, never a synthetic creature.
func lostGuardObjectGUID(guid uint64) bool {
	entry := uint32(guid >> 24 & 0xffffff)
	return guid>>48 == 0xf110 && guid&0xffffff != 0 && (entry == 55 || entry == 56)
}
func questGiverGUID(guid uint64) bool {
	return creatureGUID(guid) || lostGuardObjectGUID(guid) || itemGiverGUID(guid)
}
func giverStarts(guid uint64, quest uint32) bool {
	return creatureGUID(guid) || itemGiverGUID(guid) && quest == deedQuestID || lostGuardObjectGUID(guid) && (guid>>24&0xffffff == 55 && quest == 45 || guid>>24&0xffffff == 56 && quest == 71)
}
func giverEnds(guid uint64, quest uint32) bool {
	return creatureGUID(guid) || lostGuardObjectGUID(guid) && (guid>>24&0xffffff == 55 && quest == 37 || guid>>24&0xffffff == 56 && quest == 45)
}

func objectInteraction(w *worldv1.SnapshotResponse, a *actionsv1.State, o *worldv1.GameObjectState, self uint64) (string, float64, bool) {
	if self == 0 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.Self.MapKnown || !w.Self.AliveKnown || !w.Self.Alive || !w.Self.UnitFlagsKnown || w.Self.UnitFlags&(0x1|0x4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x2000000|0x8000000) != 0 || !w.Self.MovementFlagsKnown || !movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags) {
		return "character_not_observed_safe_for_quest_object", 0, false
	}
	if a == nil || a.Position == nil || !a.Position.Known || !a.Position.MapKnown || a.Position.MapId != w.Self.MapId || !finite(a.Position.X) || !finite(a.Position.Y) || !finite(a.Position.Z) {
		return "current_character_position_unknown", 0, false
	}
	p := a.Position
	if o == nil || !lostGuardObjectGUID(o.Guid) || uint64(o.Entry) != o.Guid>>24&0xffffff || !o.Present || !o.MapKnown || o.MapId != p.MapId || !o.PositionKnown || o.PositionRevision == 0 || !finite(o.X) || !finite(o.Y) || !finite(o.Z) || !o.TypeKnown || o.GameObjectType != 2 || !o.FlagsKnown || o.Flags&0x1b != 0 || !o.CreatedByKnown || o.CreatedByGuid != 0 || !o.StateKnown {
		return "quest_object_not_observed_actionable_in_this_map", 0, false
	}
	d := math.Sqrt(math.Pow(float64(o.X-p.X), 2) + math.Pow(float64(o.Y-p.Y), 2) + math.Pow(float64(o.Z-p.Z), 2))
	if a.Moving || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackUnconfirmed || a.Traveling || a.Practicing {
		return "character_motion_or_cast_unconfirmed", d, true
	}
	if d > 4 || math.Abs(float64(o.Z-p.Z)) > 2 {
		return "quest_object_outside_conservative_range", d, true
	}
	return "", d, true
}

func (s *Service) objectGate(ctx context.Context, guid uint64) (*worldv1.SnapshotResponse, error) {
	// Actions may refresh world; take the authoritative object view afterwards.
	a, err := s.actions(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.world(ctx)
	if err != nil {
		return nil, err
	}
	if a.InteractionOwner != "quests" || a.InteractionLeaseRemainingMs < 1000 {
		return nil, status.Error(codes.FailedPrecondition, "quest object lease is no longer fresh")
	}
	// Estimated motion is client evidence, kept separate from the raw server
	// pose. A server correction invalidates its anchor; never use the old pose.
	if !questPositionBound(w, a, s.selfGUID) {
		return nil, status.Error(codes.FailedPrecondition, "quest object character pose binding changed")
	}
	var selected *worldv1.GameObjectState
	for _, o := range w.QuestObjects {
		if o != nil && o.Guid == guid {
			if selected != nil && !proto.Equal(selected, o) {
				return nil, status.Error(codes.FailedPrecondition, "contradictory quest object observations")
			}
			selected = o
		}
	}
	reason, _, _ := objectInteraction(w, a, selected, s.selfGUID)
	if reason != "" {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	return w, nil
}

// Client estimates are usable only while anchored to the same raw server pose.
func questPositionBound(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) bool {
	if w == nil || w.Self == nil || a == nil || a.Position == nil || !a.Position.Known || !a.Position.MapKnown || !w.Self.Present || !w.Self.MapKnown || w.Self.Guid != self || a.Position.MapId != w.Self.MapId {
		return false
	}
	p := a.Position
	x, y, z := w.Self.X, w.Self.Y, w.Self.Z
	bound := w.Self.PositionKnown
	if p.Estimated {
		anchor := w.DiscoveryAnchor
		bound = anchor != nil && anchor.Estimated && anchor.SelfGuid == self && anchor.MapId == w.Self.MapId && anchor.ServerPositionRevision == w.Self.PositionRevision
		if bound {
			x, y, z = anchor.X, anchor.Y, anchor.Z
		}
	}
	return bound && finite(x) && finite(y) && finite(z) && finite(p.X) && finite(p.Y) && finite(p.Z) && math.Abs(float64(x-p.X)) <= .01 && math.Abs(float64(y-p.Y)) <= .01 && math.Abs(float64(z-p.Z)) <= .01
}
