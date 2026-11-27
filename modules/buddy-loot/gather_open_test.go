package buddyloot

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func exactGatherRequest(s *Service, f *fakeSession) *lootv1.OpenGatherRequest {
	return &lootv1.OpenGatherRequest{SessionId: s.state.SessionId, SelfGuid: s.selfGUID, SourceGuid: harvestGUID,
		QuestId: harvestQuestID, ItemId: harvestItemID, OpenSpell: harvestOpenSpell, RequestId: "chosen-gather-1",
		VisitId: f.world.VisitRevision, ExpectedPositionRevision: f.actions.Position.Revision,
		ExpectedObjectRevision:    f.world.QuestObjects[0].PositionRevision,
		ExpectedInventoryRevision: f.world.InventoryRevision, ExpectedQuestLogRevision: f.world.SelfQuestLogRevision}
}

func TestGatherAutomaticTickOnlyObservesUntilLuaSelectsSource(t *testing.T) {
	_, f, _ := harvestRig(t)
	s := NewAutomaticService(f, 1, "loot-session")
	for i := 0; i < 3; i++ {
		s.Tick(context.Background())
	}
	o, err := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if err != nil || !o.GatherCandidatesKnown || o.VisitId != 1 || len(o.QuestObjects) != 1 || len(o.GatherRecipes) != 1 ||
		o.GatherRecipes[0].QuestId != harvestQuestID || o.QuestLogRevision != f.world.SelfQuestLogRevision {
		t.Fatal("raw gather observations unavailable", o, err)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "acquire_interaction") || strings.Contains(call, "approach_loot") {
			t.Fatal("native read/Tick selected or approached a quest object", f.calls)
		}
	}
	if len(f.packets) != 0 || len(s.gatherSeen) != 0 || s.state.LastGather != nil || s.state.PendingGameObjectGuid != 0 {
		t.Fatal("native automatic path still chose a gather", &s.state, f.packets)
	}
}

func TestGatherExactCallerSourceIsNotReplacedByNearerCandidate(t *testing.T) {
	s, f, _ := harvestRig(t)
	far := proto.Clone(f.world.QuestObjects[0]).(*worldv1.GameObjectState)
	far.Guid++
	far.X = 4
	f.world.QuestObjects = append(f.world.QuestObjects, far)
	enable(t, s)
	r := exactGatherRequest(s, f)
	r.SourceGuid = far.Guid
	out, err := s.OpenGather(context.Background(), r)
	if err != nil || !out.OpeningTransportAttempted || !out.OpeningTransportWritten || out.SourceGuid != far.Guid ||
		out.RequestId != r.RequestId || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell ||
		!bytes.Equal(f.packets[0].Payload, gatherOpenPacket(1, far.Guid, r.OpenSpell)) || s.state.LastGather.ObjectGuid != far.Guid {
		t.Fatal("exact caller source/recipe replaced or lost", out, err, f.packets)
	}
	before := len(f.calls)
	if again, err := s.OpenGather(context.Background(), r); err != nil || !proto.Equal(out, again) || len(f.calls) != before {
		t.Fatal("same request ID replayed physical work", again, err, f.calls)
	}
	changed := proto.Clone(r).(*lootv1.OpenGatherRequest)
	changed.QuestId++
	if _, err := s.OpenGather(context.Background(), changed); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("request ID rebound its recipe", err, f.packets)
	}
}

func TestGatherStaleOrUnsupportedRequestHasZeroPhysicalEffects(t *testing.T) {
	for _, fault := range []string{"visit", "position", "object", "inventory", "quest_revision", "quest", "item", "spell", "other_body", "foreign_lease", "quest_complete", "duplicate_source", "no_approach"} {
		t.Run(fault, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			enable(t, s)
			r := exactGatherRequest(s, f)
			switch fault {
			case "visit":
				r.VisitId++
			case "position":
				r.ExpectedPositionRevision++
			case "object":
				r.ExpectedObjectRevision++
			case "inventory":
				r.ExpectedInventoryRevision++
			case "quest_revision":
				r.ExpectedQuestLogRevision++
			case "quest":
				r.QuestId++
			case "item":
				r.ItemId++
			case "spell":
				r.OpenSpell = 3365
			case "other_body":
				r.SelfGuid++
			case "foreign_lease":
				f.leaseOwner = "trade"
			case "quest_complete":
				f.world.SelfQuestLog[0].Complete = true
			case "duplicate_source":
				f.world.QuestObjects = append(f.world.QuestObjects, proto.Clone(f.world.QuestObjects[0]).(*worldv1.GameObjectState))
			case "no_approach":
				f.world.QuestObjects[0].X = 10
			}
			if _, err := s.OpenGather(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("bad gather admitted", fault, err)
			}
			for _, call := range f.calls {
				if strings.Contains(call, "acquire_interaction") || strings.Contains(call, "approach_loot") {
					t.Fatal("bad request changed custody/motion", fault, f.calls)
				}
			}
			if len(f.packets) != 0 || len(s.gatherRequests) != 0 || len(s.gatherSeen) != 0 || s.state.LastGather != nil {
				t.Fatal("bad request changed gather custody", fault, &s.state, f.packets)
			}
		})
	}
}

func TestGatherUnknownOpeningCannotReplayUnderNewRequestOrEnable(t *testing.T) {
	s, f, _ := harvestRig(t)
	enable(t, s)
	f.failOpcode = opcode.CMSGCastSpell
	r := exactGatherRequest(s, f)
	out, err := s.OpenGather(context.Background(), r)
	if err != nil || !out.OpeningTransportAttempted || out.OpeningTransportWritten || !s.retired[r.SourceGuid] || len(f.packets) != 1 {
		t.Fatal("unknown transport lost source custody", out, err, f.packets)
	}
	if again, err := s.OpenGather(context.Background(), r); err != nil || !proto.Equal(again, out) || len(f.packets) != 1 {
		t.Fatal("repeated unknown action", again, err)
	}
	enable(t, s)
	next := proto.Clone(r).(*lootv1.OpenGatherRequest)
	next.RequestId = "fresh-id-cannot-replay"
	if _, err := s.OpenGather(context.Background(), next); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("new ID/enable cleared unknown source", err, f.packets)
	}
}

func TestGatherApproachSameMapNewVisitNeverMovesOrOpens(t *testing.T) {
	s, f, _ := harvestRig(t)
	f.world.QuestObjects[0].X = 10
	enable(t, s)
	r := exactGatherRequest(s, f)
	r.Approach = true
	out, err := s.OpenGather(context.Background(), r)
	if err != nil || out.Phase != "gather_approaching" || out.OpeningTransportAttempted || len(f.packets) != 0 {
		t.Fatal("explicit finite approach not admitted truthfully", out, err, f.packets)
	}
	f.world.VisitRevision++ // Same map/source/pose, but a different world visit.
	s.Tick(context.Background())
	if f.approachCalls != 0 || len(f.packets) != 0 || s.state.PendingGameObjectGuid != 0 || s.state.LastGather.ObjectGuid != r.SourceGuid {
		t.Fatal("old request moved or opened in a replacement visit", &s.state, f.packets, f.approachCalls)
	}
}

func TestGatherLuaEditChangesExactNativeSourceAndReloadPreservesOpening(t *testing.T) {
	s, f, _ := harvestRig(t)
	far := proto.Clone(f.world.QuestObjects[0]).(*worldv1.GameObjectState)
	far.Guid++
	far.X = 4
	f.world.QuestObjects = append(f.world.QuestObjects, far)
	enable(t, s)
	o, err := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	vm := corpsePolicyVM(t, o.SessionId)
	first := corpsePolicyDecision(t, vm, o, true, false)
	if a, _ := first["action"].(map[string]any); a["sourceGuid"] != "17370386473244688426" || len(f.packets) != 0 {
		t.Fatal("default Lua did not choose without native effects", first, f.packets)
	}
	old := corpsePolicySource(t)
	edited := strings.Replace(old, "gatherPreferNearest=true", "gatherPreferNearest=false", 1)
	if edited == old {
		t.Fatal("editable gather preference missing")
	}
	if _, err := vm.Reload(context.Background(), edited, vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	second := corpsePolicyDecision(t, vm, o, true, false)
	action, _ := second["action"].(map[string]any)
	if action["sourceGuid"] != "17370386473244688427" {
		t.Fatal("same-session Lua edit did not change source", second)
	}
	delete(action, "capability")
	delete(action, "observationSequence")
	raw, _ := json.Marshal(action)
	r := &lootv1.OpenGatherRequest{}
	if err := protojson.Unmarshal(raw, r); err != nil {
		t.Fatal(err)
	}
	if out, err := s.OpenGather(context.Background(), r); err != nil || out.SourceGuid != far.Guid || !out.OpeningTransportWritten || len(f.packets) != 1 {
		t.Fatal("native did not execute exact edited-Lua selection", out, err, f.packets)
	}
	if _, err := vm.Reload(context.Background(), old, vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if next := corpsePolicyDecision(t, vm, fresh, true, false); next["action"] != nil || len(f.packets) != 1 {
		t.Fatal("reload lost pending/unknown source custody", next, f.packets)
	}
}

func TestGatherWrittenOpeningWithoutWindowSurvivesEnableToggle(t *testing.T) {
	s, f, _ := harvestRig(t)
	enable(t, s)
	r := exactGatherRequest(s, f)
	out, err := s.OpenGather(context.Background(), r)
	if err != nil || !out.OpeningTransportWritten || out.WindowObserved {
		t.Fatal(out, err)
	}
	if _, err := s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(r.SourceGuid), 1))
	s.Tick(context.Background())
	if !s.retired[r.SourceGuid] || s.state.LastGather.LootWindowObserved {
		t.Fatal("missing server window lost uncertainty", &s.state)
	}
	before := len(f.packets)
	enable(t, s)
	next := proto.Clone(r).(*lootv1.OpenGatherRequest)
	next.RequestId = "new-id-after-written-unknown"
	if _, err := s.OpenGather(context.Background(), next); status.Code(err) != codes.FailedPrecondition || len(f.packets) != before {
		t.Fatal("enable/new ID replayed an uncertain written opening", err, f.packets)
	}
}

func TestGatherTravelArrivalRemainsBoundThroughFinalWorldRead(t *testing.T) {
	for _, mode := range []string{"current", "beyond verified range", "stale arrival", "corrected after acquisition", "corrected before cast"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			enable(t, s)
			r := exactGatherRequest(s, f)
			r.ApproachWorldRevision = f.world.Self.PositionRevision
			f.actions.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: r.SourceGuid, VisitId: r.VisitId, Ready: true, Phase: "in_position", QuestArrivalWorldRevision: r.ApproachWorldRevision}
			switch mode {
			case "beyond verified range":
				f.world.QuestObjects[0].X = 3.5
			case "stale arrival":
				f.actions.TargetMovement.Ready = false
			case "corrected after acquisition":
				f.afterAcquire = func() { f.world.Self.PositionRevision++; f.actions.Position.Revision++ }
			case "corrected before cast":
				f.afterRenew = func() { f.world.Self.PositionRevision++ }
			}
			op, err := s.OpenGather(context.Background(), r)
			if mode == "current" {
				if err != nil || !op.GetOpeningTransportWritten() || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell {
					t.Fatal(op, err, f.packets)
				}
			} else if op.GetOpeningTransportAttempted() || len(f.packets) != 0 {
				t.Fatal("expired arrival reached the wire", op, err, f.packets)
			}
		})
	}
}
