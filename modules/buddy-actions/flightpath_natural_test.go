package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNormalTaxiMenuAfterNaturalLandingWithoutGroundEcho(t *testing.T) {
	p := &settlementPolicyFixture{f: newActivationPolicyFixture(t, 6, 8, 6, 8), lost: map[string]bool{}, destinationNPC: 0xf13000062400002b}
	p.accept()
	activationPolicyNoEffect(t, p.step())
	// A center distance of 5.12 is outside the old flat five-yard check.
	p.qualifySplineAt(5.12, 1.5)
	before := p.f.base.x.f.world.SelfMovementFacts
	b := binary.LittleEndian.AppendUint32(nil, 1)
	b = append(b, 0, 1, 1)
	b = append(b, settlementPolicyValues(map[int]uint32{0x3b: 8})...)
	p.worldPacket(opcode.SMSGUpdateObject, b)
	for range 8 {
		out := p.step()
		action, ok := out["action"].(map[string]any)
		if !ok {
			continue
		}
		if action["method"] != "buddy-actions.query_flightpath" {
			t.Fatal("unexpected post-landing write", action)
		}
		p.dispatch(action)
		menu := p.f.base.x.observation(t).Operation
		if !menu.TransportWritten || menu.NpcGuid != p.destinationNPC {
			t.Fatal("destination menu was not written", menu)
		}
		// Delivery of an old menu without the physical-write tag cannot settle.
		flightpathSettlementPacket(t, p.f.base.x, opcode.SMSGShowtaxinodes, flightpathMenuPayload(p.destinationNPC, 8, 6, 8), cloneFlightpathScope(p.scope))
		if p.f.base.x.observation(t).Operation.MenuKnown {
			t.Fatal("accepted a menu observed before our request")
		}
		p.arriveMenu(p.f.activation.DestinationNode)
		p.dispatch(p.until("buddy-actions.settle_flightpath"))
		w := p.f.base.x.f.world
		if a := p.f.base.x.observation(t).Activation; !a.ArrivalKnown || a.ArrivalEvidence != "server_taxi_clear_and_destination_menu" || w.SplineCompletion.Operation != nil || w.SelfMovementFacts.Revision != before.Revision || w.SelfMovementFacts.Info.X != before.Info.X {
			t.Fatal("arrival, untouched raw departure position or absent completion", p.f.base.x.observation(t), w.SelfMovementFacts)
		}
		return
	}
	t.Fatal("accepted flight naturally cleared the taxi flag with an observed destination flightmaster and remained stuck waiting for a completion")
}

func TestNaturalTaxiSettlementRequiresTheStillCurrentDestinationBody(t *testing.T) {
	p := newSettlementPolicyFixture(t, false)
	p.accept()
	p.qualifySpline()
	flags := binary.LittleEndian.AppendUint32(nil, 1)
	flags = append(flags, 0, 1, 1)
	flags = append(flags, settlementPolicyValues(map[int]uint32{0x3b: 8})...)
	p.worldPacket(opcode.SMSGUpdateObject, flags)
	p.dispatch(p.until("buddy-actions.query_flightpath"))
	raw, err := movementinfo.Encode(movementinfo.Info{Time: 987, X: float32(p.destination.X) + 60, Y: float32(p.destination.Y), Z: float32(p.destination.Z)})
	if err != nil {
		t.Fatal(err)
	}
	p.worldPacket(opcode.MSGMoveHeartbeat, append([]byte{1, 1}, raw...))
	p.arriveMenu(p.f.activation.DestinationNode)
	r := flightpathSettlementRequest(t, p.f.base.x)
	r.CompletionRevision = 0
	r.NaturalLandingRevision = p.f.base.x.f.world.SelfMotionAuthority.NaturalTaxiLandingRevision
	before := len(p.f.base.x.f.packets)
	if _, err := p.f.base.x.s.SettleFlightpath(context.Background(), r); err == nil {
		t.Fatal("fresh contradictory passenger position did not fence an older menu")
	}
	if v := p.f.base.x.observation(t); v.Activation.ArrivalKnown || !v.Activation.Pending || len(p.f.base.x.f.packets) != before {
		t.Fatal("changed existing booking or emitted another packet", v)
	}
}
