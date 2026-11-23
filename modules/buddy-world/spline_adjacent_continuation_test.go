package buddyworld

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// Synthetic Core envelopes accompany actual protocol parsing. Each fresh
// Pending/entry is adjacent in both independent visit domains; no timer or
// inferred endpoint is used as transfer or arrival evidence.
func adjacentContinuationTransfer(t *testing.T, x *completionRig, target uint32) {
	t.Helper()
	edge := transferPendingFixture(x, target, true)
	edge.Revision = x.scope.GetWorldTransfer().GetRevision() + 1
	transferPendingPacket(t, x, edge)
	final := cloneWorldTransfer(edge)
	final.Revision, final.Phase, final.Known = edge.Revision+2, "ack_written", true
	final.ToVisitRevision, final.ToMapId = final.FromVisitRevision+1, target
	final.EntryMotionRevision = x.scope.MotionRevision + 1
	final.WorldportAckMotionRevision, final.WorldportAckWritten = final.EntryMotionRevision+1, true
	x.scope.VisitBound, x.scope.VisitRevision, x.scope.MapId = true, final.ToVisitRevision, target
	x.scope.SplineRevision++
	x.scope.WorldTransfer = final
	x.scope.MotionRevision++ // the actual automatic ACK's separate Core barrier
	body := login()
	binary.LittleEndian.PutUint32(body, target)
	x.deliver(t, opcode.SMSGNewWorld, body)
	fields := vitalFields()
	fields[unitFlags] = 0x100000
	x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
	r := x.start(t, completionFlyingHeader(0, 15), x.scope.SplineRevision+1)
	if !r.WorldTransfer.ContinuationKnown || !r.SelfMotionAuthority.Known || r.VisitRevision+8 != r.SelfMotionAuthority.TransportScope.VisitRevision {
		t.Fatal("fixture lacks independently qualified next adjacent segment", r)
	}
}

func assertAdjacentCompletionReplay(t *testing.T, x *completionRig, req *worldv1.CompleteSplineRequest, want *worldv1.SplineCompletionOperation) {
	t.Helper()
	r, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || r.SelfGuid != want.SelfGuid || r.VisitRevision != want.VisitRevision || r.Revision != want.Revision ||
		!proto.Equal(r.Operation, want) || r.RetirementAvailable || r.RetirementCurrent || r.TransferAdoption != nil || r.TransferAdoptionCurrent {
		t.Fatal("historical completion borrowed replacement identity or custody", r, err, want)
	}
}

func TestAdjacentSplineTransfersRetainEveryUnknownRequestAndHandoff(t *testing.T) {
	x := newCompletionRig(t)
	var requests []*worldv1.CompleteSplineRequest
	var operations []*worldv1.SplineCompletionOperation
	var handoffs []*worldv1.AdoptSplineTransferRequest
	var receipts []*worldv1.SplineTransferAdoption
	for segment := 0; segment < 4; segment++ {
		x.now = x.now.Add(time.Second)
		x.f.sendError = nil
		if segment < 2 {
			x.f.sendError = errors.New("synthetic lost response after independently known Core report")
		}
		req := x.request(t)
		r, err := x.s.CompleteSpline(context.Background(), req)
		if err != nil || r.Operation == nil || r.Operation.TransportUnknown != (segment < 2) || r.Operation.Pending != (segment < 2) {
			t.Fatal("segment did not retain its actual dispatch outcome", segment, r, err)
		}
		requests, operations = append(requests, req), append(operations, r.Operation)
		if segment == 3 {
			break
		}
		adjacentContinuationTransfer(t, x, uint32((segment+1)%2))
		x.now = x.now.Add(time.Second)
		if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil {
			t.Fatal("distinct adjacent segment bypassed explicit native handoff", segment)
		}
		adopt := transferAdoptionRequest(t, x)
		before := ownMovementSnapshot(t, x.s)
		view, err := x.s.AdoptSplineTransfer(context.Background(), adopt)
		if err != nil || !view.TransferAdoptionCurrent || view.Operation != nil || view.Revision != uint64(2+segment*2) ||
			!proto.Equal(view.TransferAdoption.Transfer, before.WorldTransfer) || !proto.Equal(view.TransferAdoption.Transfer.PredecessorCompletion, r.Operation) {
			t.Fatal("later exact adjacent handoff was unavailable or rewrote predecessor", segment, view, err)
		}
		handoffs, receipts = append(handoffs, adopt), append(receipts, view.TransferAdoption)
	}
	latest := ownMovementSnapshot(t, x.s)
	if latest.SplineCompletion.Revision != 7 || !proto.Equal(latest.SplineCompletion.TransferAdoption, receipts[2]) ||
		latest.SplineCompletion.TransferAdoptionCurrent || len(x.s.completion.completionArchive) != 3 || len(x.s.completion.transferArchive) != 2 {
		t.Fatal("Snapshot grew history or lost latest-only projection", latest.SplineCompletion)
	}
	for i := range requests {
		if !x.s.completion.consumed[completionKey(requests[i].TransportScope)] {
			t.Fatal("old physical generation lost its consumed tombstone", i)
		}
		if i < len(requests)-1 {
			assertAdjacentCompletionReplay(t, x, requests[i], operations[i])
		}
	}
	for i, req := range handoffs {
		r, err := x.s.AdoptSplineTransfer(context.Background(), req)
		want := receipts[i]
		if err != nil || r.SelfGuid != want.SelfGuid || r.VisitRevision != want.ToVisitRevision || r.Revision != want.Revision ||
			r.Operation != nil || r.RetirementAvailable || r.RetirementCurrent || r.TransferAdoptionCurrent || !proto.Equal(r.TransferAdoption, want) {
			t.Fatal("historical handoff borrowed latest slot or rearmed its grant", i, r, err)
		}
		r.TransferAdoption.Transfer.PredecessorCompletion.Pending = false
		r.TransferAdoption.Transfer.CurrentScope.ConnectionId = "caller mutation"
	}
	for i := range requests[:3] {
		assertAdjacentCompletionReplay(t, x, requests[i], operations[i])
	}
	if len(x.f.packets) != 4 || x.f.clockCalls != 4 || !proto.Equal(latest.SplineCompletion, ownMovementSnapshot(t, x.s).SplineCompletion) {
		t.Fatal("historical retries mutated current state or replayed transport", x.f.clockCalls, len(x.f.packets))
	}
}

func TestAdjacentSplineTransferStillRequiresLatestCompleteObservedEdge(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale previous edge", true: "unobserved entry"}[missing], func(t *testing.T) {
			x, _, _ := transferAdoptionRig(t, true)
			first := transferAdoptionRequest(t, x)
			if _, err := x.s.AdoptSplineTransfer(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			x.now = x.now.Add(time.Second)
			current, err := x.s.CompleteSpline(context.Background(), x.request(t))
			if err != nil {
				t.Fatal(err)
			}
			latestReceipt := proto.Clone(x.s.completion.transferAdoption).(*worldv1.SplineTransferAdoption)
			if missing {
				edge := transferPendingFixture(x, 0, true)
				edge.Revision = 4
				// No actual Pending was observed by World before this entry.
				x.scope.MotionRevision = edge.PendingMotionRevision
				transferEntryPacket(t, x, edge)
			}
			bad := proto.Clone(first).(*worldv1.AdoptSplineTransferRequest)
			bad.ExpectedRevision = current.Revision
			if _, err := x.s.AdoptSplineTransfer(context.Background(), bad); err == nil || x.s.completion.revision != current.Revision ||
				!proto.Equal(x.s.completion.operation, current.Operation) || !proto.Equal(x.s.completion.transferAdoption, latestReceipt) ||
				len(x.s.completion.transferArchive) != 0 || len(x.f.packets) != 2 {
				t.Fatal("stale or unobserved edge replaced current retained handoff", err)
			}
		})
	}
}

func TestAdjacentCompletionArchivesRetiredAndZeroSendRequestsForLaterReuse(t *testing.T) {
	x, predecessorRequest, predecessor := transferAdoptionRig(t, true)
	firstAdoptionRequest := transferAdoptionRequest(t, x)
	firstAdoption, err := x.s.AdoptSplineTransfer(context.Background(), firstAdoptionRequest)
	if err != nil {
		t.Fatal(err)
	}
	x.now = x.now.Add(time.Second)
	complete := x.request(t)
	if _, err := x.s.CompleteSpline(context.Background(), complete); err != nil {
		t.Fatal(err)
	}
	retirementGround(t, x, true)
	retire := retirementRequest(t, x)
	retired, err := x.s.RetireSplineCompletion(context.Background(), retire)
	if err != nil {
		t.Fatal(err)
	}
	// A new actual taxi source can reuse the service after a prior booking's
	// factual retirement; the old receipt and its unknown send remain history.
	x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0x100000}))
	x.start(t, completionFlyingHeader(0, 16), 21)
	x.now = x.now.Add(time.Second)
	x.f.sendError = modcb.PacketNotDispatchedError("synthetic final scoped refusal")
	refusedRequest := x.request(t)
	refused, err := x.s.CompleteSpline(context.Background(), refusedRequest)
	if err != nil || refused.Operation.Phase != "not_dispatched" || refused.Operation.Pending {
		t.Fatal("fixture lacks typed known-zero-send refusal", refused, err)
	}
	x.f.sendError = nil
	newRequest := x.request(t)
	if _, err := x.s.CompleteSpline(context.Background(), newRequest); err != nil {
		t.Fatal("explicit current CAS could not reuse known-zero-send source", err)
	}
	packets, clocks := len(x.f.packets), x.f.clockCalls
	assertAdjacentCompletionReplay(t, x, predecessorRequest, predecessor)
	assertAdjacentCompletionReplay(t, x, complete, retired.Operation)
	assertAdjacentCompletionReplay(t, x, refusedRequest, refused.Operation)
	r, err := x.s.RetireSplineCompletion(context.Background(), retire)
	if err != nil || r.Revision != retired.Revision || r.VisitRevision != retired.VisitRevision || r.SelfGuid != retired.SelfGuid ||
		!proto.Equal(r.Operation, retired.Operation) || r.RetirementAvailable || r.RetirementCurrent || r.TransferAdoption != nil || r.TransferAdoptionCurrent {
		t.Fatal("historical retirement borrowed the reused slot identity", r, err)
	}
	adjacentContinuationTransfer(t, x, 1)
	adopt, err := x.s.AdoptSplineTransfer(context.Background(), transferAdoptionRequest(t, x))
	if err != nil || !adopt.TransferAdoptionCurrent || !x.s.completion.consumed[completionKey(complete.TransportScope)] ||
		!x.s.completion.consumed[completionKey(newRequest.TransportScope)] || len(x.f.packets) != packets || x.f.clockCalls != clocks {
		t.Fatal("reused service lost old evidence or refused fresh adjacent handoff", adopt, err)
	}
	old, err := x.s.AdoptSplineTransfer(context.Background(), firstAdoptionRequest)
	if err != nil || !proto.Equal(old.TransferAdoption, firstAdoption.TransferAdoption) || old.Revision != firstAdoption.Revision || old.TransferAdoptionCurrent || old.Operation != nil {
		t.Fatal("a later source reused or relabeled the previous booking's handoff", old, err)
	}
}
