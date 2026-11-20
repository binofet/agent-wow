package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
)

const policyEvocation = uint32(12051)

type policySelfManaWorld struct {
	policySpellWorld
}

func (w policySelfManaWorld) Snapshot(ctx context.Context, req *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	view, err := w.policySpellWorld.Snapshot(ctx, req)
	view.Self.Power, view.Self.ChannelSpellKnown = 40, true
	return view, err
}

func evocationChannel(actor uint64, values ...uint32) []byte {
	body := spellFixturePackedGUID(actor)
	for _, value := range values {
		body = binary.LittleEndian.AppendUint32(body, value)
	}
	return body
}

func evocationSpellGo(count byte) []byte {
	caster := spellFixturePackedGUID(policyMageGUID)
	body := append(caster, caster...)
	body = binary.LittleEndian.AppendUint32(append(body, count), policyEvocation)
	body = binary.LittleEndian.AppendUint32(body, 0x100)
	body = binary.LittleEndian.AppendUint32(body, 0)
	body = binary.LittleEndian.AppendUint64(append(body, 1), policyMageGUID)
	return binary.LittleEndian.AppendUint32(append(body, 0), 0)
}

func TestPackagedActionsPolicyEvocation(t *testing.T) {
	runner := &policySpellRunner{world: policySelfManaWorld{}, hostOwnsSelfMana: true}
	manager, ctx, writes := startPackagedPolicySpellWith(t, runner, policyEvocation)
	observe := func() *actionsv1.SelfManaObservationResponse {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "buddy-actions.self_mana_observation", nil)
		if err != nil {
			t.Fatal(err)
		}
		view := &actionsv1.SelfManaObservationResponse{}
		if err := protojson.Unmarshal(raw, view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	native := func() *actionsv1.State {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "buddy-actions.status", nil)
		if err != nil {
			t.Fatal(err)
		}
		state := &actionsv1.State{}
		if err := protojson.Unmarshal(raw, state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	initial := observe()
	if !initial.GetHostOwned() || !initial.GetSpellbookKnown() || initial.GetSelfGuid() != policyMageGUID ||
		initial.GetCaster().GetPower() != 40 || initial.GetCaster().GetMaxPower() != 128 ||
		!initial.GetCaster().GetChannelKnown() || !initial.GetActivity().GetIdle() || !initial.GetThreat().GetKnown() ||
		len(initial.GetSpells()) != 1 || initial.Spells[0].GetSpellId() != policyEvocation ||
		!initial.Spells[0].GetSelfCastSupported() || !initial.Spells[0].GetChanneled() ||
		!initial.Spells[0].GetReadyKnown() || !initial.Spells[0].GetReady() {
		t.Fatalf("reflected observation did not expose a ready, low-mana mage: %v", initial)
	}
	if err := manager.StartObservedPolicy(ctx, packagedObservedPolicy(t, "self-mana.lua", "before"),
		"self_mana=buddy-actions.self_mana_observation"); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	policy := manager.policy
	manager.mu.Unlock()
	before := policy.vm.Status()
	var cast policySpellWrite
	select {
	case cast = <-writes:
	case <-ctx.Done():
		t.Fatal("no Evocation reached the offline transport")
	}
	want := binary.LittleEndian.AppendUint32([]byte{1}, policyEvocation)
	want = append(append(want, 0, 2, 0, 0, 0), spellFixturePackedGUID(policyMageGUID)...)
	origin := modsession.WriteOrigin{SessionID: initial.GetSessionId(), Module: "buddy-actions", Writer: "lua",
		PolicyRevision: before.Revision, Generation: before.Epoch, ActionID: "1"}
	if cast.opcode != opcode.CMSGCastSpell || !bytes.Equal(cast.body, want) || cast.origin != origin {
		t.Fatalf("self cast = %#x %x %+v, want %#x %x %+v", cast.opcode, cast.body, cast.origin,
			opcode.CMSGCastSpell, want, origin)
	}
	waitForPackagedSpellTransport(t, ctx, policy)
	if state := native(); !state.GetCastPending() || !state.GetLastSpellOperation().GetTransportWritten() ||
		state.GetLastSpellOperation().GetServerCastGo() || state.GetLastSpellOperation().GetEffectObserved() {
		t.Fatalf("cast should be in flight after transport: %v", state.GetLastSpellOperation())
	}

	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellGo, evocationSpellGo(cast.body[0]))
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelStart, evocationChannel(policyMageGUID+1, policyEvocation, 8000))
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelStart, evocationChannel(policyMageGUID, policySheepID, 8000))
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelUpdate, evocationChannel(policyMageGUID, 0))
	if state := native(); !state.GetCastPending() || !state.GetLastSpellOperation().GetServerCastGo() ||
		state.GetLastSpellOperation().GetChannelStarted() || state.GetLastSpellOperation().GetEffectObserved() {
		t.Fatalf("foreign or unmatched channel packets changed the in-flight cast: %v", state.GetLastSpellOperation())
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelStart, evocationChannel(policyMageGUID, policyEvocation, 8000))
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelUpdate, evocationChannel(policyMageGUID, 2000))
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelUpdate, evocationChannel(policyMageGUID+1, 0))
	if state := native(); !state.GetCastPending() || state.GetLastSpellOperation().GetPhase() != "channeling" ||
		!state.GetLastSpellOperation().GetChannelStarted() || state.GetLastSpellOperation().GetChannelRemainingMs() != 2000 {
		t.Fatalf("matching live channel lost cast custody: %v", state.GetLastSpellOperation())
	}

	revision, err := policy.Reload(ctx, packagedObservedPolicy(t, "self-mana.lua", "after"), before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if after := policy.vm.Status(); after.Revision != revision || after.Revision == before.Revision ||
		after.Epoch == before.Epoch || after.SessionID != before.SessionID || after.Pending != 1 {
		t.Fatalf("reload lost the in-flight channel: %+v", after)
	}
	readReceipts := func(results string, pending int) map[string]any {
		t.Helper()
		out, extra, err := policy.Step(ctx, map[string]any{"fixtureRead": true}, nil)
		if err != nil || extra != nil || out["results"] != results || out["revisionLabel"] != "after" ||
			policy.vm.Status().Pending != pending {
			t.Fatalf("Lua self-mana readback = %+v, %+v, %v, status %+v", out, extra, err, policy.vm.Status())
		}
		last, _ := out["last"].(map[string]any)
		return last
	}
	const transport = "/transport_accepted:1"
	readReceipts(transport, 1)
	end := evocationChannel(policyMageGUID, 0)
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelUpdate, end)
	if state := native(); state.GetCastPending() || !state.GetLastSpellOperation().GetChannelEnded() ||
		state.GetLastSpellOperation().GetEffectObserved() || state.GetLastSpellOperation().GetPhase() != "channel_ended_effect_unverified" {
		t.Fatalf("channel end failed to clear cast custody or falsely verified mana gain: %v", state.GetLastSpellOperation())
	}
	const settled = transport + "/settled_unverified:1"
	effect := readReceipts(settled, 0)
	if effect["capability"] != "cast_self" || effect["spell_id"] != float64(policyEvocation) ||
		effect["target_guid"] != fmt.Sprint(policyMageGUID) || effect["phase"] != "channel_ended_effect_unverified" ||
		effect["transport_written"] != true || effect["server_cast_go"] != true || effect["effect_observed"] != false {
		t.Fatalf("Lua did not receive the typed unverified end: %+v", effect)
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.MSGChannelUpdate, end)
	after := observe()
	if after.GetCaster().GetPower() != 40 || len(after.GetSpells()) != 1 ||
		!after.Spells[0].GetReadyKnown() || after.Spells[0].GetReady() {
		t.Fatalf("reflected mana should stay low with Evocation still on cooldown: %v", after)
	}

	// Observe another real driver tick with the ledger empty and mana still low.
	cursor := policy.vm.Status().Cursor
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for policy.vm.Status().Cursor == cursor {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("reflected policy driver did not tick after settlement")
		}
	}
	readReceipts(settled, 0)
	select {
	case extra := <-writes:
		t.Fatalf("unexpected repeated world write: %+v", extra)
	default:
	}
}
