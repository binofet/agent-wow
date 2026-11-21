package buddyactions

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Native route/admission fixtures need a healthy evaluator to exercise their
// mechanical assertions. Keep the actual default source; the separate moving
// watchdog regression uses the real production five-millisecond allowance.
func healthyServiceTravelRuntime(t *testing.T) *behavior.Runtime {
	t.Helper()
	runtime, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Reload(behaviors.FollowFormationSource, ""); err != nil {
		t.Fatal(err)
	}
	return runtime
}

type servicePolicyRig struct {
	s       *Service
	f       *fakeSession
	advance func(time.Duration)
	request *actionsv1.ApproachQuestRequest
	travel  func(context.Context, *actionsv1.ApproachQuestRequest) (*actionsv1.State, error)
}

func serviceTravelPolicyRig(t *testing.T, kind string) servicePolicyRig {
	t.Helper()
	var r servicePolicyRig
	if kind == "vendor" {
		r.s, r.f, r.advance, r.request = vendorTravelRig(t)
		r.travel = r.s.TravelVendor
	} else {
		r.s, r.f, r.advance, r.request = trainerTravelRig(t)
		r.travel = r.s.TravelTrainer
	}
	r.s.sessionID = r.f.party.SessionId
	return r
}

func serviceArrivalSource(t *testing.T, yards string) string {
	t.Helper()
	const declaration = "local service_arrival_yards = 3"
	source := strings.Replace(behaviors.FollowFormationSource, declaration, "local service_arrival_yards = "+yards, 1)
	if source == behaviors.FollowFormationSource {
		t.Fatal("test did not edit the actual shared Lua service-arrival constant")
	}
	return source
}

func TestServiceTravelPolicyDefaultUsesVerifiedRouteWithoutOtherActions(t *testing.T) {
	for _, kind := range []string{"vendor", "trainer"} {
		t.Run(kind, func(t *testing.T) {
			r := serviceTravelPolicyRig(t, kind)
			npc := r.f.world.NearbyCreatures[0]
			got, err := r.travel(context.Background(), r.request)
			if err != nil || got.Reason != kind+"_travel_within_distance" || got.Moving || got.Traveling ||
				!got.NavAvailable || distance(r.s.pos, position{npc.X, npc.Y, npc.Z, 0}) > 3 ||
				r.s.formation.sourceRevision == "" || r.s.formation.remote != nil ||
				countPackets(r.f, opcode.MSGMoveStartForward) != 1 || countPackets(r.f, opcode.MSGMoveStop) != 1 {
				t.Fatal("standalone default did not use the actual Lua choice and verified executor", got, err, r.s.pos, r.f.packets)
			}
			for _, packet := range r.f.packets {
				if packet.Opcode != opcode.MSGMoveStartForward && packet.Opcode != opcode.MSGMoveHeartbeat && packet.Opcode != opcode.MSGMoveStop {
					t.Fatal("service travel wrote a non-movement action", packet)
				}
			}
		})
	}
}

func TestServiceTravelPolicyEditChangesInitialArrivalInSameBody(t *testing.T) {
	for _, kind := range []string{"vendor", "trainer"} {
		t.Run(kind, func(t *testing.T) {
			r := serviceTravelPolicyRig(t, kind)
			r.f.world.NearbyCreatures[0].X = r.f.world.Self.X + 5
			host := newFollowPolicySession(t, r.s, r.f)
			r.s.UseHostFollowPolicy()
			plans := 0
			r.s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
				plans++
				return []position{from, to}, nil
			}
			first, err := r.travel(context.Background(), r.request)
			if err != nil || first.Reason != kind+"_travel_within_distance" || plans != 1 || countPackets(r.f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal("default three-yard arrival did not require the real route", first, err, plans, r.f.packets)
			}
			packets, sid := len(r.f.packets), r.s.sessionID
			// The actual source remains five yards away; a newer authoritative
			// revision returns the local estimate to that observed pose.
			r.f.world.Self.PositionRevision++
			revision := host.reload(t, serviceArrivalSource(t, "6"))
			second, err := r.travel(context.Background(), r.request)
			if err != nil || second.Reason != kind+"_travel_within_distance" || second.Moving || second.Traveling ||
				plans != 1 || len(r.f.packets) != packets || r.s.sessionID != sid || r.s.formation.sourceRevision != revision {
				t.Fatal("same-session Lua edit did not change initial arrival", second, err, plans, r.f.packets)
			}
			input := host.inputs[len(host.inputs)-1]
			travel, ok := input["travel"].(map[string]any)
			if input["mode"] != "service_travel" || !ok || travel["kind"] != kind || travel["routeKnown"] != false {
				t.Fatal("initial arrival lost the common NPC travel facts", input)
			}
		})
	}
}

func TestServiceTravelPolicyReloadChangesCompletionDuringSameJourney(t *testing.T) {
	for _, kind := range []string{"vendor", "trainer"} {
		t.Run(kind, func(t *testing.T) {
			r := serviceTravelPolicyRig(t, kind)
			host := newFollowPolicySession(t, r.s, r.f)
			r.s.UseHostFollowPolicy()
			sid, reloaded, revision := r.s.sessionID, false, ""
			r.s.approachDelay = func(ctx context.Context, d time.Duration) error {
				r.advance(d)
				if !reloaded {
					if !r.s.state.Moving {
						t.Fatal("reload fixture was not in the actual journey")
					}
					revision = host.reload(t, serviceArrivalSource(t, "6"))
					reloaded = true
				}
				return ctx.Err()
			}
			got, err := r.travel(context.Background(), r.request)
			npc := r.f.world.NearbyCreatures[0]
			gap := distance(r.s.pos, position{npc.X, npc.Y, npc.Z, 0})
			if err != nil || !reloaded || got.Reason != kind+"_travel_within_distance" || got.Moving || got.Traveling ||
				gap <= 3 || gap > 6 || r.s.sessionID != sid || r.s.formation.sourceRevision != revision ||
				countPackets(r.f, opcode.MSGMoveStartForward) != 1 || countPackets(r.f, opcode.MSGMoveStop) != 1 {
				t.Fatal("edited completion did not take effect in the same finite journey", got, err, gap, r.f.packets)
			}
			last := host.inputs[len(host.inputs)-1]
			travel, ok := last["travel"].(map[string]any)
			if last["mode"] != "service_travel" || !ok || travel["kind"] != kind || travel["routeKnown"] != true || travel["remainingRoute"].(float64) > 6 {
				t.Fatal("route completion bypassed the same Lua route facts", last)
			}
		})
	}
}

func TestServiceTravelPolicyBentRouteCannotArriveByDirectDistanceAlone(t *testing.T) {
	for _, kind := range []string{"vendor", "trainer"} {
		t.Run(kind, func(t *testing.T) {
			r := serviceTravelPolicyRig(t, kind)
			r.f.world.NearbyCreatures[0].X = r.f.world.Self.X + 5
			r.s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
				return []position{from, {x: from.x, y: from.y + 20, z: from.z}, {x: to.x, y: from.y + 20, z: from.z}, to}, nil
			}
			host := newFollowPolicySession(t, r.s, r.f)
			host.runtime = healthyServiceTravelRuntime(t) // Route semantics; production watchdog remains separate.
			r.s.UseHostFollowPolicy()
			actualLua := r.s.formation.remote
			guardObserved, reloaded := false, false
			r.s.formation.remote = func(input map[string]any) (bool, string, map[string]any, error) {
				available, revision, output, err := actualLua(input)
				if reloaded && err == nil && input["mode"] == "service_travel" {
					observed := host.inputs[len(host.inputs)-1]
					travel := observed["travel"].(map[string]any)
					self, owner := observed["self"].(map[string]any), observed["owner"].(map[string]any)
					dx, dy, dz := self["x"].(float64)-owner["x"].(float64), self["y"].(float64)-owner["y"].(float64), self["z"].(float64)-owner["z"].(float64)
					if travel["routeKnown"] == true && travel["remainingRoute"].(float64) > 6 && dx*dx+dy*dy+dz*dz <= 36 && output["arrived"] == false {
						guardObserved = true
					}
				}
				return available, revision, output, err
			}
			r.s.approachDelay = func(ctx context.Context, d time.Duration) error {
				r.advance(d)
				if !reloaded {
					host.reload(t, serviceArrivalSource(t, "6"))
					reloaded = true
				}
				return ctx.Err()
			}
			got, err := r.travel(context.Background(), r.request)
			if err != nil || !guardObserved || got.Reason != kind+"_travel_within_distance" || got.Moving || got.Traveling || countPackets(r.f, opcode.MSGMoveHeartbeat) == 0 {
				t.Fatal("direct proximity skipped the remaining verified bend", got, err, guardObserved, r.f.packets)
			}
		})
	}
}

func TestServiceTravelPolicyMalformedOrUnavailableHostNeverUsesStaticArrival(t *testing.T) {
	for _, kind := range []string{"vendor", "trainer"} {
		for _, failure := range []string{"missing arrived", "wrong arrived type", "transport"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				r := serviceTravelPolicyRig(t, kind)
				r.f.world.NearbyCreatures[0].X = r.f.world.Self.X + 2
				host := newFollowPolicySession(t, r.s, r.f)
				r.s.UseHostFollowPolicy()
				if failure == "transport" {
					host.bad = "transport"
				} else {
					decision := "{}"
					if failure == "wrong arrived type" {
						decision = `{arrived="true"}`
					}
					source := strings.Replace(behaviors.FollowFormationSource, "return M", `
local original=M.decide
M.decide=function(input)
    if input.mode=="service_travel" then return `+decision+` end
    return original(input)
end
return M`, 1)
					host.reload(t, source)
				}
				got, err := r.travel(context.Background(), r.request)
				if got == nil || got.Reason == kind+"_travel_within_distance" || got.Moving || got.Traveling || len(r.f.packets) != 0 || len(host.inputs) == 0 {
					t.Fatal("bad current policy fell back to the old static arrival", got, err, r.f.packets)
				}
			})
		}
	}
}

func TestServiceTravelPolicyLateCallbackCannotCompleteOrWriteWithChangedFacts(t *testing.T) {
	for _, kind := range []string{"vendor", "trainer"} {
		for _, stage := range []string{"initial", "route"} {
			for _, change := range []string{"body", "session", "visit", "raw XYZ", "NPC", "lease expiry", "replacement", "controller", "cancel"} {
				t.Run(fmt.Sprintf("%s/%s/%s", kind, stage, change), func(t *testing.T) {
					r := serviceTravelPolicyRig(t, kind)
					if stage == "initial" {
						r.f.world.NearbyCreatures[0].X = r.f.world.Self.X + 2
					}
					host := newFollowPolicySession(t, r.s, r.f)
					r.s.UseHostFollowPolicy()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					actualLua, injected := r.s.formation.remote, false
					r.s.formation.remote = func(input map[string]any) (bool, string, map[string]any, error) {
						available, revision, output, err := actualLua(input)
						if !injected && err == nil && input["mode"] == "service_travel" {
							travel := host.inputs[len(host.inputs)-1]["travel"].(map[string]any)
							if travel["routeKnown"] == (stage == "route") {
								if output["arrived"] != (stage == "initial") {
									t.Fatal("late-fact fixture did not reach the intended actual Lua decision", input, output)
								}
								injected = true
								switch change {
								case "body":
									r.f.world.Self.Guid++
								case "session":
									r.s.sessionID = "replacement-body"
								case "visit":
									r.f.world.VisitRevision++
								case "raw XYZ":
									r.f.world.Self.Y += .25 // Keep PositionRevision unchanged to test exact raw facts.
								case "NPC":
									r.f.world.NearbyCreatures[0].X += .25
								case "lease expiry":
									r.advance(31 * time.Second)
								case "replacement":
									r.s.interactionToken = "replacement-token"
									r.s.interactionUntil = r.s.now().Add(30 * time.Second)
									r.s.state.InteractionOwner = "quests"
									r.s.state.Moving, r.s.state.Traveling = true, true
									r.s.state.TravelNpcGuid = 780
									r.s.state.Reason, r.s.state.NavReason = "replacement_reason", "replacement_nav"
									r.s.state.NavAvailable = true
								case "controller":
									r.s.state.Reason = "new_controller_reason"
								case "cancel":
									cancel()
								}
							}
						}
						return available, revision, output, err
					}
					got, err := r.travel(ctx, r.request)
					if !injected || got == nil || got.Reason == kind+"_travel_within_distance" || len(r.f.packets) != 0 {
						t.Fatal("late callback facts authorized old completion or a movement write", injected, got, err, r.f.packets)
					}
					if change == "cancel" && status.Code(err) != codes.Canceled {
						t.Fatal("cancellation after policy evaluation was lost", got, err)
					}
					if change == "replacement" {
						if got.Reason != "replacement_reason" || got.NavReason != "replacement_nav" || !got.NavAvailable || !got.Moving || !got.Traveling ||
							got.TravelNpcGuid != 780 || got.InteractionOwner != "quests" || r.s.interactionToken != "replacement-token" || r.s.questApproaching {
							t.Fatal("old travel cleanup changed the replacement controller", got)
						}
					} else if got.Moving || got.Traveling || r.s.questApproaching {
						t.Fatal("blocked travel retained movement or its reservation", got)
					}
				})
			}
		}
	}
}
