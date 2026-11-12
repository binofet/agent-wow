package buddytrade

import (
	"context"
	"encoding/binary"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"math"
	"testing"
)

// Source-order offsets are independent of the production lookup reader.
func comparisonTemplate(id uint32) []byte {
	b := lookupTemplate(id)
	binary.LittleEndian.PutUint32(b[4:], 4)
	binary.LittleEndian.PutUint32(b[8:], 1)
	prefix := 16 + len("Fixture Staff") + 4
	for _, i := range []int{2, 3, 11, 12, 13, 14, 15, 16, 17} {
		binary.LittleEndian.PutUint32(b[prefix+i*4:], 0)
	}
	binary.LittleEndian.PutUint32(b[prefix+6*4:], 20)
	armor := prefix + 22*4 + 2*8 + 8 + 2*12
	for i := 1; i <= 6; i++ {
		binary.LittleEndian.PutUint32(b[armor+i*4:], 0)
	}
	spells := armor + 7*4 + 3*4
	for i := 0; i < 5; i++ {
		binary.LittleEndian.PutUint32(b[spells+i*24:], 0)
	}
	return b
}
func TestArmorComparisonOnlyPositiveFullNormalQuery(t *testing.T) {
	b := comparisonTemplate(904000)
	got, err := parseLookupItem(b)
	if err != nil || !got.Available || !got.ArmorComparisonKnown {
		t.Fatal(got, err)
	}
	for i := 0; i < len(b); i++ {
		got, err := parseLookupItem(b[:i])
		if err == nil && got.GetArmorComparisonKnown() {
			t.Fatal("partial query promoted omitted zeros", i)
		}
	}
	unavailable, err := parseLookupItem(u32(904000 | 0x80000000))
	if err != nil || unavailable.ArmorComparisonKnown {
		t.Fatal("unavailable metadata became comparison proof")
	}
}
func TestArmorComparisonOmittedBenefitAndUnknownAlwaysRetained(t *testing.T) {
	base := comparisonTemplate(904000)
	prefix := 16 + len("Fixture Staff") + 4
	scaling := prefix + 22*4 + 2*8
	armor := scaling + 8 + 2*12
	tail := len(base) - 29*4
	offsets := []int{scaling, scaling + 4}
	for i := 1; i <= 6; i++ {
		offsets = append(offsets, armor+i*4)
	}
	for _, i := range []int{0, 1, 2, 3, 4, 7, 8, 9, 10, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 25, 26, 27, 28} {
		offsets = append(offsets, tail+i*4)
	}
	for _, at := range offsets {
		b := append([]byte(nil), base...)
		binary.LittleEndian.PutUint32(b[at:], 1)
		got, err := parseLookupItem(b)
		if err != nil || got.ArmorComparisonKnown {
			t.Fatal("hidden property incorrectly comparable", at, got, err)
		}
	}
	for _, bits := range []uint32{math.Float32bits(1), math.Float32bits(float32(math.NaN())), math.Float32bits(float32(math.Inf(1))), 0x80000000} {
		b := append([]byte(nil), base...)
		binary.LittleEndian.PutUint32(b[tail+25*4:], bits)
		got, err := parseLookupItem(b)
		if err != nil || got.ArmorComparisonKnown {
			t.Fatal("armor modifier silently treated as zero", got, err)
		}
	}
	b := append([]byte(nil), base...)
	binary.LittleEndian.PutUint32(b[4:], 2)
	got, err := parseLookupItem(b)
	if err != nil || got.ArmorComparisonKnown {
		t.Fatal("weapon implied armor comparison")
	}
}
func TestArmorComparisonAllowsNormalBindAndDurabilityNotFunctionalExtras(t *testing.T) {
	for _, bonding := range []uint32{0, 1, 2} {
		b := comparisonTemplate(904000)
		tail := len(b) - 29*4
		bond := tail - len("Fixture description") - 1 - 4
		binary.LittleEndian.PutUint32(b[bond:], bonding)
		binary.LittleEndian.PutUint32(b[tail+11*4:], 30)
		binary.LittleEndian.PutUint32(b[tail+24*4:], 25)
		got, err := parseLookupItem(b)
		if err != nil || !got.ArmorComparisonKnown {
			t.Fatal("ordinary bind/durability/disenchant requirement blocked", bonding, got, err)
		}
	}
}
func TestArmorComparisonRequiresQueriedServerFrameAndClones(t *testing.T) {
	s, _ := fixture()
	packet(s, opcode.SMSGItemQuerySingleResponse, comparisonTemplate(904000))
	if s.lookup.items[904000] != nil {
		t.Fatal("unsolicited comparison cached")
	}
	req := &tradev1.LookupItemsRequest{ItemIds: []uint32{904000}}
	before, err := s.LookupItems(context.Background(), req)
	if err != nil || before.Items[0].ArmorComparisonKnown {
		t.Fatal("pending comparison fabricated")
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, comparisonTemplate(904000))
	after, err := s.LookupItems(context.Background(), req)
	if err != nil || !after.Items[0].ArmorComparisonKnown {
		t.Fatal(after, err)
	}
	after.Items[0].ArmorComparisonKnown = false
	again, _ := s.LookupItems(context.Background(), req)
	if !again.Items[0].ArmorComparisonKnown {
		t.Fatal("cache aliases response")
	}
}
