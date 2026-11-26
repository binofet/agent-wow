package buddyactions

import (
	"context"
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Preserve the real public World request boundary; the remaining snapshot is
// the same deterministic observed actor fixture used by normal CastSpell tests.
type strictGenericWorld struct {
	*fakeSession
	validator *buddyworld.Service
	queries   []*worldv1.SnapshotRequest
}

func (f *strictGenericWorld) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		query := &worldv1.SnapshotRequest{}
		if err := r.Request.UnmarshalTo(query); err != nil {
			return nil, err
		}
		f.queries = append(f.queries, proto.Clone(query).(*worldv1.SnapshotRequest))
		if _, err := f.validator.Snapshot(ctx, query); err != nil {
			return nil, err
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}
func strictGenericRig(t *testing.T) (*Service, *fakeSession, *strictGenericWorld) {
	t.Helper()
	s, f, _ := genericCastRig(t)
	w := &strictGenericWorld{fakeSession: f, validator: buddyworld.NewService(s.selfGUID)}
	s.session = w
	return s, f, w
}
func TestGenericLearnedPlayerBuffUsesActualWorldRequestContract(t *testing.T) {
	for _, spell := range []uint32{1243, 20217} {
		t.Run(fmt.Sprint(spell), func(t *testing.T) {
			s, f, w := strictGenericRig(t)
			s.applySpellbookLocked(map[uint32]bool{spell: true})
			request := genericRequest(s, spell)
			request.Targets = &actionsv1.SpellCastTargets{Mask: 2, ObjectGuid: 3}
			result, err := s.CastSpell(context.Background(), request)
			if err != nil || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell || !result.CastPending || !result.LastSpellOperation.TransportWritten || result.LastSpellOperation.EffectObserved {
				t.Fatal("observed learned player cast refused or effect invented", result, err, f.packets)
			}
			if len(w.queries) != 1 || w.queries[0].CreatureGuid != 0 || !w.queries[0].IncludeGatheringObjects {
				t.Fatal("player was used as NPC lookup or object coverage lost", w.queries)
			}
		})
	}
}
func TestGenericTargetSnapshotPreservesNPCObjectCorpseAndOwnedItem(t *testing.T) {
	const npc = uint64(0xf130000063000001)
	const object = uint64(0xf110000063000001)
	const corpse = uint64(0xf101000000000001)
	const item = uint64(0x4000000000000042)
	for _, kind := range []string{"npc", "gameobject", "corpse", "owned_item"} {
		t.Run(kind, func(t *testing.T) {
			s, f, w := strictGenericRig(t)
			r := genericRequest(s, 133)
			want := uint64(0)
			switch kind {
			case "npc":
				want = npc
				r.Targets = &actionsv1.SpellCastTargets{Mask: 2, ObjectGuid: npc}
				f.lookupCreatures = map[uint64]*worldv1.CreatureState{npc: {Guid: npc, Present: true, MapKnown: true, MapId: f.world.Self.MapId}}
			case "gameobject":
				r.Targets = &actionsv1.SpellCastTargets{Mask: 0x800, ObjectGuid: object}
				f.world.NearbyGatheringObjects = []*worldv1.GameObjectState{{Guid: object, Present: true, MapKnown: true, MapId: f.world.Self.MapId}}
			case "corpse":
				r.Targets = &actionsv1.SpellCastTargets{Mask: 0x200, ObjectGuid: corpse}
				f.world.PlayerCorpses = []*worldv1.CorpseState{{Guid: corpse, Present: true, MapKnown: true, MapId: f.world.Self.MapId}}
			case "owned_item":
				r.Targets = &actionsv1.SpellCastTargets{Mask: 16, ItemGuid: item}
				f.world.BackpackKnown = true
				f.world.Backpack = []*worldv1.BackpackSlot{{ItemGuid: item, Entry: 42, StackCount: 1}}
			}
			if _, err := s.CastSpell(context.Background(), r); err != nil || len(f.packets) != 1 {
				t.Fatal(kind, err, f.packets)
			}
			if len(w.queries) != 1 || w.queries[0].CreatureGuid != want || !w.queries[0].IncludeGatheringObjects {
				t.Fatal("lost exact NPC/object coverage", w.queries, want)
			}
		})
	}
}
func TestGenericPlayerSnapshotStillFencesMissingVisitBodyAndUnknown(t *testing.T) {
	for _, kind := range []string{"absent", "visit", "body", "world_error", "unknown_write"} {
		t.Run(kind, func(t *testing.T) {
			s, f, _ := strictGenericRig(t)
			r := genericRequest(s, 133)
			r.Targets = &actionsv1.SpellCastTargets{Mask: 2, ObjectGuid: 3}
			switch kind {
			case "absent":
				f.world.Owner.Present = false
			case "visit":
				f.world.VisitRevision = 2
			case "body":
				f.world.Self.Guid = 4
			case "world_error":
				f.failSnapshot = true
			case "unknown_write":
				f.failSend = true
			}
			result, err := s.CastSpell(context.Background(), r)
			if kind != "unknown_write" {
				if status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 || s.state.CastPending {
					t.Fatal("invalid observation wrote cast", result, err, f.packets)
				}
				return
			}
			if err != nil || len(f.packets) != 1 || !result.CastPending || result.LastSpellOperation.Phase != "transport_unconfirmed_no_retry" {
				t.Fatal(result, err, f.packets)
			}
			if _, err = s.CastSpell(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
				t.Fatal("unknown write replayed", err, f.packets)
			}
		})
	}
}
