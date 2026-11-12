package buddymail

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

const mailListGolden = "010000000159004d00000000000000000000000000000000000000003d00000010270000000000000000f04100000000547261696e696e672073746970656e64004f6e6520676f6c6420666f7220636c61737320747261696e696e670000"

func goldenList(t *testing.T) []byte {
	t.Helper()
	p, err := hex.DecodeString(mailListGolden)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestMailBinaryGoldens(t *testing.T) {
	entries, err := decodeMailList(goldenList(t))
	if err != nil || len(entries) != 1 || entries[0].id != 77 || !entries[0].stipend() {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	if got := hex.EncodeToString(mailboxPacket(0xf110022b0e000001)); got != "0100000e2b0210f1" {
		t.Fatal(got)
	}
	if got := hex.EncodeToString(takeMoneyPacket(0xf110022b0e000001, 77)); got != "0100000e2b0210f14d000000" {
		t.Fatal(got)
	}
	ack, _ := hex.DecodeString("4d0000000100000000000000")
	r, err := decodeMailResult(ack)
	if err != nil || r.id != 77 || r.action != 1 || r.result != 0 {
		t.Fatalf("ack=%+v %v", r, err)
	}
}

func TestPartyBagsExactObservedAttachmentProtocol(t *testing.T) {
	p := partyBagList(4, 77, 1000)
	rows, err := decodeMailList(p)
	if err != nil || len(rows) != 1 || !rows[0].partyBags(1) || rows[0].attachments[3].itemLow != 1003 || rows[0].attachments[3].index != 3 || rows[0].attachments[3].count != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for n := range len(p) {
		if _, err := decodeMailList(p[:n]); err == nil {
			t.Fatalf("accepted prefix%d", n)
		}
	}
	for name, change := range map[string]func(*mailEntry){
		"subject": func(m *mailEntry) { m.subject += " " }, "sender": func(m *mailEntry) { m.sender = 2 }, "stationery": func(m *mailEntry) { m.stationery = 41 }, "type": func(m *mailEntry) { m.typeID = 3 }, "cod": func(m *mailEntry) { m.cod = 1 }, "money": func(m *mailEntry) { m.money = 1 }, "entry": func(m *mailEntry) { m.attachments[0].entry = 1 }, "count": func(m *mailEntry) { m.attachments[0].count = 2 }, "zero_guid": func(m *mailEntry) { m.attachments[0].itemLow = 0 }, "duplicate_guid": func(m *mailEntry) { m.attachments[1].itemLow = m.attachments[0].itemLow }, "duplicate_index": func(m *mailEntry) { m.attachments[1].index = 0 }, "index": func(m *mailEntry) { m.attachments[0].index = 4 },
	} {
		t.Run(name, func(t *testing.T) {
			m := rows[0]
			m.attachments = append([]mailAttachment{}, m.attachments...)
			change(&m)
			if m.partyBags(1) {
				t.Fatal("inexact envelope eligible")
			}
		})
	}
	for _, n := range []int{0, 5, 12} {
		r, err := decodeMailList(partyBagList(n, 77, 1000))
		if err != nil || r[0].partyBags(1) {
			t.Fatalf("count%d eligible %v", n, err)
		}
	}
	if got := hex.EncodeToString(takeItemPacket(testMailbox, 77, 1000)); got != "0100000e2b0210f14d000000e8030000" {
		t.Fatal(got)
	}
	r, err := decodeMailResult(bagAck(77, 1000, 1, 0))
	if err != nil || r.action != 2 || r.itemLow != 1000 || r.count != 1 {
		t.Fatal(r, err)
	}
	if _, err := decodeMailResult(bagAck(77, 1000, 1, 0)[:19]); err == nil {
		t.Fatal("short item ACK")
	}
	if takeItemPacket(testMailbox, 77, 0) != nil {
		t.Fatal("zero item guid")
	}
}

func TestPartyBagsConsoleSenderIsBoundToActualRecipient(t *testing.T) {
	for _, self := range []uint64{1, 4, 5, 6, 8} {
		p := partyBagList(4, 77, 1000)
		// MailHandler emits the normal raw player sender GUID at row offset7.
		binary.LittleEndian.PutUint64(p[5+7:], self)
		rows, err := decodeMailList(p)
		if err != nil || len(rows) != 1 || rows[0].sender != self || !rows[0].partyBags(self) {
			t.Fatal(self, rows, err)
		}
		if rows[0].partyBags(self+1) || rows[0].partyBags(0) || rows[0].stipend() {
			t.Fatal("recipient-specific bag mail accepted as foreign or stipend", self)
		}
		for n := range len(p) {
			if _, err := decodeMailList(p[:n]); err == nil {
				t.Fatalf("self%d accepted prefix%d", self, n)
			}
		}
		rows[0].sender = 0
		if !rows[0].partyBags(self) {
			t.Fatal("legacy zero sender rejected")
		}
	}
}

func TestMailListEveryTruncationAndBounds(t *testing.T) {
	p := goldenList(t)
	for n := range len(p) {
		if _, err := decodeMailList(p[:n]); err == nil {
			t.Fatalf("accepted prefix%d", n)
		}
	}
	for _, bad := range [][]byte{append(append([]byte{}, p...), 0), {0, 0, 0, 0, 51}, {0, 0, 0, 0, 1}} {
		if _, err := decodeMailList(bad); err == nil {
			t.Fatalf("accepted%X", bad)
		}
	}
	for _, offset := range []int{5, 6, 11, 92, 93} {
		bad := append([]byte{}, p...)
		bad[offset] = 255
		if _, err := decodeMailList(bad); err == nil {
			t.Fatalf("accepted corrupt offset%d", offset)
		}
	}
	dupe := append(append([]byte{}, p...), p[5:]...)
	dupe[0], dupe[4] = 2, 2
	if _, err := decodeMailList(dupe); err == nil {
		t.Fatal("duplicate mail ID accepted")
	}
}
func TestStipendRequiresExactConsoleMoneyOnly(t *testing.T) {
	rows, err := decodeMailList(goldenList(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*mailEntry){
		"player": func(m *mailEntry) { m.sender = 3 }, "creature": func(m *mailEntry) { m.typeID = 3 },
		"stationery": func(m *mailEntry) { m.stationery = 41 }, "subject": func(m *mailEntry) { m.subject = "Training stipend " },
		"money": func(m *mailEntry) { m.money = 9999 }, "cod": func(m *mailEntry) { m.cod = 1 }, "items": func(m *mailEntry) { m.items = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			m := rows[0]
			change(&m)
			if m.stipend() {
				t.Fatal("wrong mail eligible")
			}
		})
	}
}
func TestAttachedItemsAreBoundsCheckedAndNeverEligible(t *testing.T) {
	p := goldenList(t)
	p[len(p)-1] = 1
	p = append(p, make([]byte, 118)...)
	binary.LittleEndian.PutUint16(p[5:], uint16(len(p)-5))
	rows, err := decodeMailList(p)
	if err != nil || len(rows) != 1 || rows[0].stipend() {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if _, err = decodeMailList(p[:len(p)-1]); err == nil {
		t.Fatal("truncated attachment accepted")
	}
}
func TestMailResultStrictConditionalLength(t *testing.T) {
	p, _ := hex.DecodeString("4d0000000100000000000000")
	for n := range len(p) {
		if _, err := decodeMailResult(p[:n]); err == nil {
			t.Fatalf("prefix%d", n)
		}
	}
	if _, err := decodeMailResult(append(p, 0)); err == nil {
		t.Fatal("trailing byte accepted")
	}
	p[8] = 1
	if _, err := decodeMailResult(p); err == nil {
		t.Fatal("missing equip error accepted")
	}
	p = append(p, 2, 0, 0, 0)
	r, err := decodeMailResult(p)
	if err != nil || r.equip != 2 {
		t.Fatalf("equip:%+v %v", r, err)
	}
	p[4], p[8] = 2, 0
	p = append(p, 0, 0, 0, 0)
	if _, err = decodeMailResult(p); err != nil {
		t.Fatal(err)
	}
}
func FuzzMailPackets(f *testing.F) {
	p, _ := hex.DecodeString(mailListGolden)
	f.Add(p)
	f.Add([]byte{1, 0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, p []byte) {
		rows, err := decodeMailList(p)
		if err == nil && len(rows) > 50 {
			t.Fatal("unbounded")
		}
		_, _ = decodeMailResult(p)
	})
}
