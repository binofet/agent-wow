package world

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"os"
	"path/filepath"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestEncounterFollowLeavesWaterEntryToDestinationPlanner(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := fallbackObservation()
	self := o.Players[fallbackGUID(fallbackSelf)]
	self.MovementKnown, self.PositionKnown, self.PositionRevision = true, true, 1
	owner := fallbackPlayer(fallbackLeader)
	owner.MovementKnown, owner.PositionKnown, owner.Swimming = true, true, true
	owner.X, owner.Z = 10, -34
	o.Navigation = &actionsv1.NavigationObservation{Following: true, BodyMovementSupported: true, BodyLandingSupported: true, FollowRevision: 1, OwnerGuid: fallbackLeader, FollowOwner: owner}
	for _, phase := range []string{"swim_follow_route_unavailable", "follow_navigation_unavailable", "following_formation"} {
		o.Navigation.Phase = phase
		p.expectNoAction(p.observe(o))
	}
}

func TestFollowWaterHistoricalCheckpointAndLateReceipt(t *testing.T) {
	module, e := os.ReadFile(filepath.Join("..", "..", "behaviors", "encounter-movement.lua"))
	if e != nil {
		t.Fatal(e)
	}
	source := "local movement=(function()\n" + string(module) + waterCheckpointFixture
	h, e := behavior.NewSession(context.Background(), "water-history", source)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	for i := 0; i < 2; i++ {
		d, e := h.Decide(context.Background(), map[string]any{})
		if e != nil || d.Output["retained"] != true || d.Output["unrelated"] != true {
			t.Fatal(d, e)
		}
		if i == 0 {
			if _, e = h.Reload(context.Background(), source+"\n-- reload", h.Status().Revision); e != nil {
				t.Fatal(e)
			}
		}
	}
}

const waterCheckpointFixture = `
end)()
return {on_event=movement.on_event,export_state=function() return {version=1,data=movement.export_state()} end,import_state=function(s) return movement.import_state(s.data) end,decide=function(frame)
  local state=movement.export_state()
  if not state.waterProbe then movement.import_state({identity="old",waterProbe={epoch="s:4:1:2:1",sequence="8",geometry="prior"}}) end
  movement.on_event({receipt={outcome="unknown",payload={capability="move_body",sequence_id="8",reconciliationRequired=true}}})
  local after=movement.export_state()
  return {retained=after.waterProbe.epoch=="s:4:1:2:1" and after.waterProbe.geometry=="prior" and after.waterProbe.uncertain==true,unrelated=after.identity=="old"}
end}`

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
