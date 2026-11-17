package modrt

import (
	"context"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
)

type policyEncounterWorld struct{ policySpellWorld }

func (w policyEncounterWorld) Snapshot(ctx context.Context, req *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	view, err := w.policySpellWorld.Snapshot(ctx, req)
	view.VisitRevision, view.ObservedPackets = 1, 10
	return view, err
}

func TestPackagedActionsEncounterLeaseTracksObservedPolicy(t *testing.T) {
	runner := &policySpellRunner{world: policyEncounterWorld{}, hostOwnsEncounter: true}
	manager, ctx, writes := startPackagedPolicySpellWith(t, runner, policySheepID)
	observe := func() *actionsv1.EncounterObservationResponse {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, encounterObservationMethod, nil)
		if err != nil {
			t.Fatal(err)
		}
		view := &actionsv1.EncounterObservationResponse{}
		if err := protojson.Unmarshal(raw, view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
	initial := observe()
	if !initial.GetHostRequested() || initial.GetHostOwned() || initial.GetVisitId() != 1 {
		t.Fatal("profile opt-in or the diagnostic read activated the writer", initial)
	}
	source := fmt.Sprintf(`
local state = {}
return {
  on_event = function(event)
    if event.kind == "encounter_observation" then state.visit = event.value.visitId end
    return true
  end,
  decide = function(frame)
    local visit = state.visit
    if not visit or visit == "0" then return {} end
    local output = { encounterOwnership = {visitId = visit, observationSequence = frame.input.observationSequence} }
    if not state.proposed then
      state.proposed = true
      output.action = { capability = "polymorph", sessionId = frame.session_id, visitId = visit,
                        selfGuid = "%d", targetGuid = "%d", spellId = %d }
    end
    return output
  end,
  export_state = function() return {version = 1, data = state} end,
  import_state = function(checkpoint) state = checkpoint.data; return true end,
}`, policyMageGUID, policyWolfGUID, policySheepID)
	policyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := manager.StartObservedPolicy(policyCtx, source, "encounter="+encounterObservationMethod); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	policy := manager.policy
	manager.mu.Unlock()
	boot := policy.vm.Status()
	readPolicySpellCast(t, ctx, writes, modsession.WriteOrigin{SessionID: initial.GetSessionId(), Module: actionsModule,
		Writer: "lua", PolicyRevision: boot.Revision, Generation: boot.Epoch, ActionID: "1"})
	if view := observe(); !view.GetHostOwned() || view.GetVisitId() != initial.GetVisitId() {
		t.Fatal("the observed policy never acquired the current encounter", view)
	}
	inert := `local state = {}; return {
      decide = function() return {} end,
      on_event = function() return true end,
      export_state = function() return {version = 1, data = state} end,
      import_state = function(checkpoint) state = checkpoint.data; return true end,
    }`
	if _, err := policy.Reload(ctx, inert, boot.Revision); err != nil {
		t.Fatal(err)
	}
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for observe().GetHostOwned() {
		select {
		case <-ctx.Done():
			t.Fatal("attached policy without a current ownership claim retained the lease")
		case <-poll.C:
		}
	}
	select {
	case write := <-writes:
		t.Fatal("ownership change replayed a cast", write)
	default:
	}
}
