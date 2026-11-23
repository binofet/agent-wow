package buddyactions

import (
	"context"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNormalPortalIndependentWorldAndCoreVisitDomains(t *testing.T) {
	s, f, _, _, _ := boxPortalEntryFixture(t, true)
	a := f.world.SelfMotionAuthority
	if f.world.VisitRevision != 1 || a.VisitRevision != 1 || a.Known || a.TransportScope.VisitRevision != 9 ||
		s.followPortal.sourceScope == a.TransportScope {
		t.Fatal("fixture lacks a detached actual parsed mapping in independent domains", f.world, s.followPortal)
	}
	s.Tick(context.Background())
	if !s.followPortal.notified || countPackets(f, opcode.CMSGAreatrigger) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("ordinary ground correlation was mistaken for spline authority or equal counters", s.stateLocked(), f.packets)
	}
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGAreatrigger && (p.GetTransportScope().GetVisitRevision() != 9 || p.TransportScope.MotionRevision <= a.TransportScope.MotionRevision) {
			t.Fatal("trigger report did not preserve the actual Core domain", p)
		}
	}
}

func TestNormalPortalCoreFirstSameMapEntryCannotUseOldWorldWitness(t *testing.T) {
	for _, change := range []string{"Core new visit", "current World mapping rebased", "new connection", "current source ahead of report"} {
		t.Run(change, func(t *testing.T) {
			s, f, _, _, _ := boxPortalEntryFixture(t, true)
			f.onOwnMovementReport = func(p *modv1.WorldPacket) {
				switch change {
				case "Core new visit":
					// Both module queues still expose World1/Core9. The actual
					// transport already crossed a same-map boundary into Core10;
					// this is a current successful STOP, not a fake stale report.
					p.TransportScope.VisitRevision = 10
					f.ownMovementReport.TransportScope.VisitRevision = 10
					f.portalCoreScope.VisitRevision = 10
				case "current World mapping rebased":
					// Even if a later read exposes a different correlation, it
					// must not relabel the captured World1/Core9 witness.
					p.TransportScope.VisitRevision = 10
					f.ownMovementReport.TransportScope.VisitRevision = 10
					f.portalCoreScope.VisitRevision = 10
					f.world.SelfMotionAuthority.TransportScope.VisitRevision = 10
				case "new connection":
					p.TransportScope.ConnectionId = "replacement-connection"
					f.ownMovementReport.TransportScope.ConnectionId = p.TransportScope.ConnectionId
					f.portalCoreScope.ConnectionId = p.TransportScope.ConnectionId
				case "current source ahead of report":
					f.world.SelfMotionAuthority.TransportScope.MotionRevision = p.TransportScope.MotionRevision + 1
				}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 0 || s.followPortal.notified ||
				s.followPortal.sourceScope.VisitRevision != 9 || s.followPortal.sourceScope.ConnectionId != "synthetic-actions-portal-connection" || s.state.MotionUnconfirmed {
				t.Fatal("a new Core source freshened the old module witness", change, s.followPortal, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestNormalPortalCoreFirstSameMapEntryRefusesEvenTheStop(t *testing.T) {
	s, f, _, _, _ := boxPortalEntryFixture(t, true)
	// The Core queue has consumed NEW_WORLD into Core10. Both module queues
	// still expose World1/Core9; the boundary also erased the old own report.
	f.portalCoreScope.VisitRevision = 10
	f.ownMovementReport = nil
	s.Tick(context.Background())
	if len(f.packets) != 0 || s.followPortal.notified || s.state.MotionUnconfirmed || s.state.LastTransportFailure == nil ||
		s.state.LastTransportFailure.OutcomeUnknown || s.state.LastTransportFailure.Opcode != opcode.MSGMoveStop ||
		s.followPortal.sourceScope.VisitRevision != 9 || f.world.SelfMotionAuthority.TransportScope.VisitRevision != 9 {
		t.Fatal("a stale module visit minted a new STOP in Core's replacement visit", s.followPortal, s.stateLocked(), f.packets)
	}
}

func TestNormalPortalScopedStopUsesCurrentKnownLocalWriteScope(t *testing.T) {
	s, f, _, _, _ := boxPortalEntryFixture(t, true)
	// A prior successful ordinary write advances only Core's motion source.
	// The parsed World scope remains World1/Core9/motion1, without relabeling.
	if !s.movementLocked(context.Background(), opcode.MSGMoveStop, s.pos, false) {
		t.Fatal("the synthetic prior ordinary STOP failed")
	}
	prior := f.portalCoreScope.MotionRevision
	if prior <= f.world.SelfMotionAuthority.TransportScope.MotionRevision {
		t.Fatal("fixture did not advance the independent local-write source")
	}
	s.Tick(context.Background())
	if !s.followPortal.notified || countPackets(f, opcode.MSGMoveStop) != 2 || countPackets(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("current own-written scope could not admit a mapped STOP", s.stateLocked(), f.packets)
	}
	seen := 0
	for _, p := range f.packets {
		if p.Opcode == opcode.MSGMoveStop {
			seen++
			if seen == 2 && (p.GetTransportScope().GetVisitRevision() != 9 || p.GetTransportScope().GetMotionRevision() != prior) {
				t.Fatal("portal STOP freshened World correlation instead of retaining the current own-written scope", p)
			}
		}
	}
}

func TestNormalPortalMissingOrReboundParsedMappingCannotBootstrapStop(t *testing.T) {
	for _, change := range []string{"absent", "wrong World tag", "Core visit changed before proposal", "zero motion"} {
		t.Run(change, func(t *testing.T) {
			s, f, _, _, _ := boxPortalEntryFixture(t, true)
			switch change {
			case "absent":
				f.world.SelfMotionAuthority = nil
			case "wrong World tag":
				f.world.SelfMotionAuthority.VisitRevision++
			case "Core visit changed before proposal":
				f.world.SelfMotionAuthority.TransportScope.VisitRevision = 10
			case "zero motion":
				f.world.SelfMotionAuthority.TransportScope.MotionRevision = 0
			}
			s.Tick(context.Background())
			if len(f.packets) != 0 || s.followPortal.notified || s.followPortal.sourceScope.VisitRevision != 9 || s.state.MotionUnconfirmed {
				t.Fatal("missing or superseded parsed facts became a new STOP source", change, s.followPortal, s.stateLocked(), f.packets)
			}
		})
	}
}
