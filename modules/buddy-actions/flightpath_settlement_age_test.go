package buddyactions

import "testing"

func TestFlightpathSettlementLuaUsesCanonicalUint64AgeBounds(t *testing.T) {
	p := newSettlementPolicyFixture(t, false)
	p.accept()
	activationPolicyNoEffect(t, p.step())
	p.qualifySpline()
	p.dispatch(p.until("buddy-world.complete_spline"))
	p.ground()
	p.dispatch(p.until("buddy-world.retire_spline_completion"))
	self := p.f.input["observations"].(map[string]any)["world"].(map[string]any)["self"].(map[string]any)
	before := len(p.methods)
	for _, age := range []any{"2001", "18446744073709551615", "02000", float64(2000)} {
		self["positionAgeMs"] = age
		activationPolicyNoEffect(t, p.step())
		if len(p.methods) != before {
			t.Fatal("stale or malformed uint64 age issued another query", age, p.methods)
		}
	}
	self["positionAgeMs"] = "2000"
	action := p.until("buddy-actions.query_flightpath")
	if action["method"] != "buddy-actions.query_flightpath" {
		t.Fatal("canonical age boundary did not allow its exact native query", action)
	}
}
