package gamecatalog

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed lockpicking.json
var lockpickingData []byte

var lockpickingRequirements = sync.OnceValue(func() map[uint32]uint32 {
	var catalog struct {
		Requirements map[uint32]uint32 `json:"requirements"`
	}
	if err := json.Unmarshal(lockpickingData, &catalog); err != nil {
		panic("invalid generated lockpicking catalog: " + err.Error())
	}
	return catalog.Requirements
})

// LockpickingRequirement returns the minimum actual Pick Lock skill for a
// build-12340 lock. A missing row means the lock has no Pick Lock skill case.
func LockpickingRequirement(lockID uint32) (uint32, bool) {
	rank, ok := lockpickingRequirements()[lockID]
	return rank, ok
}
