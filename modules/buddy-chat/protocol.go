// Package buddychat implements explicit player chat for a WoW 3.3.5a session.
package buddychat

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
)

const maxTextBytes = 255

var errUnsupportedChat = errors.New("unsupported chat")

// Wire layout and limits verified against AzerothCore's 3.3.5a implementation:
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ChatHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Chat/Chat.cpp#L250
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/SharedDefines.h
// Outbound: uint32 type, uint32 language, optional whisper recipient, message.
// Both strings are NUL terminated. Universal
// language is rejected by the server for ordinary client chat.
func encodeChat(request *chatv1.SendRequest) ([]byte, error) {
	if request == nil {
		return nil, errors.New("send request is required")
	}
	if request.Channel != chatv1.Channel_SAY && request.Channel != chatv1.Channel_PARTY && request.Channel != chatv1.Channel_WHISPER {
		return nil, errors.New("send supports only SAY, PARTY, and WHISPER")
	}
	if request.Channel == chatv1.Channel_WHISPER {
		if len(request.Recipient) > 48 || utf8.RuneCountInString(request.Recipient) < 2 || utf8.RuneCountInString(request.Recipient) > 12 || !utf8.ValidString(request.Recipient) {
			return nil, errors.New("whisper recipient must be a character name of 2–12 letters")
		}
		for _, c := range request.Recipient {
			if !unicode.IsLetter(c) {
				return nil, errors.New("whisper recipient must contain only letters")
			}
		}
	} else if request.Recipient != "" {
		return nil, errors.New("recipient is only valid for WHISPER")
	}
	if request.Language != chatv1.Language_COMMON && request.Language != chatv1.Language_ORCISH {
		return nil, errors.New("choose COMMON or ORCISH explicitly; the character must know that language")
	}
	if len(request.Text) == 0 || len(request.Text) > maxTextBytes || strings.TrimSpace(request.Text) == "" {
		return nil, errors.New("text must contain 1–255 UTF-8 bytes of nonblank text")
	}
	if !utf8.ValidString(request.Text) {
		return nil, errors.New("text must be valid UTF-8")
	}
	// ParseCommands runs before normal delivery. Preserve ordinary ellipses
	// and punctuation while refusing the server's .command / !command forms.
	if len(request.Text) > 1 && (request.Text[0] == '.' || request.Text[0] == '!') && request.Text[1] != request.Text[0] && request.Text[1] != ' ' {
		return nil, errors.New("server commands are not supported by chat send")
	}
	for _, c := range request.Text {
		if c < 0x20 || c == 0x7f || c == '|' {
			return nil, errors.New("text must be plain chat without NUL, control characters, or WoW markup (|)")
		}
	}
	size := 8 + len(request.Text) + 1
	if request.Channel == chatv1.Channel_WHISPER {
		size += len(request.Recipient) + 1
	}
	payload := make([]byte, size)
	binary.LittleEndian.PutUint32(payload, uint32(request.Channel))
	binary.LittleEndian.PutUint32(payload[4:], uint32(request.Language))
	offset := 8
	if request.Channel == chatv1.Channel_WHISPER {
		copy(payload[offset:], request.Recipient)
		offset += len(request.Recipient) + 1
	}
	copy(payload[offset:], request.Text)
	return payload, nil
}

// Supported SMSG_MESSAGECHAT layout: uint8 type, int32 language, uint64 sender,
// uint32 flags, uint64 receiver, uint32 text length INCLUDING NUL, text, uint8 tag.
// GM-message, monster, channel, addon, and other layouts are intentionally ignored.
func decodeChat(payload []byte) (*chatv1.Message, error) {
	if len(payload) == 0 {
		return nil, errors.New("missing chat type")
	}
	channel := chatv1.Channel(payload[0])
	switch channel {
	case chatv1.Channel_SAY, chatv1.Channel_PARTY, chatv1.Channel_WHISPER, chatv1.Channel_WHISPER_INFORM, chatv1.Channel_PARTY_LEADER:
	default:
		return nil, errUnsupportedChat
	}
	if len(payload) < 31 {
		return nil, errors.New("truncated chat header")
	}
	language := int32(binary.LittleEndian.Uint32(payload[1:5]))
	sender := binary.LittleEndian.Uint64(payload[5:13])
	if language == -1 || sender == 0 || sender>>48 != 0 {
		return nil, errUnsupportedChat
	}
	length := binary.LittleEndian.Uint32(payload[25:29])
	if length < 2 || length > maxTextBytes+1 || len(payload) != 29+int(length)+1 {
		return nil, errors.New("invalid chat text length")
	}
	text := payload[29 : 29+int(length)-1]
	if payload[29+int(length)-1] != 0 || bytes.IndexByte(text, 0) >= 0 || !utf8.Valid(text) {
		return nil, errors.New("invalid chat text encoding or terminator")
	}
	return &chatv1.Message{
		Channel: channel, Language: language, SenderGuid: sender,
		ReceiverGuid: binary.LittleEndian.Uint64(payload[17:25]), Text: string(text),
		ChatTag: uint32(payload[len(payload)-1]),
	}, nil
}
