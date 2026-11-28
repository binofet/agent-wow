package buddyactions

import (
	"context"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func groundedSurfaceInput(t *testing.T) (*Service, *fakeSession, *actionsv1.BodyMovementRequest, context.Context) {
	t.Helper()
	s, f, request, ctx, _ := idleBodyFixture(t, false)
	seedSyntheticPortalWorldScope(f, 9)
	s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
	raw := f.world
	raw.Self.MovementFlags = passengerFlag
	s.refreshPositionLocked(raw)
	// The realm last sent the embarkation position. The finite surface writer
	// has since landed/settled on shore and physically written this ground STOP.
	s.pos = position{x: 9, y: 2, z: 1}
	s.posEstimated = true
	body, err := movementinfo.MoverFrame(s.selfGUID, movementinfo.Info{X: s.pos.x, Y: s.pos.y, Z: s.pos.z, Time: 50})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("d", 64)
	if _, err := s.session.SendPacket(modcb.WithMovementAttempt(ctx, id), &modv1.SendPacketRequest{Opcode: opcode.MSGMoveStop, Payload: body}); err != nil {
		t.Fatal(err)
	}
	if err := s.recordSurfaceFrameLocked(ctx, raw, opcode.MSGMoveStop, body, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.currentSurfaceReportLocked(ctx, raw); err != nil {
		t.Fatal("initial shore STOP", err)
	}
	return s, f, request, ctx
}

func TestGroundMountAfterDisembarkAdmitsTheSameBodyAsItsObservation(t *testing.T) {
	s, f, _, ctx := groundedSurfaceInput(t)
	toggle(t, s, true)
	learnSpells(s, 23238)
	mountForTravel(t, s, f.world.Owner, 23240)
	if !s.movementLocked(ctx, opcode.MSGMoveStartForward, s.pos, true) {
		t.Fatal("initial shore walk", s.stateLocked())
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fact := s.followMountObservationLocked(ctx, w)
	if !fact.Ready || !fact.FollowMoving || !fact.GetOwnerMounted() {
		t.Fatal("current selectable ground mount", fact)
	}
	guard := &actionsv1.FollowMountGuard{OwnerGuid: f.world.Owner.Guid, FollowRevision: s.followRevision, OwnerMountRevision: fact.OwnerMountRevision}
	_, err = s.AcquireMovement(ctx, &actionsv1.MovementOwnerRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, Owner: "ground_mount_follow", LeaseMs: 3000, SpellId: 23238, FollowMount: guard})
	if err != nil || s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 2 {
		t.Fatal("selected preparatory STOP", err, s.stateLocked())
	}
	_, err = s.CastSpell(ctx, &actionsv1.CastSpellRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SpellId: 23238, FollowMount: guard})
	if err != nil || countPackets(f, opcode.CMSGCastSpell) != 1 || f.world.Self.MovementFlags != passengerFlag {
		t.Fatal("selected mount write or raw movement changed", err, s.stateLocked())
	}
}

func TestOrdinaryMoveBodyAfterDisembarkRetainsOwnGround(t *testing.T) {
	for _, jump := range []bool{false, true} {
		t.Run(map[bool]string{false: "walk", true: "jump"}[jump], func(t *testing.T) {
			s, f, r, ctx := groundedSurfaceInput(t)
			r.Jump = jump
			before := proto.Clone(f.world.Self)
			result, err := s.MoveBody(ctx, r)
			if err != nil || result.GetBodyMovement().GetPhase() != "body_input_written" ||
				!result.BodyMovement.TransportWritten || result.Moving || result.MotionUnconfirmed ||
				countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 2 {
				t.Fatalf("ground input phase=%q written=%v frames=%v err=%v", result.GetBodyMovement().GetPhase(), result.GetBodyMovement().GetTransportWritten(), f.packets, err)
			}
			current, err := s.currentSurfaceReportLocked(ctx, f.world)
			if err != nil || current.GetOpcode() != opcode.MSGMoveStop || s.surfaceMovementWorldLocked(ctx, f.world).Self.MovementFlags != 0 || !proto.Equal(before, f.world.Self) {
				t.Fatalf("latest own ground STOP=%v err=%v raw=%v", current, err, f.world.Self)
			}
			n := len(f.packets)
			if _, err := s.MoveBody(ctx, r); err != nil || len(f.packets) != n {
				t.Fatal("completed input repeated", err)
			}
		})
	}
}
