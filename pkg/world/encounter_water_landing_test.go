package world

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestEncounterFollowSelectsMeasuredWaterInput(t *testing.T) {
	for _, mode := range []string{"swimmer", "lower_shore", "inferred", "unseen", "foreign_mover", "disabled", "uncertain", "known_refusal", "unknown_receipt"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := fallbackObservation()
			self := o.Players[fallbackGUID(fallbackSelf)]
			self.MovementKnown, self.PositionKnown, self.PositionRevision = true, true, 1
			owner := fallbackPlayer(fallbackLeader)
			owner.MovementKnown, owner.PositionKnown, owner.Swimming = true, true, true
			owner.X, owner.Z = 10, -34
			n := &actionsv1.NavigationObservation{Following: true, BodyMovementSupported: true, BodyLandingSupported: true, FollowRevision: 1, OwnerGuid: fallbackLeader, FollowOwner: owner, Phase: "swim_follow_route_unavailable"}
			o.Navigation = n
			switch mode {
			case "inferred":
				owner.PositionInferred = true
			case "unseen":
				owner.Present = false
			case "foreign_mover":
				n.MovementOwner = &actionsv1.MovementOwnerState{Active: true}
			case "disabled":
				n.BodyLandingSupported = false
			case "uncertain":
				n.MotionUnconfirmed = true
			case "lower_shore":
				owner.Swimming = false
				n.Phase = "follow_enabled_waiting_for_safe_observations"
			}
			seq := p.observe(o)
			decision := p.decide(seq)
			action, _ := decision.Output["action"].(map[string]any)
			switch mode {
			case "inferred", "unseen", "foreign_mover", "disabled", "uncertain":
				if action != nil {
					t.Fatal("did not yield", action)
				}
				return
			}
			if action["capability"] != "move_body" || action["landing"] != "follow_water" || action["durationMs"] != float64(500) || action["sequenceId"] != seq {
				t.Fatal("normal follow did not propose a measured crossing", decision.Output)
			}
			if mode != "known_refusal" && mode != "unknown_receipt" {
				return
			}
			id, err := p.host.ReserveAction(decision)
			if err != nil {
				t.Fatal(err)
			}
			outcome := behavior.ActionNotDispatched
			payload := map[string]any{"capability": "move_body", "phase": "local_physics_refused", "sequence_id": seq}
			if mode == "unknown_receipt" {
				outcome = behavior.ActionSettledUnverified
				payload["phase"] = "transport_unconfirmed_no_retry"
				payload["reconciliationRequired"] = true
			}
			_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: payload}})
			if err != nil {
				t.Fatal(err)
			}
			p.expectNoAction(p.observe(o))
			if _, err := p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- retain crossing", p.host.Status().Revision); err != nil {
				t.Fatal(err)
			}
			p.expectNoAction(p.observe(o))
			owner.X++
			if mode == "unknown_receipt" {
				p.expectNoAction(p.observe(o))
				self.PositionRevision++
			}
			next, _ := p.decide(p.observe(o)).Output["action"].(map[string]any)
			if next["landing"] != "follow_water" {
				t.Fatal("fresh corrected geometry did not regain normal follow", next)
			}
		})
	}
}

func TestScopedWaterStopRequiresExactKnownEntry(t *testing.T) {
	e := scopedBodyEngine(t, &scopeWriteConn{})
	wet := func(forward bool, z float32) []byte {
		i := movementinfo.Info{Flags: movementinfo.SwimmingFlag, PitchPresent: true, Time: 19, X: 1, Y: 2, Z: z}
		if forward {
			i.Flags |= 1
		}
		b, err := movementinfo.Encode(i)
		if err != nil {
			t.Fatal(err)
		}
		return append([]byte{3, 99, 0}, b...)
	}
	stop := func(z float32) error {
		_, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, wet(false, z), e.transport.snapshot())
		return err
	}
	if err := stop(-30); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("water STOP without current water entry", err)
	}
	if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStartSwim, wet(true, -30), e.transport.snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := stop(-32); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("changed water STOP pose", err)
	}
	// Even a rejected subsequent movement retires the previous exposed own
	// report. Exercise the positive transition in a new synthetic entry.
	e = scopedBodyEngine(t, &scopeWriteConn{})
	if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStartSwim, wet(true, -30), e.transport.snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := stop(-30); err != nil {
		t.Fatal("current stationary wet STOP", err)
	}
	if e.s.OwnMovementReport().GetOpcode() != opcode.MSGMoveStop {
		t.Fatal("lost exact terminal report")
	}
	if err := stop(-30); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("water STOP replay", err)
	}
}
