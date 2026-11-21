package modrt

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func controlBreakRuntimeSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "control-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local policy=(function()\n" + string(b) + "\nend)()\n" + `local obs
 return {on_event=function(e)if e.kind=='encounter_observation' then obs=e.value end return policy.on_event(e) end,
 decide=function(frame)return {encounterOwnership={visitId=obs.visitId,observationSequence=frame.input.observationSequence},action=policy.decide(frame,obs)} end,
 export_state=function()return {version=1,data={obs=obs,control=policy.export_state()}}end,
 import_state=function(c)obs=c.data.obs;return policy.import_state(c.data.control)end}`
}
func runtimeControlFear(t *testing.T, native *buddyactions.Service, spell uint32) {
	t.Helper()
	p := append(spellFixturePackedGUID(friendlyEncounterSelf), 5)
	p = binary.LittleEndian.AppendUint32(p, spell)
	if spell != 0 {
		p = append(p, 0x89, 40, 1)
	} // exact negative self aura, unknown duration
	if _, err := native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: p}); err != nil {
		t.Fatal(err)
	}
}
func runtimeControlBook(t *testing.T, native *buddyactions.Service, ids ...uint32) {
	t.Helper()
	p := []byte{0, byte(len(ids)), 0}
	for _, id := range ids {
		p = binary.LittleEndian.AppendUint32(p, id)
		p = append(p, 0, 0)
	}
	p = append(p, 0, 0)
	if _, err := native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: p}); err != nil {
		t.Fatal(err)
	}
}
func controlBreakStep(t *testing.T, m *Manager, p *SessionPolicy) (*behavior.ActionReceipt, map[string]any) {
	t.Helper()
	ctx, seq := observeFriendlyEncounter(t, m, p)
	out, r, err := p.Step(ctx, map[string]any{"observationSequence": seq}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r, out
}
func TestPolicyControlBreakRealOwnedNativeSelectedRankAndUnknownReload(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	runtimeControlBook(t, native, 59752, 18499)
	cb.world.Self.UnitFlags |= 0x800000
	runtimeControlFear(t, native, 7399)
	source := controlBreakRuntimeSource(t)
	// A same-session edit chooses the longer racial over the default short-CD
	// learned ability. No Go selector may replace the Lua-selected rank.
	source = strings.Replace(source, "spell_weights={}", "spell_weights={[59752]=10}", 1)
	p, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	send := m.session.SendPacket
	m.session.SendPacket = func(ctx context.Context, code uint32, body []byte) error {
		send(ctx, code, body)
		return errors.New("actual control-break transport unknown")
	}
	r, out := controlBreakStep(t, m, p)
	if r == nil || r.Outcome != behavior.ActionUnknown || r.Payload["spell_id"] != uint32(59752) || len(cb.writes) != 1 || cb.writes[0].Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(cb.writes[0].Payload[1:]) != 59752 {
		t.Fatal("actual owned Lua selection did not reach exact native", r, out, cb.writes)
	}
	if r.Payload["effect_observed"] != false || r.Payload["aura_revision"] != "1" {
		t.Fatal(r)
	}
	if _, err := p.vm.Reload(context.Background(), source+"\n-- keep unknown fear custody", p.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if r, out := controlBreakStep(t, m, p); r != nil || len(cb.writes) != 1 || p.vm.Status().Pending != 1 {
		t.Fatal("unknown write replayed", r, out, p.vm.Status())
	}
	// Actual aura removal retires the action without claiming our cast succeeded.
	runtimeControlFear(t, native, 0)
	cb.world.Self.UnitFlags &^= 0x800000
	if r, out := controlBreakStep(t, m, p); r != nil || len(cb.writes) != 1 || p.vm.Status().Pending != 0 {
		t.Fatal("actual control clear left global action pending", r, out, p.vm.Status())
	}
}
func TestPolicyControlBreakKnownNoWriteCanRetrySameApplication(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	runtimeControlBook(t, native, 59752)
	cb.world.Self.UnitFlags |= 0x800000
	runtimeControlFear(t, native, 7399)
	p, err := m.StartPolicy(context.Background(), controlBreakRuntimeSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	cb.reject = true
	if r, out := controlBreakStep(t, m, p); r == nil || r.Outcome != behavior.ActionNotDispatched || len(cb.writes) != 0 {
		t.Fatal(r, out, cb.writes)
	}
	cb.reject = false
	if r, out := controlBreakStep(t, m, p); r == nil || r.Outcome != behavior.ActionTransportAccepted || len(cb.writes) != 1 {
		t.Fatal("typed core refusal consumed exact fear source", r, out, cb.writes)
	}
}
func TestPolicyControlBreakOwnedTrinketExactPacketAndForeignVisit(t *testing.T) {
	for _, mode := range []string{"valid", "foreign_key", "stale_visit", "stale_aura"} {
		t.Run(mode, func(t *testing.T) {
			m, native, cb := friendlyEncounterManager(t)
			cb.world.Self.UnitFlags |= 0x800000
			runtimeControlFear(t, native, 7399)
			cb.world.SelfEquipmentKnown = true
			cb.world.SelfEquipment = []*worldv1.EquipmentSlotState{{Slot: 14, SlotKnown: true, MetadataKnown: true, ItemGuid: 0x4000000000000059, Entry: 18854, StackCount: 1}}
			p, err := m.StartPolicy(context.Background(), controlBreakRuntimeSource(t))
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			ctx, seq := observeFriendlyEncounter(t, m, p)
			switch mode {
			case "foreign_key":
				m.instances[actionsModule].launch.Environment[modcb.PolicyEncounterKeyEnv] = strings.Repeat("b", 64)
			case "stale_visit":
				cb.world.VisitRevision = 2
			case "stale_aura":
				runtimeControlFear(t, native, 7399)
			}
			_, r, err := p.Step(ctx, map[string]any{"observationSequence": seq}, nil)
			if mode != "valid" {
				if len(cb.writes) != 0 || err == nil && (r == nil || r.Outcome != behavior.ActionNotDispatched) {
					t.Fatal("foreign/stale control authority wrote", mode, r, err, cb.writes)
				}
				return
			}
			if err != nil || r == nil || r.Outcome != behavior.ActionTransportAccepted || len(cb.writes) != 1 {
				t.Fatal(r, err, cb.writes)
			}
			packet := cb.writes[0]
			if packet.Opcode != opcode.CMSGUseItem || len(packet.Payload) != 24 || packet.Payload[0] != 255 || packet.Payload[1] != 14 || binary.LittleEndian.Uint32(packet.Payload[3:]) != 42292 || binary.LittleEndian.Uint64(packet.Payload[7:]) != 0x4000000000000059 {
				t.Fatal(packet)
			}
			op := &policySpell{capability: "fear_break", spell: 42292, target: friendlyEncounterSelf, controlItem: 0x4000000000000059, controlSlot: 14}
			if !op.matchesActionPacket(packet.Opcode, packet.Payload) {
				t.Fatal("exact item packet refused")
			}
			for _, offset := range []int{0, 1, 3, 7, 15, 23} {
				bad := append([]byte(nil), packet.Payload...)
				bad[offset]++
				if op.matchesActionPacket(packet.Opcode, bad) {
					t.Fatal("different item/slot/spell/target header admitted", offset)
				}
			}
		})
	}
}
