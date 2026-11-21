package modrt

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

type followFallbackPackagedWorld struct{ policySpellWorld }

func (followFallbackPackagedWorld) Snapshot(context.Context, *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	player := func(guid uint64, x float32) *worldv1.PlayerState {
		return &worldv1.PlayerState{Guid: guid, Present: true, ClassKnown: true, ClassId: 8,
			MapKnown: true, PositionKnown: true, X: x, PositionRevision: 1, MovementFlagsKnown: true,
			AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100,
			PowerKnown: true, Power: 128, MaxPower: 128, UnitFlagsKnown: true, ChannelSpellKnown: true,
			ShapeshiftFormKnown: true, AuraStateKnown: true, SpeedsKnown: true, WalkSpeed: 2.5, RunSpeed: 7}
	}
	return &worldv1.SnapshotResponse{Self: player(policyMageGUID, 0), Owner: player(3, 2.5), VisitRevision: 1}, nil
}

func (followFallbackPackagedWorld) ObserveClientMovement(context.Context, *worldv1.ClientMovementRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func TestPackagedActionsFollowFallbackUsesReloadedFullSourceWithoutHostTicks(t *testing.T) {
	runner := &policySpellRunner{world: followFallbackPackagedWorld{}}
	manager, ctx, writes := startPackagedPolicySpellWith(t, runner, policySheepID)
	pid := runner.action.Process.Pid
	// Keep the actual fixture transport drained throughout native background
	// ticks, including on a failing test, so its 16-entry queue cannot overflow.
	var captureMu sync.Mutex
	var captured []policySpellWrite
	stop, drained := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case write := <-writes:
				captureMu.Lock()
				captured = append(captured, write)
				captureMu.Unlock()
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-drained })

	source := followFallbackBundle(t)
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	sid, revision := host.vm.Status().SessionID, host.vm.Status().Revision
	identity := map[string]any{"sessionId": sid, "characterGuid": "11"}
	readSource := func(wantSource, wantRevision string) {
		t.Helper()
		request, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		body, err := manager.InvokeJSON(ctx, "session.policy_source", request)
		var view policySourceResponse
		if err != nil || json.Unmarshal(body, &view) != nil || view.SessionID != sid || view.CharacterGUID != "11" || view.Source != wantSource || view.Revision != wantRevision ||
			!view.FollowFallback.Available || view.FollowFallback.Revision != wantRevision || view.FollowFallback.LastError != "" {
			t.Fatalf("packaged follow source identity/metadata changed: %v, %s", err, body)
		}
	}
	readSource(source, revision)
	if _, err := manager.InvokeJSON(ctx, "buddy-actions.follow", json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	waitReason := func(want string) *actionsv1.State {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		var latest actionsv1.State
		for {
			body, err := manager.InvokeJSON(ctx, "buddy-actions.status", nil)
			if err != nil || protojson.Unmarshal(body, &latest) != nil {
				t.Fatalf("packaged native follow status unavailable: %v, %s", err, body)
			}
			if latest.GetSessionId() != sid || latest.GetSelfGuid() != policyMageGUID || !latest.GetFollowing() || latest.GetMoving() || latest.GetMotionUnconfirmed() {
				t.Fatalf("native body or standing follow changed during source edit: %v", &latest)
			}
			if latest.GetReason() == want {
				return &latest
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Fatalf("native follow never reached %q: %v", want, &latest)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	waitReason("within_follow_distance")
	reload := func(candidate, expected string) (string, error) {
		t.Helper()
		request, err := json.Marshal(map[string]any{"sessionId": sid, "characterGuid": "11", "expectedRevision": expected, "source": candidate})
		if err != nil {
			t.Fatal(err)
		}
		body, err := manager.InvokeJSON(ctx, "session.reload_policy", request)
		if err != nil {
			return "", err
		}
		var response struct {
			SessionID, CharacterGUID, Revision string
		}
		if err := json.Unmarshal(body, &response); err != nil || response.SessionID != sid || response.CharacterGUID != "11" || response.Revision == "" {
			t.Fatalf("CAS returned a different packaged body: %v, %s", err, body)
		}
		return response.Revision, nil
	}
	edited := strings.Replace(source, "local near_stop, near_limit = 3,30", "local near_stop, near_limit = 1,30", 1)
	if edited == source {
		t.Fatal("full editable bundle did not contain the shared near-owner policy")
	}
	changed, err := reload(edited, revision)
	if err != nil || changed == revision {
		t.Fatal("same-session source edit failed", changed, err)
	}
	// This production binary has no navigation assets. The one-yard policy
	// must ask for a route and report the genuine physical-planner refusal.
	blocked := waitReason("follow_navigation_unavailable")
	if blocked.GetNavAvailable() || blocked.GetNavReason() == "" {
		t.Fatal("missing geometry was reported as an executable route", blocked)
	}
	readSource(edited, changed)
	if _, err := reload(source, revision); err == nil {
		t.Fatal("stale CAS replaced the current full source")
	}
	readSource(edited, changed)
	if _, err := reload("return {broken Lua", changed); err == nil {
		t.Fatal("invalid source replaced the current full source")
	}
	readSource(edited, changed)
	waitReason("follow_navigation_unavailable")
	if restored, err := reload(source, changed); err != nil || restored != revision {
		t.Fatal("same-session default restoration failed", restored, err)
	}
	waitReason("within_follow_distance")
	readSource(source, revision)
	if runner.action.Process.Pid != pid {
		t.Fatal("source edit restarted the packaged Actions process")
	}
	checkpoint, err := host.vm.State(ctx)
	if err != nil || checkpoint.Status.Cursor != 0 || checkpoint.Status.Buffered != 0 || checkpoint.Status.Pending != 0 || checkpoint.LatestDecision != nil || host.encounterLeaseHeld {
		t.Fatal("native fallback advanced the persistent policy or acquired its action writer", checkpoint, err)
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	for _, write := range captured {
		switch write.opcode {
		case opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop, opcode.CMSGCastSpell, opcode.CMSGAttackswing:
			t.Fatal("hold or blocked physical route emitted a gameplay write", write)
		}
	}
}
