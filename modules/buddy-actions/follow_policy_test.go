package buddyactions

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// All game observations and packets continue through the existing fake. Only
// the private pure-policy read is intercepted, using the real Lua evaluator.
type followPolicySession struct {
	*fakeSession
	t             *testing.T
	sid           string
	self          uint64
	available     bool
	runtime       *behavior.Runtime
	inputs        []map[string]any
	bad           string
	fixedDecision map[string]any
	fixedRevision string
}

func newFollowPolicySession(t *testing.T, s *Service, f *fakeSession) *followPolicySession {
	t.Helper()
	host := &followPolicySession{fakeSession: f, t: t, sid: s.sessionID, self: s.selfGUID, available: true, runtime: behavior.New()}
	host.reload(t, behaviors.FollowFormationSource)
	s.session = host
	return host
}

func (f *followPolicySession) reload(t *testing.T, source string) string {
	t.Helper()
	revision, err := f.runtime.Reload(source, f.runtime.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func (f *followPolicySession) InvokeModule(ctx context.Context, req *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if req.Module != "session" || req.Method != "follow_decision" {
		return f.fakeSession.InvokeModule(ctx, req, opts...)
	}
	value := &structpb.Struct{}
	if req.GetRequest() == nil || req.Request.UnmarshalTo(value) != nil {
		f.t.Fatal("private follow read did not use a typed Struct", req)
	}
	fields := value.AsMap()
	input, ok := fields["input"].(map[string]any)
	if !ok || len(fields) != 3 || fields["sessionId"] != f.sid || fields["selfGuid"] != strconv.FormatUint(f.self, 10) || input["selfGuid"] != fields["selfGuid"] {
		f.t.Fatal("private follow read lost its exact body binding", fields)
	}
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) > 101*time.Millisecond {
		f.t.Fatal("private follow read lacks a bounded deadline", deadline)
	}
	f.inputs = append(f.inputs, input)
	switch f.bad {
	case "transport":
		return nil, errors.New("pure host unavailable")
	case "nil response":
		return nil, nil
	case "missing Any":
		return &modv1.InvokeModuleResponse{}, nil
	case "wrong protobuf":
		wrong, _ := anypb.New(&emptypb.Empty{})
		return &modv1.InvokeModuleResponse{Result: wrong}, nil
	}
	decision := map[string]any{}
	revision := ""
	if f.available {
		if f.fixedDecision != nil {
			decision, revision = f.fixedDecision, f.fixedRevision
		} else {
			var err error
			decision, err = f.runtime.Evaluate(input)
			if err != nil {
				f.t.Logf("pure follow evaluation failed: %v; runtime status: %+v", err, f.runtime.Status())
				return nil, err
			}
			revision = f.runtime.Status().Revision
		}
	}
	response := map[string]any{"available": f.available, "revision": revision, "decision": decision}
	switch f.bad {
	case "missing availability":
		delete(response, "available")
	case "availability type":
		response["available"] = "true"
	case "missing revision":
		delete(response, "revision")
	case "invalid revision":
		response["revision"] = "current"
	case "missing decision":
		delete(response, "decision")
	case "decision type":
		response["decision"] = "use static fallback"
	case "extra field":
		response["fallback"] = true
	}
	result, err := structpb.NewStruct(response)
	if err != nil {
		f.t.Fatal(err)
	}
	packed, err := anypb.New(result)
	if err != nil {
		f.t.Fatal(err)
	}
	return &modv1.InvokeModuleResponse{Result: packed}, nil
}

func TestFollowPolicyStandaloneAndExplicitUnavailableKeepDefaults(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "host has no export"}[optIn], func(t *testing.T) {
			s, f, advance := rig(t)
			s.sessionID = f.party.SessionId
			host := newFollowPolicySession(t, s, f)
			host.available = false
			if optIn {
				s.UseHostFollowPolicy()
			}
			toggle(t, s, true)
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 1 || math.Abs(float64(s.pos.x)-1.4) > .001 || !s.state.Following {
				t.Fatal("legacy no-host follow behavior changed", s.pos, s.stateLocked(), f.packets)
			}
			if optIn && len(host.inputs) != 2 || !optIn && len(host.inputs) != 0 || s.formation.sourceRevision != host.runtime.Status().Revision {
				t.Fatal("default availability did not control the private read", optIn, host.inputs, s.formation.sourceRevision)
			}
		})
	}
}

func TestFollowPolicyForwardsOwnerPlayerFlagsToLua(t *testing.T) {
	s, f, _, _, _ := followGoalFixture(t)
	s.navDataDir = ""
	f.world.Owner.PlayerFlags = 2
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("could not observe planning pose")
	}
	source := strings.Replace(behaviors.FollowFormationSource, "return M", `
local original=M.decide
M.decide=function(input)
    local out=original(input)
    if input.owner.playerFlags==2 then out.startDistance,out.stopDistance=1,1 end
    return out
end
return M`, 1)
	host.reload(t, source)
	out := s.followFormationPlanLocked(s.now(), f.world, 5, playerPosition(f.world.Owner), false)
	owner := host.inputs[0]["owner"].(map[string]any)
	if !out.Valid || out.StopDistance != 1 || owner["playerFlags"] != float64(2) || len(f.packets) != 0 {
		t.Fatal("owner flags did not reach the editable Lua choice as raw facts", owner, out, f.packets)
	}
}

func TestFollowPolicyEditedSourceReachesFallbackAfterFiniteGoalExpiry(t *testing.T) {
	for _, mode := range []string{"formation", "near_owner"} {
		t.Run(mode, func(t *testing.T) {
			s, f, req, ctx, advance := followGoalFixture(t)
			if mode == "near_owner" {
				s.navDataDir = ""
			}
			if !s.refreshPositionLocked(f.world) {
				t.Fatal("fixture did not observe its source pose")
			}
			host := newFollowPolicySession(t, s, f)
			s.UseHostFollowPolicy()
			if _, err := s.SetFollowFormation(ctx, req); err != nil {
				t.Fatal(err)
			}
			owner := playerPosition(f.world.Owner)
			finite := s.followFormationPlanLocked(s.now(), f.world, 5, owner, false)
			if finite.Position != (position{req.X, req.Y, req.Z, req.Orientation}) || len(host.inputs) != 0 {
				t.Fatal("fallback displaced an unexpired exact host goal", finite, host.inputs)
			}
			advance(encounterLeaseDuration)
			before := s.followFormationPlanLocked(s.now(), f.world, 5, owner, false)
			firstRevision := host.runtime.Status().Revision
			if !before.Valid || s.formation.sourceRevision != firstRevision || len(host.inputs) != 1 {
				t.Fatal("expired host goal did not consult the current source", before, host.inputs, s.formation.sourceRevision)
			}
			old, replacement := "[5]={3.8,-3.1}", "[5]={6.2,-3.1}"
			if mode == "near_owner" {
				old, replacement = "local near_stop, near_limit = 3,30", "local near_stop, near_limit = 1,30"
			}
			source := strings.Replace(behaviors.FollowFormationSource, old, replacement, 1)
			if source == behaviors.FollowFormationSource {
				t.Fatal("test did not edit the actual shared Lua")
			}
			revision := host.reload(t, source)
			advance(300 * time.Millisecond)
			after := s.followFormationPlanLocked(s.now(), f.world, 5, owner, false)
			if !after.Valid || revision == firstRevision || s.formation.sourceRevision != revision || len(f.packets) != 0 || s.sessionID != req.SessionId || s.followRevision != req.FollowRevision {
				t.Fatal("edited fallback was not current in the same body", after, s.formation.sourceRevision, revision, f.packets)
			}
			if mode == "formation" && math.Abs(float64(before.Position.x-after.Position.x)-2.4) > .002 || mode == "near_owner" && (before.StopDistance != 3 || after.StopDistance != 1) {
				t.Fatal("source edit silently reverted to the compiled default", before, after)
			}
			s.lastStep = s.now()
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStartForward) != 1 || !s.state.Moving || s.formation.sourceRevision != revision {
				t.Fatal("edited current goal did not reach the native verified executor", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestFollowPolicyEditedPortalOrderReachesLeaseFallback(t *testing.T) {
	s, f, req, ctx, advance := portalGoalFixture(t)
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	candidateReads := func() int {
		count := 0
		for _, input := range host.inputs {
			if input["mode"] == "portal_catch_up" {
				count++
			}
		}
		return count
	}
	if _, err := s.SetPortalCatchup(ctx, req); err != nil {
		t.Fatal(err)
	}
	if active := s.portalCandidatesLocked(f.world, s.now()); len(active) != 2 || candidateReads() != 0 {
		t.Fatal("fallback displaced the active host's finite candidates", active, host.inputs)
	}
	advance(encounterLeaseDuration)
	before := s.portalCandidatesLocked(f.world, s.now())
	if len(before) != 7 || candidateReads() != 1 {
		t.Fatal("expired portal goal did not use the current shared source", before, host.inputs)
	}
	source := strings.Replace(behaviors.FollowFormationSource, "portal_side = {8,12,16}, {-6,6}", "portal_side = {8,12,16}, {6,-6}", 1)
	if source == behaviors.FollowFormationSource {
		t.Fatal("test did not edit the actual shared portal order")
	}
	revision := host.reload(t, source)
	after := s.portalCandidatesLocked(f.world, s.now())
	if len(after) != 7 || before[0] != after[0] || before[1].x == after[1].x || s.formation.sourceRevision != revision {
		t.Fatal("portal fallback ignored current source ordering", before, after, s.formation.sourceRevision)
	}
	start, ground := s.pos, s.followPortal.ground
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if distance(from, start) < .01 && distance(to, ground) < .01 {
			return nil, errors.New("direct corridor unavailable")
		}
		return []position{from, to}, nil
	}
	s.Tick(context.Background())
	if s.followPortal.catchUpTarget != after[1] || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 0 {
		t.Fatal("edited portal bend did not reach native geometry qualification", s.followPortal, f.packets)
	}
}

func TestFollowPolicyHostErrorsAndMalformedRepliesPreserveMemoryWithoutBuiltinFallback(t *testing.T) {
	for _, bad := range []string{"transport", "nil response", "missing Any", "wrong protobuf", "missing availability", "availability type", "missing revision", "invalid revision", "missing decision", "decision type", "extra field"} {
		t.Run(bad, func(t *testing.T) {
			s, f, _ := formationRig(t, 1, 5)
			s.sessionID = f.party.SessionId
			f.world.Owner.Health = f.world.Owner.MaxHealth
			// This test exercises malformed callback replies after a committed
			// decision. Keep that setup independent of the pure Lua watchdog;
			// positive source/reload tests above exercise the real evaluator.
			host := &followPolicySession{fakeSession: f, t: t, sid: s.sessionID, self: s.selfGUID, available: true,
				fixedRevision: strings.Repeat("a", 64), fixedDecision: map[string]any{
					"position":      map[string]any{"x": 1, "y": 0, "z": 0, "o": 0},
					"startDistance": 2, "stopDistance": 1, "ready": true, "hold": true,
					"phase":  "within_personal_follow_space",
					"memory": map[string]any{"guid": strconv.FormatUint(s.selfGUID, 10), "marker": "committed"},
				}}
			s.session = host
			s.UseHostFollowPolicy()
			owner := playerPosition(f.world.Owner)
			first := s.formation.Plan(s.now(), s.selfGUID, 5, owner, false, playerPosition(f.world.Self), false, false)
			if !first.Valid || s.formation.memory == nil {
				t.Fatal("fixture did not retain committed callback memory", first)
			}
			memory := make(map[string]any, len(s.formation.memory))
			for key, value := range s.formation.memory {
				memory[key] = value
			}
			revision := s.formation.sourceRevision
			host.bad = bad
			got := s.formation.Plan(s.now().Add(time.Second), s.selfGUID, 5, owner, false, playerPosition(f.world.Self), false, false)
			if got.Valid || got.Ready || !reflect.DeepEqual(memory, s.formation.memory) || s.formation.sourceRevision != revision || s.formation.runtime != nil {
				t.Fatal("host failure fell back or erased committed state", got, memory, s.formation.memory, s.formation.sourceRevision)
			}
			toggle(t, s, true)
			s.Tick(context.Background())
			if len(f.packets) != 0 || s.state.Moving || !s.state.Following {
				t.Fatal("failed host source reached gameplay via a stale default", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestFollowPolicyCurrentDecisionStillRequiresRealRouteAndKeepsUnknownPortalAttempt(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	host := newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		return nil, errors.New("physical route blocked")
	}
	toggle(t, s, true)
	s.Tick(context.Background())
	if len(host.inputs) == 0 || len(f.packets) != 0 || s.state.Moving || s.state.NavAvailable {
		t.Fatal("current Lua output bypassed normal physical route qualification", s.stateLocked(), f.packets)
	}
	s, f, _, _, advance := portalGoalFixture(t)
	host = newFollowPolicySession(t, s, f)
	s.UseHostFollowPolicy()
	s.followPortal.notified = true
	witness := s.followPortal.witness
	advance(2 * encounterLeaseDuration)
	if candidates := s.portalCandidatesLocked(f.world, s.now()); len(candidates) == 0 {
		t.Fatal("fixture did not evaluate the current source after expiry")
	}
	s.Tick(context.Background())
	if !s.followPortal.notified || s.followPortal.witness != witness || countPackets(f, opcode.CMSGAreatrigger) != 0 || len(f.packets) != 0 {
		t.Fatal("source evaluation or expiry rearmed an already attempted crossing", s.followPortal, f.packets)
	}
}
