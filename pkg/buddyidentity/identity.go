// Package buddyidentity validates the explicitly selected buddy operating mode.
package buddyidentity

import (
	"errors"
	"strconv"
)

const CompanionOwnerGUID uint64 = 3

// Identity binds observations and controls to the actual session character.
// OwnerGUID is zero only when independent mode was explicitly selected.
type Identity struct {
	SelfGUID  uint64
	OwnerGUID uint64
}

// Parse reads AGENT_WOW_CHARACTER_GUID and BUDDY_INDEPENDENT values. An absent
// mode retains companion behavior; invalid input never enables independence.
func Parse(characterGUID, independent string) (Identity, error) {
	self, err := strconv.ParseUint(characterGUID, 10, 64)
	if err != nil || self == 0 || self>>48 != 0 {
		return Identity{}, errors.New("AGENT_WOW_CHARACTER_GUID must identify a player")
	}
	owner := CompanionOwnerGUID
	switch independent {
	case "", "false":
	case "true":
		owner = 0
	default:
		return Identity{}, errors.New("BUDDY_INDEPENDENT must be true or false")
	}
	if self == owner {
		return Identity{}, errors.New("companion character must be distinct from owner GUID3; independent mode requires BUDDY_INDEPENDENT=true")
	}
	return Identity{SelfGUID: self, OwnerGUID: owner}, nil
}

func (i Identity) Independent() bool { return i.SelfGUID != 0 && i.OwnerGUID == 0 }
