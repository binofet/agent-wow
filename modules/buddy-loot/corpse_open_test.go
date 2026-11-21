package buddyloot

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func exactCorpseRig(t *testing.T) (*Service, *fakeSession, *lootv1.OpenCorpseRequest) {
	t.Helper()
	s, f, _ := rig(t)
	s.state.SessionId, f.actions.SessionId = "loot-session", "loot-session"
	f.actions.Position.Revision = 1
	f.world.LootableCorpses[0].PositionRevision = 1
	return s, f, &lootv1.OpenCorpseRequest{SessionId: "loot-session", SelfGuid: 1, CorpseGuid: corpseGUID, RequestId: "chosen-corpse",
		ExpectedPositionRevision: 1, ExpectedCorpseRevision: 1, ExpectedInventoryRevision: f.world.InventoryRevision}
}

func TestExactCorpseOpenPreservesSourcePacketsReceiptsAndIdempotency(t *testing.T) {
	s, f, r := exactCorpseRig(t)
	// The shared policy cannot be turned off by the legacy gather-control bit.
	s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: false})
	op, err := s.OpenCorpse(context.Background(), r)
	if err != nil || !op.OpeningTransportAttempted || !op.OpeningTransportWritten || op.WindowObserved || op.Terminal ||
		len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGLoot || binary.LittleEndian.Uint64(f.packets[0].Payload) != r.CorpseGuid {
		t.Fatal("exact source did not produce one honest OPEN", op, err, f.packets)
	}
	if _, err = s.OpenCorpse(context.Background(), proto.Clone(r).(*lootv1.OpenCorpseRequest)); err != nil || len(f.packets) != 1 {
		t.Fatal("same exact request replayed OPEN", err, f.packets)
	}
	changed := proto.Clone(r).(*lootv1.OpenCorpseRequest)
	changed.CorpseGuid++
	if _, err = s.OpenCorpse(context.Background(), changed); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("idempotency ID changed its source", err, f.packets)
	}
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0))
	op, err = s.CloseCorpse(context.Background(), &lootv1.CloseCorpseRequest{SessionId: r.SessionId, SelfGuid: r.SelfGuid, CorpseGuid: r.CorpseGuid})
	if err != nil || f.leaseOwner != "" || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootRelease || op.ReleaseObserved || op.Terminal {
		t.Fatal("close did not yield body while preserving unconfirmed UI receipt", op, err, f.packets)
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(corpseGUID), 1))
	s.Tick(context.Background())
	op, err = s.OpenCorpse(context.Background(), r)
	if err != nil || !op.WindowObserved || !op.ReleaseObserved || !op.Terminal || op.Phase != "window_and_release_observed" || len(f.packets) != 2 {
		t.Fatal("actual matching release did not settle original operation", op, err, f.packets)
	}
}

func TestExactCorpseAdmissionRejectsStaleOrPhysicallyUnavailableFacts(t *testing.T) {
	for _, name := range []string{"foreign_session", "foreign_body", "position_revision", "corpse_revision", "inventory_revision", "moving", "living_source", "permission_lost", "range", "source_changed_after_acquire"} {
		t.Run(name, func(t *testing.T) {
			s, f, r := exactCorpseRig(t)
			switch name {
			case "foreign_session":
				r.SessionId = "old-session"
			case "foreign_body":
				r.SelfGuid++
			case "position_revision":
				r.ExpectedPositionRevision++
			case "corpse_revision":
				r.ExpectedCorpseRevision++
			case "inventory_revision":
				r.ExpectedInventoryRevision++
			case "moving":
				f.actions.Moving = true
			case "living_source":
				f.world.LootableCorpses[0].Alive = true
			case "permission_lost":
				f.world.LootableCorpses[0].DynamicFlags = 0
			case "range":
				f.world.LootableCorpses[0].X = 12
			case "source_changed_after_acquire":
				f.afterAcquire = func() { f.world.LootableCorpses[0].PositionRevision++ }
			}
			op, err := s.OpenCorpse(context.Background(), r)
			if len(f.packets) != 0 || err == nil && op.GetOpeningTransportAttempted() {
				t.Fatal("stale/unavailable exact source emitted OPEN", op, err, f.packets)
			}
		})
	}
}

func TestExactCorpseUnknownOpenCannotBeReplayedByNewRequestOrDisable(t *testing.T) {
	s, f, r := exactCorpseRig(t)
	f.failOpcode = opcode.CMSGLoot
	op, err := s.OpenCorpse(context.Background(), r)
	if err != nil || !op.OpeningTransportAttempted || op.OpeningTransportWritten || !op.Terminal {
		t.Fatal("unknown transport was confused with non-dispatch/success", op, err)
	}
	s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: true})
	r.RequestId = "different-intent-same-uncertain-source"
	if _, err = s.OpenCorpse(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
		t.Fatal("a new intent/enable replayed uncertain OPEN", err, f.packets)
	}
}

type refusedCorpseCallback struct {
	*fakeSession
	err error
}

func (f refusedCorpseCallback) SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return nil, f.err
}
func TestExactCorpseCallbackRefusalDoesNotInventTransportOrRetireSource(t *testing.T) {
	for _, known := range []bool{true, false} {
		s, f, r := exactCorpseRig(t)
		err := status.Error(codes.PermissionDenied, "transport failed with a similar status")
		if known {
			err = modcb.PacketNotDispatchedError("core rejected packet before transport")
		}
		s.session = refusedCorpseCallback{fakeSession: f, err: err}
		op, e := s.OpenCorpse(context.Background(), r)
		if e != nil || op.OpeningTransportAttempted == known || s.retired[corpseGUID] == known || s.seen[corpseGUID] == known || !op.Terminal {
			t.Fatal("callback refusal and unknown send were conflated", known, op, e, s.retired)
		}
		if known {
			s.session = f
			r.RequestId = "fresh-intent-after-known-non-dispatch"
			op, e = s.OpenCorpse(context.Background(), r)
			if e != nil || !op.OpeningTransportWritten || len(f.packets) != 1 {
				t.Fatal("known no-write poisoned the valid source", op, e, f.packets)
			}
		}
	}
}

func TestProductionLootTicksCannotSelectOrdinaryCorpses(t *testing.T) {
	s, f, _ := exactCorpseRig(t)
	s.automatic = true
	for _, enabled := range []bool{true, false, true} {
		s.Enable(context.Background(), &lootv1.EnableRequest{Enabled: enabled})
		s.Tick(context.Background())
	}
	if len(f.packets) != 0 || f.leaseOwner != "" || f.approachCalls != 0 {
		t.Fatal("native timer still chose/opened/approached ordinary corpse", f.packets, f.calls)
	}
}

func TestExactCorpseOpenDoesNotChooseCombatOrBagSpacePolicy(t *testing.T) {
	s, f, r := exactCorpseRig(t)
	f.world.Self.UnitFlags |= 0x80000
	for i, item := range f.world.InventoryBags[0].Slots {
		item.ItemGuid = 0x4000000000000100 + uint64(i)
		item.Entry = 25
		item.StackCount = 1
		item.MetadataKnown = true
	}
	op, err := s.OpenCorpse(context.Background(), r)
	if err != nil || !op.OpeningTransportWritten || len(f.packets) != 1 {
		t.Fatal("packet-valid exact OPEN still imposed native combat/free-slot preference", op, err, f.packets)
	}
}

func corpsePolicySource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../behaviors/corpse-loot.lua")
	if err != nil {
		t.Fatal(err)
	}
	source := "local loot=(function()\n" + string(raw) + "\nend)()\nreturn {on_event=loot.on_event,export_state=loot.export_state,import_state=loot.import_state,decide=function(f)if f.input.testNow then f.clock={monotonicMs=f.input.testNow} end;return loot.decide(f,f.input.encounter,f.input.quiet,f.input.urgent)end}"
	return source
}
func corpsePolicyVM(t *testing.T, sid string) *behavior.Session {
	t.Helper()
	source := corpsePolicySource(t)
	vm, err := behavior.NewSessionWithOptions(context.Background(), sid, source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	return vm
}

func corpsePolicyDecision(t *testing.T, vm *behavior.Session, o *lootv1.CorpseObservation, quiet, urgent bool, now ...int64) map[string]any {
	t.Helper()
	raw, err := protojson.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	seq, err := vm.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: value})
	if err != nil {
		t.Fatal(err)
	}
	n := strconv.FormatUint(seq, 10)
	input := map[string]any{"quiet": quiet, "urgent": urgent, "observationSequence": n, "observationSequences": map[string]any{"corpse_loot": n}, "encounter": map[string]any{"selfGuid": strconv.FormatUint(o.SelfGuid, 10), "visitId": "1"}}
	if len(now) > 0 {
		input["testNow"] = now[0]
	}
	d, err := vm.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return d.Output
}

func TestSharedLuaChoosesNearestCorpseAndApproachesBeforeCustody(t *testing.T) {
	s, f, _ := exactCorpseRig(t)
	f.world.LootableCorpses[0].X = 10
	far := proto.Clone(f.world.LootableCorpses[0]).(*worldv1.CreatureState)
	far.Guid++
	far.X = 11
	f.world.LootableCorpses = append([]*worldv1.CreatureState{far}, f.world.LootableCorpses...)
	f.world.Owner.Alive = false
	f.world.Owner.PlayerFlags = 2
	o, err := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	vm := corpsePolicyVM(t, o.SessionId)
	out := corpsePolicyDecision(t, vm, o, true, false)
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "approach_target" || a["targetGuid"] != strconv.FormatUint(corpseGUID, 10) || f.leaseOwner != "" || len(f.packets) != 0 {
		t.Fatal("Lua did not select/approach nearest eligible source independently", out, f.calls)
	}
	f.actions.Position.X = 8
	f.actions.Position.Revision++
	o, _ = s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	out = corpsePolicyDecision(t, vm, o, true, false)
	a, _ = out["action"].(map[string]any)
	if a["capability"] != "loot_open" || a["corpseGuid"] != strconv.FormatUint(corpseGUID, 10) || a["expectedPositionRevision"] != "2" {
		t.Fatal("actual arrival did not produce an exact bound OPEN", out)
	}
	if out = corpsePolicyDecision(t, vm, o, false, false); out["action"] != nil {
		t.Fatal("loot displaced an urgent gameplay lane", out)
	}
}

// Existing wire/settlement tests call this helper instead of granting native
// target policy. Their actual OPEN source now comes from the shared Lua module.
var corpseTestVMs sync.Map

func corpsePolicyPaused(t *testing.T, s *Service) bool {
	t.Helper()
	value, ok := corpseTestVMs.Load(s)
	if !ok {
		return false
	}
	snapshot, e := value.(*behavior.Session).State(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	data, _ := snapshot.Checkpoint["data"].(map[string]any)
	paused, _ := data["paused"].(map[string]any)
	return len(paused) > 0
}

func tickCorpseLua(t *testing.T, s *Service) {
	t.Helper()
	if (s.token == "" || s.exactCorpse) && s.cleanup.source == 0 && !s.questGather {
		o, err := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
		if err == nil {
			var vm *behavior.Session
			if previous, ok := corpseTestVMs.Load(s); ok {
				vm = previous.(*behavior.Session)
			} else {
				vm = corpsePolicyVM(t, o.SessionId)
				corpseTestVMs.Store(s, vm)
				t.Cleanup(func() { corpseTestVMs.Delete(s) })
			}
			out := corpsePolicyDecision(t, vm, o, true, false, s.now().UnixMilli())
			if a, ok := out["action"].(map[string]any); ok && a["capability"] == "approach_target" {
				delete(a, "capability")
				delete(a, "observationSequence")
				raw, _ := json.Marshal(a)
				r := &actionsv1.ApproachTargetRequest{}
				if err = protojson.Unmarshal(raw, r); err != nil {
					t.Fatal(err)
				}
				_ = s.invoke(context.Background(), "buddy-actions", "approach_target", r, &actionsv1.State{})
			} else if ok && a["capability"] == "loot_close" {
				retire, _ := a["retireUnconfirmed"].(bool)
				_, _ = s.CloseCorpse(context.Background(), &lootv1.CloseCorpseRequest{SessionId: o.SessionId, SelfGuid: o.SelfGuid, CorpseGuid: s.state.PendingCorpseGuid, RetireUnconfirmed: retire})
			} else if ok && a["capability"] == "loot_open" {
				delete(a, "capability")
				delete(a, "observationSequence")
				raw, _ := json.Marshal(a)
				r := &lootv1.OpenCorpseRequest{}
				if err = protojson.Unmarshal(raw, r); err != nil {
					t.Fatal(err)
				}
				_, _ = s.OpenCorpse(context.Background(), r)
			} else if ok && a["capability"] == "loot_resume_source" {
				delete(a, "capability")
				delete(a, "observationSequence")
				raw, _ := json.Marshal(a)
				r := &lootv1.ResumeSourceRequest{}
				if err = protojson.Unmarshal(raw, r); err != nil {
					t.Fatal(err)
				}
				_, _ = s.ResumeSource(context.Background(), r)
			} else if ok && (a["capability"] == "loot_money" || a["capability"] == "loot_slot") {
				money := a["capability"] == "loot_money"
				delete(a, "capability")
				delete(a, "observationSequence")
				raw, _ := json.Marshal(a)
				r := &lootv1.TakeLootRequest{}
				if err = protojson.Unmarshal(raw, r); err != nil {
					t.Fatal(err)
				}
				if money {
					_, _ = s.TakeMoney(context.Background(), r)
				} else {
					_, _ = s.TakeSlot(context.Background(), r)
				}
			}
		}
	}
	s.Tick(context.Background())
}
