package world

import (
	"context"
	"os/exec"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func flightpointEncounter(t *testing.T) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse, string) {
	t.Helper()
	source, err := exec.Command("../../dev/encounter-bundle").Output()
	if err != nil {
		t.Fatal(err)
	}
	host, err := behavior.NewSession(context.Background(), fallbackSession, string(source))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	o := waitTaskObservation()
	o.Players["4"].CombatKnown = true
	o.Players["4"].GroundRunSpeed = proto.Float32(7)
	o.Players["3"].X = 13
	o.Players["3"].CombatKnown = true
	o.Interaction = &actionsv1.InteractionObservation{}
	o.Navigation.FollowRevision = 1
	o.Navigation.GroundNavigationConfigured = true
	o.Flightpath = &actionsv1.FlightpathObservationResponse{SessionId: fallbackSession, SelfGuid: 4, VisitBound: true, VisitId: 1}
	addFallbackTarget(o, fallbackNPC, 0, 1000)
	npc := o.Targets[fallbackGUID(fallbackNPC)]
	npc.Attackable, npc.UnitFlagsKnown, npc.PositionRevision = false, true, 5
	npc.X, npc.Y, npc.Z = 12, 10, 10
	npc.Flightmaster = &actionsv1.EncounterFlightmaster{FactionTemplateFriendly: true, StationaryPositionKnown: true, PositionAgeMs: 105000, ObservationAgeMs: 105000}
	return &fallbackPolicyTest{t: t, host: host}, o, string(source)
}

func flightpointDecision(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse, want string) behavior.Decision {
	t.Helper()
	d := p.decide(observeSparseEncounter(t, p, o))
	a, _ := d.Output["action"].(map[string]any)
	if want == "" && a != nil || want != "" && a["capability"] != want {
		t.Fatalf("wanted %q, got %v", want, d.Output)
	}
	return d
}

func settleFlightpointDecision(t *testing.T, p *fallbackPolicyTest, d behavior.Decision, outcome behavior.ActionOutcome, result map[string]any) {
	t.Helper()
	request, _ := d.Output["actionRequestId"].(string)
	a := d.Output["action"].(map[string]any)
	if request == "" {
		t.Fatal("missing selected request identity")
	}
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{"policy_request_id": request, "capability": a["capability"], "result": result}}})
	if err != nil {
		t.Fatal(err)
	}
}

func acquireFlightpoint(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse) {
	t.Helper()
	acquire := flightpointDecision(t, p, o, "acquire_idle_interaction")
	if acquire.Output["followFormation"] != nil {
		t.Fatal("competing movement", acquire.Output)
	}
	o.Interaction.Owner, o.Interaction.Token = "exploration", "owned-token"
	settleFlightpointDecision(t, p, acquire, behavior.ActionConfirmed, map[string]any{"state": map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "interactionOwner": "exploration"}, "token": o.Interaction.Token})
}

func TestOrdinaryFlightpointReconcilesOneOwnQueryAndRestoresFollow(t *testing.T) {
	for _, mode := range []string{"already_known", "learned", "learned_after_unknown", "menu_without_learning"} {
		t.Run(mode, func(t *testing.T) {
			p, o, source := flightpointEncounter(t)
			acquireFlightpoint(t, p, o)
			status := flightpointDecision(t, p, o, "query_flightpath")
			a := status.Output["action"].(map[string]any)
			if a["mode"] != "status" || a["expectedRevision"] != "0" || a["npcGuid"] != fallbackGUID(fallbackNPC) {
				t.Fatal(a)
			}
			settleFlightpointDecision(t, p, status, behavior.ActionSettledUnverified, nil)
			o.Flightpath.Revision = 1
			op := &actionsv1.FlightpathOperation{Revision: 1, SessionId: fallbackSession, SelfGuid: 4, VisitId: 1, NpcGuid: fallbackNPC, Mode: "status", Phase: "requested", TransportWritten: true, Pending: true}
			o.Flightpath.Operation = op
			flightpointDecision(t, p, o, "")
			op.Pending, op.Phase, op.StatusKnown, op.NodeKnown = false, "status_observed", true, mode == "already_known"
			if mode != "already_known" {
				learn := flightpointDecision(t, p, o, "query_flightpath")
				a = learn.Output["action"].(map[string]any)
				if a["mode"] != "discover" || a["expectedRevision"] != "1" {
					t.Fatal(a)
				}
				settleFlightpointDecision(t, p, learn, behavior.ActionSettledUnverified, nil)
				op.Revision, o.Flightpath.Revision = 2, 2
				op.Mode, op.Phase, op.Pending, op.StatusKnown = "discover", "requested", true, false
				if mode == "learned_after_unknown" {
					op.TransportWritten, op.TransportUnknown = false, true
				}
				op.LearningNoticeObserved = true
				flightpointDecision(t, p, o, "")
				if _, err := p.host.Reload(context.Background(), source+"\n-- retained exact flight query", p.host.Status().Revision); err != nil {
					t.Fatal(err)
				}
				flightpointDecision(t, p, o, "")
				op.Pending = false
				if mode == "menu_without_learning" {
					op.Phase, op.MenuKnown, op.SourceNode = "menu_observed", true, 6
				} else {
					op.Phase, op.Learned, op.StatusKnown, op.NodeKnown = "learned", true, true, true
					menu := flightpointDecision(t, p, o, "query_flightpath")
					if a := menu.Output["action"].(map[string]any); a["expectedRevision"] != "2" || a["mode"] != "discover" {
						t.Fatal(a)
					}
					settleFlightpointDecision(t, p, menu, behavior.ActionSettledUnverified, nil)
					op.Revision, o.Flightpath.Revision = 3, 3
					op.Phase, op.MenuKnown, op.SourceNode = "menu_observed", true, 6
				}
			}
			release := flightpointDecision(t, p, o, "release_interaction")
			if a := release.Output["action"].(map[string]any); a["token"] != o.Interaction.Token || a["owner"] != "exploration" {
				t.Fatal(a)
			}
			settleFlightpointDecision(t, p, release, behavior.ActionSettledUnverified, nil)
			o.Interaction = &actionsv1.InteractionObservation{}
			flightpointDecision(t, p, o, "")
			if _, err := p.host.Reload(context.Background(), source+"\n-- retained per-character result", p.host.Status().Revision); err != nil {
				t.Fatal(err)
			}
			flightpointDecision(t, p, o, "")
			ck, err := p.host.State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			known := ck.Checkpoint["data"].(map[string]any)["flightpoint"].(map[string]any)["data"].(map[string]any)["known"].(map[string]any)[fallbackGUID(fallbackNPC)]
			if (known != nil) != (mode != "menu_without_learning") || mode == "learned" && known != float64(6) || !o.Navigation.Following || o.Navigation.FollowRevision != 1 {
				t.Fatal(mode, known, o.Navigation)
			}
		})
	}
}

func TestOrdinaryFlightpointYieldsWhenTargetOrIntentChanges(t *testing.T) {
	for _, mode := range []string{"hostile", "unknown_movement", "foreign_owner", "owner_far", "default_owner_far", "default_owner_near", "independent", "combat", "stop", "despawn", "out_of_range", "bounded_walk"} {
		t.Run(mode, func(t *testing.T) {
			p, o, _ := flightpointEncounter(t)
			npc := o.Targets[fallbackGUID(fallbackNPC)]
			switch mode {
			case "hostile":
				npc.Flightmaster.FactionTemplateFriendly = false
			case "unknown_movement":
				npc.Flightmaster.StationaryPositionKnown = false
			case "foreign_owner":
				o.Interaction = &actionsv1.InteractionObservation{Owner: "trade", Token: "other"}
			case "owner_far":
				o.Players["3"].X = 60
			case "default_owner_far", "default_owner_near", "independent":
				o.Navigation.FollowFormationSupported = true
				o.Navigation.Following, o.Navigation.FollowRevision = false, 0
				o.Navigation.FollowOwner = o.Players["3"]
				if mode == "default_owner_far" {
					o.Players["3"].X = 60
					break
				}
				if mode == "independent" {
					o.Navigation.Independent = true
					o.Navigation.FollowOwner = nil
					o.Navigation.OwnerGuid = 0
				}
				acquireFlightpoint(t, p, o)
				d := flightpointDecision(t, p, o, "query_flightpath")
				target := d.Output["action"].(map[string]any)["opportunity"].(map[string]any)
				if mode == "independent" && target["followOwnerGuid"] != nil || mode == "default_owner_near" && target["followOwnerGuid"] != "3" {
					t.Fatal(target)
				}
				return
			case "combat":
				o.Players["4"].InCombat = true
			case "stop":
				o.Navigation.Following = false
			case "out_of_range":
				npc.X = 28
			case "despawn":
				acquireFlightpoint(t, p, o)
				o.Targets = map[string]*actionsv1.EncounterCreature{}
				o.Flightpath = nil
				flightpointDecision(t, p, o, "release_interaction")
				return
			case "bounded_walk":
				npc.X = 19
				acquireFlightpoint(t, p, o)
				d := flightpointDecision(t, p, o, "travel_to")
				a := d.Output["action"].(map[string]any)
				target := a["flightmaster"].(map[string]any)
				if target["guid"] != fallbackGUID(npc.Guid) || target["positionRevision"] != "5" || target["followRevision"] != "1" {
					t.Fatal(a)
				}
				return
			}
			flightpointDecision(t, p, o, "")
		})
	}
}

func TestCompanionMountsBeforeNewFlightpointAndCompletesExistingDiscovery(t *testing.T) {
	p, o, _ := flightpointEncounter(t)
	ride := &actionsv1.FollowMountObservation{SelfMounted: proto.Bool(false), OwnerMounted: proto.Bool(true), OwnerGroundSpell: 458, OwnerMountRevision: 1, Ready: true}
	o.Navigation.GroundMount = ride
	o.Spells["23238"] = &actionsv1.EncounterSpell{Known: true, Available: true, GroundMountSpeedPercent: 100}
	d := flightpointDecision(t, p, o, "cast_spell")
	if a := d.Output["action"].(map[string]any); a["spellId"] != float64(23238) || a["followMount"] == nil {
		t.Fatal(a)
	}
	ride.SelfMounted, ride.SelfGroundSpell = proto.Bool(true), 23238
	acquireFlightpoint(t, p, o)
	// A newly witnessed owner dismount cannot replace the flightmaster's
	// already acquired writer or the query it must reconcile.
	ride.OwnerMounted, ride.OwnerGroundSpell, ride.OwnerDismounted, ride.OwnerMountRevision = proto.Bool(false), 0, true, 2
	status := flightpointDecision(t, p, o, "query_flightpath")
	if a := status.Output["action"].(map[string]any); a["mode"] != "status" {
		t.Fatal(a)
	}
	settleFlightpointDecision(t, p, status, behavior.ActionSettledUnverified, nil)
	o.Flightpath.Revision = 1
	o.Flightpath.Operation = &actionsv1.FlightpathOperation{Revision: 1, SessionId: fallbackSession, SelfGuid: 4, VisitId: 1, NpcGuid: fallbackNPC, Mode: "status", Phase: "status_observed", TransportWritten: true, StatusKnown: true, NodeKnown: true}
	release := flightpointDecision(t, p, o, "release_interaction")
	settleFlightpointDecision(t, p, release, behavior.ActionSettledUnverified, nil)
	o.Interaction = &actionsv1.InteractionObservation{}
	flightpointDecision(t, p, o, "")
	flightpointDecision(t, p, o, "dismount_ground")
}
