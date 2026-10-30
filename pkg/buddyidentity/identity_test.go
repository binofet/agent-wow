package buddyidentity

import "testing"

func TestExplicitModePreservesCompanionDefault(t *testing.T) {
	for _, mode := range []string{"", "false", "true"} {
		got, err := Parse("42", mode)
		if err != nil || got.SelfGUID != 42 || got.Independent() != (mode == "true") {
			t.Fatalf("mode %q: %+v, %v", mode, got, err)
		}
		if mode != "true" && got.OwnerGUID != 3 {
			t.Fatalf("legacy owner changed: %+v", got)
		}
	}
}

func TestInvalidModeOrIdentityFailsClosed(t *testing.T) {
	for _, guid := range []string{"", "0", "-1", "not-a-guid", "281474976710656", "18446744073709551616"} {
		for _, mode := range []string{"", "false", "true"} {
			if got, err := Parse(guid, mode); err == nil || got.Independent() {
				t.Fatalf("accepted invalid GUID %q in mode %q", guid, mode)
			}
		}
	}
	for _, mode := range []string{"1", "TRUE", "False", " true", "false ", "independent"} {
		if got, err := Parse("42", mode); err == nil || got.Independent() {
			t.Fatalf("accepted ambiguous mode %q", mode)
		}
	}
}

func TestOwnerCharacterRequiresExplicitIndependence(t *testing.T) {
	for _, mode := range []string{"", "false"} {
		if _, err := Parse("3", mode); err == nil {
			t.Fatalf("accepted owner as companion in mode %q", mode)
		}
	}
	got, err := Parse("3", "true")
	if err != nil || got.SelfGUID != 3 || !got.Independent() {
		t.Fatalf("independent self3: %+v, %v", got, err)
	}
}
