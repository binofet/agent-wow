package buddyactions

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestOwnerAFKDoesNotVetoFollowAndRemainsVisibleToLua(t *testing.T) {
	// Lotharen's actual body/class shape: AFK is a raw owner flag, never an
	// inability of the following player to execute a legal movement packet.
	s, f, advance := formationRig(t, 87, 2)
	s.sessionID = f.party.SessionId
	f.world.Owner.PlayerFlags = 2
	if reason := safePlayers(f.world, s.selfGUID); reason != "" {
		t.Fatal("AFK owner failed mechanical movement admission", reason)
	}
	toggle(t, s, true)
	for range 8 {
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if countPackets(f, opcode.MSGMoveStartForward) == 0 || !s.state.Following || f.world.Owner.PlayerFlags != 2 {
		t.Fatal("AFK suppressed legal follow or flag was hidden", s.stateLocked())
	}
	observed, err := s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	owner := observed.GetNavigation().GetFollowOwner()
	if owner.GetGuid() != ownerGUID || owner.GetPlayerFlags() != 2 {
		t.Fatal("attached Lua lost raw owner flags", owner)
	}
	field := owner.ProtoReflect().Descriptor().Fields().ByName("player_flags")
	if field == nil || field.Number() != 34 {
		t.Fatal("binary descriptor lacks the factual AFK field", field)
	}
	raw, err := protojson.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value["playerFlags"] != float64(2) {
		t.Fatal("sparse Lua observation omitted AFK", string(raw))
	}
}

func TestOwnerAFKDoesNotVetoAnExactOwnedLootApproach(t *testing.T) {
	s, f, advance, r := approachRig(t)
	f.world.Owner.PlayerFlags = 2
	var out *actionsv1.State
	for range 10 {
		var err error
		out, err = s.ApproachLoot(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if !out.GetMoving() {
			break
		}
		advance(200 * time.Millisecond)
	}
	if out == nil || out.GetMoving() || out.GetPosition().GetX() < 8.99 || countPackets(f, opcode.MSGMoveStartForward) == 0 || f.world.Owner.PlayerFlags != 2 {
		t.Fatal("AFK owner blocked selected legal loot movement", out, f.packets)
	}
}

func TestOwnerAFKDoesNotSuppressSelectedHealing(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	learn(s)
	f.world.Owner.PlayerFlags = 2
	out, err := s.CastHeal(context.Background(), &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 2050})
	if err != nil || !out.GetCastPending() || countPackets(f, opcode.CMSGCastSpell) != 1 || f.world.Owner.PlayerFlags != 2 {
		t.Fatal("AFK owner disabled a learned selected heal", out, err)
	}
}
