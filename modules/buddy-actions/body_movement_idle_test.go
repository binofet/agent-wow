package buddyactions

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Synthetic counterpart of Core's exact scope admission and post-write raw
// report. Core's separate owner-loop tests establish the actual transport fence.
type idleBodyReportSession struct {
	sessionAPI
	f *fakeSession
}

func (c *idleBodyReportSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.TransportScope != nil {
		c.f.mu.Lock()
		current := clonePortalScope(c.f.portalCoreScope)
		c.f.mu.Unlock()
		if !proto.Equal(current, r.TransportScope) {
			return nil, modcb.PacketNotDispatchedError("synthetic idle motion scope superseded")
		}
	}
	out, err := c.sessionAPI.SendPacket(ctx, r, opts...)
	if err == nil && (r.Opcode == opcode.MSGMoveSetFacing || r.Opcode == opcode.MSGMoveJump || r.Opcode == opcode.MSGMoveFallLand || r.Opcode == opcode.MSGMoveStartSwim) {
		c.f.mu.Lock()
		defer c.f.mu.Unlock()
		if c.f.portalCoreScope == nil {
			return nil, errors.New("synthetic body scope unavailable")
		}
		c.f.portalCoreScope.MotionRevision++
		c.f.ownMovementReport = &modv1.WorldPacket{Opcode: r.Opcode, Payload: append([]byte(nil), r.Payload...), TransportScope: clonePortalScope(c.f.portalCoreScope)}
	}
	return out, err
}

func idleBodyFixture(t *testing.T, ownLease bool) (*Service, *fakeSession, *actionsv1.BodyMovementRequest, context.Context, func(time.Duration)) {
	t.Helper()
	s, f, _, ctx, advance := targetMovementFixture(t)
	r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 701, DurationMs: 50}
	if ownLease {
		seedSyntheticPortalWorldScope(f, 9)
		lease, err := s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Owner: "exploration", LeaseMs: 30000})
		if err != nil {
			t.Fatal(err)
		}
		r.IdleToken = lease.Token
		s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
	}
	s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, start buddynav.Point, _, _ float64, d time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
		end := start
		end.X += .35
		return []buddynav.BodyMotionSample{{Point: start}, {At: d, Point: end}}, nil
	}
	s.bodyMotionWait = func(_ context.Context, d time.Duration) error { advance(d); return nil }
	return s, f, r, ctx, advance
}

func TestBodyInputIdleLeaseRetainsCustodyAndPreservesOrdinaryWire(t *testing.T) {
	var legacy []*modv1.SendPacketRequest
	for _, withLease := range []bool{false, true} {
		s, f, r, ctx, _ := idleBodyFixture(t, withLease)
		binding, token, until := s.idleInteraction, s.interactionToken, s.interactionUntil
		out, err := s.MoveBody(ctx, r)
		if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || !out.BodyMovement.TransportWritten || s.state.Moving || s.state.MotionUnconfirmed ||
			countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
			t.Fatal("normal body input did not finish under its existing custody", withLease, out, err, f.packets)
		}
		if s.idleInteraction != binding || s.interactionToken != token || s.interactionUntil != until {
			t.Fatal("body input acquired, renewed or released the parent lease", s.idleInteraction)
		}
		if !withLease {
			legacy = f.packets
		} else {
			if s.state.InteractionOwner != "exploration" || len(legacy) != len(f.packets) {
				t.Fatal("parent lease was lost", out, f.packets)
			}
			for i := range legacy {
				if legacy[i].Opcode != f.packets[i].Opcode || !bytes.Equal(legacy[i].Payload, f.packets[i].Payload) || f.packets[i].TransportScope == nil {
					t.Fatal("optional lease changed game wire or lost its scope", i, legacy[i], f.packets[i])
				}
			}
			encoded, err := protojson.Marshal(out)
			if err != nil || bytes.Contains(encoded, []byte(token)) {
				t.Fatal("body result exposed the private idle bearer", string(encoded), err)
			}
		}
	}
}

func TestBodyInputIdleTokenCannotUseLegacyForeignOrExpiringCustody(t *testing.T) {
	for _, kind := range []string{"legacy acquisition", "missing token", "foreign token", "wrong binding visit", "expiring lease", "foreign owner"} {
		t.Run(kind, func(t *testing.T) {
			s, f, r, ctx, _ := idleBodyFixture(t, true)
			switch kind {
			case "legacy acquisition":
				s.idleInteraction = idleInteractionBinding{}
			case "missing token":
				r.IdleToken = ""
			case "foreign token":
				r.IdleToken = "foreign"
			case "wrong binding visit":
				s.idleInteraction.visit = 2
			case "expiring lease":
				s.interactionUntil = s.now().Add(999 * time.Millisecond)
			case "foreign owner":
				s.state.InteractionOwner = "trade"
			}
			binding, token := s.idleInteraction, s.interactionToken
			out, err := s.MoveBody(ctx, r)
			if status.Code(err) != codes.FailedPrecondition || out != nil || len(f.packets) != 0 || s.bodyMoving || s.state.MotionUnconfirmed || s.idleInteraction != binding || s.interactionToken != token {
				t.Fatal("unqualified idle bearer gained body movement", kind, out, err, f.packets)
			}
		})
	}
}

func TestBodyInputIdleLeaseRecheckedBeforeFirstAndFinalPhysicalWrite(t *testing.T) {
	for _, seam := range []string{"planner", "first clock", "wait", "final clock"} {
		for _, change := range []string{"token", "owner", "body", "same-map visit", "Core visit", "lease deadline"} {
			t.Run(seam+"/"+change, func(t *testing.T) {
				s, f, r, ctx, advance := idleBodyFixture(t, true)
				mutated := false
				mutate := func() {
					if mutated {
						return
					}
					mutated = true
					switch change {
					case "token":
						s.interactionToken = "replacement"
						s.idleInteraction.token = s.interactionToken
					case "owner":
						s.state.InteractionOwner = "trade"
					case "body":
						s.selfGUID = 8
					case "same-map visit":
						s.encounterVisit.revision = 2
						f.world.VisitRevision = 2
					case "Core visit":
						f.portalCoreScope.VisitRevision = 10
						f.ownMovementReport = nil
					case "lease deadline":
						s.interactionUntil = s.now().Add(999 * time.Millisecond)
					}
				}
				plan := s.bodyMotionPlan
				s.bodyMotionPlan = func(ctx context.Context, dir string, m uint32, p buddynav.Point, b, v float64, d time.Duration, j bool) ([]buddynav.BodyMotionSample, error) {
					if seam == "planner" {
						mutate()
					}
					return plan(ctx, dir, m, p, b, v, d, j)
				}
				clocks := 0
				f.onClock = func() {
					clocks++
					if seam == "first clock" && clocks == 1 || seam == "final clock" && clocks == 4 {
						mutate()
					}
				}
				s.bodyMotionWait = func(_ context.Context, d time.Duration) error {
					advance(d)
					if seam == "wait" {
						mutate()
					}
					return nil
				}
				out, err := s.MoveBody(ctx, r)
				want := 0
				if seam == "wait" {
					want = 2
				}
				if seam == "final clock" {
					want = 3
				}
				if err != nil || !mutated || len(f.packets) != want || countPackets(f, opcode.MSGMoveStop) != 0 || out.GetBodyMovement().GetPhase() == "body_input_written" || s.bodyMoving {
					t.Fatal("revoked idle body input wrote after its binding changed", seam, change, out, err, f.packets)
				}
				if change == "token" && s.interactionToken != "replacement" || change == "owner" && s.state.InteractionOwner != "trade" {
					t.Fatal("old body operation overwrote replacement custody", s.stateLocked())
				}
			})
		}
	}
}

func TestBodyInputIdleTokenRequestCannotBeReboundDuringPlanning(t *testing.T) {
	s, f, r, ctx, _ := idleBodyFixture(t, true)
	plan := s.bodyMotionPlan
	s.bodyMotionPlan = func(ctx context.Context, dir string, m uint32, p buddynav.Point, b, v float64, d time.Duration, j bool) ([]buddynav.BodyMotionSample, error) {
		r.IdleToken = "replacement"
		s.interactionToken, s.idleInteraction.token = r.IdleToken, r.IdleToken
		return plan(ctx, dir, m, p, b, v, d, j)
	}
	out, err := s.MoveBody(ctx, r)
	if err != nil || len(f.packets) != 0 || out.GetBodyMovement().GetPhase() == "body_input_written" || s.interactionToken != "replacement" {
		t.Fatal("caller mutation rebound the immutable parent bearer", out, err, f.packets)
	}
}
