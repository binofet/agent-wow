package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func retirementAttempt(t *testing.T, unknown bool) (*completionRig, *worldv1.CompleteSplineRequest, *worldv1.SplineCompletionOperation) {
	t.Helper()
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	if unknown {
		x.f.sendError = errors.New("completion response lost after possible write")
	}
	req := x.request(t)
	r, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || r.Operation == nil || r.Operation.TransportUnknown != unknown || r.Operation.TransportWritten == unknown || len(x.f.packets) != 1 {
		t.Fatal("attempt fixture", r, err)
	}
	return x, req, r.Operation
}

// This is an actual synthetic received full MovementInfo, not the inferred ACK
// endpoint image. It deliberately needs no reference endpoint or distance rule.
func retirementGroundMessage(x float32) []byte {
	b := new(bytes.Buffer)
	b.Write([]byte{1, 1})
	u32(b, 0)
	_ = binary.Write(b, binary.LittleEndian, uint16(0))
	u32(b, 456)
	for _, v := range []float32{x, 24, 34, .5} {
		f32(b, v)
	}
	u32(b, 0)
	return b.Bytes()
}

func retirementGround(t *testing.T, x *completionRig, flagsFirst bool) *worldv1.SnapshotResponse {
	t.Helper()
	flags := func() { x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 8})) }
	if flagsFirst {
		flags()
	}
	x.deliver(t, opcode.MSGMoveHeartbeat, retirementGroundMessage(14))
	if !flagsFirst {
		flags()
	}
	r := ownMovementSnapshot(t, x.s)
	if !r.SelfMovementFacts.Known || r.SelfMovementFacts.TransportScope == nil || !r.Self.PositionKnown || r.Self.PositionInferred || r.SelfMotionAuthority.Known || !r.SelfMotionAuthority.UnitFlagsKnown || r.SelfMotionAuthority.UnitFlags&0x100000 != 0 {
		t.Fatal("ground fixture lacks actual postflight facts", r)
	}
	return r
}

func retirementRequest(t *testing.T, x *completionRig) *worldv1.RetireSplineCompletionRequest {
	t.Helper()
	r := ownMovementSnapshot(t, x.s)
	a := r.SelfMotionAuthority
	return &worldv1.RetireSplineCompletionRequest{SelfGuid: r.Self.Guid, VisitRevision: r.VisitRevision, ExpectedRevision: r.SplineCompletion.Revision, AuthorityRevision: a.Revision, MovementRevision: r.SelfMovementFacts.Revision, UnitFlagsRevision: a.UnitFlagsRevision, TransportScope: cloneMotionScope(a.TransportScope)}
}

func TestRetireSplineCompletionPreservesWrittenAndUnknownHistoryWithoutTransport(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "written", true: "unknown"}[unknown], func(t *testing.T) {
			x, completeReq, original := retirementAttempt(t, unknown)
			before := retirementGround(t, x, unknown)
			if !before.SplineCompletion.RetirementAvailable || before.SplineCompletion.RetirementCurrent {
				t.Fatal("pre-retirement admission projection missing", before.SplineCompletion)
			}
			req := retirementRequest(t, x)
			r, err := x.s.RetireSplineCompletion(context.Background(), req)
			if err != nil || r.Revision != 2 || !r.RetirementCurrent || r.Operation.Pending || !r.Operation.Retired || r.Operation.ArrivalKnown || r.Operation.Revision != 1 {
				t.Fatal(r, err)
			}
			expected := proto.Clone(original).(*worldv1.SplineCompletionOperation)
			expected.Pending = false
			expected.Retired = true
			expected.RetirementRevision = 2
			expected.RetiredMovementRevision = req.MovementRevision
			expected.RetiredUnitFlagsRevision = req.UnitFlagsRevision
			expected.RetirementScope = cloneMotionScope(req.TransportScope)
			if !proto.Equal(expected, r.Operation) || len(x.f.packets) != 1 || x.f.clockCalls != 1 || !x.s.completion.consumed[completionKey(original.TransportScope)] {
				t.Fatal("retirement rewrote delivery/physical custody", expected, r)
			}
			after := ownMovementSnapshot(t, x.s)
			if after.SelfMotionAuthority.Known || !proto.Equal(before.SelfMovementFacts, after.SelfMovementFacts) || !proto.Equal(before.SelfSplineFacts, after.SelfSplineFacts) {
				t.Fatal("retirement minted ACK or raw authority", before, after)
			}
			r.Operation.RetirementScope.MotionRevision = 9999
			replay, err := x.s.RetireSplineCompletion(context.Background(), req)
			if err != nil || replay.Revision != 2 || !proto.Equal(expected, replay.Operation) {
				t.Fatal("retirement replay or scope detachment failed", replay, err)
			}
			replay, err = x.s.CompleteSpline(context.Background(), completeReq)
			if err != nil || !replay.Operation.Retired || replay.Revision != 2 || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
				t.Fatal("original completion replay dispatched again", replay, err)
			}
		})
	}
}

func TestRetireSplineCompletionKeepsConsumedGenerationAndAllowsDistinctFutureGeneration(t *testing.T) {
	x, _, original := retirementAttempt(t, true)
	retirementGround(t, x, false)
	if _, err := x.s.RetireSplineCompletion(context.Background(), retirementRequest(t, x)); err != nil {
		t.Fatal(err)
	}
	x.f.sendError = nil
	x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0x100000}))
	x.start(t, x.header, 17)
	x.now = x.now.Add(time.Second)
	if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || len(x.f.packets) != 1 {
		t.Fatal("retirement erased consumed original generation", err)
	}
	x.start(t, completionFlyingHeader(0, 15), 18)
	x.now = x.now.Add(time.Second)
	r, err := x.s.CompleteSpline(context.Background(), x.request(t))
	if err != nil || r.Revision != 3 || r.Operation.Revision != 3 || r.Operation.Retired || !r.Operation.TransportWritten || len(x.f.packets) != 2 || !x.s.completion.consumed[completionKey(original.TransportScope)] {
		t.Fatal("retired uncertainty blocked a distinct generation or lost old tombstone", r, err)
	}
}

func TestOwnMovementFactsRetainExactReceivedScopeWithoutRelabeling(t *testing.T) {
	x, _, _ := retirementAttempt(t, false)
	x.scope.MotionRevision++
	incoming := &modv1.WorldPacket{Opcode: opcode.MSGMoveHeartbeat, Payload: retirementGroundMessage(14), TransportScope: cloneMotionScope(x.scope)}
	if _, err := x.s.OnPacket(context.Background(), incoming); err != nil {
		t.Fatal(err)
	}
	mi := ownMovementSnapshot(t, x.s).SelfMovementFacts
	saved := proto.Clone(mi).(*worldv1.OwnMovementFacts)
	if mi.TransportScope == nil || mi.TransportScope.MotionRevision != x.scope.MotionRevision {
		t.Fatal("full received MI did not capture its packet scope", mi)
	}
	mi.TransportScope.ConnectionId = "caller-mutated"
	mi.TransportScope.MotionRevision = 9999
	incoming.TransportScope.ConnectionId = "packet-mutated"
	incoming.TransportScope.MotionRevision = 9998
	if current := ownMovementSnapshot(t, x.s).SelfMovementFacts; !proto.Equal(saved, current) {
		t.Fatal("snapshot or original packet scope aliases retained MI", saved, current)
	}
	after := x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 8})).SelfMovementFacts
	if !proto.Equal(saved, after) || after.TransportScope.MotionRevision == x.scope.MotionRevision {
		t.Fatal("later flags relabeled stale MI", saved, after)
	}
	x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{unitHealth: 40}))
	if current := ownMovementSnapshot(t, x.s).SelfMovementFacts; !proto.Equal(saved, current) {
		t.Fatal("foreign catchup refreshed received scope", saved, current)
	}
	bad := append(retirementGroundMessage(15), 0)
	x.deliver(t, opcode.MSGMoveHeartbeat, bad)
	if current := ownMovementSnapshot(t, x.s).SelfMovementFacts; !proto.Equal(saved, current) {
		t.Fatal("malformed image committed new scope", saved, current)
	}
}

func TestRetireSplineCompletionRequiresNewActualImageAndFlags(t *testing.T) {
	for _, name := range []string{"missing_image", "missing_flags", "spline_enabled_image", "inferred_endpoint_only"} {
		t.Run(name, func(t *testing.T) {
			x, _, _ := retirementAttempt(t, true)
			switch name {
			case "missing_image":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 8}))
			case "missing_flags":
				x.deliver(t, opcode.MSGMoveHeartbeat, retirementGroundMessage(14))
			case "spline_enabled_image":
				b := retirementGroundMessage(14)
				binary.LittleEndian.PutUint32(b[2:], 0x08000000)
				x.deliver(t, opcode.MSGMoveHeartbeat, b)
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 8}))
			case "inferred_endpoint_only":
				x.s.state.players[0].trajectory.controlReturned = true
				ownMovementSnapshot(t, x.s)
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 8}))
			}
			_, err := x.s.RetireSplineCompletion(context.Background(), retirementRequest(t, x))
			if err == nil || ownMovementSnapshot(t, x.s).SplineCompletion.Revision != 1 || ownMovementSnapshot(t, x.s).SplineCompletion.Operation.Retired || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
				t.Fatal("missing actual postflight facts retired uncertainty", name, err)
			}
			if name == "missing_image" && !strings.Contains(err.Error(), "new actual own MovementInfo") {
				t.Fatal("missing actual image was not reported explicitly", err)
			}
		})
	}
}

func TestRetireSplineCompletionCannotRearmAfterUnsupportedAttemptHistory(t *testing.T) {
	for _, name := range []string{"malformed", "gap", "unsupported", "missing_scope", "pose_only", "control", "create", "stop", "retransmission", "new_source", "same_map_visit", "dead_then_alive", "nonfinite_then_finite", "inactive_then_active"} {
		t.Run(name, func(t *testing.T) {
			x, _, _ := retirementAttempt(t, true)
			switch name {
			case "malformed":
				x.deliver(t, opcode.MSGMoveHeartbeat, append(retirementGroundMessage(14), 0))
			case "gap":
				x.scope.MotionRevision++
			case "inactive_then_active":
				x.scope.SplineActive = false
				x.deliver(t, opcode.MSGMoveHeartbeat, retirementGroundMessage(14))
				x.scope.SplineActive = true
			case "unsupported":
				x.deliver(t, opcode.SMSGFlightSplineSync, make([]byte, 4))
			case "missing_scope":
				packet(t, x.s, opcode.MSGMoveHeartbeat, retirementGroundMessage(14))
			case "pose_only":
				b := new(bytes.Buffer)
				u32(b, 1)
				b.Write([]byte{1, 1, 1, 0x40, 0})
				for _, v := range []float32{14, 24, 34, .5} {
					f32(b, v)
				}
				x.deliver(t, opcode.SMSGUpdateObject, b.Bytes())
			case "control":
				x.deliver(t, opcode.SMSGClientControlUpdate, []byte{1, 1, 1})
			case "create":
				x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
			case "stop":
				x.start(t, ownSplineWithID(t, monsterStopGolden, 0), 18)
			case "retransmission":
				x.start(t, x.header, 17)
			case "new_source":
				x.start(t, completionFlyingHeader(0, 15), 18)
			case "same_map_visit":
				x.scope.VisitRevision++
				x.scope.SplineActive = false
				x.deliver(t, opcode.SMSGNewWorld, login())
			case "dead_then_alive":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 0}))
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 50}))
			case "nonfinite_then_finite":
				b, err := movementinfo.Encode(movementinfo.Info{Flags: 0x02000000, X: 14, Y: 24, Z: 34, PitchPresent: true, Pitch: float32(math.NaN())})
				if err != nil {
					t.Fatal(err)
				}
				x.deliver(t, opcode.MSGMoveHeartbeat, ownMovementMessage(1, b))
			}
			retirementGround(t, x, false)
			x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{unitHealth: 40}))
			if _, err := x.s.RetireSplineCompletion(context.Background(), retirementRequest(t, x)); err == nil {
				t.Fatal("later healthy/foreign facts rearmed unsupported attempt", name)
			}
			r := ownMovementSnapshot(t, x.s).SplineCompletion
			if r.Revision != 1 || r.Operation.Retired || !r.Operation.Pending || !r.Operation.TransportUnknown || r.RetirementAvailable || r.RetirementCurrent || len(x.f.packets) != 1 {
				t.Fatal("failed retirement changed uncertain history", name, r)
			}
		})
	}
}

func TestRetirementCurrentIsLiveAndCannotRestoreAfterPoseOnlySupersession(t *testing.T) {
	x, completeReq, _ := retirementAttempt(t, true)
	retirementGround(t, x, false)
	req := retirementRequest(t, x)
	r, err := x.s.RetireSplineCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	history := proto.Clone(r.Operation).(*worldv1.SplineCompletionOperation)
	r1 := x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{unitHealth: 40}))
	if !r1.SplineCompletion.RetirementCurrent || !proto.Equal(history, r1.SplineCompletion.Operation) || r1.SplineCompletion.Revision != 2 || r1.SelfMotionAuthority.TransportScope.MotionRevision <= history.RetirementScope.MotionRevision {
		t.Fatal("harmless catchup rewrote or lost retirement", r1)
	}
	r1 = x.deliver(t, opcode.MSGMoveHeartbeat, retirementGroundMessage(15))
	if !r1.SplineCompletion.RetirementCurrent || !proto.Equal(history, r1.SplineCompletion.Operation) {
		t.Fatal("new actual same-source pose rewrote retirement history", r1)
	}
	b := new(bytes.Buffer)
	u32(b, 1)
	b.Write([]byte{1, 1, 1, 0x40, 0})
	for _, v := range []float32{15, 24, 34, .5} {
		f32(b, v)
	}
	x.deliver(t, opcode.SMSGUpdateObject, b.Bytes())
	retirementGround(t, x, false)
	r, err = x.s.RetireSplineCompletion(context.Background(), req)
	if err != nil || r.RetirementCurrent || !proto.Equal(history, r.Operation) || r.Revision != 2 {
		t.Fatal("history replay restored invalid live retirement proof", r, err)
	}
	r, err = x.s.CompleteSpline(context.Background(), completeReq)
	if err != nil || r.RetirementCurrent || !proto.Equal(history, r.Operation) || len(x.f.packets) != 1 {
		t.Fatal("old completion replay restored proof or dispatched", r, err)
	}
}

func TestRetireSplineCompletionRejectsStaleBindingsAndNoDispatch(t *testing.T) {
	for _, name := range []string{"self", "visit", "counter", "authority", "movement", "flags", "scope", "missing_scope"} {
		t.Run(name, func(t *testing.T) {
			x, _, _ := retirementAttempt(t, true)
			retirementGround(t, x, false)
			req := retirementRequest(t, x)
			switch name {
			case "self":
				req.SelfGuid++
			case "visit":
				req.VisitRevision++
			case "counter":
				req.ExpectedRevision++
			case "authority":
				req.AuthorityRevision++
			case "movement":
				req.MovementRevision++
			case "flags":
				req.UnitFlagsRevision++
			case "scope":
				req.TransportScope.MotionRevision++
			case "missing_scope":
				req.TransportScope = nil
			}
			if _, err := x.s.RetireSplineCompletion(context.Background(), req); err == nil || ownMovementSnapshot(t, x.s).SplineCompletion.Revision != 1 || len(x.f.packets) != 1 {
				t.Fatal("stale retirement binding mutated custody", name, err)
			}
		})
	}
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	x.f.sendError = modcb.PacketNotDispatchedError("core scope fence refused")
	r, err := x.s.CompleteSpline(context.Background(), x.request(t))
	if err != nil || r.Operation.Phase != "not_dispatched" {
		t.Fatal(r, err)
	}
	retirementGround(t, x, false)
	if _, err := x.s.RetireSplineCompletion(context.Background(), retirementRequest(t, x)); err == nil || ownMovementSnapshot(t, x.s).SplineCompletion.Operation.Retired {
		t.Fatal("zero-send receipt was retired as an attempted write", err)
	}
}

func TestRetireSplineCompletionRefusesCounterExhaustion(t *testing.T) {
	x, _, _ := retirementAttempt(t, true)
	retirementGround(t, x, false)
	x.s.completion.revision = ^uint64(0)
	if _, err := x.s.RetireSplineCompletion(context.Background(), retirementRequest(t, x)); err == nil || x.s.completion.revision != ^uint64(0) || x.s.completion.operation.Retired || len(x.f.packets) != 1 {
		t.Fatal("retirement exhausted counter wrapped or mutated custody", err)
	}
}
