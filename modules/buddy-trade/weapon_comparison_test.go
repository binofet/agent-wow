package buddytrade

import (
	"encoding/binary"
	"testing"
)

func TestWeaponComparisonRequiresCompleteOrdinaryPropertyEvidence(t *testing.T) {
	base := comparisonTemplate(904001)
	binary.LittleEndian.PutUint32(base[4:], 2)
	for _, mutate := range []struct {
		name string
		edit func([]byte)
		want bool
	}{
		{"complete", func([]byte) {}, true},
		{"set", func(body []byte) { binary.LittleEndian.PutUint32(body[len(body)-29*4+10*4:], 1) }, false},
		{"socket", func(body []byte) { binary.LittleEndian.PutUint32(body[len(body)-29*4+16*4:], 1) }, false},
		{"partial", func(body []byte) { body[len(body)-1] = 1 }, false},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			body := append([]byte(nil), base...)
			mutate.edit(body)
			item, err := parseLookupItem(body)
			if err != nil || item.WeaponComparisonKnown != mutate.want || item.ArmorComparisonKnown {
				t.Fatalf("comparison fact = %v, error = %v", item, err)
			}
		})
	}
	for at := range len(base) {
		item, err := parseLookupItem(base[:at])
		if err == nil && item.GetWeaponComparisonKnown() {
			t.Fatalf("truncated query asserted weapon comparison at %d", at)
		}
	}
}
