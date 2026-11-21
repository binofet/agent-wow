package buddyactions

import (
	"context"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNativeTickNeverSelectsResurrectionPatient(t *testing.T) {
	for _, mode := range []string{"idle", "following", "buffing", "expired_host_lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := resRig(t)
			switch mode {
			case "following":
				toggle(t, s, true)
			case "buffing":
				s.state.Buffing = true
				s.buffUntil = s.now().Add(time.Hour)
			case "expired_host_lease":
				activateEncounterWriter(t, s, f)
				advance(encounterLeaseDuration)
			}
			for i := 0; i < 3; i++ {
				s.Tick(context.Background())
				advance(100 * time.Millisecond)
			}
			if countPackets(f, opcode.CMSGCastSpell) != 0 || len(s.resurrectionBindings) != 0 {
				t.Fatal("native scheduler chose a resurrection", s.stateLocked(), f.packets)
			}
			w, _ := s.snapshot(context.Background())
			options := s.encounterSupportLocked(context.Background(), w, s.onlineHealingGroup(f.party.Group))
			if len(options.Resurrections) != 1 || !options.Resurrections[0].Ready {
				t.Fatal("native passivity hid the learned resurrection mechanic", options)
			}
			if _, err := requestResurrection(s); err != nil || lastCast(f) != 2006 {
				t.Fatal("Lua/caller-selected cast could not execute", err, s.stateLocked())
			}
		})
	}
}
