package modrt

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcast"
	"google.golang.org/protobuf/types/known/emptypb"
)

func genericRuntimeSource(spell uint32, extra string) string {
	return fmt.Sprintf(`local obs; local issued=false; local selected=%d
return {on_event=function(e)if e.kind=='encounter_observation' then obs=e.value end return true end,
 decide=function(f)if issued then return {} end issued=true
 return {encounterOwnership={visitId=obs.visitId,observationSequence=f.input.observationSequence},
 action={capability='cast_spell',sessionId=f.session_id,selfGuid='22',visitId=obs.visitId,spellId=selected%s}}end,
 export_state=function()return {version=1,data={obs=obs,issued=issued,selected=selected}}end,
 import_state=function(c)obs=c.data.obs;issued=c.data.selected==selected and c.data.issued or false;return true end}`, spell, extra)
}
func runtimeGenericGo(native *buddyactions.Service, count byte, id uint32, channel bool) {
	p := append(spellFixturePackedGUID(friendlyEncounterSelf), spellFixturePackedGUID(friendlyEncounterSelf)...)
	p = append(p, count)
	p = binary.LittleEndian.AppendUint32(p, id)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = append(p, 0, 0)
	p = binary.LittleEndian.AppendUint32(p, 0)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: p})
	if channel {
		start := spellFixturePackedGUID(friendlyEncounterSelf)
		start = binary.LittleEndian.AppendUint32(start, id)
		start = binary.LittleEndian.AppendUint32(start, 8000)
		native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.MSGChannelStart, Payload: start})
		end := append(spellFixturePackedGUID(friendlyEncounterSelf), 0, 0, 0, 0)
		native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.MSGChannelUpdate, Payload: end})
	}
}

func TestPolicyGenericSpellLearnsAndCastsGroundImplicitAndUnknownInSameBody(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	p, err := m.StartPolicy(context.Background(), genericRuntimeSource(2120, ",targets={mask=64,destination={x=1,y=2,z=3}}"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	for i, id := range []uint32{2120, 10, 1449, 4000000000} {
		extra := ""
		if i < 2 {
			extra = ",targets={mask=64,destination={x=1,y=2,z=3}}"
		}
		if i > 0 {
			if _, err = p.Reload(context.Background(), genericRuntimeSource(id, extra)+strings.Repeat(" ", i), p.vm.Status().Revision); err != nil {
				t.Fatal(err)
			}
		}
		// New IDs arrive from real module packet ingestion after the SAME VM and
		// native service are running; no catalog reload or native restart occurs.
		learned := binary.LittleEndian.AppendUint32(nil, id)
		learned = append(learned, 0, 0)
		if _, err = native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLearnedSpell, Payload: learned}); err != nil {
			t.Fatal(err)
		}
		r, out := controlBreakStep(t, m, p)
		if r == nil || r.Outcome != behavior.ActionTransportAccepted || r.Payload["spell_id"] != id || len(cb.writes) != i+1 {
			t.Fatal(id, r, out, cb.writes)
		}
		wantLen := 10
		if i < 2 {
			wantLen = 23
		}
		if len(cb.writes[i].Payload) != wantLen || binary.LittleEndian.Uint32(cb.writes[i].Payload[1:]) != id {
			t.Fatal(cb.writes[i])
		}
		runtimeGenericGo(native, cb.writes[i].Payload[0], id, id == 10)
		// Existing observation/trailer stream delivers the real terminal execution
		// receipt and clears the ledger without inventing damage/channel effects.
		_, _ = native.EncounterObservation(context.Background(), &emptypb.Empty{})
		_, _ = controlBreakStep(t, m, p)
		if p.vm.Status().Pending != 0 {
			t.Fatal("cast execution did not settle", id, p.vm.Status())
		}
	}
}

func TestPolicyGenericSpellExactCallbackTargetsAndUnknownNoReplay(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	runtimeControlBook(t, native, 1449)
	p, err := m.StartPolicy(context.Background(), genericRuntimeSource(1449, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	send := m.session.SendPacket
	m.session.SendPacket = func(ctx context.Context, code uint32, b []byte) error {
		send(ctx, code, b)
		return errors.New("actual cast transport unknown")
	}
	r, out := controlBreakStep(t, m, p)
	if r == nil || r.Outcome != behavior.ActionUnknown || len(cb.writes) != 1 {
		t.Fatal(r, out, cb.writes)
	}
	if _, err = p.Reload(context.Background(), genericRuntimeSource(1449, "")+" ", p.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	r, _ = controlBreakStep(t, m, p)
	if r != nil || len(cb.writes) != 1 || p.vm.Status().Pending != 1 {
		t.Fatal("unknown write replayed", r, p.vm.Status())
	}
	// Exact byte fence rejects destination or composed-target substitution.
	request := &actionsv1.CastSpellRequest{SelfGuid: 22, SpellId: 2120, Targets: &actionsv1.SpellCastTargets{Mask: 64, Destination: &actionsv1.SpellCastLocation{X: 1, Y: 2, Z: 3}}}
	op := &policySpell{genericRequest: request, spell: 2120, origin: modsession.WriteOrigin{SessionID: "same", Module: actionsModule}, admitted: true}
	packet, e := spellcast.Encode(1, request)
	if e != nil || !op.matchesActionPacket(opcode.CMSGCastSpell, packet) {
		t.Fatal(e)
	}
	packet[len(packet)-1] ^= 1
	if op.matchesActionPacket(opcode.CMSGCastSpell, packet) {
		t.Fatal("altered target admitted")
	}
}

func TestPolicyGenericCancelWireForms(t *testing.T) {
	for _, channel := range []bool{false, true} {
		r := &actionsv1.CancelSpellRequest{SpellId: 10, Channel: channel}
		op := &policySpell{cancelRequest: r}
		body := binary.LittleEndian.AppendUint32(nil, 10)
		code := uint32(opcode.CMSGCancelChannelling)
		if !channel {
			code = opcode.CMSGCancelCast
			body = append([]byte{7}, body...)
		}
		if !op.matchesActionPacket(code, body) || op.matchesActionPacket(code, append(body, 0)) {
			t.Fatal(channel, body)
		}
		body[len(body)-1] ^= 1
		if op.matchesActionPacket(code, body) {
			t.Fatal("foreign cancel spell admitted")
		}
	}
}
