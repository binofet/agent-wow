package buddyactions

import (
	"context"
	"fmt"
	"testing"
	"time"

	actv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const testUnendingBreath = 5697

func TestUnendingBreathMaintainsActualPartyOnDryGroundWithBuffsOff(t *testing.T) {
	ctx := context.Background()
	s, f, advance := buffRig(t, 9, testUnendingBreath)
	s.Buffs(ctx, &actv1.ToggleRequest{Enabled: false})
	for _, guid := range []uint64{4, 5, 8, 99} {
		p := proto.Clone(f.world.Owner).(*worldv1.PlayerState)
		p.Guid = guid
		f.world.NearbyPlayers = append(f.world.NearbyPlayers, p)
		if guid != 99 {
			f.party.Group.Members = append(f.party.Group.Members, &partyv1.Member{Guid: guid, Online: true})
		}
	}
	var targets []uint64
	for range 5 {
		s.Tick(ctx)
		if s.buffOp == nil || s.buffOp.view.Phase != "waiting_server" || s.buffOp.view.SpellId != testUnendingBreath {
			t.Fatal("missing automatic water breathing cast", s.buffOp)
		}
		targets = append(targets, s.buffOp.view.TargetGuid)
		confirmBuff(t, s, false)
		advance(3 * time.Second)
	}
	s.Tick(ctx)
	if fmt.Sprint(targets) != "[1 3 4 5 8]" || s.state.BuffsConfirmed != 5 || len(buffCasts(f)) != 5 || s.state.Buffing {
		t.Fatal("did not cover all and only the actual party independently of ordinary buffs", targets, s.state.BuffsConfirmed)
	}
}

func TestUnendingBreathRefreshesSwimmingAllyAndPreservesFollow(t *testing.T) {
	ctx := context.Background()
	s, f, advance := buffRig(t, 9, testUnendingBreath)
	s.Buffs(ctx, &actv1.ToggleRequest{Enabled: false})
	for _, player := range []*worldv1.PlayerState{f.world.Self, f.world.Owner} {
		player.MapId = 48
		player.MovementFlags = swimMovementFlag
	}
	s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(s.selfGUID, s.selfGUID, 1, testUnendingBreath, 0)})
	s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(ownerGUID, s.selfGUID, 1, testUnendingBreath, 62000)})
	s.Tick(ctx)
	if len(buffCasts(f)) != 0 {
		t.Fatal("recast an observed untimed or healthy finite aura")
	}
	s.state.Following, s.state.Moving = true, true
	s.followUntil = s.now().Add(time.Minute)
	advance(3 * time.Second)
	s.Tick(ctx)
	if len(buffCasts(f)) != 1 || s.buffOp.view.TargetGuid != ownerGUID || !s.state.Following || s.state.Moving || !s.swimming {
		t.Fatal("did not refresh a wet Joel and retain the swim follow intent", s.buffOp, s.state.Following, s.state.Moving, s.swimming)
	}
	confirmBuff(t, s, false)
	advance(3 * time.Second)
	f.party.Group.Known = false
	s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(s.selfGUID, s.selfGUID, 1, 0, 0)})
	s.Tick(ctx)
	if len(buffCasts(f)) != 1 {
		t.Fatal("attempted periodic water breathing without verified group membership")
	}
}

func TestUnendingBreathRetriesMissingAuraForAllyInCombat(t *testing.T) {
	ctx := context.Background()
	s, f, advance := buffRig(t, 9, testUnendingBreath)
	f.world.Owner.UnitFlags |= 0x80000
	s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: buffAura(s.selfGUID, s.selfGUID, 1, testUnendingBreath, 600000)})
	s.Tick(ctx)
	if len(buffCasts(f)) != 1 || s.buffOp.view.TargetGuid != ownerGUID {
		t.Fatal("only the caster must be out of combat", s.buffOp)
	}
	s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: buffGo(s, ownerGUID)})
	advance(9 * time.Second)
	s.Tick(ctx)
	if len(buffCasts(f)) != 1 || s.buffOp.view.Phase != "outcome_unconfirmed" {
		t.Fatal("a cast without aura evidence must remain unconfirmed during cooldown", s.buffOp)
	}
	advance(5 * time.Second)
	s.Tick(ctx)
	if len(buffCasts(f)) != 2 || s.buffOp.view.TargetGuid != ownerGUID || s.buffOp.view.Phase != "waiting_server" {
		t.Fatal("missing aura permanently blocked water breathing", s.buffOp)
	}
	confirmBuff(t, s, false)
	advance(20 * time.Second)
	s.Tick(ctx)
	if len(buffCasts(f)) != 2 || s.state.BuffsConfirmed != 1 {
		t.Fatal("retry repeated despite a verified aura")
	}
}
