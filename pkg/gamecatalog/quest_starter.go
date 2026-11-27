package gamecatalog

import (
	_ "embed"
	"encoding/json"
	"sync"
)

type QuestStarter struct {
	QuestID    uint32 `json:"questId"`
	MinLevel   uint32 `json:"minLevel"`
	MaxStack   uint32 `json:"maxStack"`
	Repeatable bool   `json:"repeatable"`
}

//go:embed quest-starters.json
var questStarterData []byte

var manualQuestStarters = sync.OnceValue(func() map[uint32]QuestStarter {
	var reference struct {
		Items map[uint32]QuestStarter `json:"items"`
	}
	if err := json.Unmarshal(questStarterData, &reference); err != nil {
		panic("invalid generated quest starter catalog: " + err.Error())
	}
	return reference.Items
})

// ManualQuestStarter describes an item whose ordinary offer needs an explicit
// acceptance. The realm determines this character's eligibility for the quest.
func ManualQuestStarter(entry uint32) (QuestStarter, bool) {
	starter, ok := manualQuestStarters()[entry]
	return starter, ok
}
