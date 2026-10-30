package buddychat

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
)

func TestEncodeChatWireLayout(t *testing.T) {
	for _, tc := range []struct {
		channel  chatv1.Channel
		language chatv1.Language
		want     string
	}{
		{chatv1.Channel_SAY, chatv1.Language_COMMON, "010000000700000068656c6c6f00"},
		{chatv1.Channel_PARTY, chatv1.Language_ORCISH, "020000000100000068656c6c6f00"},
	} {
		got, err := encodeChat(&chatv1.SendRequest{Channel: tc.channel, Language: tc.language, Text: "hello"})
		if err != nil || hex.EncodeToString(got) != tc.want {
			t.Fatalf("unexpected CMSG_MESSAGECHAT payload %x, %v; want %s", got, err, tc.want)
		}
	}
	// The server's limit is bytes, so a 256-byte string with only 128 runes fails.
	if _, err := encodeChat(&chatv1.SendRequest{Channel: chatv1.Channel_SAY, Language: chatv1.Language_COMMON, Text: strings.Repeat("é", 128)}); err == nil {
		t.Fatal("accepted text longer than the server's byte limit")
	}
	if _, err := encodeChat(&chatv1.SendRequest{Channel: chatv1.Channel_SAY, Language: chatv1.Language_COMMON, Text: strings.Repeat("a", 255)}); err != nil {
		t.Fatal("rejected the exact server byte limit", err)
	}
}

func TestEncodeChatRejectsInvalidRequests(t *testing.T) {
	for _, text := range []string{"", " \t", "hello\x00injected", "hello\nworld", "hello\rworld", "hello\x01", "|Hinvalid", ".account create", "!gm on", string([]byte{0xff})} {
		if _, err := encodeChat(&chatv1.SendRequest{Channel: chatv1.Channel_SAY, Language: chatv1.Language_COMMON, Text: text}); err == nil {
			t.Errorf("accepted invalid text %q", text)
		}
	}
	for _, text := range []string{"...ready", "!!", ".", ". hello", "hello!"} {
		if _, err := encodeChat(&chatv1.SendRequest{Channel: chatv1.Channel_SAY, Language: chatv1.Language_COMMON, Text: text}); err != nil {
			t.Errorf("rejected ordinary punctuation %q: %v", text, err)
		}
	}
	for _, request := range []*chatv1.SendRequest{
		nil,
		{Channel: chatv1.Channel_WHISPER, Language: chatv1.Language_COMMON, Text: "hello"},
		{Channel: chatv1.Channel_PARTY_LEADER, Language: chatv1.Language_COMMON, Text: "hello"},
		{Channel: chatv1.Channel_SAY, Text: "hello"},
		{Channel: chatv1.Channel_SAY, Language: chatv1.Language(999), Text: "hello"},
	} {
		if _, err := encodeChat(request); err == nil {
			t.Errorf("accepted unsupported send request %v", request)
		}
	}
}

func TestDecodePlayerChatGoldenPacket(t *testing.T) {
	// Literal SMSG_MESSAGECHAT fixture following BuildChatPacket's default
	// branch: SAY, COMMON, sender 42, flags 0, receiver 0, six text bytes, tag 0.
	payload, err := hex.DecodeString("01070000002a000000000000000000000000000000000000000600000068656c6c6f0000")
	if err != nil {
		t.Fatal(err)
	}
	message, err := decodeChat(payload)
	if err != nil || message.Channel != chatv1.Channel_SAY || message.Language != 7 || message.SenderGuid != 42 || message.ReceiverGuid != 0 || message.Text != "hello" || message.ChatTag != 0 {
		t.Fatalf("decoded wrong fields: %v, %v", message, err)
	}
	for _, channel := range []chatv1.Channel{chatv1.Channel_PARTY, chatv1.Channel_WHISPER, chatv1.Channel_PARTY_LEADER} {
		packet := makePlayerChat(channel, 123, 42, "hi é")
		message, err := decodeChat(packet)
		if err != nil || message.Channel != channel || message.ReceiverGuid != 42 || message.Text != "hi é" {
			t.Fatalf("failed supported player chat %v: %v, %v", channel, message, err)
		}
	}
}

func TestDecodeChatRejectsTruncatedOrMalformedPackets(t *testing.T) {
	valid := makePlayerChat(chatv1.Channel_WHISPER, 123, 42, "hello")
	for end := 0; end < len(valid); end++ {
		if _, err := decodeChat(valid[:end]); err == nil {
			t.Fatalf("accepted truncated packet ending at byte %d", end)
		}
	}
	for _, mutate := range []func([]byte) []byte{
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[25:], 0xffffffff); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[25:], 0); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[25:], 257); return p },
		func(p []byte) []byte { p[len(p)-2] = 'x'; return p },
		func(p []byte) []byte { p[30] = 0; return p },
		func(p []byte) []byte { p[29] = 0xff; return p },
		func(p []byte) []byte { return append(p, 1) },
	} {
		if _, err := decodeChat(mutate(append([]byte(nil), valid...))); err == nil {
			t.Fatal("accepted malformed player chat")
		}
	}
}

func TestDecodeChatIgnoresUnsupportedTypesAndAddons(t *testing.T) {
	for _, packet := range [][]byte{
		{0},  // SYSTEM has another purpose; no parsing of its body is attempted.
		{12}, // MONSTER_SAY has a different, variable-length layout.
		{17}, // CHANNEL also has a different layout.
		makePlayerChat(chatv1.Channel_SAY, 0, 0, "system"),
		makePlayerChat(chatv1.Channel_SAY, 0xf130000000000001, 0, "npc"),
	} {
		if _, err := decodeChat(packet); !errors.Is(err, errUnsupportedChat) {
			t.Fatalf("unsupported packet not ignored: %x, %v", packet, err)
		}
	}
	addon := makePlayerChat(chatv1.Channel_PARTY, 42, 0, "addon")
	binary.LittleEndian.PutUint32(addon[1:], 0xffffffff)
	if _, err := decodeChat(addon); !errors.Is(err, errUnsupportedChat) {
		t.Fatal("addon traffic not ignored", err)
	}
}

func FuzzDecodeChat(f *testing.F) {
	f.Add(makePlayerChat(chatv1.Channel_SAY, 42, 0, "hello"))
	f.Add([]byte{12})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, packet []byte) {
		message, err := decodeChat(packet)
		if err == nil && (message == nil || len(message.Text) == 0 || len(message.Text) > maxTextBytes) {
			t.Fatal("decoder accepted an invalid message")
		}
	})
}

func makePlayerChat(channel chatv1.Channel, sender, receiver uint64, text string) []byte {
	packet := make([]byte, 30+len(text)+1)
	packet[0] = byte(channel)
	binary.LittleEndian.PutUint32(packet[1:], 7)
	binary.LittleEndian.PutUint64(packet[5:], sender)
	binary.LittleEndian.PutUint64(packet[17:], receiver)
	binary.LittleEndian.PutUint32(packet[25:], uint32(len(text)+1))
	copy(packet[29:], text)
	return packet
}
