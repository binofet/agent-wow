package buddyactions

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestFailedPacketTransportPreservesIntentWithoutUnrelatedWrites(t *testing.T) {
	for _, code := range []uint32{opcode.CMSGCastSpell, opcode.CMSGMessagechat, opcode.CMSGSetSelection} {
		s, f, _ := rig(t)
		s.state.Following, s.state.Assisting, s.state.Practicing = true, true, true
		s.followUntil, s.assistUntil, s.practiceUntil = s.now().Add(time.Hour), s.now().Add(time.Hour), s.now().Add(time.Hour)
		s.state.PracticePhase = "traveling"
		s.autoTarget = enemyGUID
		s.state.AutoAttacking = true
		s.state.AutoAttackTargetGuid = enemyGUID
		f.failSend = true
		if s.sendLocked(context.Background(), code, []byte{1}) {
			t.Fatal("unknown transport counted as success")
		}
		failure := s.state.LastTransportFailure
		if !s.state.Following || !s.state.Assisting || !s.state.Practicing || s.state.PracticePhase != "traveling" ||
			!s.state.AutoAttacking || s.autoTarget != enemyGUID || s.state.AutoAttackUnconfirmed || s.autoStopping ||
			len(f.packets) != 1 || s.state.TransportWrites != 0 || failure.GetOpcode() != code || failure.GetSequence() != 1 ||
			!failure.GetOutcomeUnknown() || failure.GetError() != "ambiguous transport failure" {
			t.Fatal("packet error canceled gameplay or sent a second operation", s.stateLocked(), len(f.packets))
		}
	}
}

func TestMovementTransportUnknownKeepsSameLuaPolicyAndResumesOnFreshPose(t *testing.T) {
	s, f, r, ctx, advance := targetMovementFixture(t)
	s.state.Following, s.state.Assisting = true, true
	s.followUntil, s.assistUntil = s.now().Add(time.Hour), s.now().Add(time.Hour)
	script, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "encounter-movement.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local movement=(function()\n" + string(script) + "\nend)()\n" + `
return {decide=function(frame)
  return {action=movement.decide(frame.input,frame.cursor,frame.input.goalGuid,{rangeYards=4.5}).action}
end,on_event=movement.on_event,export_state=function()return {version=1,data={}} end,
import_state=function(v)return v.version==1 end}`
	vm, err := behavior.NewSession(context.Background(), s.sessionID, source)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	initial := vm.Status()
	choose := func() {
		t.Helper()
		observed, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(observed)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		input["goalGuid"] = encounterGUID(r.TargetGuid)
		decision, err := vm.Decide(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		action, _ := decision.Output["action"].(map[string]any)
		if action["capability"] != "approach_target" || action["targetGuid"] != input["goalGuid"] {
			t.Fatal("same Lua stopped producing its standing movement intent", decision.Output)
		}
	}
	choose()
	f.failSend = true
	out, err := s.ApproachTarget(ctx, r)
	if err != nil || !out.GetMotionUnconfirmed() || out.GetMoving() || len(f.packets) != 1 || out.GetTransportWrites() != 0 {
		t.Fatal("failed movement was not fenced", out, err)
	}
	failure := out.GetLastTransportFailure()
	if !failure.GetOutcomeUnknown() || failure.GetOpcode() != opcode.MSGMoveStartForward {
		t.Fatal("missing factual packet outcome", failure)
	}
	f.failSend = false
	for range 3 {
		s.Tick(context.Background())
		choose()
		if _, err := s.ApproachTarget(ctx, r); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("unknown physical state admitted another movement", err)
		}
	}
	if len(f.packets) != 1 || !s.state.Following || !s.state.Assisting {
		t.Fatal("unknown movement replayed or standing intent lost", s.stateLocked(), len(f.packets))
	}
	first := append([]byte(nil), f.packets[0].Payload...)
	// A real new stopped server position, rather than a command or a guessed
	// acknowledgement, resolves the old body's physical uncertainty.
	f.world.Self.PositionRevision++
	f.world.Self.X = 2
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	choose()
	out, err = s.ApproachTarget(ctx, r)
	if err != nil || out.GetMotionUnconfirmed() || !out.GetMoving() || !out.GetFollowing() || !out.GetAssisting() ||
		out.GetTransportWrites() != 1 || len(f.packets) != 2 || bytes.Equal(first, f.packets[1].Payload) ||
		out.GetLastTransportFailure().GetSequence() != failure.GetSequence() ||
		vm.Status().SessionID != initial.SessionID || vm.Status().Revision != initial.Revision {
		t.Fatal("fresh observed pose did not resume same Lua/body intent", out, err, vm.Status())
	}
}

func TestUnknownStopKeepsIntentsAndCurrentAttackWithoutReplaying(t *testing.T) {
	s, f, _ := rig(t)
	s.refreshPositionLocked(f.world)
	s.state.Following, s.state.Assisting, s.state.Moving = true, true, true
	s.autoTarget = enemyGUID
	s.state.AutoAttacking = true
	s.state.AutoAttackTargetGuid = enemyGUID
	f.failSend = true
	if s.stopLocked(context.Background()) || !s.state.MotionUnconfirmed || s.state.Moving ||
		!s.state.Following || !s.state.Assisting || !s.state.AutoAttacking || s.state.AutoAttackUnconfirmed ||
		len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStop {
		t.Fatal("unknown stop revoked intent or authored attack stop", s.stateLocked(), f.packets)
	}
	f.failSend = false
	if s.stopLocked(context.Background()) || len(f.packets) != 1 {
		t.Fatal("ambiguous stop was replayed without a fresh observation")
	}
}

func TestClosedSessionStillRetiresGameplayIntents(t *testing.T) {
	s, f, _ := rig(t)
	s.state.Following, s.state.Assisting, s.state.Practicing = true, true, true
	s.BeforeLogout(context.Background(), &emptypb.Empty{})
	before := len(f.packets)
	s.Tick(context.Background())
	if !s.closed || s.state.Following || s.state.Assisting || s.state.Practicing || len(f.packets) != before {
		t.Fatal("closed session retained runnable intent", s.stateLocked())
	}
}
