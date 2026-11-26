package modrt

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// Attached Lua's production dispatcher, the installed descriptors, the real
// native Actions service and its scoped callback all participate in this test.
func TestAttachedLuaSummoningUsesTheSelectedBodyAndActualOffer(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	p, err := m.StartPolicyWithOptions(context.Background(), idleInteractionPolicy, behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	scope := &modv1.TransportScope{ConnectionId: "summoning-fixture", SelfGuid: 22, VisitBound: true, VisitRevision: 9, MotionRevision: 21}
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGNewWorld, Payload: make([]byte, 20), TransportScope: scope})
	cb.world.VisitRevision = 2
	cb.party = &partyv1.StateResponse{SessionId: friendlyEncounterSession, Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 77, Members: []*partyv1.Member{{Guid: 22, Online: true, Self: true}, {Guid: 42, Online: true}}}}
	var writes []uint32
	m.session.SendScopedPacket = func(ctx context.Context, code uint32, body []byte, scope *modv1.TransportScope) error {
		origin := modsession.WriteOriginFromContext(ctx)
		if origin.Writer != "lua" || origin.SessionID != friendlyEncounterSession || origin.Module != actionsModule || origin.ActionID == "" ||
			scope.GetConnectionId() != "summoning-fixture" || scope.VisitRevision != 9 {
			t.Fatal("physical write lost provenance", origin, scope)
		}
		writes = append(writes, code)
		return nil
	}
	step := func(capability string, fields map[string]any, written bool) {
		t.Helper()
		fields["capability"], fields["sessionId"], fields["selfGuid"], fields["visitId"] = capability, friendlyEncounterSession, "22", "2"
		ctx := context.WithValue(context.Background(), policyEncounterVisitKey{}, policyEncounterObservation{visit: 2, sequence: "observed"})
		_, receipt, err := p.Step(ctx, map[string]any{"request": fields}, nil)
		if err != nil || receipt == nil || receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["transport_written"] != written || receipt.Payload["effect_observed"] != false {
			t.Fatalf("%s: %#v %v", capability, receipt, err)
		}
	}
	step("select_target", map[string]any{"expectedGroupGuid": "77", "targetGuid": "42"}, true)
	// Ordinary selection is echoed separately by World before opening the stone.
	cb.world.Self.TargetKnown, cb.world.Self.TargetGuid = true, 42
	obj := &worldv1.GameObjectState{Guid: uint64(0xf110)<<48 | 194097<<24 | 3, Entry: 194097, CreatedByKnown: true, CreatedByGuid: 22,
		Present: true, MapKnown: true, TypeKnown: true, GameObjectType: 23, FlagsKnown: true, PositionKnown: true}
	cb.world.RequestedGameObjects, cb.world.SummoningObjects = []*worldv1.GameObjectState{obj}, []*worldv1.GameObjectState{obj}
	state, err := native.EncounterObservation(context.Background(), nil)
	if err != nil || state.GetSummoning().GetVisitId() != 2 || len(state.GetSummoningObjects()) != 1 {
		t.Fatal("missing Lua encounter facts", state, err)
	}
	use := map[string]any{"objectGuid": strconv.FormatUint(obj.Guid, 10), "expectedGroupGuid": "77", "expectedUseRevision": "0", "expectedCreatorGuid": "22", "selectedMemberGuid": "42"}
	step("use_summoning_object", use, true)
	step("use_summoning_object", use, false)
	offer := binary.LittleEndian.AppendUint64(nil, 42)
	offer = binary.LittleEndian.AppendUint32(offer, 27)
	offer = binary.LittleEndian.AppendUint32(offer, 90_000)
	received := proto.Clone(scope).(*modv1.TransportScope)
	received.SummonOfferSequence = 6
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSummonRequest, Payload: offer, TransportScope: received})
	response := map[string]any{"summonerGuid": "42", "offerSequence": "6", "accept": true}
	step("respond_to_summon", response, true)
	step("respond_to_summon", response, false)
	if len(writes) != 3 || writes[0] != opcode.CMSGSetSelection || writes[1] != opcode.CMSGGameobjUse || writes[2] != opcode.CMSGSummonResponse {
		t.Fatal("wrong or replayed normal game packets", writes)
	}
	r, _ := native.ObserveSummoning(context.Background(), nil)
	if r.GetOffer().GetSequence() != 6 || r.Offer.Pending || !r.Offer.TransportWritten || r.LastUse.Request.ObjectGuid != obj.Guid {
		t.Fatal(r)
	}
}
