package buddyactions

import (
	"context"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Unlike the ordinary fake, enforce the real World.Snapshot input contract.
// This catches routing failures before the position resolver can see a player.
type snapshotRoutingSession struct {
	*fakeSession
	queries []uint64
}

func (f *snapshotRoutingSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		q := &worldv1.SnapshotRequest{}
		if err := r.Request.UnmarshalTo(q); err != nil {
			return nil, err
		}
		f.queries = append(f.queries, q.CreatureGuid)
		if q.CreatureGuid != 0 && (q.CreatureGuid>>48 != 0xf130 || q.CreatureGuid&0xffffff == 0) {
			return nil, status.Error(codes.InvalidArgument, "creature_guid must identify a creature or be zero")
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

func TestSnapshotTargetRoutingUsesOnlyValidNPCFilter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		guid  uint64
		exact uint64
	}{
		{"player", 87, 0},
		{"corpse", 0xf101000000000123, 0},
		{"npc", enemyGUID, enemyGUID},
		{"invalid_npc", 0xf130000001000000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, ctx, _ := targetMovementFixture(t)
			adapter := &snapshotRoutingSession{fakeSession: f}
			s.session = adapter
			if _, err := s.readWorldSnapshot(ctx, tc.guid); err != nil {
				t.Fatal("target could not reach the normal World observations", err)
			}
			if len(adapter.queries) != 1 || adapter.queries[0] != tc.exact {
				t.Fatalf("wrong exact-query routing: %v, want %d", adapter.queries, tc.exact)
			}
		})
	}
}

func TestApproachTargetRetainsObservedPlayersAndCorpses(t *testing.T) {
	for _, kind := range []string{"nearby_player", "dead_owner", "corpse", "npc"} {
		t.Run(kind, func(t *testing.T) {
			s, f, r, ctx, _ := targetMovementFixture(t)
			adapter := &snapshotRoutingSession{fakeSession: f}
			s.session = adapter
			wantQuery := uint64(0)
			switch kind {
			case "nearby_player":
				r.TargetGuid = 87
				f.world.NearbyPlayers = append(f.world.NearbyPlayers, &worldv1.PlayerState{Guid: 87, Present: true, MapKnown: true, MapId: f.world.Self.MapId, PositionKnown: true, X: 10})
			case "dead_owner":
				r.TargetGuid = f.world.Owner.Guid
				f.world.Owner.Alive, f.world.Owner.Health, f.world.Owner.X = false, 0, 10
			case "corpse":
				r.TargetGuid = 0xf101000000000123
				f.world.PlayerCorpses = []*worldv1.CorpseState{{Guid: r.TargetGuid, OwnerGuid: 3, Present: true, MapKnown: true, MapId: f.world.Self.MapId, PositionKnown: true, X: 10}}
			case "npc":
				wantQuery = enemyGUID
			}
			out, err := s.ApproachTarget(ctx, r)
			if err != nil || out.GetTargetMovement().GetTargetGuid() != r.TargetGuid || out.TargetMovement.Phase != "approaching" || !out.Moving {
				t.Fatal("exact observed target never reached movement", out, err)
			}
			if len(adapter.queries) == 0 || adapter.queries[0] != wantQuery {
				t.Fatalf("approach issued an invalid World filter: %v", adapter.queries)
			}
		})
	}
}
