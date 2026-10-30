package buddyquests

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

const npcGUID = uint64(0xf130000033000001)
const listHex = "01000033000030f14772656574696e677300000000000000000001070000000200000001000000000000000041205465737420517565737400"
const gossipHex = "01000033000030f100000000440000000000000001000000070000000200000001000000000000000041205465737420517565737400"
const detailsHex = "01000033000030f10000000000000000070000004120546573742051756573740046696e642074686520706572736f6e2e00537065616b20746f207468656d2e0001000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000000000000"

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	p, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuestWireGolden(t *testing.T) {
	if got := hex.EncodeToString(npcPacket(npcGUID)); got != "01000033000030f1" {
		t.Fatal(got)
	}
	if got := hex.EncodeToString(questPacket(npcGUID, 7, true)); got != "01000033000030f10700000000000000" {
		t.Fatal("accept packet", got)
	}
	if got := hex.EncodeToString(questPacket(npcGUID, 7, false)); got != "01000033000030f10700000000" {
		t.Fatal("details packet", got)
	}
	for _, gossip := range []bool{false, true} {
		data := unhex(t, listHex)
		if gossip {
			data = unhex(t, gossipHex)
		}
		o, err := decodeList(data, gossip)
		if err != nil || o.NpcGuid != npcGUID || len(o.Quests) != 1 || o.Quests[0].QuestId != 7 || o.Quests[0].Title != "A Test Quest" || !o.Quests[0].Available || o.Quests[0].Level != 1 {
			t.Fatalf("list mismatch: %v %v", o, err)
		}
		for n := range len(data) {
			if _, err := decodeList(data[:n], gossip); err == nil {
				t.Fatalf("truncated list accepted: gossip%v byte%d", gossip, n)
			}
		}
		if _, err := decodeList(append(bytes.Clone(data), 0), gossip); err == nil {
			t.Fatal("trailing list byte accepted")
		}
	}
	o, err := decodeDetails(unhex(t, detailsHex))
	if err != nil || o.NpcGuid != npcGUID || len(o.Quests) != 1 || !o.Quests[0].Available || o.Quests[0].Details != "Find the person." || o.Quests[0].Objectives != "Speak to them." {
		t.Fatalf("details mismatch: %v %v", o, err)
	}
}

func TestQuestDetailsAndMenuMalformedBounds(t *testing.T) {
	p := unhex(t, detailsHex)
	for n := range len(p) {
		if _, err := decodeDetails(p[:n]); err == nil {
			t.Fatalf("truncated details accepted at%d", n)
		}
	}
	for _, change := range []func([]byte){
		func(p []byte) { p[7] = 0 },                                 // Not a creature.
		func(p []byte) { p[8] = 1 },                                 // Party share divider, out of this slice.
		func(p []byte) { p[16] = 0 },                                // QuestID0.
		func(p []byte) { p[20] = 0xff },                             // Invalid UTF-8 title.
		func(p []byte) { p[65] = 2 },                                // activateAccept is a boolean.
		func(p []byte) { binary.LittleEndian.PutUint32(p[75:], 7) }, // choices count >6.
	} {
		bad := bytes.Clone(p)
		change(bad)
		if _, err := decodeDetails(bad); err == nil {
			t.Fatalf("malformed details accepted: %x", bad)
		}
	}
	if _, err := decodeDetails(append(bytes.Clone(p), 0)); err == nil {
		t.Fatal("trailing details accepted")
	}
	if _, err := decodeList(make([]byte, 65537), false); err == nil {
		t.Fatal("oversized list accepted")
	}
	bad := unhex(t, listHex)
	bad[26] = 33
	if _, err := decodeList(bad, false); err == nil {
		t.Fatal("unbounded quest list accepted")
	}
	bad = unhex(t, gossipHex)
	binary.LittleEndian.PutUint32(bad[16:], ^uint32(0))
	if _, err := decodeList(bad, true); err == nil {
		t.Fatal("unbounded gossip menu accepted")
	}
}

func TestCompletionOffersRemainNonActionable(t *testing.T) {
	for _, flags := range []uint32{0x80008, 0} {
		p := unhex(t, listHex)
		binary.LittleEndian.PutUint32(p[31:], 4)
		binary.LittleEndian.PutUint32(p[39:], flags)
		o, err := decodeList(p, false)
		if err != nil || o.Quests[0].Available {
			t.Fatalf("nonordinary offer actionable: %v %v", o, err)
		}
	}
}

func starterGossip(t testing.TB) []byte {
	t.Helper()
	p := unhex(t, gossipHex)[:41]
	binary.LittleEndian.PutUint32(p[24:], 783)
	binary.LittleEndian.PutUint32(p[36:], 0x80008)
	return append(p, []byte("A Threat Within\x00")...)
}

func TestAutoAcceptFlagDoesNotBlockExplicitStarterQuestPickup(t *testing.T) {
	o, err := decodeList(starterGossip(t), true)
	if err != nil || len(o.Quests) != 1 || o.Quests[0].QuestId != 783 || o.Quests[0].Flags != 0x80008 || o.Quests[0].Title != "A Threat Within" || !o.Quests[0].Available {
		t.Fatal("explicit available starter offer was filtered", o, err)
	}
	p := unhex(t, detailsHex)
	binary.LittleEndian.PutUint32(p[16:], 783)
	binary.LittleEndian.PutUint32(p[66:], 0x80008)
	o, err = decodeDetails(p)
	if err != nil || !o.Quests[0].Available || o.Quests[0].Flags != 0x80008 {
		t.Fatal("active details offer was filtered by client auto-accept hint", o, err)
	}
	p[65] = 0
	o, err = decodeDetails(p)
	if err != nil || o.Quests[0].Available {
		t.Fatal("inactive details became actionable", o, err)
	}
}

func FuzzQuestPackets(f *testing.F) {
	f.Add(unhex(f, listHex))
	f.Add(unhex(f, gossipHex))
	f.Add(unhex(f, detailsHex))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, p []byte) { decodeList(p, false); decodeList(p, true); decodeDetails(p) })
}
