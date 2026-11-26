package buddyactions

import (
	"context"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type idlePreflightReadFault struct {
	*fakeSession
	reads int
	err   error
}

func (f *idlePreflightReadFault) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		f.reads++
		if f.reads == 2 {
			return nil, f.err
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

func TestIdleKnownUnsentTransientReadKeepsFreshScopedOwedStop(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			s, f, r, ctx, advance := idleBodyFixture(t, true)
			s.refreshPositionLocked(f.world)
			scope, err := s.newIdleMotionScopeLocked(ctx, r.IdleToken, f.world, false)
			if err != nil {
				t.Fatal(err)
			}
			ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
			if !s.movementLocked(ctx, opcode.MSGMoveStartForward, s.pos, true) {
				t.Fatal(s.stateLocked())
			}
			before := s.pos
			advance(200 * time.Millisecond)
			reader := &idlePreflightReadFault{fakeSession: f, err: status.Error(code, "synthetic dependency read unavailable")}
			s.session = reader
			index := 0
			path := []position{s.pos, {x: s.pos.x + 10, y: s.pos.y, z: s.pos.z}}
			ok, reason := s.navigationStepResultLocked(ctx, path, &index, finite, 7, true)
			if ok || reader.reads != 2 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || s.pos != before || s.state.LastTransportFailure == nil || s.state.LastTransportFailure.OutcomeUnknown {
				t.Fatal("failed read dispatched or altered checkpoint", ok, reason, reader.reads, s.stateLocked(), f.packets)
			}
			if scope.broken || s.state.MotionUnconfirmed {
				t.Error("known-unsent observation read destroyed prior physical proof", reason, s.stateLocked())
			}
			if !s.stopLocked(ctx) || countPackets(f, opcode.MSGMoveStop) != 1 || s.state.Moving || s.state.MotionUnconfirmed {
				t.Error("fresh exact scoped owed STOP could not settle", s.stateLocked(), f.packets)
			}
			for _, p := range f.packets {
				if p.TransportScope == nil || p.TransportScope.VisitRevision != 9 {
					t.Fatal("cleanup lost actual Core scope", p)
				}
			}
			if s.pos != before || countPackets(f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal("cleanup invented progress or replayed START", s.pos, f.packets)
			}
		})
	}
}

func TestIdlePrewriteUnclassifiedFailuresStillBreakScope(t *testing.T) {
	for _, err := range []error{errors.New("invalid snapshot"), status.Error(codes.Canceled, "canceled read"), status.Error(codes.PermissionDenied, "not a transient read")} {
		t.Run(err.Error(), func(t *testing.T) {
			s, f, r, ctx, advance := idleBodyFixture(t, true)
			s.refreshPositionLocked(f.world)
			scope, e := s.newIdleMotionScopeLocked(ctx, r.IdleToken, f.world, false)
			if e != nil {
				t.Fatal(e)
			}
			ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
			if !s.movementLocked(ctx, opcode.MSGMoveStartForward, s.pos, true) {
				t.Fatal(s.stateLocked())
			}
			advance(200 * time.Millisecond)
			s.session = &idlePreflightReadFault{fakeSession: f, err: err}
			index := 0
			s.navigationStepResultLocked(ctx, []position{s.pos, {x: s.pos.x + 10, y: s.pos.y, z: s.pos.z}}, &index, finite, 7, true)
			if !scope.broken || !s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("unclassified failure gained cleanup authority", s.stateLocked(), f.packets)
			}
			if s.stopLocked(ctx) || countPackets(f, opcode.MSGMoveStop) != 0 {
				t.Fatal("broken source wrote cleanup", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestExplicitIdleStopRefusalDoesNotEraseEarlierUncertainty(t *testing.T) {
	for _, mode := range []string{"clock_unavailable", "source_unavailable", "ambiguous_stop", "confirmed_stop"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, ctx, _ := idleBodyFixture(t, true)
			s.refreshPositionLocked(f.world)
			scope, err := s.newIdleMotionScopeLocked(ctx, r.IdleToken, f.world, false)
			if err != nil {
				t.Fatal(err)
			}
			ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
			if !s.movementLocked(ctx, opcode.MSGMoveStartForward, s.pos, true) {
				t.Fatal(s.stateLocked())
			}
			s.state.MotionUnconfirmed = true
			switch mode {
			case "clock_unavailable":
				f.failClock = true
			case "source_unavailable":
				f.failSnapshot = true
			case "ambiguous_stop":
				f.failSend = true
				f.failSendOpcode = opcode.MSGMoveStop
			}
			n := len(f.packets)
			result, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "confirmed_stop" {
				if result.MotionUnconfirmed || result.Moving || len(f.packets)-n != 1 || f.packets[n].Opcode != opcode.MSGMoveStop {
					t.Fatal("validated explicitSTOP did not settle once", result, f.packets[n:])
				}
				return
			}
			if !result.MotionUnconfirmed {
				t.Fatal("failed publicFollow(false) erased original uncertainty", result, f.packets[n:])
			}
			want := 0
			if mode == "ambiguous_stop" {
				want = 1
			}
			if len(f.packets)-n != want {
				t.Fatal("refusal dispatched or stop replayed", mode, f.packets[n:])
			}
		})
	}
}

func TestIdleReadRecoveryStillRequiresCurrentPhysicalSource(t *testing.T) {
	for _, mode := range []string{"visit", "control", "pose", "core_barrier", "world_envelope_only"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, ctx, advance := idleBodyFixture(t, true)
			s.refreshPositionLocked(f.world)
			scope, err := s.newIdleMotionScopeLocked(ctx, r.IdleToken, f.world, false)
			if err != nil {
				t.Fatal(err)
			}
			ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
			if !s.movementLocked(ctx, opcode.MSGMoveStartForward, s.pos, true) {
				t.Fatal(s.stateLocked())
			}
			advance(200 * time.Millisecond)
			s.session = &idlePreflightReadFault{fakeSession: f, err: status.Error(codes.Unavailable, "read unavailable")}
			index := 0
			s.navigationStepResultLocked(ctx, []position{s.pos, {x: s.pos.x + 10, y: s.pos.y, z: s.pos.z}}, &index, finite, 7, true)
			switch mode {
			case "visit":
				s.encounterVisit.revision++
			case "control":
				s.clientControlLost = true
			case "pose":
				f.world.Self.X++
				f.world.Self.PositionRevision++
			case "core_barrier":
				f.world.SelfMotionAuthority.TransportScope.MotionRevision++
				f.portalCoreScope.MotionRevision++
				f.ownMovementReport = nil
			case "world_envelope_only":
				f.world.SelfMotionAuthority.Revision++
			}
			stopped := s.stopLocked(ctx)
			if mode == "world_envelope_only" {
				if !stopped || s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStop) != 1 {
					t.Fatal("irrelevant World envelope blocked exact source cleanup", s.stateLocked(), f.packets)
				}
			} else if stopped || countPackets(f, opcode.MSGMoveStop) != 0 {
				t.Fatal("changed physical source gained cleanup", mode, s.stateLocked(), f.packets)
			}
			if countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal("unsent progress retried", f.packets)
			}
		})
	}
}
