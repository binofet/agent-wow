package buddyloot

import (
	"encoding/hex"
	"testing"
)

func TestLootProtocolGoldenAndBounds(t *testing.T) {
	if hex.EncodeToString(guidPacket(corpseGUID)) != "01000001000030f1" {
		t.Fatal("raw corpse GUID framing changed")
	}
	wire := windowPacket(corpseGUID, 5, lootItem{slot: 2, id: 123, count: 3, slotType: 4})
	got, err := decodeWindow(wire)
	if err != nil || got.guid != corpseGUID || got.gold != 5 || len(got.items) != 1 || got.items[0].id != 123 {
		t.Fatalf("decode %+v err%v", got, err)
	}
	for n := 0; n < len(wire); n++ {
		if _, err := decodeWindow(wire[:n]); err == nil {
			t.Fatalf("accepted truncated window%d", n)
		}
	}
	if _, err := decodeWindow(append(wire, 0)); err == nil {
		t.Fatal("accepted excess tail")
	}
	errorPacket := append(guidPacket(corpseGUID), 0, 4)
	result, err := decodeWindow(errorPacket)
	if err != nil || !result.failed || result.errorCode != 4 {
		t.Fatal("loot error framing")
	}
	push := pushPacket(1, 123, 3)
	if result, err := decodePush(push); err != nil || result.player != 1 || result.count != 3 {
		t.Fatal("item push framing")
	}
	for n := 0; n < len(push); n++ {
		if _, err := decodePush(push[:n]); err == nil {
			t.Fatal("accepted truncated push")
		}
	}
}
func TestLootWindowRejectsDuplicateAndRestrictedMalformedSlots(t *testing.T) {
	for _, wire := range [][]byte{windowPacket(corpseGUID, 0, lootItem{slot: 1, id: 1, count: 1}, lootItem{slot: 1, id: 2, count: 1}), windowPacket(corpseGUID, 0, lootItem{slot: 1, id: 1, count: 0}), windowPacket(corpseGUID, 0, lootItem{slot: 1, id: 1, count: 1, slotType: 5})} {
		if _, err := decodeWindow(wire); err == nil {
			t.Fatal("accepted invalid slot")
		}
	}
}
func FuzzLootDecoders(f *testing.F) {
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, p []byte) { _, _ = decodeWindow(p); _, _ = decodePush(p) })
}
