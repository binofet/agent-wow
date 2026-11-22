package buddyworld

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// These are synthetic immutable owner-loop facts alongside actual World wire
// parsing. Core's own tests qualify their production generation; no live realm,
// booking causality, server ACK acceptance or arrival is claimed here.
func transferPendingFixture(x *completionRig, target uint32, report bool) *modv1.ObservedWorldTransfer {
	from := x.scope
	return &modv1.ObservedWorldTransfer{Revision: 1, Phase: "pending", ConnectionId: from.ConnectionId, SelfGuid: from.SelfGuid,
		FromVisitRevision: from.VisitRevision, FromMapId: from.MapId, FromMotionRevision: from.MotionRevision,
		FromSplineIdKnown: from.SplineIdKnown, FromSplineId: from.SplineId, FromSplineRevision: from.SplineRevision,
		FromSplineSourceRevision: from.SplineSourceRevision, FromSplineSourceOpcode: from.SplineSourceOpcode, FromSplineActive: from.SplineActive,
		PendingMotionRevision: from.MotionRevision + 1, TargetMapKnown: true, TargetMapId: target,
		CompletionReportWritten: report, CompletionReportMotionRevision: x.s.completion.operation.GetTransportScope().GetMotionRevision()}
}

func transferPendingPacket(t *testing.T, x *completionRig, edge *modv1.ObservedWorldTransfer) *worldv1.SnapshotResponse {
	t.Helper()
	x.scope.VisitBound, x.scope.VisitRevision = false, 0
	x.scope.SplineActive, x.scope.SplineIdKnown = false, false
	x.scope.SplineId, x.scope.SplineSourceRevision, x.scope.SplineSourceOpcode = 0, 0, 0
	x.scope.SplineRevision++
	x.scope.WorldTransfer = cloneWorldTransfer(edge)
	return x.deliver(t, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, edge.TargetMapId))
}

func transferEntryPacket(t *testing.T, x *completionRig, edge *modv1.ObservedWorldTransfer) *worldv1.SnapshotResponse {
	t.Helper()
	final := cloneWorldTransfer(edge)
	final.Revision, final.Phase, final.Known = 3, "ack_written", true
	final.ToVisitRevision, final.ToMapId = final.FromVisitRevision+1, final.TargetMapId
	final.EntryMotionRevision = x.scope.MotionRevision + 1
	final.WorldportAckMotionRevision = final.EntryMotionRevision + 1
	final.WorldportAckWritten = true
	x.scope.VisitBound, x.scope.VisitRevision, x.scope.MapId = true, final.ToVisitRevision, final.ToMapId
	x.scope.SplineRevision++
	x.scope.WorldTransfer = final
	// NEW_WORLD reaches observers after the known automatic ACK, whose own
	// owner-loop barrier is separate from the inbound entry counter.
	x.scope.MotionRevision++
	body := login()
	binary.LittleEndian.PutUint32(body, final.TargetMapId)
	return x.deliver(t, opcode.SMSGNewWorld, body)
}

func transferNewTaxi(t *testing.T, x *completionRig) *worldv1.SnapshotResponse {
	t.Helper()
	fields := vitalFields()
	fields[unitFlags] = 0x100000
	r := x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
	if !r.SelfMovementFacts.Known || r.WorldTransfer.ContinuationKnown {
		t.Fatal("new CREATE must collect actual facts without claiming standalone continuation", r)
	}
	return x.start(t, completionFlyingHeader(0, 15), 20)
}

func transferAttempt(t *testing.T, unknown bool) (*completionRig, *worldv1.CompleteSplineRequest, *worldv1.SplineCompletionOperation) {
	t.Helper()
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	if unknown {
		x.f.sendError = errors.New("module reply lost after independently known Core write")
	}
	req := x.request(t)
	r, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || r.Operation.TransportUnknown != unknown {
		t.Fatal("predecessor attempt fixture", r, err)
	}
	return x, req, r.Operation
}

func TestWorldTransferPairsIndependentVisitDomainsAndRetainsUnknownCustody(t *testing.T) {
	for _, target := range []uint32{0, 1} {
		t.Run(map[uint32]string{0: "map_zero", 1: "cross_map"}[target], func(t *testing.T) {
			x, originalReq, original := transferAttempt(t, true)
			edge := transferPendingFixture(x, target, true)
			r := transferPendingPacket(t, x, edge)
			if r.WorldTransfer.Known || r.WorldTransfer.ContinuationKnown || !r.WorldTransfer.PredecessorQualified || r.SelfMovementFacts.Known || r.SelfSplineFacts.Known || !r.SplineCompletion.Operation.Pending {
				t.Fatal("pending transfer adopted or relabeled old facts", r)
			}
			r = transferEntryPacket(t, x, edge)
			if !r.WorldTransfer.Known || r.WorldTransfer.FromVisitRevision != 1 || r.WorldTransfer.ToVisitRevision != 2 || r.WorldTransfer.Core.FromVisitRevision != 9 || r.WorldTransfer.Core.ToVisitRevision != 10 || r.SelfMovementFacts.Known || r.WorldTransfer.ContinuationKnown {
				t.Fatal("entry mixed independent visit counters or invented continuation", r)
			}
			r = transferNewTaxi(t, x)
			f := r.WorldTransfer
			if !f.Known || !f.PredecessorQualified || !f.ContinuationKnown || f.CurrentScope.MapId != target || f.CurrentScope.VisitRevision != 10 || f.CurrentAuthorityRevision != r.SelfMotionAuthority.Revision || f.CurrentMovementRevision != r.SelfMovementFacts.Revision || f.CurrentUnitFlagsMotionRevision <= f.Core.WorldportAckMotionRevision || f.ArrivalKnown || f.BookingCausalityKnown || !proto.Equal(original, f.PredecessorCompletion) || !proto.Equal(original, r.SplineCompletion.Operation) {
				t.Fatal("qualified candidate changed old report or claimed causality/arrival", r)
			}
			x.now = x.now.Add(2 * time.Second)
			packets, clocks := len(x.f.packets), x.f.clockCalls
			if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || len(x.f.packets) != packets || x.f.clockCalls != clocks {
				t.Fatal("candidate released unknown completion custody", err)
			}
			replay, err := x.s.CompleteSpline(context.Background(), originalReq)
			if err != nil || !proto.Equal(original, replay.Operation) || !x.s.completion.consumed[completionKey(original.TransportScope)] {
				t.Fatal("transfer lost original receipt or consumed key", replay, err)
			}
		})
	}
}

func TestWorldTransferLatePredecessorCallbackUpdatesOnlyItsRetainedOutcome(t *testing.T) {
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	x.f.sendError = errors.New("known Core write but module unary reply lost")
	x.f.onSend = func() {
		edge := transferPendingFixture(x, 1, true)
		transferPendingPacket(t, x, edge)
		transferEntryPacket(t, x, edge)
		r := transferNewTaxi(t, x)
		if !r.WorldTransfer.Known || r.WorldTransfer.PredecessorCompletion.Phase != "dispatching" || r.WorldTransfer.PredecessorQualified || r.WorldTransfer.ContinuationKnown {
			t.Fatal("in-flight callback became a fabricated terminal report", r)
		}
	}
	r, err := x.s.CompleteSpline(context.Background(), x.request(t))
	if err != nil || !r.Operation.TransportUnknown || !r.Operation.Pending {
		t.Fatal(r, err)
	}
	view := ownMovementSnapshot(t, x.s)
	if !view.WorldTransfer.PredecessorQualified || !view.WorldTransfer.ContinuationKnown || !view.WorldTransfer.PredecessorCompletion.TransportUnknown || !view.WorldTransfer.PredecessorCompletion.Pending || !proto.Equal(view.WorldTransfer.PredecessorCompletion, r.Operation) {
		t.Fatal("late callback outcome did not remain attached to exact old attempt", view)
	}
}

func TestWorldTransferGenericPairDoesNotInventQualifiedPredecessor(t *testing.T) {
	for _, name := range []string{"no_completion", "no_core_report", "wrong_report_stamp", "not_dispatched"} {
		t.Run(name, func(t *testing.T) {
			x := newCompletionRig(t)
			if name != "no_completion" {
				x.now = x.now.Add(time.Second)
				if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err != nil {
					t.Fatal(err)
				}
			}
			edge := transferPendingFixture(x, 1, name != "no_core_report" && name != "no_completion")
			switch name {
			case "wrong_report_stamp":
				edge.CompletionReportMotionRevision++
			case "not_dispatched":
				x.s.completion.operation.TransportWritten = false
				x.s.completion.operation.Phase = "not_dispatched"
			}
			transferPendingPacket(t, x, edge)
			transferEntryPacket(t, x, edge)
			r := transferNewTaxi(t, x)
			if !r.WorldTransfer.Known || r.WorldTransfer.PredecessorQualified || r.WorldTransfer.ContinuationKnown || !r.SelfMotionAuthority.Known {
				t.Fatal("generic transfer edge was promoted to flight proof", name, r)
			}
		})
	}
}

func TestWorldTransferRejectsUnpairedOrContradictoryEntryMetadata(t *testing.T) {
	for _, name := range []string{"wrong_old_source", "missing_target_known", "wrong_target", "unknown_ack", "wrong_connection", "wrong_core_visit", "entry_counter_gap", "ack_counter_gap", "login_entry", "missing_pending"} {
		t.Run(name, func(t *testing.T) {
			x, _, _ := transferAttempt(t, true)
			edge := transferPendingFixture(x, 1, true)
			switch name {
			case "wrong_old_source":
				edge.FromSplineId++
			case "missing_target_known":
				edge.TargetMapKnown = false
			}
			if name != "missing_pending" {
				transferPendingPacket(t, x, edge)
			}
			// Start from the normal postACK metadata, then inject one contradiction
			// into that actual World entry packet before invoking the real parser.
			final := cloneWorldTransfer(edge)
			final.Known, final.Phase, final.Revision = true, "ack_written", 3
			final.ToVisitRevision, final.ToMapId = 10, 1
			final.EntryMotionRevision = x.scope.MotionRevision + 1
			final.WorldportAckMotionRevision = final.EntryMotionRevision + 1
			final.WorldportAckWritten = true
			switch name {
			case "wrong_target":
				final.TargetMapId = 0
			case "unknown_ack":
				final.WorldportAckWritten = false
				final.WorldportAckUnknown = true
			case "wrong_connection":
				final.ConnectionId = "replacement-connection"
			case "wrong_core_visit":
				final.ToVisitRevision = 11
			case "entry_counter_gap":
				final.EntryMotionRevision++
				final.WorldportAckMotionRevision++
			case "ack_counter_gap":
				final.WorldportAckMotionRevision++
			}
			x.scope.VisitBound, x.scope.VisitRevision, x.scope.MapId = true, final.ToVisitRevision, 1
			x.scope.SplineIdKnown, x.scope.SplineActive = false, false
			x.scope.SplineSourceRevision, x.scope.SplineSourceOpcode = 0, 0
			x.scope.WorldTransfer = final
			x.scope.MotionRevision = final.WorldportAckMotionRevision - 1
			body := login()
			binary.LittleEndian.PutUint32(body, 1)
			op := uint32(opcode.SMSGNewWorld)
			if name == "login_entry" {
				op = opcode.SMSGLoginVerifyWorld
			}
			r := x.deliver(t, op, body)
			if r.WorldTransfer.Known || r.WorldTransfer.ContinuationKnown {
				t.Fatal("contradictory/unpaired entry became known", name, r)
			}
		})
	}
}

func TestWorldTransferContinuationCannotRearmAfterMissingMalformedOrReplacedFacts(t *testing.T) {
	for _, name := range []string{"gap", "missing_scope", "missing_edge", "malformed", "control", "unsupported_sync", "relative_source", "create_only", "replaced_source", "replaced_create", "taxi_clear_then_restore", "taxi_clear_before_source", "death_before_source"} {
		t.Run(name, func(t *testing.T) {
			x, _, _ := transferAttempt(t, true)
			edge := transferPendingFixture(x, 1, true)
			transferPendingPacket(t, x, edge)
			transferEntryPacket(t, x, edge)
			fields := vitalFields()
			fields[unitFlags] = 0x100000
			if name == "replaced_source" || name == "replaced_create" || name == "taxi_clear_then_restore" {
				if r := transferNewTaxi(t, x); !r.WorldTransfer.ContinuationKnown {
					t.Fatal("qualified fixture", r)
				}
			}
			before := ownMovementSnapshot(t, x.s).SelfMovementFacts
			switch name {
			case "gap":
				x.scope.MotionRevision++
			case "missing_scope":
				packet(t, x.s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
			case "missing_edge":
				saved := x.scope.WorldTransfer
				x.scope.WorldTransfer = nil
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
				x.scope.WorldTransfer = saved
			case "malformed":
				x.deliver(t, opcode.MSGMoveHeartbeat, []byte{1, 1, 0})
				if got := ownMovementSnapshot(t, x.s).SelfMovementFacts; !proto.Equal(before, got) {
					t.Fatal("malformed rollback changed raw facts", before, got)
				}
			case "control":
				x.deliver(t, opcode.SMSGClientControlUpdate, []byte{1, 1, 1})
			case "unsupported_sync":
				x.deliver(t, opcode.SMSGFlightSplineSync, make([]byte, 4))
			case "relative_source":
				x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
				body := completionFlyingHeader(0, 15)
				body = append([]byte{1, 1, 1, 42, 0}, body[2:]...)
				x.scope.SplineSourceOpcode = opcode.SMSGMonsterMoveTransport
				x.scope.SplineSourceRevision = x.scope.MotionRevision + 1
				x.scope.SplineRevision = 20
				x.scope.SplineIdKnown, x.scope.SplineActive = true, true
				x.deliver(t, opcode.SMSGMonsterMoveTransport, body)
			case "create_only":
				x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
				r := ownMovementSnapshot(t, x.s)
				if !r.WorldTransfer.Known || r.WorldTransfer.ContinuationKnown {
					t.Fatal("CREATE-only source became continuation", r)
				}
				return
			case "replaced_source":
				x.start(t, completionFlyingHeader(0, 16), 21)
			case "replaced_create":
				x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
			case "taxi_clear_then_restore":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0}))
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, fields))
			case "taxi_clear_before_source":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0}))
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0x100000}))
			case "death_before_source":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 0}))
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 50}))
			}
			x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
			r := x.start(t, completionFlyingHeader(0, 17), 22)
			if !r.WorldTransfer.Known || r.WorldTransfer.ContinuationKnown || !r.SplineCompletion.Operation.Pending || !r.SplineCompletion.Operation.TransportUnknown {
				t.Fatal("later flags/source rearmed invalid continuation or released old custody", name, r)
			}
		})
	}
}

func TestWorldTransferUnknownInitialLifeMayWaitWithoutRearmingDeath(t *testing.T) {
	x, _, _ := transferAttempt(t, true)
	edge := transferPendingFixture(x, 1, true)
	transferPendingPacket(t, x, edge)
	transferEntryPacket(t, x, edge)
	x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 101}))
	r := transferNewTaxi(t, x)
	if !r.WorldTransfer.ContinuationKnown {
		t.Fatal("unknown initial health should wait for actual known life, unlike witnessed death", r)
	}
}

func TestWorldTransferUnknownInitialFlagsWaitForActualTaxiObservation(t *testing.T) {
	x, _, _ := transferAttempt(t, true)
	edge := transferPendingFixture(x, 1, true)
	transferPendingPacket(t, x, edge)
	transferEntryPacket(t, x, edge)
	x.deliver(t, opcode.MSGMoveHeartbeat, retirementGroundMessage(14))
	r := x.start(t, completionFlyingHeader(0, 15), 20)
	if r.WorldTransfer.ContinuationKnown || r.WorldTransfer.CurrentUnitFlagsKnown {
		t.Fatal("old-visit taxi flag survived entry as current", r)
	}
	r = x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0x100000}))
	if !r.WorldTransfer.ContinuationKnown {
		t.Fatal("unknown initial flags should await an actual new-visit taxi flag", r)
	}
}

func TestWorldTransferReportStampMayPrecedeHarmlessSourceCatchup(t *testing.T) {
	x, _, old := transferAttempt(t, false)
	x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
	edge := transferPendingFixture(x, 1, true)
	if edge.FromMotionRevision <= old.TransportScope.MotionRevision {
		t.Fatal("fixture did not advance harmless source catchup")
	}
	transferPendingPacket(t, x, edge)
	transferEntryPacket(t, x, edge)
	r := transferNewTaxi(t, x)
	if !r.WorldTransfer.ContinuationKnown || r.WorldTransfer.Core.CompletionReportMotionRevision != old.TransportScope.MotionRevision {
		t.Fatal("harmless pre-Pending catchup invalidated exact reported source", r)
	}
}

func TestWorldTransferIgnoredNonmotionPacketPreservesPendingBaseline(t *testing.T) {
	x, _, _ := transferAttempt(t, true)
	edge := transferPendingFixture(x, 1, true)
	before := transferPendingPacket(t, x, edge)
	// Taxi status is not consumed by World and is not a Core motion barrier.
	// Its immutable envelope therefore remains exactly the Pending envelope.
	body := append(binary.LittleEndian.AppendUint64(nil, 0xf13000012400002b), 1)
	if _, err := x.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGTaxinodeStatus, Payload: body, TransportScope: cloneMotionScope(x.scope)}); err != nil {
		t.Fatal(err)
	}
	ignored := ownMovementSnapshot(t, x.s)
	if ignored.IgnoredPackets != before.IgnoredPackets+1 || !proto.Equal(before.WorldTransfer, ignored.WorldTransfer) {
		t.Fatal("ignored nonmotion packet poisoned the captured Pending context", before.WorldTransfer, ignored)
	}
	transferEntryPacket(t, x, edge)
	after := transferNewTaxi(t, x)
	if !after.WorldTransfer.Known || !after.WorldTransfer.ContinuationKnown || !after.SplineCompletion.Operation.Pending {
		t.Fatal("unchanged Pending envelope could not pair and qualify continuation", after)
	}
}

func TestWorldTransferSnapshotsDetachHistoryAndDoNotRefreshIt(t *testing.T) {
	x, _, _ := transferAttempt(t, false)
	edge := transferPendingFixture(x, 1, true)
	transferPendingPacket(t, x, edge)
	transferEntryPacket(t, x, edge)
	r := transferNewTaxi(t, x)
	expected := proto.Clone(r.WorldTransfer).(*worldv1.WorldTransferObservation)
	r.WorldTransfer.Core.TargetMapId = 999
	r.WorldTransfer.PredecessorAuthority.TransportScope.ConnectionId = "caller"
	r.WorldTransfer.PredecessorCompletion.TransportScope.SplineId = 99
	r.WorldTransfer.CurrentScope.WorldTransfer.ConnectionId = "caller"
	if after := ownMovementSnapshot(t, x.s).WorldTransfer; !proto.Equal(expected, after) {
		t.Fatal("snapshot aliases changed transfer evidence", expected, after)
	}
	x.now = x.now.Add(5 * time.Second)
	if after := ownMovementSnapshot(t, x.s).WorldTransfer; !proto.Equal(expected, after) {
		t.Fatal("snapshot/elapsed duration refreshed transfer history", expected, after)
	}
	beforeRaw := ownMovementSnapshot(t, x.s).SelfMovementFacts
	r = x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{unitHealth: 40}))
	if !r.WorldTransfer.ContinuationKnown || !proto.Equal(beforeRaw, r.SelfMovementFacts) || !proto.Equal(expected.PredecessorAuthority, r.WorldTransfer.PredecessorAuthority) || !proto.Equal(expected.PredecessorCompletion, r.WorldTransfer.PredecessorCompletion) {
		t.Fatal("harmless catchup relabeled historical/raw facts", r)
	}
}
