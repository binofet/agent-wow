package buddysupplies

import (
	"testing"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	"google.golang.org/protobuf/proto"
)

func TestFixedCatalogPolicy(t *testing.T) {
	for _, want := range []catalogItem{{6256, 356, true}, {6365, 356, true}, {7005, 393, true}, {2901, 186, true}, {6217, 333, true}, {4470, 185, true}, {4471, 185, false}, {2320, 197, false}} {
		got, ok := catalogLookup(want.entry)
		if !ok || got != want {
			t.Fatalf("entry%d got%+v,%v want%+v", want.entry, got, ok, want)
		}
	}
	for _, entry := range []uint32{0, 1, 6255, 6257, 7006, 0x80000000, 0xffffffff} {
		if got, ok := catalogLookup(entry); ok || got != (catalogItem{}) {
			t.Fatalf("accepted uncatalogued entry%d: %+v", entry, got)
		}
	}
}

func TestCatalogRequiresCompleteMatchingMetadata(t *testing.T) {
	spec, _ := catalogLookup(6256)
	valid := &tradev1.ItemDetails{ItemId: 6256, Available: true, ItemClass: 2, Subclass: 20, InventoryType: 17}
	if reason := validateCatalogMetadata(valid, spec); reason != "" {
		t.Fatal(reason)
	}
	for _, mutate := range []func(*tradev1.ItemDetails){
		func(p *tradev1.ItemDetails) { p.ItemId = 7005 },
		func(p *tradev1.ItemDetails) { p.Available = false },
		func(p *tradev1.ItemDetails) { p.Pending = true },
		func(p *tradev1.ItemDetails) { p.ItemClass = 4 },
		func(p *tradev1.ItemDetails) { p.Subclass = 7 },
		func(p *tradev1.ItemDetails) { p.InventoryType = 13 },
	} {
		item := proto.Clone(valid).(*tradev1.ItemDetails)
		mutate(item)
		if reason := validateCatalogMetadata(item, spec); reason == "" {
			t.Fatalf("accepted incomplete or wrong pole: %+v", item)
		}
	}
	if validateCatalogMetadata(nil, spec) == "" {
		t.Fatal("accepted missing metadata")
	}
	for _, invalid := range []catalogItem{{0, 0, false}, {6256, 333, true}, {6256, 356, false}} {
		if validateCatalogMetadata(valid, invalid) == "" {
			t.Fatalf("accepted forged spec%+v", invalid)
		}
	}
	// Non-pole prices and template shapes are actual server data, not guessed
	// constants. Character requirements remain the service's responsibility.
	for _, entry := range []uint32{7005, 2901, 6217, 4470, 4471, 2320} {
		spec, _ := catalogLookup(entry)
		item := &tradev1.ItemDetails{ItemId: entry, Available: true, BuyPrice: 12345, RequiredLevel: 80, RequiredSkill: 333, RequiredSkillRank: 450}
		if reason := validateCatalogMetadata(item, spec); reason != "" {
			t.Fatalf("validator invented non-pole metadata: %s", reason)
		}
		item.Pending = true
		if validateCatalogMetadata(item, spec) == "" {
			t.Fatal("accepted pending non-pole metadata")
		}
	}
}
