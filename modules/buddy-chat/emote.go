package buddychat

import (
	"encoding/binary"
	"errors"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
)

// CMSG_TEXT_EMOTE is uint32 textEmote, uint32 emoteNum, uint64 target GUID.
// The emote number is fixed at 0; the server selects its animation via DBC.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ChatHandler.cpp#L657
// Text IDs: BOW=17, CHEER=21, WAVE=101 (EmotesText.dbc enum):
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/SharedDefines.h#L1572
// Player GUIDs have high type bits 0 and can include realm bits 32–47:
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Object/ObjectGuid.h
func encodeEmote(request *chatv1.EmoteRequest) ([]byte, error) {
	if request == nil {
		return nil, errors.New("emote request is required")
	}
	switch request.Emote {
	case chatv1.Emote_WAVE, chatv1.Emote_BOW, chatv1.Emote_CHEER:
	default:
		return nil, errors.New("emote supports only WAVE, BOW, and CHEER")
	}
	if target := request.TargetGuid; target != 0 && (target>>48 != 0 || uint32(target) == 0) {
		return nil, errors.New("target_guid must be 0 for untargeted or a player GUID with a nonzero character counter")
	}
	payload := make([]byte, 16)
	binary.LittleEndian.PutUint32(payload, uint32(request.Emote))
	// emoteNum at bytes 4–7 remains 0; arbitrary variants are not exposed.
	binary.LittleEndian.PutUint64(payload[8:], request.TargetGuid)
	return payload, nil
}
