// Package buddyquests implements explicitly requested normal NPC quest pickup.
package buddyquests

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode/utf8"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
)

// Public AzerothCore 3.3.5a packet producers/consumers:
// src/server/game/Handlers/QuestHandler.cpp
// src/server/game/Entities/Creature/GossipDef.cpp
// src/server/game/Entities/Player/PlayerQuest.cpp
// https://github.com/azerothcore/azerothcore-wotlk
const autoAcceptFlag = uint32(0x80000)

var malformed = errors.New("malformed quest packet")

type reader struct {
	data []byte
	err  error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.data) {
		r.err = malformed
		return nil
	}
	v := r.data[:n]
	r.data = r.data[n:]
	return v
}
func (r *reader) u8() uint32 {
	p := r.take(1)
	if p == nil {
		return 0
	}
	return uint32(p[0])
}
func (r *reader) u32() uint32 {
	p := r.take(4)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(p)
}
func (r *reader) u64() uint64 {
	p := r.take(8)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(p)
}
func (r *reader) text(limit int) string {
	if r.err != nil {
		return ""
	}
	n := bytes.IndexByte(r.data[:min(len(r.data), limit+1)], 0)
	if n < 0 {
		r.err = malformed
		return ""
	}
	s := string(r.take(n + 1)[:n])
	if !utf8.ValidString(s) {
		r.err = malformed
	}
	for _, c := range s {
		if (c < 32 && c != '\n' && c != '\r' && c != '\t') || c == 127 {
			r.err = malformed
		}
	}
	return s
}
func (r *reader) done() error {
	if r.err != nil || len(r.data) != 0 {
		return malformed
	}
	return nil
}
func creatureGUID(guid uint64) bool { return guid>>48 == 0xf130 && guid&0xffffff != 0 }

func npcPacket(guid uint64) []byte { return binary.LittleEndian.AppendUint64(nil, guid) }
func questPacket(guid uint64, id uint32, accept bool) []byte {
	p := binary.LittleEndian.AppendUint32(npcPacket(guid), id)
	if accept {
		return binary.LittleEndian.AppendUint32(p, 0)
	}
	return append(p, 0)
}

func menuQuest(r *reader) *questsv1.Quest {
	q := &questsv1.Quest{QuestId: r.u32(), Icon: r.u32(), Level: int32(r.u32()), Flags: r.u32()}
	repeat := r.u8()
	q.Repeatable = repeat == 1
	q.Title = r.text(512)
	if q.QuestId == 0 || q.Title == "" || repeat > 1 {
		r.err = malformed
	}
	// Icon2 is an ordinary available quest. Other icons include completion,
	// repeatable/autocomplete dialogs, which this pickup-only slice cannot take.
	q.Available = q.Icon == 2 && q.Flags&autoAcceptFlag == 0
	return q
}
func menuQuests(r *reader, count uint32) []*questsv1.Quest {
	if count > 32 {
		r.err = malformed
		return nil
	}
	quests := make([]*questsv1.Quest, 0, int(count))
	seen := map[uint32]bool{}
	for range count {
		q := menuQuest(r)
		if seen[q.QuestId] {
			r.err = malformed
		}
		seen[q.QuestId] = true
		quests = append(quests, q)
	}
	return quests
}

func decodeList(payload []byte, gossip bool) (*questsv1.Offer, error) {
	if len(payload) > 65536 {
		return nil, malformed
	}
	r := reader{data: payload}
	offer := &questsv1.Offer{NpcGuid: r.u64(), Source: "quest_list"}
	if gossip {
		offer.Source = "gossip_quest_list"
		r.u32()
		r.u32()
		count := r.u32()
		if count > 32 {
			return nil, malformed
		}
		for range count {
			r.u32()
			r.u8()
			coded := r.u8()
			r.u32()
			r.text(4096)
			r.text(4096)
			if coded > 1 {
				r.err = malformed
			}
		}
		offer.Quests = menuQuests(&r, r.u32())
	} else {
		offer.Greeting = r.text(8192)
		r.u32()
		r.u32()
		offer.Quests = menuQuests(&r, r.u8())
	}
	if !creatureGUID(offer.NpcGuid) {
		r.err = malformed
	}
	return offer, r.done()
}

func decodeDetails(payload []byte) (*questsv1.Offer, error) {
	if len(payload) > 65536 {
		return nil, malformed
	}
	r := reader{data: payload}
	offer := &questsv1.Offer{NpcGuid: r.u64(), Source: "quest_details"}
	divider := r.u64()
	q := &questsv1.Quest{QuestId: r.u32(), Title: r.text(512), Details: r.text(16384), Objectives: r.text(8192)}
	active := r.u8()
	q.Flags = r.u32()
	r.u32()
	finished := r.u8()
	choices := r.u32()
	if choices > 6 {
		return nil, malformed
	}
	r.take(int(choices) * 12)
	items := r.u32()
	if items > 4 {
		return nil, malformed
	}
	r.take(int(items) * 12)
	r.take(8 + 32 + 60) // money, XP; honor/spells/title/talents/arena; reputation arrays.
	emotes := r.u32()
	if emotes > 4 {
		return nil, malformed
	}
	r.take(int(emotes) * 8)
	if !creatureGUID(offer.NpcGuid) || divider != 0 || q.QuestId == 0 || q.Title == "" || active > 1 || finished != 0 {
		r.err = malformed
	}
	q.Available = active == 1 && q.Flags&autoAcceptFlag == 0
	offer.Quests = []*questsv1.Quest{q}
	return offer, r.done()
}
