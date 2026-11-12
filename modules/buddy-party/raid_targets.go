package buddyparty

import partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"

const raidIconCount = 8

type raidTargetUpdate struct {
	complete bool
	marks    []*partyv1.RaidTarget
}

// AzerothCore Group::SetTargetIcon sends mode 0, setter GUID, slot, target GUID.
// Group::SendTargetIconList sends mode 1 followed by zero or more slot/GUID pairs.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Groups/Group.cpp
func decodeRaidTargets(payload []byte) (*raidTargetUpdate, error) {
	r := reader{data: payload}
	if len(payload) == 0 {
		return nil, errMalformed
	}
	switch r.u8() {
	case 0:
		if len(r.data) != 8+1+8 {
			return nil, errMalformed
		}
		setter, slot, target := r.u64(), r.u8(), r.u64()
		if slot >= raidIconCount {
			return nil, errMalformed
		}
		return &raidTargetUpdate{marks: []*partyv1.RaidTarget{{Icon: slot + 1, TargetGuid: target, SetterGuid: setter}}}, nil
	case 1:
		if len(r.data)%9 != 0 || len(r.data)/9 > raidIconCount {
			return nil, errMalformed
		}
		out := &raidTargetUpdate{complete: true}
		var slots [raidIconCount]bool
		targets := make(map[uint64]bool)
		for len(r.data) != 0 {
			slot, target := r.u8(), r.u64()
			if slot >= raidIconCount || slots[slot] || target == 0 || targets[target] {
				return nil, errMalformed
			}
			slots[slot], targets[target] = true, true
			out.marks = append(out.marks, &partyv1.RaidTarget{Icon: slot + 1, TargetGuid: target})
		}
		return out, nil
	default:
		return nil, errUnsupported
	}
}

func applyRaidTargets(previous *partyv1.RaidTargetState, groupGUID, revision uint64, update *raidTargetUpdate) *partyv1.RaidTargetState {
	out := &partyv1.RaidTargetState{GroupGuid: groupGUID, Revision: revision, Complete: update.complete}
	var marks [raidIconCount]*partyv1.RaidTarget
	if update.complete {
		out.ListRevision = revision
	} else if previous.GetGroupGuid() == groupGUID {
		out.Complete = previous.Complete
		out.ListRevision = previous.ListRevision
		for _, mark := range previous.Marks {
			marks[mark.Icon-1] = mark
		}
	}
	for _, mark := range update.marks {
		if mark.TargetGuid == 0 {
			marks[mark.Icon-1] = nil
			continue
		}
		// The server gives any target at most one icon. A reassignment
		// supersedes an earlier binding of that same target.
		for i, old := range marks {
			if old.GetTargetGuid() == mark.TargetGuid {
				marks[i] = nil
			}
		}
		marks[mark.Icon-1] = mark
	}
	for _, mark := range marks {
		if mark != nil {
			out.Marks = append(out.Marks, mark)
		}
	}
	return out
}

func sameMarkerRoster(a, b *partyv1.Group) bool {
	if !a.GetKnown() || !b.GetKnown() || !a.GetInGroup() || !b.GetInGroup() || a.GroupGuid == 0 || a.GroupGuid != b.GroupGuid || a.TypeFlags != b.TypeFlags || len(a.Members) != len(b.Members) {
		return false
	}
	// AzerothCore clears icons on a non-raid member add. A new roster
	// therefore invalidates the old view; an ordinary counter update does not.
	set := make(map[uint64]bool, len(a.Members))
	for _, member := range a.Members {
		set[member.Guid] = true
	}
	for _, member := range b.Members {
		if !set[member.Guid] {
			return false
		}
	}
	return true
}
