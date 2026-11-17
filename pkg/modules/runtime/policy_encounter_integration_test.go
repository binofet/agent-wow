package modrt

import (
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
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

type packagedEncounterCombatWorld struct {
	packagedHostileWorld
}

func (w *packagedEncounterCombatWorld) Snapshot(ctx context.Context, req *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	view, err := w.packagedHostileWorld.Snapshot(ctx, &worldv1.SnapshotRequest{CreatureGuid: policyWolfGUID})
	if err != nil {
		return nil, err
	}
	view.VisitRevision, view.ObservedPackets = 1, 10
	view.RequestedCreature.DynamicFlagsKnown = true
	view.NearbyCreatures = []*worldv1.CreatureState{view.RequestedCreature}
	if req.GetCreatureGuid() != policyWolfGUID {
		view.RequestedCreature = nil
	}
	return view, nil
}

func TestPackagedActionsEncounterBundleDefendsWithoutMarks(t *testing.T) {
	world := &packagedEncounterCombatWorld{}
	runner := &policySpellRunner{world: world, hostOwnsEncounter: true}
	manager, ctx, writes := startPackagedPolicySpellWith(t, runner, fixtureSmite)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLoginVerifyWorld, make([]byte, 20))

	source, err := exec.CommandContext(ctx, "../../../dev/encounter-bundle").CombinedOutput()
	if err != nil {
		t.Fatalf("assemble encounter policy: %s %v", source, err)
	}
	policyCtx, stop := context.WithCancel(ctx)
	defer stop()
	if err := manager.StartObservedPolicy(policyCtx, string(source), "encounter="+encounterObservationMethod); err != nil {
		t.Fatal(err)
	}
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		raw, err := manager.InvokeJSON(ctx, encounterObservationMethod, nil)
		view := &actionsv1.EncounterObservationResponse{}
		if err != nil || protojson.Unmarshal(raw, view) != nil {
			t.Fatalf("observe native encounter: %s %v", raw, err)
		}
		if view.GetHostOwned() {
			break
		}
		select {
		case unexpected := <-writes:
			t.Fatalf("unengaged creature provoked a write: %+v", unexpected)
		case <-ctx.Done():
			t.Fatal("actual encounter bundle did not claim the playable visit")
		case <-poll.C:
		}
	}
	world.attacker.Store(true)
	manager.mu.Lock()
	policy := manager.policy
	manager.mu.Unlock()
	boot := policy.vm.Status()
	expected := binary.LittleEndian.AppendUint32([]byte{1}, fixtureSmite)
	expected = append(expected, 0, 2, 0, 0, 0)
	expected = append(expected, spellFixturePackedGUID(policyWolfGUID)...)
	cast := checkPackagedWrite(t, ctx, writes, opcode.CMSGCastSpell, expected, modsession.WriteOrigin{
		SessionID: boot.SessionID, Module: actionsModule, Writer: "lua",
		PolicyRevision: boot.Revision, Generation: boot.Epoch, ActionID: "1",
	})
	stop()
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellGo, fixtureHostileGo(cast.body[0], fixtureSmite))
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellnonmeleedamagelog, fixtureHostileDamage(policyWolfGUID))
	if err := policy.vm.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if op := packagedHostileEffect(t, ctx, manager); !op.GetEffectObserved() || op.GetTargetGuid() != policyWolfGUID ||
		policy.vm.Status().Pending != 0 {
		t.Fatalf("the selected attacker's damage did not settle the Lua action: %v", op)
	}
	noPackagedWrites(t, writes)
}
