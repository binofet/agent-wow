package buddyactions

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type instanceEntryPolicyFixture struct {
	t            *testing.T
	s            *Service
	f            *fakeSession
	advance      func(time.Duration)
	vm           *behavior.Session
	source       string
	decision     behavior.Decision
	methods      []string
	lootSequence uint64
}

func newInstanceEntryPolicyFixture(t *testing.T) *instanceEntryPolicyFixture {
	t.Helper()
	s, f, r, advance, _ := newAreaTriggerFixture(t)
	// The task itself must acquire custody. Remove only the fixture's setup
	// lease through its real native release, before the policy starts.
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	f.party.Group.GroupGuid, f.party.Group.LeaderGuid = 7, 3
	activateEncounterWriter(t, s, f)
	s.session = &idleBodyReportSession{sessionAPI: s.session, f: f}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", filepath.Join("..", "..", "dev", "encounter-bundle"))
	full, err := command.Output()
	if err != nil {
		t.Fatal("maintained full encounter bundle composition failed", err)
	}
	vm, err := behavior.NewSession(context.Background(), s.sessionID, string(full))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	return &instanceEntryPolicyFixture{t: t, s: s, f: f, advance: advance, vm: vm, source: string(full)}
}

func (p *instanceEntryPolicyFixture) observe() uint64 {
	p.t.Helper()
	visit := p.f.world.VisitRevision
	if _, err := p.s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, visit), wrapperspb.UInt64(visit)); err != nil {
		p.t.Fatal(err)
	}
	view, err := p.s.EncounterObservation(context.Background(), &emptypb.Empty{})
	if err != nil || !view.GetHostOwned() || view.VisitId != visit || view.SessionId != p.s.sessionID {
		p.t.Fatal("actual native encounter projection was not current", view, err)
	}
	seq, err := p.vm.Enqueue(behavior.Event{Kind: "encounter_observation", Value: instanceEntryPolicyJSON(p.t, view)})
	if err != nil {
		p.t.Fatal(err)
	}
	return seq
}

func (p *instanceEntryPolicyFixture) command(value map[string]any) {
	p.observe()
	if _, err := p.vm.Enqueue(behavior.Event{Kind: "command", Value: value}); err != nil {
		p.t.Fatal(err)
	}
}

func instanceEntryPolicyCommand() map[string]any {
	return map[string]any{"task": "enter_instance", "id": "mara-entry-1", "ownerGuid": "3", "destinationMapId": 349,
		"allowedTriggerIds": []any{3133, 3134}, "triggerId": 3133}
}

func (p *instanceEntryPolicyFixture) step() map[string]any {
	p.t.Helper()
	input := map[string]any{"kind": "encounter", "observationSequence": strconv.FormatUint(p.observe(), 10)}
	if p.lootSequence != 0 {
		input["observationSequences"] = map[string]any{"corpse_loot": strconv.FormatUint(p.lootSequence, 10)}
	}
	d, err := p.vm.Decide(context.Background(), input)
	if err != nil {
		p.t.Fatal("actual full-bundle decision failed", err, p.vm.Status())
	}
	p.decision = d
	return d.Output
}

func instanceEntryPolicyJSON(t *testing.T, value proto.Message) map[string]any {
	t.Helper()
	// Match the actual Runtime adapter, including canonical zero revisions and
	// explicit false knownness. This is metadata, never a game-effect receipt.
	b, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (p *instanceEntryPolicyFixture) currentTask() map[string]any {
	state := p.taskState()
	task, _ := state["current"].(map[string]any)
	return task
}

func (p *instanceEntryPolicyFixture) taskState() map[string]any {
	p.t.Helper()
	s, err := p.vm.State(context.Background())
	if err != nil {
		p.t.Fatal(err)
	}
	data, _ := s.Checkpoint["data"].(map[string]any)
	tasks, _ := data["tasks"].(map[string]any)
	state, _ := tasks["data"].(map[string]any)
	return state
}

func (p *instanceEntryPolicyFixture) action(capability string) map[string]any {
	p.t.Helper()
	out := p.step()
	action, _ := out["action"].(map[string]any)
	if action["capability"] != capability {
		p.t.Fatal("full bundle did not select the expected entry action", capability, out, p.currentTask())
	}
	return action
}

func instanceEntryPolicyDecode(t *testing.T, action map[string]any, request proto.Message, keys ...string) {
	t.Helper()
	values := map[string]any{}
	for _, key := range keys {
		if v, ok := action[key]; ok {
			values[key] = v
		}
	}
	b, err := json.Marshal(values)
	if err != nil || protojson.Unmarshal(b, request) != nil {
		t.Fatal("actual Lua action did not match its compiled native request", action, string(b), err)
	}
}

func (p *instanceEntryPolicyFixture) native(action map[string]any) proto.Message {
	p.t.Helper()
	capability, _ := action["capability"].(string)
	p.methods = append(p.methods, capability)
	ctx := encounterHostContext(testEncounterKey, p.f.world.VisitRevision)
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set("agentwow-policy-spell", strings.Repeat("d", 64))
	ctx = metadata.NewIncomingContext(ctx, md)
	var response proto.Message
	var err error
	switch capability {
	case "instance_entry_status":
		response, err = p.s.Status(ctx, &emptypb.Empty{})
	case "area_trigger_observation":
		response, err = p.s.AreaTriggerObservation(ctx, &emptypb.Empty{})
	case "travel_to":
		r := &actionsv1.TravelToRequest{}
		instanceEntryPolicyDecode(p.t, action, r, "token", "mapId", "x", "y", "z", "arrivalDistance")
		response, err = p.s.TravelTo(ctx, r)
	case "area_trigger_reference":
		r := &actionsv1.AreaTriggerReferenceRequest{}
		instanceEntryPolicyDecode(p.t, action, r, "sessionId", "selfGuid", "visitId", "destinationMapId")
		response, err = p.s.AreaTriggerReference(ctx, r)
	case "acquire_idle_interaction":
		r := &actionsv1.IdleInteractionRequest{}
		instanceEntryPolicyDecode(p.t, action, r, "sessionId", "selfGuid", "visitId", "owner", "token", "leaseMs")
		response, err = p.s.AcquireIdleInteraction(ctx, r)
	case "notify_area_trigger":
		r := &actionsv1.NotifyAreaTriggerRequest{}
		instanceEntryPolicyDecode(p.t, action, r, "sessionId", "selfGuid", "visitId", "token", "triggerId", "expectedPositionRevision", "expectedRevision", "clientRequestId")
		response, err = p.s.NotifyAreaTrigger(ctx, r)
	case "release_interaction":
		r := &actionsv1.InteractionRelease{}
		instanceEntryPolicyDecode(p.t, action, r, "sessionId", "selfGuid", "visitId", "owner", "token")
		response, err = p.s.ReleaseInteraction(ctx, r)
	case "movement_owner":
		if action["mode"] == "release" {
			r := &actionsv1.MovementOwnerRelease{}
			instanceEntryPolicyDecode(p.t, action, r, "sessionId", "selfGuid", "owner", "token")
			response, err = p.s.ReleaseMovement(ctx, r)
		} else {
			r := &actionsv1.MovementOwnerRequest{}
			instanceEntryPolicyDecode(p.t, action, r, "sessionId", "selfGuid", "owner", "token", "leaseMs")
			response, err = p.s.AcquireMovement(ctx, r)
		}
	case "move_body":
		r := &actionsv1.BodyMovementRequest{}
		instanceEntryPolicyDecode(p.t, action, r, "sessionId", "selfGuid", "visitId", "sequenceId", "bearingRadians", "durationMs", "jump", "idleToken")
		response, err = p.s.MoveBody(ctx, r)
	default:
		p.t.Fatal("entry fixture received an uncommissioned action", action)
	}
	if err != nil {
		p.t.Fatal("actual native action refused the exact full-bundle proposal", action, err)
	}
	return response
}

func (p *instanceEntryPolicyFixture) reserve() uint64 {
	p.t.Helper()
	id, err := p.vm.ReserveAction(p.decision)
	if err != nil {
		p.t.Fatal(err)
	}
	return id
}

func (p *instanceEntryPolicyFixture) receipt(id uint64, action map[string]any, response proto.Message, lost bool) {
	p.t.Helper()
	capability := action["capability"].(string)
	payload := map[string]any{"capability": capability}
	outcome := behavior.ActionConfirmed
	if lost {
		// The actual Runtime adapter terminally settles its host call with
		// reconciliationRequired, retaining native delivery uncertainty. This
		// permits the task's read-only poll without replaying the mutation.
		outcome = behavior.ActionSettledUnverified
		payload["reconciliationRequired"], payload["error"] = true, "native RPC reply lost"
	} else {
		payload["result"] = instanceEntryPolicyJSON(p.t, response)
		if capability == "notify_area_trigger" || capability == "move_body" || capability == "travel_to" {
			outcome = behavior.ActionSettledUnverified
		}
		if capability == "move_body" {
			state := response.(*actionsv1.State)
			phase := state.GetBodyMovement().GetPhase()
			payload["phase"] = phase
			// Mirror the real Runtime receipt only for a native known full
			// body write, including its final STOP. Refusals/unknowns stay false.
			payload["transport_written"] = state.GetBodyMovement().GetTransportWritten() && (phase == "body_input_written" || phase == "body_settled_written")
		}
		if capability == "release_interaction" {
			outcome = behavior.ActionSettledUnverified
			payload["phase"] = "interaction_released"
		}
	}
	if _, err := p.vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: p.s.sessionID, ActionID: id, Outcome: outcome, Payload: payload}}); err != nil {
		p.t.Fatal(err)
	}
}

func (p *instanceEntryPolicyFixture) dispatch(capability string) proto.Message {
	action := p.action(capability)
	id := p.reserve()
	response := p.native(action)
	p.receipt(id, action, response, false)
	return response
}

func (p *instanceEntryPolicyFixture) prepareNotify() map[string]any {
	p.command(instanceEntryPolicyCommand())
	p.dispatch("instance_entry_status")
	p.dispatch("area_trigger_reference")
	p.dispatch("area_trigger_observation")
	p.dispatch("acquire_idle_interaction")
	return p.action("notify_area_trigger")
}

func TestInstanceEntryPolicyFullBundleCASLostNotifyReconcilesAndRequiresFreshRegroup(t *testing.T) {
	p := newInstanceEntryPolicyFixture(t)
	action := p.prepareNotify()
	id := p.reserve()
	p.f.failSend, p.f.failSendOpcode = true, opcode.CMSGAreatrigger
	response := p.native(action).(*actionsv1.AreaTriggerOperation)
	p.f.failSend = false
	if !response.TransportUnknown || response.TransportWritten || !response.Pending || p.s.followPortal.portal.trigger != 0 || countPackets(p.f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("real explicit notification was not retained", response, p.f.packets)
	}
	before, err := p.vm.State(context.Background())
	if err != nil || len(before.Pending) != 1 || p.currentTask()["operation"].(map[string]any)["clientRequestId"] != response.ClientRequestId {
		t.Fatal("notifying reservation was not in the actual checkpoint", before, err)
	}
	if strings.Count(p.source, "regroup_yards=20") != 1 {
		t.Fatal("current full bundle lost its uniquely editable entry tuning")
	}
	edited := strings.Replace(p.source, "regroup_yards=20", "regroup_yards=5", 1)
	revision, err := p.vm.Reload(context.Background(), edited, before.Status.Revision)
	after, readErr := p.vm.State(context.Background())
	if err != nil || readErr != nil || revision == before.Status.Revision || after.Status.SessionID != before.Status.SessionID ||
		!reflect.DeepEqual(before.Checkpoint, after.Checkpoint) || !reflect.DeepEqual(before.Pending, after.Pending) {
		t.Fatal("full-bundle CAS lost the pending native identity", after, err, readErr)
	}
	if _, err := p.vm.Reload(context.Background(), p.source, before.Status.Revision); !errors.Is(err, behavior.ErrRevisionConflict) {
		t.Fatal("stale CAS replaced current policy", err)
	}
	p.receipt(id, action, response, true)
	p.dispatch("area_trigger_observation")
	if task := p.currentTask(); task["notifyId"] != response.ClientRequestId || countPackets(p.f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("lost reply changed the original notification", task, p.f.packets)
	}
	// Read a real native destination receipt from the synthetic adjacent Core
	// correlation+Actions entry packets, with no booking/server-causality claim.
	setAreaTriggerDestinationFacts(t, p.s, p.f, response)
	p.dispatch("area_trigger_observation")
	p.dispatch("instance_entry_status")
	release := p.action("release_interaction")
	if release["visitId"] != "2" || release["token"] != p.s.interactionToken {
		t.Fatal("old owned token was released against a stale visit", release)
	}
	releaseID := p.reserve()
	released := p.native(release)
	p.receipt(releaseID, release, released, false)
	out := p.step()
	if out["task"].(map[string]any)["phase"] != "waiting_for_fresh_owner_regroup" || out["action"] != nil {
		t.Fatal("map entry was mistaken for owner regroup", out)
	}
	p.f.world.Owner.Present, p.f.world.Owner.MapKnown, p.f.world.Owner.PositionKnown = true, true, true
	p.f.world.Owner.MapId, p.f.world.Owner.X, p.f.world.Owner.Y, p.f.world.Owner.Z = 349, p.f.world.Self.X+10, p.f.world.Self.Y, p.f.world.Self.Z
	p.f.world.Owner.PositionRevision++
	for _, kind := range []string{"stale", "inferred", "fresh but beyond edited five yards"} {
		p.f.world.Owner.PositionAgeMs = 0
		p.f.world.Owner.PositionInferred = false
		if kind == "stale" {
			p.f.world.Owner.PositionAgeMs = 2001
		}
		if kind == "inferred" {
			p.f.world.Owner.PositionInferred = true
		}
		out = p.step()
		if out["task"].(map[string]any)["phase"] != "waiting_for_fresh_owner_regroup" {
			t.Fatal("unqualified owner or superseded tuning completed task", kind, out)
		}
	}
	p.f.world.Owner.PositionInferred = false
	p.f.world.Owner.X = p.f.world.Self.X + 4
	out = p.step()
	if task := out["task"].(map[string]any); task["phase"] != "complete" || task["reason"] != "regroup_observed" || p.s.interactionToken != "" ||
		countPackets(p.f, opcode.CMSGAreatrigger) != 1 || p.vm.Status().Revision != revision {
		t.Fatal("fresh actual regroup did not complete current edited task", out, p.f.packets)
	}
	want := []string{"instance_entry_status", "area_trigger_reference", "area_trigger_observation", "acquire_idle_interaction", "notify_area_trigger", "area_trigger_observation", "area_trigger_observation", "instance_entry_status", "release_interaction"}
	if !reflect.DeepEqual(p.methods, want) {
		t.Fatal("full bundle retried or added native mechanics", p.methods)
	}
	retired := p.taskState()["retired"].(map[string]any)["mara-entry-1"].(map[string]any)
	archived := retired["task"].(map[string]any)
	native := archived["native"].(map[string]any)
	history := archived["history"].([]any)
	if retired["reason"] != "regroup_observed" || native["transportUnknown"] != true || native["error"] != response.Error ||
		native["sourceScope"].(map[string]any)["visitRevision"] != "9" || len(history) != 1 ||
		history[0].(map[string]any)["receipt"].(map[string]any)["payload"].(map[string]any)["reconciliationRequired"] != true {
		t.Fatal("completion discarded original UNKNOWN/source/history", retired)
	}
	beforeArchive, err := p.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.vm.Reload(context.Background(), edited+"\n-- retained completed entry evidence", revision); err != nil {
		t.Fatal(err)
	}
	afterArchive, err := p.vm.State(context.Background())
	if err != nil || !reflect.DeepEqual(beforeArchive.Checkpoint, afterArchive.Checkpoint) {
		t.Fatal("CAS lost retired UNKNOWN evidence", afterArchive, err)
	}
}

func TestInstanceEntryPolicyKnownDeathRetiresOnlyItsOwnEntryCustody(t *testing.T) {
	for _, acquired := range []bool{false, true} {
		t.Run(map[bool]string{false: "before acquisition", true: "after acquisition"}[acquired], func(t *testing.T) {
			p := newInstanceEntryPolicyFixture(t)
			p.command(instanceEntryPolicyCommand())
			if acquired {
				p.dispatch("instance_entry_status")
				p.dispatch("area_trigger_reference")
				p.dispatch("area_trigger_observation")
				p.dispatch("acquire_idle_interaction")
			}
			p.f.world.Self.Alive, p.f.world.Self.Health = false, 0
			if acquired {
				p.dispatch("instance_entry_status")
				p.dispatch("release_interaction")
			}
			out := p.step()
			if out["task"].(map[string]any)["phase"] != "complete" || out["task"].(map[string]any)["reason"] != "dead" || p.currentTask() != nil ||
				p.s.interactionToken != "" || len(p.f.packets) != 0 || p.s.areaTrigger.revision != 0 {
				t.Fatal("dead body continued entry or kept known own lease", out, p.methods, p.f.packets)
			}
			// The full bundle's ordinary death policy may separately propose a
			// release-spirit action. This test authorizes/dispatches no such RPC.
			if !acquired && len(p.methods) != 0 {
				t.Fatal("known death ran entry RPCs before custody", p.methods)
			}
			retired := p.taskState()["retired"].(map[string]any)["mara-entry-1"].(map[string]any)
			if retired["reason"] != "dead" || retired["task"].(map[string]any)["finishing"] != "dead" {
				t.Fatal("entry death evidence was not archived", retired)
			}
		})
	}
}

func (p *instanceEntryPolicyFixture) freshOwner(distance float32) {
	p.f.world.Owner.Present, p.f.world.Owner.MapKnown, p.f.world.Owner.PositionKnown = true, true, true
	p.f.world.Owner.MapId, p.f.world.Owner.X, p.f.world.Owner.Y, p.f.world.Owner.Z = 349, p.f.world.Self.X+distance, p.f.world.Self.Y, p.f.world.Self.Z
	p.f.world.Owner.PositionAgeMs, p.f.world.Owner.PositionInferred = 0, false
	p.f.world.Owner.PositionRevision++
}

func TestInstanceEntryPolicyExpiredOrRefusedReleaseReadsWithoutRepeatingWrites(t *testing.T) {
	for _, mode := range []string{"expired lease", "known release refusal", "unknown release"} {
		t.Run(mode, func(t *testing.T) {
			p := newInstanceEntryPolicyFixture(t)
			action := p.prepareNotify()
			id := p.reserve()
			op := p.native(action).(*actionsv1.AreaTriggerOperation)
			p.receipt(id, action, op, false)
			if mode == "expired lease" {
				p.advance(31 * time.Second)
			}
			setAreaTriggerDestinationFacts(t, p.s, p.f, op)
			p.dispatch("area_trigger_observation")
			p.dispatch("instance_entry_status")
			if mode == "expired lease" {
				out := p.step()
				if out["action"] != nil || out["task"].(map[string]any)["phase"] != "waiting_for_fresh_owner_regroup" || p.currentTask()["lastLease"].(map[string]any)["absentOrReplacedObserved"] != true {
					t.Fatal("known absent lease attempted stale release", out, p.currentTask())
				}
			} else {
				if mode == "known release refusal" {
					oldToken := p.s.interactionToken
					if _, err := p.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{SessionId: p.s.sessionID, SelfGuid: p.s.selfGUID, VisitId: 2, Owner: "exploration", Token: oldToken}); err != nil {
						t.Fatal(err)
					}
					if _, err := p.s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: p.s.sessionID, SelfGuid: p.s.selfGUID, VisitId: 2, Owner: "exploration", LeaseMs: 30000}); err != nil {
						t.Fatal(err)
					}
				}
				release := p.action("release_interaction")
				rid := p.reserve()
				if mode == "unknown release" {
					response := p.native(release)
					p.receipt(rid, release, response, true)
					p.freshOwner(1)
					for range 3 {
						out := p.step()
						if out["action"] != nil || out["task"].(map[string]any)["phase"] != "reconcile_release_interaction" {
							t.Fatal("known absence erased UNKNOWN release", out)
						}
					}
					return
				}
				request := &actionsv1.InteractionRelease{}
				instanceEntryPolicyDecode(t, release, request, "sessionId", "selfGuid", "visitId", "owner", "token")
				p.methods = append(p.methods, "release_interaction")
				_, err := p.s.ReleaseInteraction(context.Background(), request)
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatal("replacement token was not protected", err)
				}
				if _, err := p.vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: p.s.sessionID, ActionID: rid, Outcome: behavior.ActionNotDispatched, Payload: map[string]any{"capability": "release_interaction", "error": "exact token replaced"}}}); err != nil {
					t.Fatal(err)
				}
				p.dispatch("instance_entry_status")
				p.dispatch("instance_entry_status")
				replacement := p.s.interactionToken
				if _, err := p.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{SessionId: p.s.sessionID, SelfGuid: p.s.selfGUID, VisitId: 2, Owner: "exploration", Token: replacement}); err != nil {
					t.Fatal(err)
				}
				p.dispatch("instance_entry_status")
				p.step()
			}
			p.freshOwner(1)
			out := p.step()
			if out["task"].(map[string]any)["phase"] != "complete" || countPackets(p.f, opcode.CMSGAreatrigger) != 1 {
				t.Fatal("fresh regroup could not finish known local-token retirement", out)
			}
			releases := 0
			for _, capability := range p.methods {
				if capability == "release_interaction" {
					releases++
				}
			}
			want := 0
			if mode == "known release refusal" {
				want = 1
			}
			if releases != want {
				t.Fatal("a failed or expired release was automatically written again", p.methods)
			}
		})
	}
}

func TestInstanceEntryPolicyMalformedIntentCannotRequestNativeEntry(t *testing.T) {
	for _, kind := range []string{"no allowlist", "foreign owner syntax", "trigger outside allowlist", "invalid recovery", "missing route coordinate", "whitespace task ID", "control task ID"} {
		t.Run(kind, func(t *testing.T) {
			p := newInstanceEntryPolicyFixture(t)
			command := instanceEntryPolicyCommand()
			switch kind {
			case "no allowlist":
				delete(command, "allowedTriggerIds")
			case "foreign owner syntax":
				command["ownerGuid"] = "0"
			case "trigger outside allowlist":
				command["triggerId"] = 3135
			case "invalid recovery":
				command["startRecovery"] = map[string]any{"absoluteBearingRadians": 0, "durationMs": 501}
			case "missing route coordinate":
				command["routeGoal"] = map[string]any{"x": -1484.07, "y": 2617.57}
			case "whitespace task ID":
				command["id"] = " entry-id "
			case "control task ID":
				command["id"] = "entry\nid"
			}
			p.command(command)
			out := p.step()
			if out["action"] != nil || out["task"].(map[string]any)["phase"] != "invalid_enter_instance_intent" || p.currentTask() != nil || len(p.f.packets) != 0 || p.s.areaTrigger.revision != 0 {
				t.Fatal("malformed command authorized a normal entry mechanic", kind, out, p.f.packets)
			}
		})
	}
}

func TestInstanceEntryPolicyRecoveryKeepsOneIdleLeaseThroughKnownSettleAndWalk(t *testing.T) {
	for _, settle := range []bool{false, true} {
		t.Run(map[bool]string{false: "walk", true: "known settle then one walk"}[settle], func(t *testing.T) {
			p := newInstanceEntryPolicyFixture(t)
			command := instanceEntryPolicyCommand()
			command["startRecovery"] = map[string]any{"absoluteBearingRadians": 0, "durationMs": 50}
			planCalls := 0
			p.s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, start buddynav.Point, _, _ float64, d time.Duration, jump bool) ([]buddynav.BodyMotionSample, error) {
				planCalls++
				if d != 50*time.Millisecond || jump {
					t.Fatal("Lua changed the explicit diagnostic input", d, jump)
				}
				end := start
				if settle && planCalls == 1 {
					end.Z -= .1
					return []buddynav.BodyMotionSample{{Point: start, Settling: true}, {At: 100 * time.Millisecond, Point: end, Settling: true}}, nil
				}
				end.X += .35
				return []buddynav.BodyMotionSample{{Point: start}, {At: d, Point: end}}, nil
			}
			p.s.bodyMotionWait = func(_ context.Context, d time.Duration) error { p.advance(d); return nil }
			p.command(command)
			p.dispatch("instance_entry_status")
			p.dispatch("area_trigger_reference")
			p.dispatch("area_trigger_observation")
			lease := p.dispatch("acquire_idle_interaction").(*actionsv1.InteractionLease)
			until := p.s.interactionUntil
			body := p.action("move_body")
			if body["idleToken"] != lease.Token {
				t.Fatal("body input did not retain the exact parent idle lease", body)
			}
			id := p.reserve()
			moved := p.native(body).(*actionsv1.State)
			p.receipt(id, body, moved, false)
			phase := "body_input_written"
			if settle {
				phase = "body_settled_written"
			}
			if moved.GetBodyMovement().GetPhase() != phase || !moved.BodyMovement.TransportWritten {
				t.Fatal("actual idle-owned body input failed", moved, p.f.packets)
			}
			p.dispatch("instance_entry_status")
			if settle {
				next := p.action("move_body")
				if next["idleToken"] != lease.Token || next["sequenceId"] == body["sequenceId"] {
					t.Fatal("known settling did not request one new exact input", next, body)
				}
				id := p.reserve()
				moved = p.native(next).(*actionsv1.State)
				p.receipt(id, next, moved, false)
				if moved.GetBodyMovement().GetPhase() != "body_input_written" {
					t.Fatal("later normal step did not complete", moved)
				}
				p.dispatch("instance_entry_status")
			}
			notify := p.action("notify_area_trigger")
			if notify["token"] != lease.Token {
				t.Fatal("normal entry changed the parent's idle lease", notify)
			}
			nid := p.reserve()
			op := p.native(notify).(*actionsv1.AreaTriggerOperation)
			p.receipt(nid, notify, op, false)
			wantCalls := 1
			if settle {
				wantCalls = 2
			}
			if !op.TransportWritten || planCalls != wantCalls || p.s.interactionToken != lease.Token || p.s.interactionUntil != until || p.s.movementOwner.owner != "" ||
				countPackets(p.f, opcode.MSGMoveStartForward) != 1 || countPackets(p.f, opcode.CMSGAreatrigger) != 1 {
				t.Fatal("recovery did not preserve finite same-lease continuation", op, p.methods, p.f.packets)
			}
			acquired := 0
			for _, capability := range p.methods {
				if capability == "movement_owner" {
					t.Fatal("entry recovery used a separate legacy movement owner", p.methods)
				}
				if capability == "acquire_idle_interaction" {
					acquired++
				}
			}
			if acquired != 1 {
				t.Fatal("entry recovery re-acquired or renewed instead of retaining its parent lease", p.methods)
			}
		})
	}
}

func TestInstanceEntryPolicyReplacedProposalDoesNotReserveTaskOperation(t *testing.T) {
	p := newInstanceEntryPolicyFixture(t)
	p.command(instanceEntryPolicyCommand())
	loot := map[string]any{"sessionId": p.s.sessionID, "selfGuid": "1", "state": map[string]any{"pendingCorpseGuid": "17379390962022744065", "phase": "abort"},
		"window": map[string]any{"observed": true, "openRequestId": "owned-loot-window"}, "actions": map[string]any{"interactionOwner": "loot"}}
	var err error
	p.lootSequence, err = p.vm.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: loot})
	if err != nil {
		t.Fatal(err)
	}
	out := p.step()
	if out["action"].(map[string]any)["capability"] != "loot_close" || p.currentTask()["operation"] != nil || len(p.f.packets) != 0 {
		t.Fatal("replaced status proposal was committed as a task operation", out, p.currentTask())
	}
	// This is scheduler-source proof only; no synthetic loot receipt or native
	// close is invented. A new observation simply removes the old window fact.
	loot["state"] = map[string]any{}
	loot["window"] = map[string]any{}
	p.lootSequence, err = p.vm.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: loot})
	if err != nil {
		t.Fatal(err)
	}
	action := p.action("instance_entry_status")
	if p.currentTask()["operation"].(map[string]any)["capability"] != action["capability"] || p.vm.Status().Pending != 0 {
		t.Fatal("unselected proposal deadlocked the task", p.currentTask())
	}
}
