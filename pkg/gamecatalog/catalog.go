// Package gamecatalog indexes pinned public content. Knowledge of a possible
// interaction never proves a live object, quest, item or successful action.
package gamecatalog

import (
	_ "embed"
	"encoding/json"
	"sync"
)

type QuestItem struct {
	QuestID uint32 `json:"questId"`
	ItemID  uint32 `json:"itemId"`
	Count   uint32 `json:"count"`
}
type Container struct {
	Name       string      `json:"name"`
	Type       uint32      `json:"type"`
	LockID     uint32      `json:"lockId"`
	Action     string      `json:"action"`
	OpenSpell  uint32      `json:"openSpell"`
	QuestItems []QuestItem `json:"questItems"`
}
type Catalog struct{ objects map[uint32]Container }

// Decode copies the input into a private immutable index. Tests can supply
// unfamiliar content without adding IDs to gameplay code.
func Decode(data []byte) (*Catalog, error) {
	var source struct {
		Objects map[uint32]Container `json:"objects"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, err
	}
	return &Catalog{objects: source.Objects}, nil
}
func (c *Catalog) Container(entry uint32) (Container, bool) {
	if c == nil {
		return Container{}, false
	}
	r, ok := c.objects[entry]
	r.QuestItems = append([]QuestItem(nil), r.QuestItems...)
	return r, ok
}

//go:embed containers.json
var containers []byte
var once sync.Once
var defaultCatalog *Catalog

func Default() *Catalog {
	once.Do(func() {
		var err error
		defaultCatalog, err = Decode(containers)
		if err != nil {
			panic("invalid generated game catalog: " + err.Error())
		}
	})
	return defaultCatalog
}
