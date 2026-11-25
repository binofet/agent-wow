package buddyloot

import (
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Observations shared by corpse and explicit quest-container fixtures.
func wolfRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	f.world.Owner = nil
	self := f.world.Self
	self.HealthKnown, self.Health, self.MaxHealth = true, 100, 100
	self.MapKnown, self.PositionKnown, self.MovementFlagsKnown, self.SpeedsKnown = true, true, true, true
	self.RunSpeed = 7
	f.world.SelfQuestLogKnown = true
	for i := range 25 {
		f.world.SelfQuestLog = append(f.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	f.world.SelfQuestLog[0].QuestId = 33
	f.world.LootableCorpses[0].Entry = 299
	return s, f, advance
}
