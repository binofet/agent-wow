package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPickLockBindsLearnedSpellAndLootWindowToExactObservedChest(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = "rook-lockpicking"
	ctx := context.Background()
	learnSpells(s, pickLockSpell)
	f.world.Self.ClassKnown, f.world.Self.ClassId = true, 4
	p := f.world.Self
	chest := &worldv1.GameObjectState{
		Guid: uint64(0xf110)<<48 | uint64(75295)<<24 | 1, Entry: 75295,
		Present: true, TypeKnown: true, GameObjectType: 3,
		MapKnown: true, MapId: p.MapId, PositionKnown: true,
		X: p.X + 2, Y: p.Y, Z: p.Z, FlagsKnown: true, Flags: 2, StateKnown: true, State: 1,
	}
	f.world.QuestObjects = []*worldv1.GameObjectState{chest}
	f.world.SelfSkillsKnown = true
	f.world.SelfSkills = []*worldv1.SkillState{{SkillId: lockpickingSkill, Value: 0}}
	req := &actionsv1.PickLockRequest{SessionId: s.sessionID, TargetGuid: chest.Guid}
	if _, err := s.PickLock(ctx, req); err == nil || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("unqualified rogue sent a cast")
	}
	f.world.SelfSkills[0].Value = 1
	s.state.CastPending = true
	if _, err := s.PickLock(ctx, req); err == nil || len(f.packets) != 0 {
		t.Fatal("Pick Lock interrupted another prepared action", err, f.packets)
	}
	s.state.CastPending = false
	result, err := s.PickLock(ctx, req)
	if err != nil || !result.GetLastSpellOperation().GetTransportWritten() {
		t.Fatal("qualified rogue could not send Pick Lock", result, err)
	}
	packet := f.packets[len(f.packets)-1]
	if packet.Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(packet.Payload[6:]) != 0x800 {
		t.Fatal("expected gameobject spell target", packet)
	}
	body := make([]byte, 14)
	body[8] = 6
	binary.LittleEndian.PutUint64(body, chest.Guid+1)
	_, _ = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGLootResponse, Payload: body})
	if s.supportOp.view.EffectObserved {
		t.Fatal("unrelated loot response confirmed the chest")
	}
	binary.LittleEndian.PutUint64(body, chest.Guid)
	_, _ = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGLootResponse, Payload: body})
	if s.supportOp.view.Phase != "confirmed_loot_window" || s.state.CastPending {
		t.Fatal("chest was not confirmed by its matching response", s.supportOp.view.Phase)
	}
}
