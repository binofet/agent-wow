package buddyactions

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type explicitDeckSession struct {
	*surfaceTestSession
	want         *actionsv1.BodyMovementRequest
	denyStop     bool
	stopAttempts int
}

func (f *explicitDeckSession) SendPacket(ctx context.Context, req *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	incoming := metadata.NewIncomingContext(ctx, md)
	token, ok := modcb.IncomingPolicySpell(incoming)
	visit, seq, present := modcb.IncomingSurfaceBodySelection(incoming)
	if !ok || token != strings.Repeat("d", 64) || !present || visit != f.want.VisitId || seq != f.want.SequenceId {
		f.t.Fatal("physical passenger frame lost original operation", req.Opcode, visit, seq)
	}
	if req.Opcode == opcode.MSGMoveStop {
		f.stopAttempts++
		if f.denyStop {
			return nil, modcb.PacketNotDispatchedError("physical scope changed at STOP")
		}
	}
	return f.surfaceTestSession.SendPacket(ctx, req, opts...)
}

func explicitDeckFixture(t *testing.T, clock uint32, local buddynav.Point, bearing float32, jump bool) (*surfaceTestRig, *actionsv1.BodyMovementRequest, context.Context) {
	t.Helper()
	r := newSurfaceTestRig(t, clock, local, surfaceTestBow, true, true)
	r.world.Owner = nil
	r.world.Self.PassengerMovement.Revision = r.world.MovingTransports[0].MovingTransport.ObjectRevision + 1
	req := &actionsv1.BodyMovementRequest{SessionId: r.s.sessionID, SelfGuid: r.s.selfGUID, VisitId: r.world.VisitRevision, SequenceId: 45, BearingRadians: bearing, DurationMs: 200, Jump: jump}
	if jump {
		req.Landing = "ground_or_water"
	}
	r.s.session = &explicitDeckSession{surfaceTestSession: r.surfaceTestSession, want: req}
	ctx := modcb.WithPolicyEncounterAccess(modcb.WithPolicySpell(context.Background(), strings.Repeat("d", 64)), testEncounterKey, r.world.VisitRevision)
	md, _ := metadata.FromOutgoingContext(ctx)
	ctx = metadata.NewIncomingContext(context.Background(), md)
	started := r.s.now()
	r.s.bodyMotionWait = func(ctx context.Context, d time.Duration) error {
		r.advance(d)
		age := uint64(r.s.now().Sub(started) / time.Millisecond)
		r.world.Self.PassengerMovement.AgeMs = age
		r.world.MovingTransports[0].MovingTransport.MovementAgeMs = age
		return ctx.Err()
	}
	return r, req, ctx
}

func TestSelectedPassengerMoveBodyWithoutOwner(t *testing.T) {
	for _, test := range []struct {
		name    string
		clock   uint32
		local   buddynav.Point
		bearing float32
		jump    bool
	}{
		{"finite deck input", 30000, surfaceTestDeck, 0, false}, {"moving ship to real water", 112000, buddynav.Point{X: 1.5347, Y: 12.8191, Z: 6.0722}, math.Pi / 2, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, req, ctx := explicitDeckFixture(t, test.clock, test.local, test.bearing, test.jump)
			out, err := r.s.MoveBody(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			b := out.BodyMovement
			if b == nil || b.Phase != "body_input_written" || !b.TransportWritten || !b.PositionEstimated || b.VisitId != req.VisitId || b.SequenceId != req.SequenceId || out.MotionUnconfirmed || out.Moving || len(r.packets) < 3 || r.packets[len(r.packets)-1].Opcode != opcode.MSGMoveStop {
				t.Fatalf("incomplete selected deck input: %v, native=%s/%s frames=%d", b, out.Reason, out.NavReason, len(r.packets))
			}
			first, last := r.frame(0), r.frame(len(r.packets)-1)
			if !first.TransportPresent || first.Transport.GUID != r.ship || r.world.Owner != nil {
				t.Fatal(first, r.world.Owner)
			}
			if test.jump {
				if r.packets[0].Opcode != opcode.MSGMoveJump || countPackets(r.fakeSession, opcode.MSGMoveStartSwim) != 1 || last.TransportPresent || !last.PitchPresent {
					t.Fatal("missing genuine water contact", last)
				}
			} else if r.packets[0].Opcode != opcode.MSGMoveStartForward || !last.TransportPresent || math.Abs(surfaceDistance(surfaceLocalPoint(first), surfaceLocalPoint(last))-1.4) > .08 {
				t.Fatal("wrong caller-selected local displacement", first, last)
			}
			n := len(r.packets)
			_, _ = r.s.MoveBody(ctx, req)
			if len(r.packets) != n {
				t.Fatal("duplicate sequence replayed")
			}
		})
	}
}

func TestSelectedPassengerMoveBodyAdmissionAndUnknown(t *testing.T) {
	for _, test := range []string{"wrong body", "wrong visit", "wrong lease", "wrong birth", "missing body", "ambiguous start", "replacement before write", "competing writer", "blocked wall"} {
		t.Run(test, func(t *testing.T) {
			r, req, ctx := explicitDeckFixture(t, 30000, surfaceTestDeck, 0, false)
			switch test {
			case "wrong body":
				req.SelfGuid++
			case "wrong visit":
				req.VisitId++
			case "wrong lease":
				req.IdleToken = "other"
			case "wrong birth":
				r.world.MovingTransports[0].MovingTransport.ObjectRevision = r.world.Self.PassengerMovement.Revision + 1
			case "missing body":
				r.world.SelfMovementFacts.WireImage = nil
			case "ambiguous start":
				r.failSend = true
			case "replacement before write":
				r.onClock = func() { r.world.MovingTransports[0].MovingTransport.ObjectRevision++; r.onClock = nil }
			case "competing writer":
				r.s.movementOwner.owner = "ranged_stance"
			case "blocked wall":
				req.Jump = true
				req.Landing = "ground_or_water"
			}
			out, err := r.s.MoveBody(ctx, req)
			if test == "ambiguous start" {
				if err != nil || out == nil || !out.MotionUnconfirmed || out.BodyMovement.Phase != "body_scope_unconfirmed_no_retry" || out.BodyMovement.TransportWritten || len(r.packets) != 1 {
					t.Fatal(err, out, len(r.packets))
				}
				r.failSend = false
				_, _ = r.s.MoveBody(ctx, req)
				if len(r.packets) != 1 {
					t.Fatal("uncertain send replayed")
				}
			} else if len(r.packets) != 0 || err == nil && (out == nil || out.BodyMovement == nil || out.BodyMovement.TransportWritten) {
				t.Fatal("unsafe passenger input", err, out, len(r.packets))
			}
		})
	}
}

func TestSelectedPassengerBodyAfterReceivingFerryWithOwnerAbsent(t *testing.T) {
	r := fieldReceivingFerry(t, true)
	r.world.Owner = nil
	r.s.Tick(context.Background())
	if _, e := r.s.RenewPolicyEncounterLease(r.ctx, wrapperspb.UInt64(2)); e != nil {
		t.Fatal(e)
	}
	req := &actionsv1.BodyMovementRequest{SessionId: r.s.sessionID, SelfGuid: r.s.selfGUID, VisitId: 2, SequenceId: 1, DurationMs: 150}
	ctx := modcb.WithPolicyEncounterAccess(modcb.WithPolicySpell(context.Background(), strings.Repeat("d", 64)), testEncounterKey, 2)
	md, _ := metadata.FromOutgoingContext(ctx)
	ctx = metadata.NewIncomingContext(context.Background(), md)
	r.s.session = &explicitDeckSession{surfaceTestSession: r.surfaceTestSession, want: req}
	initial := r.s.now()
	initialAge := r.world.MovingTransports[0].MovingTransport.MovementAgeMs
	r.s.bodyMotionWait = func(ctx context.Context, d time.Duration) error {
		r.advance(d)
		age := uint64(r.s.now().Sub(initial) / time.Millisecond)
		r.world.MovingTransports[0].MovingTransport.MovementAgeMs = initialAge + age
		r.world.Self.PassengerMovement.AgeMs = age
		return ctx.Err()
	}
	n := len(r.packets)
	out, err := r.s.MoveBody(ctx, req)
	if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || out.GetMotionUnconfirmed() || out.GetFollowing() || !out.GetLastTransportFailure().GetOutcomeUnknown() || len(r.packets) <= n+1 {
		t.Fatalf("new-visit physical input: %v %v reason=%s packets=%d", err, out.GetBodyMovement(), out.GetNavReason(), len(r.packets)-n)
	}
	start, last := r.frame(n), r.frame(len(r.packets)-1)
	if !last.TransportPresent || last.Transport.GUID != receivingTestShip || start.Transport.Time < r.progress || last.Transport.Time < start.Transport.Time || r.world.Self.PassengerMovement.Info.Transport.Time != 89379 || math.Abs(surfaceDistance(surfaceLocalPoint(start), surfaceLocalPoint(last))-1.05) > .08 {
		t.Fatal("motion used an old transform or changed the received evidence", start, last)
	}
}

func TestSelectedPassengerBodyTerminalStop(t *testing.T) {
	for _, jump := range []bool{false, true} {
		for _, unknown := range []bool{false, true} {
			clock, local, bearing := uint32(30000), surfaceTestDeck, float32(0)
			if jump {
				clock, local, bearing = 112000, buddynav.Point{X: 1.5347, Y: 12.8191, Z: 6.0722}, math.Pi/2
			}
			r, req, ctx := explicitDeckFixture(t, clock, local, bearing, jump)
			socket := r.s.session.(*explicitDeckSession)
			socket.denyStop = !unknown
			r.failSend, r.failSendOpcode = unknown, opcode.MSGMoveStop
			out, e := r.s.MoveBody(ctx, req)
			if e != nil || socket.stopAttempts != 1 || !out.GetMotionUnconfirmed() || !out.GetBodyMovement().GetTransportWritten() || out.GetBodyMovement().GetPhase() != "body_scope_unconfirmed_no_retry" {
				t.Fatal(jump, unknown, e, out.GetBodyMovement(), socket.stopAttempts)
			}
			_, _ = r.s.MoveBody(ctx, req)
			if socket.stopAttempts != 1 {
				t.Fatal("repeated the unresolved final STOP")
			}
		}
	}
}

func TestSelectedPassengerBodyExplicitReleaseInFlight(t *testing.T) {
	r, req, ctx := explicitDeckFixture(t, 112000, buddynav.Point{X: 1.5347, Y: 12.8191, Z: 6.0722}, math.Pi/2, true)
	wait := r.s.bodyMotionWait
	releasedAt := -1
	r.s.bodyMotionWait = func(c context.Context, d time.Duration) error {
		if e := wait(c, d); e != nil {
			return e
		}
		if releasedAt < 0 && len(r.packets) > 2 && !r.frame(len(r.packets)-1).TransportPresent {
			releasedAt = len(r.packets)
			if _, e := r.s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false}); e != nil {
				t.Fatal(e)
			}
			if len(r.packets) != releasedAt {
				t.Fatal("another writer attempted an airborne STOP")
			}
		}
		return nil
	}
	out, e := r.s.MoveBody(ctx, req)
	if e != nil || releasedAt < 0 || out.GetMotionUnconfirmed() || out.GetMoving() || out.GetFollowing() || countPackets(r.fakeSession, opcode.MSGMoveStop) != 1 || countPackets(r.fakeSession, opcode.MSGMoveStartSwim) != 1 {
		t.Fatal("explicit horizontal release did not settle", e, out.GetBodyMovement(), out.GetReason(), out.GetNavReason(), releasedAt, len(r.packets))
	}
	for i := releasedAt; i < len(r.packets); i++ {
		f := r.frame(i)
		if f.Flags&1 != 0 || f.JumpPresent && f.Jump.XYSpeed != 0 {
			t.Fatal("released input continued horizontally", i, f)
		}
	}
}

func TestSelectedPassengerBodyRetainsPriorIdleLease(t *testing.T) {
	r, req, ctx := explicitDeckFixture(t, 30000, surfaceTestDeck, 0, false)
	// Acquire the existing ordinary idle lease on this visit before a new,
	// positively received passenger frame becomes its current body.
	r.player(r.world.Self, surfaceTestDeck, false)
	lease, e := r.s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: r.s.sessionID, SelfGuid: req.SelfGuid, VisitId: req.VisitId, Owner: "exploration", LeaseMs: 30000})
	if e != nil {
		t.Fatal(e)
	}
	r.player(r.world.Self, surfaceTestDeck, true)
	r.world.Self.PassengerMovement.Revision = max(r.world.Self.PassengerMovement.Revision, r.world.MovingTransports[0].MovingTransport.ObjectRevision)
	req.IdleToken = lease.Token
	out, e := r.s.MoveBody(ctx, req)
	if e != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || r.s.interactionToken != lease.Token || out.GetInteractionOwner() != "exploration" {
		t.Fatal(e, out.GetBodyMovement(), out.GetInteractionOwner())
	}
}
