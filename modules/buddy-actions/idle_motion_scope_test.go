package buddyactions

import (
	"context"
	"strings"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestIdleMotionBodyReportFailureRetainsKnownWriteAndUncertainty(t *testing.T) {
	s, f, r, ctx, advance := idleBodyFixture(t, true)
	f.onOwnMovementReport = func(p *modv1.WorldPacket) { p.Payload = nil }
	out, err := s.MoveBody(ctx, r)
	if err != nil || len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveSetFacing || !s.state.MotionUnconfirmed ||
		!out.GetBodyMovement().GetTransportWritten() || out.BodyMovement.Phase != "body_scope_unconfirmed_no_retry" ||
		!strings.Contains(s.state.NavReason, "packet written; source report unavailable") || s.state.TransportWrites != 1 {
		t.Fatal("known write/report uncertainty was lost or retried", out, err, s.state.NavReason, f.packets)
	}
	advance(31 * time.Second)
	s.stateLocked() // A read must not discard an uncertain188lease.
	if s.state.InteractionOwner != "exploration" || s.interactionToken != r.IdleToken {
		t.Fatal("expired uncertain motion lost its parent custody", s.stateLocked())
	}
}

func TestIdleMotionKnownCurrentStopCanRetireOnlyMotionCleanup(t *testing.T) {
	s, f, r, ctx, advance := idleBodyFixture(t, true)
	f.onOwnMovementReport = func(p *modv1.WorldPacket) {
		if p.Opcode == opcode.MSGMoveStop {
			p.Payload = nil
		}
	}
	out, err := s.MoveBody(ctx, r)
	if err != nil || !s.state.MotionUnconfirmed || out.GetBodyMovement().GetPhase() != "body_scope_unconfirmed_no_retry" || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(out, err, f.packets)
	}
	phase := out.BodyMovement.Phase
	f.onOwnMovementReport = nil
	advance(31 * time.Second)
	s.Tick(context.Background())
	if s.state.MotionUnconfirmed || s.state.Moving || s.state.InteractionOwner != "" || s.interactionToken != "" ||
		s.state.BodyMovement.Phase != phase || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("cleanup replayed STOP or promoted the original unknown operation", s.stateLocked(), f.packets)
	}
}

func TestIdleMotionExpiredLeaseCleanupIsScopedAndCannotCrossCoreVisit(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same current source", true: "Core same-map replacement"}[changed], func(t *testing.T) {
			s, f, r, ctx, advance := idleBodyFixture(t, true)
			s.refreshPositionLocked(f.world)
			scope, err := s.newIdleMotionScopeLocked(ctx, r.IdleToken, f.world, false)
			if err != nil {
				t.Fatal(err)
			}
			ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
			if !s.movementLocked(ctx, opcode.MSGMoveStartForward, s.pos, true) {
				t.Fatal("scoped setup movement failed", s.stateLocked())
			}
			if changed {
				f.portalCoreScope.VisitRevision = 10
				f.ownMovementReport = nil
			}
			advance(31 * time.Second)
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal("cleanup started another trip", f.packets)
			}
			if changed {
				if len(f.packets) != 1 || !s.state.Moving || s.state.InteractionOwner != "exploration" {
					t.Fatal("expired source wrote into replacement Core visit", s.stateLocked(), f.packets)
				}
			} else if countPackets(f, opcode.MSGMoveStop) != 1 || s.state.Moving || s.state.InteractionOwner != "" {
				t.Fatal("healthy expiry failed to write one scopedSTOP", s.stateLocked(), f.packets)
			}
			for _, p := range f.packets {
				if p.TransportScope == nil || p.TransportScope.VisitRevision != 9 {
					t.Fatal("idle expiry used an unscoped or relabeled packet", p)
				}
			}
		})
	}
}

func TestIdleMotionTravelUsesSameScopedSourceAndRefusesLateCoreReplacement(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "current idle route", true: "Core changes before first write"}[changed], func(t *testing.T) {
			s, f, _, r := instanceEntryIdleTravelFixture(t)
			if changed {
				f.onClock = func() { f.portalCoreScope.VisitRevision = 10; f.ownMovementReport = nil }
			}
			out, err := s.TravelTo(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				if len(f.packets) != 0 || s.state.MotionUnconfirmed {
					t.Fatal("oldWorld scope entered a new Core visit", out, f.packets)
				}
			} else if out.Reason != "exploration_within_distance" || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal("normal idle route failed", out, f.packets)
			}
			for _, p := range f.packets {
				if p.TransportScope == nil || p.TransportScope.VisitRevision != 9 {
					t.Fatal("188travel bypassed its physicalsource", p)
				}
			}
		})
	}
}
