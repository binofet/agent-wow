package buddychat

import (
	"context"
	"encoding/binary"
	"errors"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	"github.com/agent-wow/agent-wow/pkg/emotecatalog"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Emotes advertises the IDs accepted by THIS installed native bridge. It is
// read-only and does not infer server acceptance from a catalog entry.
func (s *Service) Emotes(ctx context.Context, _ *emptypb.Empty) (*chatv1.EmoteCatalog, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := &chatv1.EmoteCatalog{SessionId: s.sessionID, SelfGuid: s.selfGUID, Revision: emotecatalog.Revision(), Emotes: []*chatv1.EmoteEntry{}}
	for _, entry := range emotecatalog.All().Emotes {
		result.Emotes = append(result.Emotes, &chatv1.EmoteEntry{Id: entry.ID, Name: entry.Name})
	}
	return result, nil
}

// CMSG_TEXT_EMOTE is uint32 textEmote, uint32 emoteNum, uint64 target GUID.
// The emote number is fixed at 0; the server selects its animation via DBC.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ChatHandler.cpp#L657
// All known TextEmotes IDs come from the shared, source-pinned JSON catalog.
// Player GUIDs have high type bits 0 and can include realm bits 32–47:
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Object/ObjectGuid.h
func encodeEmote(request *chatv1.EmoteRequest) ([]byte, error) {
	if request == nil {
		return nil, errors.New("emote request is required")
	}
	if request.Emote <= 0 {
		return nil, errors.New("emote must be a positive catalog TextEmotes ID")
	}
	if _, ok := emotecatalog.Lookup(uint32(request.Emote)); !ok {
		return nil, errors.New("unknown catalog TextEmotes ID")
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
