package buddyactions

import (
	"testing"
	"time"
)

func TestEmergencyDefenseHealsAfterPriorAuthorizationWouldHaveExpired(t *testing.T) {
	s, f, _, advance := selfDefenseRig(t, 5)
	s.state.Assisting = false
	advance(5 * time.Hour)
	f.world.Self.Health = 10
	runLuaHealing(t, s, false)
	if lastCast(f) != lesserHeal || s.state.HealAttempts != 1 || healingCastTarget(f) != 1 {
		t.Fatal("emergency defense displaced an urgent heal", s.stateLocked(), f.packets)
	}
}
