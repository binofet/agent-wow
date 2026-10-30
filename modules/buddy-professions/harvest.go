package buddyprofessions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func validHarvestLease(a *actionsv1.State, self uint64) bool {
	if a == nil || !a.InteractionHarvest || len(a.HarvestGuardGuids) < 2 || len(a.HarvestGuardGuids) > 42 {
		return false
	}
	seen := map[uint64]bool{}
	for _, g := range a.HarvestGuardGuids {
		if g == 0 || g >= 1<<48 || seen[g] {
			return false
		}
		seen[g] = true
	}
	return seen[self] && seen[3]
}

func (s *Service) releaseHarvestLease(ctx context.Context, op *operation, token string) bool {
	if err := s.renew(ctx, op); err == nil {
		out := &actionsv1.State{}
		if err = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "professions", Token: token}, out); err == nil {
			return out.InteractionOwner == "" && !out.Moving && !out.Traveling && !out.MotionUnconfirmed
		}
	}
	// The safety guard may already have revoked this lease to resume defense.
	// Only observe the same attached session; never stop or release a replacement.
	out := &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, out); err != nil {
		return false
	}
	return out.InteractionOwner == "" && !out.Moving && !out.Traveling && !out.MotionUnconfirmed
}

// Exact-entry results come first so a selected object survives collection
// truncation. The optional nearby collection is discovery only. Both copies of
// one GUID must describe the same actual snapshot; malformed duplicates fail.
func herbObservations(w *worldv1.SnapshotResponse) ([]*worldv1.GameObjectState, bool) {
	if w == nil || len(w.RequestedGameObjects) > 32 || len(w.NearbyGatheringObjects) > 32 {
		return nil, false
	}
	var out []*worldv1.GameObjectState
	seen := map[uint64]*worldv1.GameObjectState{}
	for _, list := range [][]*worldv1.GameObjectState{w.RequestedGameObjects, w.NearbyGatheringObjects} {
		local := map[uint64]bool{}
		for _, g := range list {
			if g == nil {
				continue
			}
			if local[g.Guid] {
				return out, false
			}
			local[g.Guid] = true
			if old := seen[g.Guid]; old != nil {
				if !proto.Equal(old, g) {
					return out, false
				}
				continue
			}
			seen[g.Guid] = g
			out = append(out, g)
		}
	}
	return out, true
}

// Ordinary looting and gathering serialize through the same exact interaction
// lease. Only the two gathering recipes can retain an enabled idle looter; no
// controller intent is changed, and pending/unknown windows remain blockers.
func (s *Service) ordinaryLootIdleLocked(skill uint32) bool {
	l := s.loot
	if l == nil || l.PendingCorpseGuid != 0 || l.PendingGameObjectGuid != 0 {
		return false
	}
	if l.Enabled {
		return (skill == 393 || skill == 182) && l.SessionId == s.state.SessionId && l.Phase == "idle"
	}
	return l.Phase == "" || l.Phase == "idle" || l.Phase == "disabled"
}
