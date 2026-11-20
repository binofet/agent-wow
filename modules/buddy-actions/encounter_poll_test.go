package buddyactions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestEncounterPollExpiresSpellAndMeleeWithoutIncomingPacket(t *testing.T) {
	for _, melee := range []bool{false, true} {
		t.Run(map[bool]string{false: "spell", true: "melee"}[melee], func(t *testing.T) {
			var s *Service
			var f *fakeSession
			var advance func(time.Duration)
			if melee {
				var req *actionsv1.MeleeRequest
				s, f, req, advance = exactMeleeFixture(t)
				if _, err := s.Melee(exactMeleeContext(), req); err != nil {
					t.Fatal(err)
				}
			} else {
				s, f, advance = startHealReceipt(t, false)
			}
			writes := len(f.packets)
			advance(time.Minute)
			for attempt := 0; attempt < 2; attempt++ {
				trailers := &policySapTrailer{}
				ctx := grpc.NewContextWithServerTransportStream(context.Background(), trailers)
				if _, err := s.EncounterObservation(ctx, &emptypb.Empty{}); err != nil {
					t.Fatal(err)
				}
				token, body, ok := modcb.PolicySpellEffect(trailers.md)
				if melee {
					token, body, ok = modcb.PolicyMeleeEffect(trailers.md)
				}
				if !ok || token == "" {
					t.Fatal("quiet world kept operation pending", trailers.md)
				}
				if melee {
					op := &actionsv1.MeleeOperation{}
					if err := proto.Unmarshal(body, op); err != nil || op.Phase != "outcome_unconfirmed_no_retry" {
						t.Fatal(op, err)
					}
				} else {
					op := &actionsv1.SpellOperation{}
					if err := proto.Unmarshal(body, op); err != nil || op.Phase != "outcome_unconfirmed_no_retry" || op.EffectObserved || op.ServerCastGo {
						t.Fatal("expiry invented an effect", op, err)
					}
				}
			}
			if len(f.packets) != writes {
				t.Fatal("receipt poll sent a game action", f.packets)
			}
		})
	}
}

func TestEncounterObservationUsesCurrentWrittenPoseForLuaApproach(t *testing.T) {
	s, f, request, ctx, advance := targetMovementFixture(t)
	f.world.RequestedCreature.X = 6
	for step := 0; step < 4; step++ {
		if _, err := s.ApproachTarget(ctx, request); err != nil {
			t.Fatal(err)
		}
		if s.targetMovement.view.Ready {
			break
		}
		advance(200 * time.Millisecond)
	}
	if !s.targetMovement.view.Ready || s.pos.x <= 0 || f.world.Self.X != 0 {
		t.Fatal("fixture failed to move while retaining raw baseline", s.pos, f.world.Self)
	}
	out, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	self := out.Players[encounterGUID(s.selfGUID)]
	if self.X != s.pos.x || self.Moving || self.PositionRevision != f.world.Self.PositionRevision {
		t.Fatal("Lua received old spawn pose", self, s.pos)
	}
	// Exercise the real Lua module with that same projected observation. Its
	// range decision must settle rather than walk again from the original spawn.
	source, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "encounter-movement.lua"))
	if err != nil {
		t.Fatal(err)
	}
	script := "local movement=(function()\n" + string(source) + "\nend)()\n" + `return {
  on_event=function() return true end, export_state=function() return {version=1,data={}} end,
  import_state=function() return true end,
  decide=function(frame) return movement.decide(frame.input,"1",frame.input.testTarget,{rangeYards=4.5}) end
 }`
	vm, err := behavior.NewSession(context.Background(), s.sessionID, script)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	input["testTarget"] = encounterGUID(request.TargetGuid)
	// The explicit requested creature is not in NearbyCreatures in this fixed
	// peer; add the observed target row without changing the own pose evidence.
	input["targets"] = map[string]any{input["testTarget"].(string): map[string]any{"present": true, "positionKnown": true, "x": float64(6), "y": float64(0), "z": float64(0)}}
	decision, err := vm.Decide(context.Background(), input)
	if err != nil || decision.Output["ready"] != true || decision.Output["action"] != nil {
		t.Fatal("Lua did not use the current pose", decision.Output, err)
	}
	// A new server revision immediately wins over the previous estimate.
	f.world.Self.PositionRevision++
	f.world.Self.X = 40
	out, err = s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil || out.Players["1"].X != 40 {
		t.Fatal("stale estimate hid a correction", out, err)
	}
}
