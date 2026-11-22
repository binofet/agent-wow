package buddyactions

import (
	"strings"
	"testing"
)

func TestFlightpathSettlementLuaKnownRefusalWaitsForExplicitRetryChoice(t *testing.T) {
	p := newSettlementPolicyFixture(t, false)
	p.accept()
	activationPolicyNoEffect(t, p.step())
	p.qualifySpline()
	p.dispatch(p.until("buddy-world.complete_spline"))
	p.ground()
	p.dispatch(p.until("buddy-world.retire_spline_completion"))
	action := p.until("buddy-actions.query_flightpath")
	p.f.base.x.client.beforeWorld = func() {
		p.f.base.x.f.world.NearbyCreatures[0].PositionAgeMs = 2001
	}
	packets := len(p.f.base.x.f.packets)
	p.dispatch(action)
	p.f.base.x.client.beforeWorld = nil
	refusal := p.f.base.x.observation(t).Operation
	if refusal.Phase != "not_dispatched" || refusal.Pending || refusal.TransportWritten || refusal.TransportUnknown || len(p.f.base.x.f.packets) != packets {
		t.Fatal("fixture did not establish a retained known zero-send refusal", refusal)
	}
	for i := 0; i < 3; i++ {
		out := p.step()
		activationPolicyNoEffect(t, out)
		if out["phase"] != "arrival_query_not_dispatched" || len(p.f.base.x.f.packets) != packets {
			t.Fatal("unchanged refusal automatically retried", out)
		}
	}
	source := strings.Replace(p.f.source, "retry_refused_queries=false", "retry_refused_queries=true", 1)
	before := p.f.r.Status().Revision
	if source == p.f.source {
		t.Fatal("retry policy is not editable in maintained Lua")
	}
	if revision, err := p.f.r.Reload(source, before); err != nil || revision == before {
		t.Fatal("retry choice CAS was not accepted", revision, err)
	}
	action = p.until("buddy-actions.query_flightpath")
	p.dispatch(action)
	if len(p.f.base.x.f.packets) != packets+1 || p.f.activationCall != 1 || !p.f.base.x.observation(t).Activation.Pending {
		t.Fatalf("explicit retry: packets %d→%d, activation calls %d, view %s, methods %v", packets, len(p.f.base.x.f.packets), p.f.activationCall, p.f.base.x.observation(t), p.methods)
	}
}
