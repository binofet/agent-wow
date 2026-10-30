package buddyquests

import (
	"encoding/binary"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
)

// AzerothCore QuestHandler.cpp consumes an unpacked NPC GUID and quest ID;
// CHOOSE_REWARD appends the zero-based index from the actual reward offer.
func turnInPacket(guid uint64, id uint32) []byte {
	return binary.LittleEndian.AppendUint32(npcPacket(guid), id)
}
func rewardPacket(guid uint64, id, choice uint32) []byte {
	return binary.LittleEndian.AppendUint32(turnInPacket(guid, id), choice)
}

func rewardItems(r *reader, limit uint32) []*questsv1.RewardItem {
	count := r.u32()
	if count > limit {
		r.err = malformed
		return nil
	}
	items := make([]*questsv1.RewardItem, 0, count)
	for i := range count {
		item := &questsv1.RewardItem{Index: i, ItemId: r.u32(), Count: r.u32(), DisplayId: r.u32()}
		if item.ItemId == 0 || item.Count == 0 {
			r.err = malformed
		}
		items = append(items, item)
	}
	return items
}

// Public PlayerMenu::SendQuestGiver{OfferReward,RequestItems} wire layouts.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Creature/GossipDef.cpp
func decodeTurnIn(payload []byte, reward bool) (*questsv1.TurnInOffer, error) {
	if len(payload) > 65536 {
		return nil, malformed
	}
	r := reader{data: payload}
	o := &questsv1.TurnInOffer{NpcGuid: r.u64(), QuestId: r.u32(), Title: r.text(512), Text: r.text(16384)}
	if reward {
		o.Source = "reward_offer"
		active := r.u8()
		o.CanComplete = active == 1
		if active > 1 {
			r.err = malformed
		}
		o.Flags = r.u32()
		r.u32() // suggested players
		emotes := r.u32()
		if emotes > 4 {
			return nil, malformed
		}
		r.take(int(emotes) * 8) // delay, emote
		o.Choices = rewardItems(&r, 6)
		o.Rewards = rewardItems(&r, 4)
		o.RewardMoney, o.RewardXp = r.u32(), r.u32()
		r.take(36 + 60) // honor/multiplier/unknown8/spells/title/talents/arena/unknown; reputation arrays.
	} else {
		o.Source = "requirements"
		r.u32() // unknown
		r.u32() // emote
		if closeOnCancel := r.u32(); closeOnCancel > 1 {
			r.err = malformed
		}
		o.Flags = r.u32()
		r.u32() // suggested players
		o.RequiredMoney = r.u32()
		o.RequiredItems = rewardItems(&r, 6)
		completion := r.u32()
		o.CanComplete = completion == 3
		if completion != 0 && completion != 3 || r.u32() != 4 || r.u32() != 8 || r.u32() != 16 {
			r.err = malformed
		}
	}
	if !creatureGUID(o.NpcGuid) || o.QuestId == 0 || o.Title == "" {
		r.err = malformed
	}
	return o, r.done()
}

// QuestPackets::QuestGiverQuestComplete is a reward receipt, distinct from the
// objective-completion notification SMSG_QUESTUPDATE_COMPLETE.
func decodeRewardReceipt(payload []byte) (*questsv1.RewardReceipt, error) {
	r := reader{data: payload}
	result := &questsv1.RewardReceipt{QuestId: r.u32(), RewardXp: r.u32(), RewardMoney: r.u32(), RewardHonor: r.u32(), RewardTalents: r.u32(), RewardArena: r.u32()}
	if result.QuestId == 0 {
		r.err = malformed
	}
	return result, r.done()
}
