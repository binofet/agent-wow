package buddyloot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func capacityPausedGather(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := harvestRig(t)
	openHarvest(t, s)
	failure := make([]byte, 18)
	failure[0] = 50
	emit(s, opcode.SMSGInventoryChangeFailure, failure)
	tickGatherWindow(t, s)
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
	tickGatherWindow(t, s)
	if got := current(t, s); !got.InventoryPaused || got.PendingGameObjectGuid != 0 || s.token != "" || got.LastGather.Status != lootv1.GatherStatus_GATHER_SERVER_REJECTED {
		t.Fatal("fixture did not reach settled server rejection", got)
	}
	return s, f, advance
}

func resumeCapacityRequest(t *testing.T, s *Service) *lootv1.ResumeSourceRequest {
	t.Helper()
	o, err := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	vm := corpsePolicyVM(t, o.SessionId)
	out := corpsePolicyDecision(t, vm, o, true, false)
	a, ok := out["action"].(map[string]any)
	if !ok || a["capability"] != "loot_resume_source" {
		t.Fatal("Lua did not choose capacity resume", out)
	}
	delete(a, "capability")
	delete(a, "observationSequence")
	raw, _ := json.Marshal(a)
	r := &lootv1.ResumeSourceRequest{}
	if err = protojson.Unmarshal(raw, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestContainerCapacityResumeHasOneLuaDecisionAndNoNativeRetry(t *testing.T) {
	s, f, advance := capacityPausedGather(t)
	baseline := current(t, s).CapacityPause
	if baseline == nil || baseline.SourceGuid != harvestGUID || baseline.GatherId != 1 || baseline.InventoryRevision != f.world.InventoryRevision {
		t.Fatal(baseline)
	}
	before := len(f.packets)
	f.world.InventoryRevision++
	for range 3 {
		advance(time.Second)
		s.Tick(context.Background())
	}
	if !current(t, s).InventoryPaused || len(f.packets) != before || !s.gatherSeen[harvestGUID] {
		t.Fatal("native selected a capacity retry", current(t, s), f.packets)
	}
	r := resumeCapacityRequest(t, s)
	receipt, err := s.ResumeSource(context.Background(), r)
	if err != nil || !receipt.Cleared || current(t, s).InventoryPaused || len(f.packets) != before || current(t, s).LastGather.ItemPushObserved {
		t.Fatal("metadata resume invented a game effect", receipt, err, current(t, s), f.packets)
	}
	if _, err = s.ResumeSource(context.Background(), proto.Clone(r).(*lootv1.ResumeSourceRequest)); err != nil || len(f.packets) != before {
		t.Fatal("same metadata request changed effects", err, f.packets)
	}
	advance(time.Second)
	s.Tick(context.Background())
	if len(f.packets) != before {
		t.Fatal("metadata resume restarted native source selection", current(t, s), f.packets)
	}
	tickGatherWindow(t, s)
	if len(f.packets) != before+1 || f.packets[before].Opcode != opcode.CMSGCastSpell || current(t, s).LastGather.Id != 2 {
		t.Fatal("Lua-selected opener could not continue after exact Lua resume", current(t, s), f.packets)
	}
}

func TestContainerCapacityLuaWaitsForFreshFactsAndYieldsToSupport(t *testing.T) {
	s, f, _ := capacityPausedGather(t)
	o, _ := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	vm := corpsePolicyVM(t, o.SessionId)
	if out := corpsePolicyDecision(t, vm, o, true, false); out["action"] != nil {
		t.Fatal("same rejected inventory revision retried", out)
	}
	f.world.InventoryRevision++
	o, _ = s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if out := corpsePolicyDecision(t, vm, o, true, true); out["action"] != nil {
		t.Fatal("resume displaced defense/support", out)
	}
	o.InventoryKnown = false
	if out := corpsePolicyDecision(t, vm, o, true, false); out["action"] != nil {
		t.Fatal("unknown bags retried", out)
	}
	o.InventoryKnown = true
	o.GeneralFreeSlots = 0
	if out := corpsePolicyDecision(t, vm, o, true, false); out["action"] != nil {
		t.Fatal("full bags retried", out)
	}
	o.GeneralFreeSlots = 1
	if _, err := vm.Reload(context.Background(), strings.Replace(corpsePolicySource(t), "resumeMinFreeSlots=1", "resumeMinFreeSlots=2", 1), vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if out := corpsePolicyDecision(t, vm, o, true, false); out["action"] != nil {
		t.Fatal("same-session edited capacity choice ignored", out)
	}
	o.GeneralFreeSlots = 2
	if out := corpsePolicyDecision(t, vm, o, true, false); out["action"] == nil {
		t.Fatal("edited Lua could not resume without reconnect", out)
	}
}

func TestExactContainerCapacityResumeRetainsSourceCustodyAndRevisionFences(t *testing.T) {
	for _, mode := range []string{"session", "body", "source", "gather", "token", "inventory", "unknown_inventory", "cleanup", "pending", "uncertain_source", "release_unobserved", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := capacityPausedGather(t)
			f.world.InventoryRevision++
			r := resumeCapacityRequest(t, s)
			ctx := context.Background()
			switch mode {
			case "session":
				r.SessionId = "old-session"
			case "body":
				r.SelfGuid++
			case "source":
				r.SourceGuid++
			case "gather":
				r.GatherId++
			case "token":
				r.PauseToken += "foreign"
			case "inventory":
				r.ExpectedInventoryRevision++
			case "unknown_inventory":
				f.world.InventoryKnown = false
			case "cleanup":
				s.cleanup.source = harvestGUID
			case "pending":
				s.state.PendingGameObjectGuid = harvestGUID
			case "uncertain_source":
				s.retired = map[uint64]bool{harvestGUID: true}
			case "release_unobserved":
				s.state.LastGather.ReleaseObserved = false
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := len(f.packets)
			_, err := s.ResumeSource(ctx, r)
			want := codes.FailedPrecondition
			if mode == "cancelled" {
				want = codes.Canceled
			}
			if status.Code(err) != want || !current(t, s).InventoryPaused || len(f.packets) != before || !s.gatherSeen[harvestGUID] {
				t.Fatal("invalid reset cleared source custody", mode, err, current(t, s), f.packets)
			}
		})
	}
}

func TestContainerCapacityOldModuleGenerationCannotClearRecreatedPause(t *testing.T) {
	old, oldWorld, _ := capacityPausedGather(t)
	oldWorld.world.InventoryRevision++
	request := resumeCapacityRequest(t, old)
	recreated, freshWorld, _ := capacityPausedGather(t)
	freshWorld.world.InventoryRevision++
	if request.SessionId != recreated.state.SessionId || request.GatherId != recreated.state.LastGather.Id || request.ExpectedInventoryRevision != freshWorld.world.InventoryRevision {
		t.Fatal("fixture did not reproduce same-SID counter reuse")
	}
	_, err := recreated.ResumeSource(context.Background(), request)
	if status.Code(err) != codes.FailedPrecondition || !current(t, recreated).InventoryPaused {
		t.Fatal("old module generation cleared a new source pause", err, current(t, recreated))
	}
}
