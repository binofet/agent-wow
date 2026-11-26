package modrt

import (
	"context"
	"encoding/binary"
	"maps"
	"strconv"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
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

func TestGroupSummoningFullEncounterBundle(t *testing.T) {
	ctx := context.Background()
	source := followFallbackBundle(t)
	const caster, one, two, recipient = 11, 12, 13, 14
	roster := &partyv1.Group{Known: true, InGroup: true, GroupGuid: 80, LeaderGuid: caster}
	for _, g := range []uint64{caster, one, two, recipient} {
		roster.Members = append(roster.Members, &partyv1.Member{Guid: g, Online: true})
	}
	type actor struct {
		vm        *behavior.Session
		o         *actionsv1.EncounterObservationResponse
		inventory string
	}
	actors := map[uint64]*actor{}
	makePlayer := func(g uint64) *actionsv1.EncounterPlayer {
		return &actionsv1.EncounterPlayer{Guid: g, Present: true, MapKnown: true, MapId: proto.Uint32(0),
			AliveKnown: true, Alive: proto.Bool(true), CombatKnown: true, MovementKnown: true, PositionKnown: true,
			PositionAgeKnown: true, PositionRevision: 1, HealthKnown: true, Health: 100, MaxHealth: 100}
	}
	for _, g := range []uint64{caster, one, two, recipient} {
		sid := "summon-fake-" + strconv.FormatUint(g, 10)
		session, err := behavior.NewSessionWithOptions(ctx, sid, source, behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		o := &actionsv1.EncounterObservationResponse{SessionId: sid, SelfGuid: g, VisitId: 1, MapId: proto.Uint32(0), HostOwned: proto.Bool(true),
			WorldObservationRevision: 1, SpellbookKnown: true, Players: map[string]*actionsv1.EncounterPlayer{},
			Party:     &actionsv1.EncounterParty{Group: proto.Clone(roster).(*partyv1.Group), Revision: 1},
			Summoning: &actionsv1.SummoningObservation{SessionId: sid, SelfGuid: g, VisitId: 1}}
		if g == recipient {
			o.Players["14"] = makePlayer(g)
		} else {
			for _, peer := range []uint64{caster, one, two} {
				o.Players[strconv.FormatUint(peer, 10)] = makePlayer(peer)
			}
		}
		if g == caster {
			o.Spells = map[string]*actionsv1.EncounterSpell{"698": {Known: true, Ready: true, MetadataKnown: true, Capability: "generic"}}
		}
		actors[g] = &actor{vm: session, o: o}
	}
	enqueue := func(a *actor, kind string, value map[string]any) string {
		t.Helper()
		s, err := a.vm.Enqueue(behavior.Event{Kind: kind, Value: value})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatUint(s, 10)
	}
	step := func(g uint64, cmd map[string]any, capability, phase string) map[string]any {
		t.Helper()
		a := actors[g]
		sequence := enqueue(a, "encounter_observation", thermapluggPolicyJSON(t, a.o))
		if cmd != nil {
			cmd = maps.Clone(cmd)
			cmd["sessionId"], cmd["selfGuid"], cmd["visitId"] = a.o.SessionId, strconv.FormatUint(g, 10), strconv.FormatUint(a.o.VisitId, 10)
			enqueue(a, "command", cmd)
		}
		out, err := a.vm.Decide(ctx, map[string]any{"kind": "encounter", "observationSequence": sequence, "observationSequences": map[string]any{"inventory": a.inventory}})
		if err != nil {
			t.Fatal(err)
		}
		action, _ := out.Output["action"].(map[string]any)
		task, _ := out.Output["task"].(map[string]any)
		if capability != "" && action["capability"] != capability || capability == "" && action != nil || phase != "" && task["phase"] != phase {
			t.Fatalf("body %d, expected action %q phase %q: got action %v task %v", g, capability, phase, action, task)
		}
		return action
	}
	plan := map[string]any{"task": "party_summon", "id": "explicit", "groupGuid": "80", "warlockGuid": "11", "helperOneGuid": "12", "helperTwoGuid": "13", "recipientGuid": "14"}
	step(caster, nil, "", "")
	duplicate := maps.Clone(plan)
	duplicate["helperTwoGuid"] = "12"
	step(caster, duplicate, "", "invalid_party_summon_intent")
	step(caster, plan, "", "waiting_for_ritual_and_shard")
	for _, g := range []uint64{one, two, recipient} {
		step(g, plan, "", "")
	}
	stock := &inventoryv1.State{SessionId: actors[caster].o.SessionId, SelfGuid: caster, InventoryKnown: true, InventoryRevision: 1,
		InventoryBags: []*worldv1.InventoryBag{{ContentsKnown: true, Slots: []*worldv1.BackpackSlot{{ItemGuid: 443, Entry: 6265, MetadataKnown: true, StackCount: 1}}}}}
	actors[caster].inventory = enqueue(actors[caster], "inventory_observation", thermapluggPolicyJSON(t, stock))
	actors[caster].o.Party.Group.Members[3].Online = false
	step(caster, nil, "", "waiting_for_online_group")
	actors[caster].o.Party.Group.Members[3].Online = true
	actors[caster].o.Players["12"].InCombat = true
	step(caster, nil, "", "waiting_for_two_helpers")
	actors[caster].o.Players["12"].InCombat = false
	if a := step(caster, nil, "cast_spell", "casting_ritual"); a["spellId"] != float64(698) {
		t.Fatal(a)
	}
	step(caster, nil, "", "awaiting_warlock_portal")
	object := func(entry, kind uint32) *worldv1.GameObjectState {
		return &worldv1.GameObjectState{Guid: thermapluggPolicyGUID(entry, 1), Entry: entry, Present: true, MapKnown: true, PositionKnown: true,
			TypeKnown: true, GameObjectType: kind, FlagsKnown: true, CreatedByKnown: true, CreatedByGuid: caster}
	}
	ritual, stone, channel := object(194108, 18), object(194097, 23), object(179944, 18)
	for _, g := range []uint64{one, two} {
		actors[g].o.SummoningObjects = []*worldv1.GameObjectState{ritual}
		a := step(g, nil, "use_summoning_object", "using_assist")
		if a["objectGuid"] != strconv.FormatUint(ritual.Guid, 10) || a["selectedMemberGuid"] != nil {
			t.Fatal(a)
		}
		step(g, nil, "", "awaiting_warlock_portal")
	}
	for _, g := range []uint64{caster, one, two} {
		actors[g].o.SummoningObjects = []*worldv1.GameObjectState{stone}
	}
	if a := step(caster, nil, "select_target", "selecting_recipient"); a["targetGuid"] != "14" {
		t.Fatal(a)
	}
	step(caster, nil, "", "awaiting_selected_recipient")
	for _, g := range []uint64{caster, one, two} {
		actors[g].o.Players["11"].SelectedTargetGuid = proto.Uint64(recipient)
	}
	a := step(caster, nil, "use_summoning_object", "using_stone")
	if a["selectedMemberGuid"] != "14" || a["objectGuid"] != strconv.FormatUint(stone.Guid, 10) {
		t.Fatal(a)
	}
	for _, g := range []uint64{caster, one, two} {
		actors[g].o.SummoningObjects = append(actors[g].o.SummoningObjects, channel)
	}
	step(one, nil, "use_summoning_object", "using_channel")
	step(two, nil, "", "awaiting_observed_regroup")
	offer := &actionsv1.SummonOffer{VisitId: 1, Sequence: 7, SummonerGuid: caster, Pending: true, TimeoutMs: 90000}
	actors[recipient].o.Summoning.Offer = offer
	step(recipient, nil, "", "awaiting_recipient_decision")
	choice := map[string]any{"task": "summon_decision", "id": "explicit", "offerSequence": "6", "summonerGuid": "11", "accept": true}
	step(recipient, choice, "", "awaiting_recipient_decision")
	choice["offerSequence"] = "7"
	actors[recipient].o.Players["14"].InCombat = true
	step(recipient, choice, "", "waiting_for_safe_stationary_body")
	offer.Sequence = 8
	actors[recipient].o.Players["14"].InCombat = false
	step(recipient, nil, "", "awaiting_recipient_decision")
	choice["offerSequence"] = "8"
	a = step(recipient, choice, "respond_to_summon", "responding_to_observed_offer")
	if a["accept"] != true || a["offerSequence"] != "8" {
		t.Fatal(a)
	}
	offer.Pending = false
	offer.TransportWritten = true
	offer.RequestedAccept = proto.Bool(true)
	step(recipient, nil, "", "awaiting_observed_regroup")
	actors[recipient].o.Players["11"] = makePlayer(caster)
	step(recipient, nil, "", "awaiting_observed_regroup")
	actors[recipient].o.Players["14"].PositionRevision++
	actors[recipient].o.Players["14"].PositionEstimated = true
	step(recipient, nil, "", "awaiting_observed_regroup")
	actors[recipient].o.Players["14"].PositionEstimated = false
	step(recipient, nil, "", "regroup_observed")
}
