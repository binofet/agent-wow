package buddyactions

import (
	"context"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func idleFlightpathParentRig(t *testing.T) (*flightpathRig, *actionsv1.IdleInteractionRequest) {
	t.Helper()
	// Reuse the native status/menu/activation/server0 fixture and its explicit
	// coherent World retirement observations; no duplicate parser is modeled.
	x := newFlightpathSettlementRig(t)
	if x.s.flightpathActivation.operation.CurrentSegment != nil || !x.s.flightpathActivation.operation.Pending {
		t.Fatal("fixture is not an ordinary same-visit accepted pending booking")
	}
	// An ordinary flight may outlast its finite departure lease. Expiry is
	// observed through native bookkeeping, rather than replacing its token.
	packets := len(x.f.packets)
	x.advance(31 * time.Second)
	status, err := x.s.Status(context.Background(), &emptypb.Empty{})
	if err != nil || status.InteractionOwner != "" || x.s.interactionToken != "" || len(x.f.packets) != packets {
		t.Fatal("finite departure custody did not expire without game writes", status, err)
	}
	// The fixture's original activation is historical; isolate the parent
	// lease operations below without claiming that activation never happened.
	x.f.packets = nil
	x.client.sendCalls = 0
	return x, &actionsv1.IdleInteractionRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID,
		VisitId: x.f.world.VisitRevision, Owner: "exploration", LeaseMs: 6000}
}

func TestIdleFlightpathParentSameVisitRetiredGroundAcquiresAndRenews(t *testing.T) {
	x, request := idleFlightpathParentRig(t)
	original := proto.Clone(x.s.flightpathActivation.operation)
	before := idleRawSnapshot(t, x.s)
	lease, err := x.s.AcquireIdleInteraction(context.Background(), request)
	if err != nil || lease.GetToken() == "" || lease.Token == x.token || lease.State.InteractionOwner != "exploration" ||
		x.s.idleInteraction != (idleInteractionBinding{x.s.sessionID, x.s.selfGUID, request.VisitId, lease.Token}) ||
		lease.State.InteractionLeaseRemainingMs != 6000 || !proto.Equal(original, x.s.flightpathActivation.operation) {
		t.Fatal("same-visit accepted retired ground could not receive fresh parent custody", lease, err)
	}
	for _, key := range []string{"state", "token", "until", "binding", "lastStep"} {
		delete(before, key)
	}
	after := idleRawSnapshot(t, x.s)
	for _, key := range []string{"state", "token", "until", "binding", "lastStep"} {
		delete(after, key)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("fresh parent lease changed unrelated raw native state")
	}
	x.advance(time.Second)
	request.Token = lease.Token
	previousUntil := x.s.interactionUntil
	renewed, err := x.s.AcquireIdleInteraction(context.Background(), request)
	if err != nil || renewed.GetToken() != lease.Token || !x.s.interactionUntil.After(previousUntil) ||
		!proto.Equal(original, x.s.flightpathActivation.operation) || len(x.f.packets) != 0 || x.f.clock != 0 || len(x.f.movementObservations) != 0 {
		t.Fatal("same-visit parent renewal changed booking or wrote gameplay", renewed, err)
	}
}

func TestIdleFlightpathParentPendingExceptionRequiresKnownAcceptedCurrentRetiredGround(t *testing.T) {
	for _, change := range []string{"unknown reply", "nonzero pending reply", "in air", "unretired", "superseded source", "wrong visit", "foreign owner"} {
		t.Run(change, func(t *testing.T) {
			x, request := idleFlightpathParentRig(t)
			switch change {
			case "unknown reply":
				x.s.flightpathActivation.operation.ReplyKnown = false
			case "nonzero pending reply":
				// A nonzero result is never proof for this pending-flight exception.
				// Ordinary terminal rejected bookings remain generic idle callers.
				x.s.flightpathActivation.operation.ReplyCode = 1
				x.s.flightpathActivation.operation.ActivationAccepted = false
				x.s.flightpathActivation.operation.Phase = "server_rejected"
			case "in air":
				x.f.world.Self.UnitFlags = 0x100000
			case "unretired":
				x.f.world.SplineCompletion.RetirementCurrent = false
			case "superseded source":
				x.f.world.SelfMotionAuthority.TransportScope.SplineRevision++
			case "wrong visit":
				x.f.world.VisitRevision++
			case "foreign owner":
				request.Owner = "trade"
			}
			original := proto.Clone(x.s.flightpathActivation.operation)
			before := idleRawSnapshot(t, x.s)
			if _, err := x.s.AcquireIdleInteraction(context.Background(), request); err == nil {
				t.Fatal("unqualified pending flight received parent custody", change)
			}
			idleNoEffects(t, x.s, x.f, before)
			if !proto.Equal(original, x.s.flightpathActivation.operation) {
				t.Fatal("refusal changed native booking receipt")
			}
		})
	}
}
