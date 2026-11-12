package buddyprofessions

import (
	"context"
	"encoding/binary"
	"testing"

	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInspectDisenchantOnlyQueriesExplicitCarriedItem(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	s.mu.Lock()
	s.deTemplates = make(map[uint32]disenchantTemplate)
	s.deRequested = make(map[uint32]bool)
	s.selectedDisenchantGUID = 0
	s.mu.Unlock()
	f.mu.Lock()
	f.packets = nil
	f.rpcEvents = nil
	f.mu.Unlock()
	r := &professionsv1.DisenchantInspectRequest{SessionId: "body", TargetGuid: disenchantBehaviorTarget}
	state, err := s.InspectDisenchant(context.Background(), r)
	if err != nil || state.DisenchantItem == nil || state.DisenchantItem.ItemGuid != r.TargetGuid || state.DisenchantItem.Ready || state.DisenchantItem.TemplateKnown {
		t.Fatal("explicit selection lacks honest pending metadata", state, err)
	}
	f.mu.Lock()
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGItemQuerySingle || len(f.packets[0].Payload) != 4 || binary.LittleEndian.Uint32(f.packets[0].Payload) != 1001 {
		t.Fatal("inspection must only query the selected actual carried entry", f.packets)
	}
	f.mu.Unlock()
	p, _, _ := disenchantMetadataFixture(1001, 2, 2, 75)
	professionPacket(t, s, opcode.SMSGItemQuerySingleResponse, p)
	state, err = s.InspectDisenchant(context.Background(), r)
	if err != nil || state.DisenchantItem == nil || !state.DisenchantItem.Ready || !state.DisenchantItem.TemplateKnown || state.DisenchantItem.RequiredSkill != 75 || state.DisenchantItem.Bag != 1 || state.DisenchantItem.Slot != 0 {
		t.Fatal("inspection did not bind complete public metadata to exact carried source", state, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.packets) != 1 || len(f.releases) != 0 || len(s.requests) != 0 || s.active != nil {
		t.Fatal("inspection performed an action or retried the reserved query")
	}
}

func TestDisenchantReadinessBlocksEveryUnconfirmedJunkReceipt(t *testing.T) {
	for _, junkStatus := range []string{"AWAITING_SERVER_SLOT_UPDATE", "TRANSPORT_UNCONFIRMED_NO_RETRY", "UNCONFIRMED_NO_RETRY", "", "FUTURE_UNKNOWN", "SERVER_SLOT_REMOVAL_CONFIRMED"} {
		t.Run(junkStatus, func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			f.mu.Lock()
			f.world.SelfSkills[1] = &worldv1.SkillState{Slot: 1, SkillId: 333, Value: 75, MaxValue: 75, Step: 1}
			f.world.Self.ChannelObjectKnown = true
			f.world.Self.ChannelSpellKnown = true
			f.mu.Unlock()
			professionPacket(t, s, opcode.SMSGInitialSpells, professionInitialFixture(13262))
			professionObserve(t, s)
			s.mu.Lock()
			s.deTrade = &tradev1.State{SessionId: "body", Phase: "IDLE", LastJunkDisposal: &tradev1.JunkDisposal{Status: junkStatus}}
			spec, _ := recipeForSpell(13262)
			reason := s.readinessLocked(spec, nil)
			s.mu.Unlock()
			if (reason == "") != (junkStatus == "SERVER_SLOT_REMOVAL_CONFIRMED") {
				t.Fatal(junkStatus, reason)
			}
		})
	}
}

func TestInspectDisenchantRejectsUnboundOrNonItemSelection(t *testing.T) {
	for _, r := range []*professionsv1.DisenchantInspectRequest{nil, {SessionId: "foreign", TargetGuid: 0x4000000000000001}, {SessionId: "body"}, {SessionId: "body", TargetGuid: 0xf110000651000001}, {SessionId: "body", TargetGuid: 0x4000000000000000}} {
		s, f := newProfessionServiceFixture(t)
		f.mu.Lock()
		f.rpcEvents = nil
		f.mu.Unlock()
		if _, err := s.InspectDisenchant(context.Background(), r); status.Code(err) != codes.InvalidArgument {
			t.Fatal(r, err)
		}
		f.mu.Lock()
		if len(f.rpcEvents) != 0 || len(f.packets) != 0 {
			t.Fatal("invalid selection reached callback")
		}
		f.mu.Unlock()
	}
}
