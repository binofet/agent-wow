package buddysupplies

import (
	"bytes"
	"encoding/binary"
	"testing"
)

const testVendor uint64 = 0xf130000a23000001
const testItemGUID uint64 = 0x4000000000000123

func vendorPacketFixture(count byte) []byte {
	p := binary.LittleEndian.AppendUint64(nil, testVendor)
	p = append(p, count)
	if count == 0 {
		return append(p, 0)
	}
	for i := uint32(1); i <= uint32(count); i++ {
		// Same entry in distinct slots is permitted (e.g. extended-cost offers).
		for _, v := range []uint32{i, 6256, 1234, 0xffffffff, 50, 10, 1, 0} {
			p = binary.LittleEndian.AppendUint32(p, v)
		}
	}
	return p
}

func buyReceiptFixture() []byte {
	p := binary.LittleEndian.AppendUint64(nil, testVendor)
	for _, v := range []uint32{7, 0xffffffff, 2} {
		p = binary.LittleEndian.AppendUint32(p, v)
	}
	return p
}

func inventoryFailureFixture(code byte) []byte {
	p := []byte{code}
	if code == 0 {
		return p
	}
	p = binary.LittleEndian.AppendUint64(p, testItemGUID)
	p = binary.LittleEndian.AppendUint64(p, 0)
	p = append(p, 0)
	switch code {
	case 1, 87:
		p = binary.LittleEndian.AppendUint32(p, 5)
	case 84, 85, 89:
		p = binary.LittleEndian.AppendUint32(p, 123)
	case 81:
		p = append(p, make([]byte, 20)...)
	}
	return p
}

func TestRequestWireFormats(t *testing.T) {
	if got := listInventoryPacket(testVendor); len(got) != 8 || binary.LittleEndian.Uint64(got) != testVendor {
		t.Fatalf("invalid list request: %x", got)
	}
	// BuyItem's unknown byte and u32 quantity are significant: 21 bytes,
	// with item preceding the one-based vendor slot.
	want := []byte{1, 0, 0, 0x23, 0xa, 0, 0x30, 0xf1, 0x70, 0x18, 0, 0, 7, 0, 0, 0, 2, 0, 0, 0, 0}
	if got := buyItemPacket(testVendor, 7, 6256, 2); !bytes.Equal(got, want) {
		t.Fatalf("buy wire=%x want=%x", got, want)
	}
	if got := autoEquipItemSlotPacket(testItemGUID, 15); len(got) != 9 || binary.LittleEndian.Uint64(got) != testItemGUID || got[8] != 15 {
		t.Fatalf("invalid equip request: %x", got)
	}
	for _, npc := range []uint64{0, 6, 0xf130000000000001, 0xf130000a23000000} {
		if listInventoryPacket(npc) != nil || buyItemPacket(npc, 1, 6256, 1) != nil {
			t.Fatalf("accepted invalid vendor %x", npc)
		}
	}
	for _, tc := range [][3]uint32{{0, 6256, 1}, {256, 6256, 1}, {1, 0, 1}, {1, 0x80000000, 1}, {1, 6256, 0}, {1, 6256, 256}} {
		if buyItemPacket(testVendor, tc[0], tc[1], tc[2]) != nil {
			t.Fatalf("accepted invalid buy fields %v", tc)
		}
	}
	if buyItemPacket(testVendor, 255, 6256, 255) == nil {
		t.Fatal("rejected bounded maximum request")
	}
	for _, tc := range []struct {
		guid uint64
		slot byte
	}{{0, 15}, {testVendor, 15}, {0x4000000000000000, 15}, {testItemGUID, 16}, {testItemGUID, 0}} {
		if autoEquipItemSlotPacket(tc.guid, tc.slot) != nil {
			t.Fatalf("accepted invalid equip %+v", tc)
		}
	}
}

func TestVendorListFramingAndValues(t *testing.T) {
	for _, count := range []byte{0, 1, 255} {
		p := vendorPacketFixture(count)
		npc, items, err := parseVendorList(p)
		if err != nil || npc != testVendor || len(items) != int(count) {
			t.Fatalf("count %d: npc=%x items=%d err=%v", count, npc, len(items), err)
		}
		if count > 0 && (items[0].Slot != 1 || items[0].Entry != 6256 || items[0].AvailableCount != -1 || items[0].PriceCopper != 50 || items[0].BuyCount != 1 || items[0].ExtendedCost != 0) {
			t.Fatalf("wrong field mapping: %+v", items[0])
		}
		for at := 0; at < len(p); at++ {
			if _, _, err := parseVendorList(p[:at]); err == nil {
				t.Fatalf("accepted count%d truncated at %d", count, at)
			}
		}
		if _, _, err := parseVendorList(append(p, 0)); err == nil {
			t.Fatal("accepted vendor trailing byte")
		}
	}
	for _, tc := range []struct {
		at    int
		value uint32
	}{{9, 0}, {9, 256}, {13, 0}, {13, 0x80000000}, {21, 0xfffffffe}, {25, 0x80000000}, {33, 0}, {33, 0x80000000}, {37, 0x80000000}} {
		p := vendorPacketFixture(1)
		binary.LittleEndian.PutUint32(p[tc.at:], tc.value)
		if _, _, err := parseVendorList(p); err == nil {
			t.Fatalf("accepted vendor field at%d=%x", tc.at, tc.value)
		}
	}
	p := vendorPacketFixture(2)
	binary.LittleEndian.PutUint32(p[41:], 1)
	if _, _, err := parseVendorList(p); err == nil {
		t.Fatal("accepted duplicate slot")
	}
	p = vendorPacketFixture(0)
	p[9] = 1
	if _, _, err := parseVendorList(p); err == nil {
		t.Fatal("accepted unknown empty-list reason")
	}
	p = vendorPacketFixture(1)
	binary.LittleEndian.PutUint64(p, 6)
	if _, _, err := parseVendorList(p); err == nil {
		t.Fatal("accepted non-creature vendor")
	}
}

func TestBuyItemReceiptUsesSlotAndBundleCount(t *testing.T) {
	p := buyReceiptFixture()
	r, err := parseBuyItem(p)
	if err != nil || r.kind != "buy_item" || r.npc != testVendor || r.item != 0 || r.slot != 7 || r.availableCount != -1 || r.count != 2 || r.code != 0 {
		t.Fatalf("wrong buy receipt: %+v %v", r, err)
	}
	for at := 0; at < len(p); at++ {
		if _, err := parseBuyItem(p[:at]); err == nil {
			t.Fatalf("accepted buy receipt prefix%d", at)
		}
	}
	if _, err := parseBuyItem(append(p, 0)); err == nil {
		t.Fatal("accepted trailing buy receipt")
	}
	for _, tc := range []struct {
		at    int
		value uint32
	}{{8, 0}, {8, 256}, {12, 0xfffffffe}, {16, 0}, {16, 256}} {
		q := bytes.Clone(p)
		binary.LittleEndian.PutUint32(q[tc.at:], tc.value)
		if _, err := parseBuyItem(q); err == nil {
			t.Fatalf("accepted buy receipt field%+v", tc)
		}
	}
	binary.LittleEndian.PutUint64(p, 0)
	if _, err := parseBuyItem(p); err == nil {
		t.Fatal("accepted unidentified success")
	}
}

func TestBuyFailuresExactFormsAndCodes(t *testing.T) {
	for _, code := range []byte{0, 1, 2, 4, 5, 7, 8, 11, 12} {
		for _, param := range []uint32{0, 99} {
			p := binary.LittleEndian.AppendUint64(nil, testVendor)
			p = binary.LittleEndian.AppendUint32(p, 6256)
			if param != 0 {
				p = binary.LittleEndian.AppendUint32(p, param)
			}
			p = append(p, code)
			r, err := parseBuyFailed(p)
			if err != nil || r.kind != "buy_failed" || r.npc != testVendor || r.item != 6256 || r.code != code || r.param != param {
				t.Fatalf("failure mismatch: %+v %v", r, err)
			}
			for at := 0; at < len(p); at++ {
				if _, err := parseBuyFailed(p[:at]); err == nil {
					t.Fatalf("accepted failure prefix%d", at)
				}
			}
			if _, err := parseBuyFailed(append(p, 0)); err == nil {
				t.Fatal("accepted failure trailing byte")
			}
		}
	}
	// Missing vendor/item identities are explicitly emitted on failure.
	if r, err := parseBuyFailed(make([]byte, 13)); err != nil || r.kind != "buy_failed" || r.code != 0 {
		t.Fatalf("zero-identity failure must remain a failure: %+v %v", r, err)
	}
	for _, code := range []byte{3, 6, 9, 10, 13, 255} {
		p := make([]byte, 13)
		p[12] = code
		if _, err := parseBuyFailed(p); err == nil {
			t.Fatalf("accepted unknown buy failure%d", code)
		}
	}
	if _, err := parseBuyFailed(make([]byte, 17)); err == nil {
		t.Fatal("accepted zero optional parameter")
	}
	p := make([]byte, 13)
	binary.LittleEndian.PutUint64(p, 6)
	if _, err := parseBuyFailed(p); err == nil {
		t.Fatal("accepted player vendor identity")
	}
	p = make([]byte, 13)
	binary.LittleEndian.PutUint32(p[8:], 0x80000000)
	if _, err := parseBuyFailed(p); err == nil {
		t.Fatal("accepted invalid item identity")
	}
}

func TestInventoryFailureExactCodeSpecificTails(t *testing.T) {
	for code := byte(0); code <= 89; code++ {
		p := inventoryFailureFixture(code)
		r, err := parseInventoryChangeFailure(p)
		if code == 74 || code == 83 {
			if err == nil {
				t.Fatalf("accepted undefined inventory code%d", code)
			}
			continue
		}
		if err != nil || r.kind != "inventory_change_failure" || r.code != code {
			t.Fatalf("code%d: %+v %v", code, r, err)
		}
		if code != 0 && (r.itemGUID1 != testItemGUID || r.itemGUID2 != 0) {
			t.Fatalf("wrong item GUIDs: %+v", r)
		}
		if (code == 1 || code == 87) && r.requiredLevel != 5 {
			t.Fatal("missing level tail")
		}
		if (code == 84 || code == 85 || code == 89) && r.limitCategory != 123 {
			t.Fatal("missing category tail")
		}
		for at := 0; at < len(p); at++ {
			if _, err := parseInventoryChangeFailure(p[:at]); err == nil {
				t.Fatalf("accepted code%d prefix%d", code, at)
			}
		}
		if _, err := parseInventoryChangeFailure(append(p, 0)); err == nil {
			t.Fatalf("accepted code%d trailing byte", code)
		}
	}
	for _, code := range []byte{90, 255} {
		if _, err := parseInventoryChangeFailure(inventoryFailureFixture(code)); err == nil {
			t.Fatal("accepted unknown inventory code")
		}
	}
	for _, mutate := range []func([]byte){func(p []byte) { p[17] = 1 }, func(p []byte) { binary.LittleEndian.PutUint64(p[1:], testVendor) }, func(p []byte) { binary.LittleEndian.PutUint64(p[9:], 6) }} {
		p := inventoryFailureFixture(23)
		mutate(p)
		if _, err := parseInventoryChangeFailure(p); err == nil {
			t.Fatal("accepted invalid inventory identity or bag type")
		}
	}
	p := inventoryFailureFixture(1)
	binary.LittleEndian.PutUint32(p[18:], 81)
	if _, err := parseInventoryChangeFailure(p); err == nil {
		t.Fatal("accepted unsupported level")
	}
	p = inventoryFailureFixture(81)
	p[18] = 1
	if _, err := parseInventoryChangeFailure(p); err == nil {
		t.Fatal("accepted unknown bind-confirm payload")
	}
}

func TestSuppliesParsersRejectOversizedFrames(t *testing.T) {
	p := make([]byte, 65536)
	if _, _, err := parseVendorList(p); err == nil {
		t.Fatal("accepted oversized vendor list")
	}
	for name, parse := range map[string]func([]byte) (purchaseReceipt, error){
		"buy_item": parseBuyItem, "buy_failed": parseBuyFailed, "inventory_change_failure": parseInventoryChangeFailure,
	} {
		if _, err := parse(p); err == nil {
			t.Fatalf("accepted oversized %s", name)
		}
	}
}

func FuzzSuppliesParsers(f *testing.F) {
	f.Add(vendorPacketFixture(1))
	f.Add(buyReceiptFixture())
	f.Add(inventoryFailureFixture(81))
	f.Add(make([]byte, 13))
	f.Fuzz(func(t *testing.T, p []byte) {
		_, _, _ = parseVendorList(p)
		_, _ = parseBuyItem(p)
		_, _ = parseBuyFailed(p)
		_, _ = parseInventoryChangeFailure(p)
	})
}
