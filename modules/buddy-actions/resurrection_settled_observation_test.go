package buddyactions

import (
	"context"
	"testing"
)

func TestResurrectionObservationUsesSameSettledPositionAsDispatch(t *testing.T) {
	for _, tc := range []struct {
		name                string
		replicated, settled float32
		ready               bool
	}{
		{"settled within range despite old replicated pose", 0, 70.3, true},
		{"settled outside range despite old replicated pose", 90, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := resRig(t)
			f.world.Self.X = tc.replicated
			f.world.Owner.X = 100
			s.snapshot(context.Background())
			s.refreshPositionLocked(f.world)
			s.pos.x = tc.settled
			options := s.encounterSupportLocked(context.Background(), f.world, s.onlineHealingGroup(f.party.Group))
			if len(options.Resurrections) != 1 || options.Resurrections[0].Ready != tc.ready {
				t.Fatal("observation disagrees with native settled range", options, s.pos)
			}
			if len(f.packets) != 0 {
				t.Fatal("observation wrote packets", f.packets)
			}
			_, err := requestResurrection(s)
			if (err == nil) != tc.ready {
				t.Fatal("observation disagrees with dispatch", options, err)
			}
		})
	}
}

func TestResurrectionSettledObservationPreservesUncertaintyAndRefreshesNewWorldRevision(t *testing.T) {
	for _, mode := range []string{"motion unknown", "moving", "new world revision"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := resRig(t)
			f.world.Self.X = 0
			f.world.Owner.X = 100
			s.snapshot(context.Background())
			s.refreshPositionLocked(f.world)
			s.pos.x = 70.3
			switch mode {
			case "motion unknown":
				s.state.MotionUnconfirmed = true
			case "moving":
				s.state.Moving = true
			case "new world revision":
				f.world.Self.PositionRevision++
			}
			options := s.encounterSupportLocked(context.Background(), f.world, s.onlineHealingGroup(f.party.Group))
			for _, option := range options.Resurrections {
				if option.Ready {
					t.Fatal("uncertain/stale native position granted readiness", options)
				}
			}
			if len(f.packets) != 0 {
				t.Fatal("read projection wrote packets", f.packets)
			}
		})
	}
}
