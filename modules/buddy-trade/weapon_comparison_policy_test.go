package buddytrade

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
)

func comparableWeaponFrame(id uint32, low, high float32) []byte {
	b := comparisonTemplate(id)
	binary.LittleEndian.PutUint32(b[4:], 2)
	binary.LittleEndian.PutUint32(b[8:], 15)
	prefix := 16 + len("Fixture Staff") + 4
	binary.LittleEndian.PutUint32(b[prefix+6*4:], 13)
	damage := prefix + 22*4 + 2*8 + 8
	binary.LittleEndian.PutUint32(b[damage:], math.Float32bits(low))
	binary.LittleEndian.PutUint32(b[damage+4:], math.Float32bits(high))
	binary.LittleEndian.PutUint32(b[damage+8:], 0)
	binary.LittleEndian.PutUint32(b[damage+12:], 0)
	binary.LittleEndian.PutUint32(b[damage+16:], 0)
	binary.LittleEndian.PutUint32(b[damage+20:], 0)
	armor := damage + 24
	binary.LittleEndian.PutUint32(b[armor:], 0)
	binary.LittleEndian.PutUint32(b[armor+7*4:], 2000)
	return b
}
func compareUnownedWeapon(t *testing.T, candidate, current *tradev1.ItemDetails) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../behaviors/gear.lua")
	if err != nil {
		t.Fatal(err)
	}
	source := "local gear=(function()\n" + string(raw) + "\nend)()\nreturn {on_event=function(e)return true end,export_state=function()return {version=1,data={}}end,import_state=function(c)return true end,decide=function(frame)return gear.preview(frame.input)end}"
	vm, err := behavior.NewSessionWithOptions(context.Background(), "unowned-gear", source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	value := func(item *tradev1.ItemDetails) map[string]any {
		encoded, err := protojson.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err = json.Unmarshal(encoded, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Instance facts belong to the offer/equipped-item observation. The template
	// reader itself never infers them from the item's name or static template.
	decision, err := vm.Decide(context.Background(), map[string]any{"candidate": value(candidate), "current": value(current),
		"candidateFacts": map[string]any{"enhancementsKnown": true}, "currentFacts": map[string]any{"itemGuid": "4611686018427387906", "enhancementsKnown": true}})
	if err != nil {
		t.Fatal(err)
	}
	return decision.Output
}
func TestUnownedWeaponNormalQueryUsesTheSharedLuaComparison(t *testing.T) {
	s, _ := fixture()
	request := &tradev1.LookupItemsRequest{ItemIds: []uint32{904100, 904101}}
	if _, err := s.LookupItems(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, comparableWeaponFrame(904100, 8, 12))
	packet(s, opcode.SMSGItemQuerySingleResponse, comparableWeaponFrame(904101, 4, 6))
	items, err := s.LookupItems(context.Background(), request)
	if err != nil || len(items.Items) != 2 {
		t.Fatal(items, err)
	}
	if !items.Items[0].ArmorComparisonKnown || !items.Items[1].ArmorComparisonKnown {
		t.Fatal("full normal weapon query lost property coverage", items)
	}
	if result := compareUnownedWeapon(t, items.Items[0], items.Items[1]); result["upgrade"] != true {
		t.Fatal("unowned query facts could not use the owned-item comparator", result)
	}
}
func TestUnownedWeaponHiddenPropertiesRemainUnknownToTheSharedComparator(t *testing.T) {
	base := comparableWeaponFrame(904100, 8, 12)
	current, err := parseLookupItem(comparableWeaponFrame(904101, 4, 6))
	if err != nil {
		t.Fatal(err)
	}
	prefix := 16 + len("Fixture Staff") + 4
	scaling := prefix + 22*4 + 2*8
	armor := scaling + 8 + 24
	tail := len(base) - 29*4
	for name, at := range map[string]int{"scaling": scaling, "resistance": armor + 4, "random-affix": tail + 8*4} {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), base...)
			binary.LittleEndian.PutUint32(b[at:], 1)
			candidate, err := parseLookupItem(b)
			if err != nil || candidate.ArmorComparisonKnown {
				t.Fatal("unrepresented property was invented as zero", candidate, err)
			}
			if result := compareUnownedWeapon(t, candidate, current); result["upgrade"] == true {
				t.Fatal("partial unowned facts authorized an upgrade", result)
			}
		})
	}
}
