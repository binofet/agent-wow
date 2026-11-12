// Package buddyparty exposes explicit group actions and server-observed state.
package buddyparty

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode"
	"unicode/utf8"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
)

// Protocol sources (normal public client/server packets, never database access):
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/GroupHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Groups/Group.cpp#L1759
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Groups/Group.h
const maxMembers = 40

var (
	errMalformed   = errors.New("malformed party packet")
	errUnsupported = errors.New("unsupported party packet variant")
)

type reader struct {
	data []byte
	err  error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.data) {
		r.err = errMalformed
		return nil
	}
	value := r.data[:n]
	r.data = r.data[n:]
	return value
}
func (r *reader) u8() uint32 {
	value := r.take(1)
	if value == nil {
		return 0
	}
	return uint32(value[0])
}
func (r *reader) u32() uint32 {
	value := r.take(4)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(value)
}
func (r *reader) u64() uint64 {
	value := r.take(8)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(value)
}
func (r *reader) name(allowEmpty bool) string {
	if r.err != nil {
		return ""
	}
	end := bytes.IndexByte(r.data[:min(len(r.data), 49)], 0)
	if end < 0 {
		r.err = errMalformed
		return ""
	}
	value := string(r.take(end + 1)[:end])
	if (!allowEmpty && value == "") || !utf8.ValidString(value) {
		r.err = errMalformed
	}
	for _, c := range value {
		if unicode.IsControl(c) || c == '|' {
			r.err = errMalformed
		}
	}
	return value
}
func (r *reader) finish() error {
	if r.err != nil || len(r.data) != 0 {
		return errMalformed
	}
	return nil
}
func playerGUID(guid uint64) bool { return guid != 0 && guid>>48 == 0 && uint32(guid) != 0 }

// CMSG_GROUP_INVITE: name CString followed by an ignored uint32, fixed to zero.
func encodeInvite(name string) ([]byte, error) {
	if !utf8.ValidString(name) || len(name) > 48 || utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 12 {
		return nil, errors.New("name must contain 2–12 Unicode letters and at most 48 UTF-8 bytes")
	}
	for _, c := range name {
		if !unicode.IsLetter(c) {
			return nil, errors.New("name must contain only Unicode letters; realm suffixes are unsupported")
		}
	}
	payload := make([]byte, len(name)+5)
	copy(payload, name)
	return payload, nil
}

// SMSG_GROUP_INVITE currently emits flag + name + uint32 + uint8(0) + uint32.
func decodeInvite(payload []byte) (string, bool, error) {
	r := reader{data: payload}
	flag, name := r.u8(), r.name(false)
	r.u32()
	count := r.u8()
	r.u32()
	if err := r.finish(); err != nil {
		return "", false, err
	}
	if flag > 1 {
		return "", false, errMalformed
	}
	if count != 0 {
		return "", false, errUnsupported
	}
	return name, flag == 1, nil
}

func decodeGroup(payload []byte, selfGUID uint64, selfName string) (*partyv1.Group, error) {
	if len(payload) > 8192 {
		return nil, errMalformed
	}
	r := reader{data: payload}
	flags, subgroup, memberFlags, roles := r.u8(), r.u8(), r.u8(), r.u8()
	if flags & ^uint32(0x1f) != 0 {
		return nil, errUnsupported
	}
	if flags&8 != 0 {
		r.take(5)
	} // LFG save status and dungeon entry.
	groupGUID, counter, count := r.u64(), r.u32(), r.u32()
	if r.err != nil || count >= maxMembers || subgroup > 7 {
		return nil, errMalformed
	}
	group := &partyv1.Group{Known: true, GroupGuid: groupGUID, TypeFlags: flags, ServerCounter: counter}
	members := make([]*partyv1.Member, 0, int(count)+1)
	members = append(members, &partyv1.Member{Guid: selfGUID, Name: selfName, Self: true, Online: true, Subgroup: subgroup, Flags: memberFlags, Roles: roles})
	seen := map[uint64]bool{selfGUID: true}
	for range count {
		member := &partyv1.Member{Name: r.name(false), Guid: r.u64(), WireStatus: r.u8(), Subgroup: r.u8(), Flags: r.u8(), Roles: r.u8()}
		if r.err != nil || !playerGUID(member.Guid) || seen[member.Guid] || member.Subgroup > 7 {
			return nil, errMalformed
		}
		seen[member.Guid] = true
		member.Online = member.WireStatus&1 != 0
		members = append(members, member)
	}
	group.LeaderGuid = r.u64()
	if count != 0 {
		r.take(13)
	} // Loot method, looter GUID, threshold, difficulties.
	if err := r.finish(); err != nil {
		return nil, err
	}
	if flags&0x10 != 0 {
		if count != 0 || group.LeaderGuid != 0 {
			return nil, errMalformed
		}
		return &partyv1.Group{Known: true, TypeFlags: flags, ServerCounter: counter}, nil
	}
	if groupGUID>>48 != 0x1f50 || uint32(groupGUID) == 0 || !playerGUID(group.LeaderGuid) || !seen[group.LeaderGuid] {
		return nil, errMalformed
	}
	group.InGroup, group.Members = true, members
	return group, nil
}

func decodeCommandResult(payload []byte) (*partyv1.CommandResult, error) {
	r := reader{data: payload}
	result := &partyv1.CommandResult{Operation: r.u32(), MemberName: r.name(true), Result: r.u32(), Value: r.u32()}
	return result, r.finish()
}

func decodeDecline(payload []byte) (string, error) {
	r := reader{data: payload}
	name := r.name(false)
	return name, r.finish()
}
