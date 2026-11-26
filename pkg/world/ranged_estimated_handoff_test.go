package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"strconv"
	"testing"
)

func TestRangedFirstNativeEstimateHandoffKeepsGroundCorrelation(t *testing.T) {
	for _, kind := range []string{"stopped", "spline", "triplet", "inferred", "moving", "motion_unknown", "foreign_owner", "wrong_self", "pending_lease", "unknown_step", "awaiting_pose", "old_visit", "wrong_completed_pose"} {
		t.Run(kind, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			self := o.Players["4"]
			delete(o.Players, "4")
			self.Guid = 5
			o.SelfGuid = 5
			o.Players["5"] = self
			o.Party.Group.Members[0].Guid = 5
			self.PositionKnown = true
			self.PositionEstimated = true
			self.PositionInferred = false
			self.MovementKnown = true
			self.Moving = false
			self.CombatKnown = true
			self.InCombat = true
			o.Navigation = &actionsv1.NavigationObservation{BodyMovementSupported: true, MovementOwner: &actionsv1.MovementOwnerState{}}
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.X = self.X + 3
			n.Y = self.Y
			n.Z = self.Z
			if kind == "spline" {
				n.X = self.X + 30
				n.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 1000, DestinationKnown: true, DestinationX: self.X + 3, DestinationY: self.Y, DestinationZ: self.Z}
			}
			if kind == "triplet" {
				for i := uint64(1); i < 3; i++ {
					v := proto.Clone(n).(*actionsv1.EncounterCreature)
					v.Guid += i
					v.X += float32(i)
					o.Targets[fallbackGUID(v.Guid)] = v
				}
			}
			d := p.decide(observeRanged(t, p, o, false))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
				t.Fatal("handoff did not first acquire", d.Output)
			}
			id, e := p.host.ReserveAction(d)
			if e != nil {
				t.Fatal(e)
			}
			if kind != "pending_lease" {
				owner := "ranged:maraudon_sludge_backline"
				_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "rook-current", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": "5", "movementOwner": map[string]any{"active": true, "owner": owner}}}}}})
				if e != nil {
					t.Fatal(e)
				}
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner}
			}
			switch kind {
			case "inferred":
				self.PositionInferred = true
			case "moving":
				self.Moving = true
			case "motion_unknown":
				o.Navigation.MotionUnconfirmed = true
			case "foreign_owner":
				o.Navigation.MovementOwner.Owner = "foreign"
			case "wrong_self":
				self.Guid = 6
			}
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			switch kind {
			case "inferred", "moving", "motion_unknown", "foreign_owner", "wrong_self", "pending_lease":
				if a["capability"] == "move_body" {
					t.Fatal("unproven handoff entered input", d.Output)
				}
				return
			}
			if a["capability"] != "move_body" {
				t.Fatal("stationary native own estimate could not start withdrawal", d.Output)
			}
			status, _ := d.Output["rangedStance"].(map[string]any)
			if status["poseSource"] != "native_self_estimate" {
				t.Fatal("estimate provenance missing", d.Output)
			}
			id, e = p.host.ReserveAction(d)
			if e != nil {
				t.Fatal(e)
			}
			outcome := behavior.ActionSettledUnverified
			if kind == "unknown_step" {
				outcome = behavior.ActionUnknown
			}
			_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{"capability": "move_body", "self_guid": "5", "visit_id": "1", "sequence_id": a["sequenceId"], "phase": "body_input_written", "transport_written": true}}})
			if e != nil {
				t.Fatal(e)
			}
			self.X -= 1.4
			seq, e := strconv.ParseUint(a["sequenceId"].(string), 10, 64)
			if e != nil {
				t.Fatal(e)
			}
			if kind != "awaiting_pose" {
				o.Navigation.BodyMovement = &actionsv1.BodyMovementState{SequenceId: seq, VisitId: 1, Phase: "body_input_written", TransportWritten: true, PositionEstimated: true, X: self.X, Y: self.Y, Z: self.Z}
			}
			if kind == "old_visit" {
				o.VisitId = 2
			}
			if kind == "wrong_completed_pose" {
				o.Navigation.BodyMovement.X += 10
			}
			if _, e = p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- retained handoff", p.host.Status().Revision); e != nil {
				t.Fatal(e)
			}
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if kind == "stopped" || kind == "spline" || kind == "triplet" {
				if a["capability"] != "move_body" {
					t.Fatal("correlated second step lost", d.Output)
				}
			} else if a["capability"] == "move_body" {
				t.Fatal("first-input exception bypassed old ground custody", d.Output)
			}
		})
	}
}
