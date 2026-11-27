package buddyactions

import (
	"context"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func goToCommand(p *instanceEntryPolicyFixture, dx float32) map[string]any {
	w := p.f.world.Self
	return map[string]any{"task": "go_to", "id": "trip-1", "mapId": w.MapId, "x": w.X + dx, "y": w.Y, "z": w.Z, "arrivalDistance": 1.5}
}

func TestGoToCommandUsesCurrentBodyAndRetainedTravel(t *testing.T) {
	p := newInstanceEntryPolicyFixture(t)
	cmd := goToCommand(p, 350)
	goal := buddynav.Point{X: cmd["x"].(float32), Y: cmd["y"].(float32), Z: cmd["z"].(float32)}
	strip := &straightTripSurface{goal: goal}
	searches := 0
	p.s.openTravel = func(_ context.Context, _ string, _ uint32, _, dst buddynav.Point) (travelSurface, error) {
		searches++
		if dst != goal {
			t.Fatal("changed requested destination", dst)
		}
		return strip, nil
	}
	p.s.approachDelay = func(ctx context.Context, d time.Duration) error { p.advance(d); return ctx.Err() }
	p.command(cmd)
	p.dispatch("instance_entry_status")
	p.dispatch("acquire_idle_interaction")
	first := p.dispatch("travel_to").(*actionsv1.State)
	if first.Reason != "exploration_ground_progress_checkpoint" || first.Position == nil || first.MotionUnconfirmed || !first.Position.Estimated {
		t.Fatal("expected a bounded settled intermediate position", first.Reason, first.Position, p.s.state.Reason)
	}
	p.dispatch("instance_entry_status")
	p.dispatch("acquire_idle_interaction")
	travel := p.dispatch("travel_to").(*actionsv1.State)
	if travel.Reason != "exploration_within_distance" || travel.MotionUnconfirmed || travel.Moving || searches != 1 {
		t.Fatal("unexpected final route", travel.Reason, searches)
	}
	p.dispatch("instance_entry_status")
	out := p.step()
	if out["task"].(map[string]any)["phase"] != "settled_at_destination" {
		t.Fatal(out)
	}
	p.dispatch("release_interaction")
	out = p.step()
	last := out["task"].(map[string]any)
	pose, _ := last["position"].(map[string]any)
	if pose["estimated"] != true || last["reason"] != "destination_at_client_estimate" || p.currentTask() != nil ||
		p.s.interactionToken != "" || countPackets(p.f, opcode.MSGMoveStartForward) != 2 || countPackets(p.f, opcode.MSGMoveStop) != 2 || countPackets(p.f, opcode.CMSGAreatrigger) != 0 {
		t.Fatal("did not release movement or preserve the actual source of the settled position", last, p.s.state.Reason, p.methods)
	}
	p.command(cmd)
	if out := p.step(); out["action"] != nil || p.currentTask() != nil {
		t.Fatal("replayed a completed request", out)
	}
}
func TestGoToRefusalCombatAndUncertainDelivery(t *testing.T) {
	for _, test := range []string{"other map", "missing coordinate", "foreign visit", "combat", "no route", "uncertain"} {
		t.Run(test, func(t *testing.T) {
			p := newInstanceEntryPolicyFixture(t)
			cmd := goToCommand(p, 15)
			switch test {
			case "other map":
				cmd["mapId"] = p.f.world.Self.MapId + 1
			case "missing coordinate":
				delete(cmd, "z")
			case "foreign visit":
				cmd["visitId"] = "99"
			}
			p.command(cmd)
			if test == "other map" || test == "missing coordinate" || test == "foreign visit" {
				if out := p.step(); out["task"].(map[string]any)["phase"] != "invalid_go_to_intent" || out["action"] != nil || p.currentTask() != nil {
					t.Fatal(out)
				}
				return
			}
			p.dispatch("instance_entry_status")
			p.dispatch("acquire_idle_interaction")
			if test == "combat" {
				p.f.world.Self.UnitFlags |= 0x80000
				p.dispatch("instance_entry_status")
				p.dispatch("release_interaction")
				if out := p.step(); out["task"].(map[string]any)["phase"] != "waiting_for_current_encounter" {
					t.Fatal(out)
				}
				p.f.world.Self.UnitFlags &^= 0x80000
				p.dispatch("instance_entry_status")
				p.dispatch("acquire_idle_interaction")
				p.action("travel_to")
				return
			}
			action := p.action("travel_to")
			id := p.reserve()
			if test == "uncertain" {
				p.receipt(id, action, nil, true)
				out := p.step()
				before, err := p.vm.State(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err = p.vm.Reload(t.Context(), p.source, before.Status.Revision); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if out["action"] != nil || out["task"].(map[string]any)["phase"] != "reconcile_travel_to" {
						t.Fatal(out)
					}
					out = p.step()
				}
				return
			}
			p.s.openTravel = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (travelSurface, error) {
				return nil, errors.New("no mesh route")
			}
			resp := p.native(action).(*actionsv1.State)
			if resp.Reason != "exploration_navigation_unavailable" {
				t.Fatal(resp.Reason)
			}
			p.receipt(id, action, resp, false)
			p.dispatch("instance_entry_status")
			p.dispatch("release_interaction")
			out := p.step()
			if out["task"].(map[string]any)["reason"] != resp.Reason || len(p.f.packets) != 0 || p.currentTask() != nil {
				t.Fatal("replayed or misstated failed travel", out, p.methods)
			}
		})
	}
}

func TestGoToRequiresCurrentPositionThroughRelease(t *testing.T) {
	p := newInstanceEntryPolicyFixture(t)
	p.command(goToCommand(p, 0))
	p.dispatch("instance_entry_status")
	out := p.step()
	if out["task"].(map[string]any)["phase"] != "settled_at_destination" {
		t.Fatal(out)
	}
	p.f.world.Self.X += 15
	p.f.world.Self.PositionRevision++
	p.dispatch("instance_entry_status")
	if p.currentTask()["arrival"] != nil {
		t.Fatal("older arrival survived a new current position", p.currentTask())
	}
	p.dispatch("acquire_idle_interaction")
	p.command(map[string]any{"task": "cancel_task", "id": "trip-1"})
	p.dispatch("instance_entry_status")
	p.dispatch("release_interaction")
	out = p.step()
	if task := out["task"].(map[string]any); task["reason"] != "canceled" || task["position"] != nil || len(p.f.packets) != 0 {
		t.Fatal(out)
	}
}
