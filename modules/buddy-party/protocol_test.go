package buddyparty

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const groupGUID = uint64(0x1f50000000000001)

// Fixed public wire fixtures, independent of the decoder's field readers.
const duoHex = "00000000010000000000501f07000000010000004a6f656c00030000000000000001000000030000000000000003000000000000000002000000"
const leftHex = "10000000010000000000501f08000000000000000000000000000000"
const inviteHex = "014a6f656c00000000000000000000"

func unhex(t testing.TB, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInviteWireAndValidation(t *testing.T) {
	payload, err := encodeInvite("Joel")
	if err != nil || hex.EncodeToString(payload) != "4a6f656c0000000000" {
		t.Fatalf("CMSG_GROUP_INVITE mismatch: %x, %v", payload, err)
	}
	for _, name := range []string{"", "J", strings.Repeat("a", 13), "Joel\x00", "Jo el", "Joel-Realm", "Jo|el", string([]byte{0xff, 0xfe})} {
		if _, err := encodeInvite(name); err == nil {
			t.Errorf("invalid name accepted: %q", name)
		}
	}
	if _, err := encodeInvite("Éowyn"); err != nil {
		t.Fatal("valid UTF-8 name rejected", err)
	}
	payload = unhex(t, inviteHex)
	name, active, err := decodeInvite(payload)
	if err != nil || name != "Joel" || !active {
		t.Fatalf("SMSG_GROUP_INVITE mismatch: %q, %v, %v", name, active, err)
	}
	payload[0] = 0
	if name, active, err := decodeInvite(payload); err != nil || active || name != "Joel" {
		t.Fatalf("already-grouped notice interpreted as invitation: %q %v %v", name, active, err)
	}
	payload[0] = 2
	if _, _, err := decodeInvite(payload); !errors.Is(err, errMalformed) {
		t.Fatal("unknown invitation flag accepted", err)
	}
	payload[0], payload[10] = 1, 1
	if _, _, err := decodeInvite(payload); !errors.Is(err, errUnsupported) {
		t.Fatal("unsupported invitation count accepted", err)
	}
	for n := range len(payload) {
		if _, _, err := decodeInvite(payload[:n]); err == nil {
			t.Fatalf("truncated invite accepted at byte %d", n)
		}
	}
	if _, _, err := decodeInvite(append(unhex(t, inviteHex), 0)); err == nil {
		t.Fatal("invite trailing bytes accepted")
	}
}

func TestGroupListGoldenAndLeaveSentinel(t *testing.T) {
	group, err := decodeGroup(unhex(t, duoHex), 2, "Arlen")
	if err != nil || !group.Known || !group.InGroup || group.GroupGuid != groupGUID || group.LeaderGuid != 3 || group.ServerCounter != 7 || len(group.Members) != 2 {
		t.Fatalf("duo group mismatch: %v, %v", group, err)
	}
	if self, joel := group.Members[0], group.Members[1]; !self.Self || self.Name != "Arlen" || self.Guid != 2 || !self.Online || joel.Self || joel.Name != "Joel" || joel.Guid != 3 || !joel.Online || joel.WireStatus != 1 {
		t.Fatalf("member identity/status mismatch: %v", group.Members)
	}
	group, err = decodeGroup(unhex(t, leftHex), 2, "Arlen")
	if err != nil || !group.Known || group.InGroup || group.GroupGuid != 0 || group.LeaderGuid != 0 || len(group.Members) != 0 || group.ServerCounter != 8 {
		t.Fatalf("old nonzero GUID in leave sentinel retained a group: %v, %v", group, err)
	}
	// LFG adds save status + dungeon entry immediately after the four header bytes.
	lfg := append([]byte{8, 0, 0, 0, 1, 2, 0, 0, 0}, unhex(t, duoHex)[4:]...)
	group, err = decodeGroup(lfg, 2, "Arlen")
	if err != nil || group.TypeFlags != 8 || len(group.Members) != 2 {
		t.Fatalf("LFG framing lost alignment: %v, %v", group, err)
	}
}

func TestGroupListRejectsMalformedAndOversizedState(t *testing.T) {
	payload := unhex(t, duoHex)
	for n := range len(payload) {
		if _, err := decodeGroup(payload[:n], 2, "Arlen"); err == nil {
			t.Fatalf("truncated group accepted at byte %d", n)
		}
	}
	for _, mutate := range []func([]byte){
		func(p []byte) { p[1] = 8 },  // Self subgroup.
		func(p []byte) { p[11] = 0 }, // Not a group GUID.
		func(p []byte) { binary.LittleEndian.PutUint32(p[16:], 40) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[16:], ^uint32(0)) },
		func(p []byte) { binary.LittleEndian.PutUint64(p[25:], 2) }, // Duplicate self.
		func(p []byte) { binary.LittleEndian.PutUint64(p[25:], 0) },
		func(p []byte) { p[34] = 8 },                                // Other member subgroup.
		func(p []byte) { binary.LittleEndian.PutUint64(p[37:], 4) }, // Leader absent.
		func(p []byte) { p[0] = 0x10 },                              // Leave sentinel with members.
	} {
		bad := bytes.Clone(payload)
		mutate(bad)
		if _, err := decodeGroup(bad, 2, "Arlen"); err == nil {
			t.Fatalf("malformed group accepted: %x", bad)
		}
	}
	if _, err := decodeGroup(append(bytes.Clone(payload), 0), 2, "Arlen"); err == nil {
		t.Fatal("group trailing bytes accepted")
	}
	if _, err := decodeGroup(make([]byte, 8193), 2, "Arlen"); err == nil {
		t.Fatal("oversized packet accepted")
	}
	payload[0] = 0x80
	if _, err := decodeGroup(payload, 2, "Arlen"); !errors.Is(err, errUnsupported) {
		t.Fatal("unknown group type not ignored", err)
	}
}

func TestGroupListMaximumRaidAndDuplicateMember(t *testing.T) {
	payload := make([]byte, 20)
	payload[0] = 2 // Raid.
	binary.LittleEndian.PutUint64(payload[4:], groupGUID)
	binary.LittleEndian.PutUint32(payload[16:], 39)
	for i := uint64(3); i < 42; i++ {
		payload = append(payload, 'J', 'o', 0)
		payload = binary.LittleEndian.AppendUint64(payload, i)
		payload = append(payload, 1, 7, 0, 0)
	}
	payload = binary.LittleEndian.AppendUint64(payload, 2)
	payload = append(payload, make([]byte, 13)...)
	group, err := decodeGroup(payload, 2, "Arlen")
	if err != nil || len(group.Members) != 40 || group.Members[39].Guid != 41 {
		t.Fatalf("maximum raid rejected: %v, %v", group, err)
	}
	// Two other members may not share one GUID.
	binary.LittleEndian.PutUint64(payload[38:], 3)
	if _, err := decodeGroup(payload, 2, "Arlen"); err == nil {
		t.Fatal("duplicate other-member GUID accepted")
	}
}

func TestPartyResultAndDeclineWire(t *testing.T) {
	payload := unhex(t, "000000004a6f656c000000000000000000")
	result, err := decodeCommandResult(payload)
	if err != nil || result.Operation != 0 || result.MemberName != "Joel" || result.Result != 0 || result.Value != 0 {
		t.Fatalf("command result mismatch: %v, %v", result, err)
	}
	if result, err := decodeCommandResult(make([]byte, 13)); err != nil || result.MemberName != "" {
		t.Fatal("server's empty result name rejected", result, err)
	}
	for n := range len(payload) {
		if _, err := decodeCommandResult(payload[:n]); err == nil {
			t.Fatalf("truncated result accepted at byte %d", n)
		}
	}
	if name, err := decodeDecline([]byte("Joel\x00")); err != nil || name != "Joel" {
		t.Fatalf("decline mismatch: %q, %v", name, err)
	}
	for _, bad := range [][]byte{nil, {0}, []byte("Joel"), []byte("Joel\x00\x00"), []byte("Jo\x01el\x00"), []byte("Jo|el\x00"), append(bytes.Repeat([]byte{'x'}, 49), 0), {255, 0}} {
		if _, err := decodeDecline(bad); err == nil {
			t.Errorf("invalid name packet accepted: %x", bad)
		}
	}
}

func FuzzPartyPackets(f *testing.F) {
	f.Add(unhex(f, duoHex))
	f.Add(unhex(f, inviteHex))
	f.Add(unhex(f, leftHex))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, payload []byte) {
		decodeInvite(payload)
		group, err := decodeGroup(payload, 2, "Arlen")
		if err == nil && (group == nil || len(group.Members) > maxMembers) {
			t.Fatalf("invalid bounded state: %v", group)
		}
		decodeCommandResult(payload)
		decodeDecline(payload)
	})
}
