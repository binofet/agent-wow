package buddytraining

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

const trainerGUID uint64 = 0xf130000155000001

// Literal wire record: spell2050,available,cost10,zero points,level2,
// required skill0/rank0/abilities0. Golden offsets follow NPCPackets.cpp.
func trainerWire(usable byte) []byte {
	p, _ := hex.DecodeString("01000055010030f1000000000100000002080000000a0000000000000000000000020000000000000000000000000000000000000000547261696e00")
	p[20] = usable
	return p
}
func TestTrainerWireGolden(t *testing.T) {
	p := trainerWire(0)
	o, e := decodeList(p)
	if e != nil {
		t.Fatal(e, len(p))
	}
	if o.NpcGuid != trainerGUID || o.TrainerType != 0 || o.Greeting != "Train" || len(o.Spells) != 1 {
		t.Fatalf("%+v", o)
	}
	q := o.Spells[0]
	if q.SpellId != 2050 || q.CostCopper != 10 || q.Usable != 0 || q.RequiredLevel != 2 || len(q.PointCost) != 2 || len(q.RequiredAbilities) != 3 {
		t.Fatalf("%+v", q)
	}
	want, _ := hex.DecodeString("01000055010030f102080000")
	if !bytes.Equal(buyPacket(trainerGUID, 2050), want) || !bytes.Equal(listPacket(trainerGUID), want[:8]) {
		t.Fatal("outbound wire differs")
	}
}
func TestTrainerListRejectsMalformed(t *testing.T) {
	good := trainerWire(0)
	for i := 0; i < len(good); i++ {
		if _, e := decodeList(good[:i]); e == nil {
			t.Fatalf("accepted prefix%d", i)
		}
	}
	cases := [][]byte{append(bytes.Clone(good), 0)}
	for _, offset := range []int{7, 10, 15, 20, 24, 33} {
		p := bytes.Clone(good)
		p[offset] = 0xff
		cases = append(cases, p)
	}
	p := bytes.Clone(good)
	p[len(p)-2] = 0
	cases = append(cases, p)
	for i, p := range cases {
		if _, e := decodeList(p); e == nil {
			t.Fatalf("accepted case%d", i)
		}
	}
	duplicate := append(bytes.Clone(good[:54]), good[16:54]...)
	duplicate = append(duplicate, 0)
	binary.LittleEndian.PutUint32(duplicate[12:], 2)
	if _, e := decodeList(duplicate); e == nil {
		t.Fatal("duplicate spell accepted")
	}
}
func TestTrainerReceiptsAndLearned(t *testing.T) {
	success := buyPacket(trainerGUID, 2050)
	g, id, reason, e := decodeReceipt(success, false)
	if e != nil || g != trainerGUID || id != 2050 || reason != 0 {
		t.Fatal(g, id, reason, e)
	}
	failed := binary.LittleEndian.AppendUint32(bytes.Clone(success), 1)
	if _, _, r, e := decodeReceipt(failed, true); e != nil || r != 1 {
		t.Fatal(r, e)
	}
	binary.LittleEndian.PutUint32(failed[12:], 3)
	if _, _, _, e := decodeReceipt(failed, true); e == nil {
		t.Fatal("unsupported failure")
	}
	learned := []byte{2, 8, 0, 0, 0, 0}
	if id, e := learnedPacket(learned, false); e != nil || id != 2050 {
		t.Fatal(id, e)
	}
	swapped := []byte{1, 8, 0, 0, 2, 8, 0, 0}
	if id, e := learnedPacket(swapped, true); e != nil || id != 2050 {
		t.Fatal(id, e)
	}
	for i := 0; i < len(success); i++ {
		if _, _, _, e := decodeReceipt(success[:i], false); e == nil {
			t.Fatal(i)
		}
	}
	for i := 0; i < len(learned); i++ {
		if _, e := learnedPacket(learned[:i], false); e == nil {
			t.Fatal(i)
		}
	}
	learned[5] = 1
	if _, e := learnedPacket(learned, false); e == nil {
		t.Fatal("nonzero learned suffix")
	}
}
func FuzzTrainerList(f *testing.F) {
	f.Add(trainerWire(0))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, p []byte) { _, _ = decodeList(p) })
}
