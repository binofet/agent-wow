package buddyactions

import (
	"context"
	"testing"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
)

type portalSettledReportFixture struct {
	sessionAPI
	old *modv1.WorldPacket
}

func TestNewExplorationGroundTripCanUseCurrentSettledSource(t *testing.T) {
	for _, tc := range []string{"new trip", "cleanup", "wrong pose", "uncertain"} {
		t.Run(tc, func(t *testing.T) {
			s, f, r, ctx, _ := idleBodyFixture(t, true)
			s.refreshPositionLocked(f.world)
			s.posEstimated = true
			old, pose := clonePortalScope(f.portalCoreScope), s.pos
			if tc == "wrong pose" {
				pose.x += 2
			}
			if tc == "uncertain" {
				s.state.MotionUnconfirmed = true
			}
			f.portalCoreScope.MotionRevision++
			f.world.SelfMotionAuthority.TransportScope = clonePortalScope(f.portalCoreScope)
			report := &modv1.WorldPacket{Opcode: opcode.MSGMoveStop, Payload: bodyMovementPacket(s.selfGUID, 12, pose, 0, 0, 0, 0, 0, false, false), TransportScope: old}
			s.session = &portalSettledReportFixture{sessionAPI: s.session, old: report}
			source, err := s.newIdleMotionScopeLocked(ctx, r.IdleToken, f.world, tc == "cleanup")
			if (err == nil) != (tc == "new trip") {
				t.Fatal(tc, err)
			}
			if err == nil && source.current.MotionRevision != f.portalCoreScope.MotionRevision {
				t.Fatal("ground trip did not start from current scope", source.current)
			}
		})
	}
}

func (f *portalSettledReportFixture) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "session" && r.Method == modcb.SettledBodyInputReportMethod && f.old != nil {
		p, _ := anypb.New(f.old)
		return &modv1.InvokeModuleResponse{Result: p}, nil
	}
	return f.sessionAPI.InvokeModule(ctx, r, opts...)
}

func TestPortalNewSelectionUsesOnlyProvenUnchangedStoppedBody(t *testing.T) {
	for _, mode := range []string{"settled", "missing", "different pose", "later write", "previous visit", "moving", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, _, _ := newAreaTriggerFixture(t)
			body := s.pos
			prior := clonePortalScope(f.portalCoreScope)
			current := clonePortalScope(prior)
			current.MotionRevision += 2
			f.portalCoreScope = current
			f.world.SelfMotionAuthority.TransportScope = clonePortalScope(current)
			s.posEstimated = true
			op := uint32(opcode.MSGMoveStop)
			switch mode {
			case "different pose":
				body.x += 1
			case "previous visit":
				prior.VisitRevision--
			case "later write":
				prior.MotionRevision = current.MotionRevision + 1
			case "moving":
				op = opcode.MSGMoveStartForward
			case "unknown":
				s.state.MotionUnconfirmed = true
			}
			report := &modv1.WorldPacket{Opcode: op, Payload: bodyMovementPacket(s.selfGUID, 10, body, 0, 0, 0, 0, 0, false, false), TransportScope: prior}
			if mode == "missing" {
				report = nil
			}
			s.session = &portalSettledReportFixture{sessionAPI: s.session, old: report}
			// The raw World baseline and the STOP can legitimately differ. The source
			// here was read after an intervening party / posture notification.
			_, err := s.NotifyAreaTrigger(context.Background(), r)
			accepted := countPackets(f, opcode.CMSGAreatrigger) == 1
			if accepted != (mode == "settled") {
				t.Fatal(mode, err, s.state.Reason, s.areaTrigger.operation, f.packets)
			}
			if mode == "settled" && countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal("new entry lacked its own STOP", f.packets)
			}
		})
	}
}
