package buddyinventory

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func addonInbound(body string) []byte {
	text := addonPrefix + body
	p := make([]byte, 30+len(text)+1)
	p[0] = 2
	binary.LittleEndian.PutUint32(p[1:], 0xffffffff)
	binary.LittleEndian.PutUint64(p[5:], 3)
	binary.LittleEndian.PutUint32(p[25:], uint32(len(text)+1))
	copy(p[29:], text)
	return p
}
func addonFixture() *inventoryv1.State {
	s := &inventoryv1.State{SessionId: "fixture", SelfGuid: 1, Revision: 1, InventoryKnown: true, InventoryRevision: 7}
	for b := uint32(0); b < 5; b++ {
		bag := &worldv1.InventoryBag{Bag: b, SlotKnown: true, ContentsKnown: true}
		if b == 0 {
			bag.SlotCount = 16
			for i := uint32(0); i < 16; i++ {
				bag.Slots = append(bag.Slots, &worldv1.BackpackSlot{Bag: 0, Slot: i})
			}
		}
		s.InventoryBags = append(s.InventoryBags, bag)
	}
	return s
}

// The literal headers are independent of the production channel constant and
// addonInbound helper: actual 3.3.5a PARTY is2 in both server and client framing.
func TestInventoryAddonActualPartyWireGoldens(t *testing.T) {
	text := addonPrefix + "1|R|0123456789ab"
	inbound, err := hex.DecodeString("02ffffffff030000000000000000000000000000000000000000000000")
	if err != nil || len(inbound) != 29 {
		t.Fatal(inbound, err)
	}
	binary.LittleEndian.PutUint32(inbound[25:], uint32(len(text)+1))
	inbound = append(inbound, []byte(text)...)
	inbound = append(inbound, 0, 0) // Terminated text, then ordinary chat tag.
	command, recognized, err := decodeInventoryAddon(inbound)
	if err != nil || !recognized || command == nil || command.kind != "R" || command.nonce != "0123456789ab" {
		t.Fatal("actual PARTY request was not accepted", command, recognized, err)
	}
	inbound[0] = 1 // SAY must not authorize an inventory request.
	if command, recognized, err = decodeInventoryAddon(inbound); command != nil || recognized || err != nil {
		t.Fatal("SAY request was treated as PARTY", command, recognized, err)
	}
	outbound, err := encodeInventoryAddon("1|R|0123456789ab")
	want, _ := hex.DecodeString("02000000ffffffff")
	want = append(want, []byte(text)...)
	want = append(want, 0)
	if err != nil || string(outbound) != string(want) {
		t.Fatalf("actual PARTY response framing differs: got%x want%x error%v", outbound, want, err)
	}
}

func TestInventoryAddonOnlyActualOwnerPartyAddonCommands(t *testing.T) {
	valid := []string{"1|R|0123456789ab", "1|M|0123456789ab|abcdef012345|7|0|0|4000000000000001|1|19", "1|E|0123456789ab|abcdef012345|7|0|0|4000000000000001|4", "1|G|0123456789ab|abcdef012345|7|0|0|4000000000000001", "1|J|0123456789ab|abcdef012345|7"}
	for _, body := range valid {
		c, yes, e := decodeInventoryAddon(addonInbound(body))
		if e != nil || !yes || c == nil {
			t.Fatal(body, c, yes, e)
		}
	}
	for _, mode := range []string{"sender", "language", "ordinary", "say", "flags", "receiver", "length", "nul", "trailing"} {
		p := addonInbound(valid[0])
		switch mode {
		case "sender":
			binary.LittleEndian.PutUint64(p[5:], 4)
		case "language":
			binary.LittleEndian.PutUint32(p[1:], 0)
		case "ordinary":
			p[0] = 0
		case "say":
			p[0] = 1
		case "flags":
			p[13] = 1
		case "receiver":
			p[17] = 3
		case "length":
			p[25] = 255
		case "nul":
			p[len(p)-2] = 'x'
		case "trailing":
			p = append(p, 0)
		}
		c, _, e := decodeInventoryAddon(p)
		if c != nil || e == nil && (mode == "flags" || mode == "receiver" || mode == "length" || mode == "nul" || mode == "trailing") {
			t.Fatal(mode, c, e)
		}
	}
	invalid := []string{"1|R|0123456789AB", "1|R|0123456789ab|extra", "1|J|0123456789ab|abcdef012345|0", "1|J|0123456789ab|abcdef012345|07", "1|M|0123456789ab|abcdef012345|7|5|0|4000000000000001|0|0", "1|M|0123456789ab|abcdef012345|7|0|0|0000000000000003|0|0", "1|E|0123456789ab|abcdef012345|7|0|0|4000000000000001|0", "1|G|0123456789ab|abcdef012345|7|0|0|4000000000000001\x00"}
	for _, body := range invalid {
		c, yes, e := decodeInventoryAddon(addonInbound(body))
		if !yes || e == nil || c != nil {
			t.Fatal(body, c, yes, e)
		}
	}
}
func TestInventoryAddonFullBoundedSnapshotUsesActualSlots(t *testing.T) {
	state := addonFixture()
	state.InventoryBags[0].Slots[15] = &worldv1.BackpackSlot{Bag: 0, Slot: 15, ItemGuid: 0x4000000000000001, Entry: 41599, StackCount: 1, MetadataKnown: true}
	for b := uint32(1); b < 5; b++ {
		bag := state.InventoryBags[b]
		bag.ContainerGuid = 0x4000000000000010 + uint64(b)
		bag.Entry = 41599
		bag.MetadataKnown = true
		bag.SlotCount = 36
		for i := uint32(0); i < 36; i++ {
			bag.Slots = append(bag.Slots, &worldv1.BackpackSlot{Bag: b, Slot: i, ItemGuid: 0x4000000000001000 + uint64(b)*36 + uint64(i), Entry: 1000 + i, StackCount: 2, MetadataKnown: true})
		}
	}
	parts, e := inventoryAddonParts("0123456789ab", "abcdef012345", state)
	if e != nil || len(parts) > 64 {
		t.Fatal(len(parts), e)
	}
	joined := strings.Join(parts, ";")
	if strings.Count(joined, "B=") != 5 || strings.Count(joined, "I=") != 145 || !strings.Contains(joined, "I=0,15,41599,1,4000000000000001") {
		t.Fatal("missing actual inventory tokens", len(parts))
	}
	for i, p := range parts {
		raw, e := encodeInventoryAddon(p)
		if e != nil || len(raw)-9 > 255 || binary.LittleEndian.Uint32(raw) != 2 || binary.LittleEndian.Uint32(raw[4:]) != 0xffffffff || !strings.HasPrefix(p, fmt.Sprintf("1|S|0123456789ab|abcdef012345|7|%d|%d|", i+1, len(parts))) {
			t.Fatal(p, e)
		}
	}
	state.InventoryKnown = false
	parts, e = inventoryAddonParts("0123456789ab", "abcdef012345", state)
	if e != nil || strings.Contains(strings.Join(parts, ";"), "I=") || strings.Count(strings.Join(parts, ";"), ",0") < 5 {
		t.Fatal("unknown inventory advertised item/free slots", parts, e)
	}
}
func TestInventoryAddonDuplicateMutationNeverQueuesTwice(t *testing.T) {
	b := newAddonBridge(nil)
	now := time.Now()
	b.now = func() time.Time { return now }
	raw := "1|M|0123456789ab|abcdef012345|7|0|0|4000000000000001|0|1"
	b.receive(addonInbound(raw))
	b.receive(addonInbound(raw))
	if len(b.queue) != 1 || len(b.requests) != 1 {
		t.Fatal("replayed mutation", b.queue)
	}
	b.receive(addonInbound(strings.TrimSuffix(raw, "1") + "2"))
	if len(b.queue) != 1 || !strings.Contains(b.outgoing[len(b.outgoing)-1], "request_nonce_payload_changed") {
		t.Fatal("changed nonce accepted")
	}
	b.receive(addonInbound("1|R|aaaaaaaaaaaa"))
	b.receive(addonInbound("1|R|aaaaaaaaaaaa"))
	if len(b.queue) != 2 {
		t.Fatal("duplicate read queued")
	}
	now = now.Add(31 * time.Second)
	b.receive(addonInbound("1|R|aaaaaaaaaaaa"))
	if len(b.queue) != 3 {
		t.Fatal("expired read nonce blocked refresh")
	}
	b.close()
	b.receive(addonInbound("1|R|bbbbbbbbbbbb"))
	if len(b.queue) != 0 || b.lifetime.Err() == nil {
		t.Fatal("logout did not revoke addon workers")
	}
}
func TestInventoryAddonRejectsOversizeAndMalformedFrames(t *testing.T) {
	if _, e := encodeInventoryAddon(strings.Repeat("x", 249)); e == nil {
		t.Fatal("oversize addon encoded")
	}
	s := addonFixture()
	s.InventoryBags[0].Slots[1] = s.InventoryBags[0].Slots[0]
	if _, e := inventoryAddonParts("0123456789ab", "abcdef012345", s); e == nil {
		t.Fatal("duplicate slot frame accepted")
	}
}
