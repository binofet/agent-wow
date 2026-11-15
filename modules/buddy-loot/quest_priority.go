package buddyloot

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
)

func neededQuestLoot(w *worldv1.SnapshotResponse, self uint64) (map[uint32]bool, bool) {
	if w.GetSelf().GetGuid() != self || !w.GetSelfQuestLogKnown() {
		return nil, false
	}
	var needed map[uint32]bool
	var counts map[uint32]uint64
	var countsKnown bool
	for _, quest := range w.SelfQuestLog {
		if quest == nil || quest.QuestId == 0 || quest.Complete || quest.Failed {
			continue
		}
		items := gamecatalog.RequiredQuestItems(quest.QuestId)
		if len(items) == 0 {
			continue
		}
		if needed == nil {
			needed = make(map[uint32]bool)
			counts, countsKnown = completeOwnItemCounts(w, self)
		}
		for _, item := range items {
			if item.ItemID != 0 && counts[item.ItemID] < uint64(item.Count) {
				needed[item.ItemID] = true
			}
		}
	}
	return needed, countsKnown
}

// Move a currently needed quest item ahead of ordinary items, regardless of
// the server's permission slot type. Keep ordinary items in their prior order.
func (s *Service) questLootFits(world *worldv1.SnapshotResponse, free int) bool {
	needed, countsKnown := neededQuestLoot(world, s.selfGUID)
	for i := s.index; i < len(s.items); i++ {
		item := s.items[i]
		if needed[item.id] && !s.removedSlots[item.slot] {
			copy(s.items[s.index+1:i+1], s.items[s.index:i])
			s.items[s.index] = item
			return true
		}
	}
	return free > 1 || !countsKnown || len(needed) == 0
}
