package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const testShipEntry uint32 = 87123

func newShipQueryRig(t *testing.T) (*completionRig, *worldv1.QueryMovingTransportTemplateRequest) {
	t.Helper()
	x := &completionRig{s: NewIndependentService(1), f: &completionSession{}, now: time.Unix(22000, 0),
		scope: &modv1.TransportScope{ConnectionId: "ship-test", SelfGuid: 1, VisitBound: true, VisitRevision: 8}}
	x.s.now = func() time.Time { return x.now }
	x.s.ConfigureSession(x.f)
	x.s.ConfigureCombatJournal("ship-session", nil)
	x.deliver(t, opcode.SMSGLoginVerifyWorld, login())
	x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	return x, createQueryShip(t, x, testShipGUID)
}

func createQueryShip(t *testing.T, x *completionRig, guid uint64) *worldv1.QueryMovingTransportTemplateRequest {
	t.Helper()
	w := x.deliver(t, opcode.SMSGUpdateObject, shipCreate(guid, testShipEntry, 15, 13450))
	for _, hull := range w.MovingTransports {
		if hull.Guid == guid && hull.MovingTransport.ObjectRevision != 0 {
			return &worldv1.QueryMovingTransportTemplateRequest{SessionId: "ship-session", SelfGuid: 1,
				VisitRevision: w.VisitRevision, Guid: guid, Entry: hull.Entry, ObjectRevision: hull.MovingTransport.ObjectRevision}
		}
	}
	t.Fatal("newly created ship absent")
	return nil
}

func shipQueryReply(entry, kind, display, path uint32) []byte {
	b := new(bytes.Buffer)
	for _, v := range []uint32{entry, kind, display} {
		u32(b, v)
	}
	b.WriteString("Test ferry\x00\x00\x00\x00\x00\x00\x00")
	data := [24]uint32{path, 29, 2}
	for _, v := range data {
		u32(b, v)
	}
	f32(b, 1.25)
	b.Write(make([]byte, 24))
	return b.Bytes()
}

func queryShip(t *testing.T, x *completionRig, req *worldv1.QueryMovingTransportTemplateRequest) *worldv1.QueryMovingTransportTemplateResponse {
	t.Helper()
	got, err := x.s.QueryMovingTransportTemplate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMovingTransportTemplateQueryAndLateArrival(t *testing.T) {
	for _, early := range []bool{false, true} {
		x, req := newShipQueryRig(t)
		reply := shipQueryReply(req.Entry, 15, 3015, 422)
		before := x.deliver(t, opcode.SMSGGameobjectQueryResponse, reply).MovingTransports[0]
		if before.MovingTransport.Template != nil || len(x.f.packets) != 0 {
			t.Fatal("passive observation sent or accepted an unrequested query")
		}
		if early {
			x.f.onSend = func() {
				if got := queryShip(t, x, req); got.Phase != "dispatching" || got.TransportWritten || got.Template != nil {
					t.Fatal("overlapping call reused a pending network operation", got)
				}
				x.deliver(t, opcode.SMSGGameobjectQueryResponse, reply)
			}
		}
		got := queryShip(t, x, req)
		want := binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32(nil, req.Entry), req.Guid)
		if !got.TransportWritten || len(x.f.packets) != 1 || x.f.packets[0].Opcode != 0x05e || !bytes.Equal(x.f.packets[0].Payload, want) {
			t.Fatal("ordinary query lost its entry or full moving-transport GUID", got, x.f.packets)
		}
		if !early {
			if got.Phase != "transport_written" || got.Template != nil || !proto.Equal(got, queryShip(t, x, req)) {
				t.Fatal("write alone was not kept pending", got)
			}
			x.deliver(t, opcode.SMSGGameobjectQueryResponse, reply)
		}
		x.now = x.now.Add(1250 * time.Millisecond)
		got = queryShip(t, x, req)
		m := got.Template
		if m == nil || got.Phase != "transport_written" || !got.TransportWritten || m.Guid != req.Guid || m.Entry != req.Entry ||
			m.SelfGuid != req.SelfGuid || m.VisitRevision != req.VisitRevision || m.DisplayId != 3015 ||
			m.TaxiPathId != 422 || m.MoveSpeed != 29 || m.AccelerationRate != 2 || m.AgeMs != 1250 || m.Revision <= req.ObjectRevision || len(x.f.packets) != 1 {
			t.Fatal("matching static reply lost its provenance or route data", got)
		}
		w, err := x.s.Snapshot(context.Background(), &worldv1.SnapshotRequest{GameObjectEntry: req.Entry})
		hull := w.MovingTransports[0]
		if err != nil || !proto.Equal(m, hull.MovingTransport.Template) || !proto.Equal(hull, w.RequestedGameObjects[0]) ||
			hull.MovingTransport.TemplateQueryPhase != got.Phase || hull.PositionRevision != before.PositionRevision ||
			hull.MovingTransport.MovementRevision != before.MovingTransport.MovementRevision || hull.X != before.X || hull.MovingTransport.MovementAgeMs != 1250 {
			t.Fatal("static template rewrote the instance's observed geometry", hull, err)
		}
	}
}

func TestMovingTransportTemplateRejectsUnboundOrMalformedSources(t *testing.T) {
	x, req := newShipQueryRig(t)
	for _, change := range []func(*worldv1.QueryMovingTransportTemplateRequest){
		func(v *worldv1.QueryMovingTransportTemplateRequest) { v.SessionId = "foreign" },
		func(v *worldv1.QueryMovingTransportTemplateRequest) { v.SelfGuid++ },
		func(v *worldv1.QueryMovingTransportTemplateRequest) { v.VisitRevision++ },
		func(v *worldv1.QueryMovingTransportTemplateRequest) { v.Guid++ },
		func(v *worldv1.QueryMovingTransportTemplateRequest) { v.Entry++ },
		func(v *worldv1.QueryMovingTransportTemplateRequest) { v.ObjectRevision++ },
	} {
		bad := proto.Clone(req).(*worldv1.QueryMovingTransportTemplateRequest)
		change(bad)
		if _, err := x.s.QueryMovingTransportTemplate(context.Background(), bad); err == nil || len(x.f.packets) != 0 {
			t.Fatal("unbound query dispatched", bad, err)
		}
	}
	queryShip(t, x, req)
	correct := shipQueryReply(req.Entry, 15, 3015, 422)
	for _, b := range [][]byte{shipQueryReply(req.Entry+1, 15, 3015, 422), shipQueryReply(req.Entry, 8, 3015, 422),
		shipQueryReply(req.Entry, 15, 3016, 422), shipQueryReply(req.Entry, 15, 3015, 0), correct[:len(correct)-1], append(bytes.Clone(correct), 0)} {
		x.deliver(t, opcode.SMSGGameobjectQueryResponse, b)
	}
	for _, change := range []func(*modv1.TransportScope){
		func(v *modv1.TransportScope) { v.ConnectionId = "other" },
		func(v *modv1.TransportScope) { v.SelfGuid++ },
		func(v *modv1.TransportScope) { v.VisitRevision++ },
		func(v *modv1.TransportScope) { v.MotionRevision = 1 },
	} {
		scope := cloneMotionScope(x.scope)
		change(scope)
		x.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGGameobjectQueryResponse, Payload: correct, TransportScope: scope})
	}
	if got := queryShip(t, x, req); got.Template != nil || got.Phase != "transport_written" || len(x.f.packets) != 1 {
		t.Fatal("foreign or malformed reply bound to the ship", got)
	}
	x.deliver(t, opcode.SMSGGameobjectQueryResponse, correct)
	if got := queryShip(t, x, req); got.GetTemplate().GetTaxiPathId() != 422 || len(x.f.packets) != 1 {
		t.Fatal("genuine later reply was lost", got)
	}
}

func TestMovingTransportTemplateRetainsOriginalObjectAndWriteOutcome(t *testing.T) {
	x, original := newShipQueryRig(t)
	another := createQueryShip(t, x, testShipGUID+1)
	queryShip(t, x, original)
	replacement := createQueryShip(t, x, testShipGUID)
	if replacement.ObjectRevision == original.ObjectRevision {
		t.Fatal("replacement reused an object generation")
	}
	if _, err := x.s.QueryMovingTransportTemplate(context.Background(), original); err == nil {
		t.Fatal("removed source remained eligible")
	}
	for _, req := range []*worldv1.QueryMovingTransportTemplateRequest{another, replacement} {
		if got := queryShip(t, x, req); got.Phase != "outstanding_template_query" || got.TransportWritten || got.Template != nil {
			t.Fatal("a second hull borrowed the original request", got)
		}
	}
	reply := shipQueryReply(original.Entry, 15, 3015, 422)
	w := x.deliver(t, opcode.SMSGGameobjectQueryResponse, reply)
	if len(x.f.packets) != 1 || w.MovingTransports[0].MovingTransport.Template != nil || w.MovingTransports[1].MovingTransport.Template != nil {
		t.Fatal("old entry-only response rebound to a current ship")
	}
	x.f.sendError = errors.New("reply from physical writer unavailable")
	if got := queryShip(t, x, another); got.Phase != "write_outcome_unknown" || got.TransportWritten || got.Template != nil {
		t.Fatal(got)
	}
	if got := queryShip(t, x, another); got.Phase != "write_outcome_unknown" || len(x.f.packets) != 2 {
		t.Fatal("unknown static read reissued", got)
	}
	x.deliver(t, opcode.SMSGGameobjectQueryResponse, reply)
	if got := queryShip(t, x, another); got.Phase != "write_outcome_unknown" || got.TransportWritten || got.GetTemplate().GetGuid() != another.Guid {
		t.Fatal("actual late static data changed original write custody", got)
	}
	x.f.sendError = modcb.PacketNotDispatchedError("already stopped")
	x.f.onSend = func() { x.deliver(t, opcode.SMSGGameobjectQueryResponse, reply) }
	if got := queryShip(t, x, replacement); got.Phase != "not_dispatched" || got.TransportWritten || got.Template != nil || len(x.f.packets) != 3 {
		t.Fatal("staged reply manufactured a known-unwritten source", got)
	}
	if got := queryShip(t, x, replacement); got.Template != nil || len(x.f.packets) != 3 {
		t.Fatal("known refusal was replayed", got)
	}
}

func TestMovingTransportTemplateDoesNotBridgeSameMapReentry(t *testing.T) {
	x, initial := newShipQueryRig(t)
	queryShip(t, x, initial)
	oldScope := cloneMotionScope(x.scope)
	x.deliver(t, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
	x.scope.VisitRevision++
	x.deliver(t, opcode.SMSGNewWorld, login())
	x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	current := createQueryShip(t, x, testShipGUID)
	if initial.VisitRevision == current.VisitRevision || initial.ObjectRevision == current.ObjectRevision {
		t.Fatal("fixture reused a World visit or hull lifetime")
	}
	if _, err := x.s.QueryMovingTransportTemplate(context.Background(), initial); err == nil {
		t.Fatal("previous visit still eligible")
	}
	if got := queryShip(t, x, current); !got.TransportWritten || got.Template != nil || len(x.f.packets) != 2 {
		t.Fatal("previous visit blocked a new static read", got)
	}
	x.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGGameobjectQueryResponse,
		Payload: shipQueryReply(initial.Entry, 15, 3015, 422), TransportScope: oldScope})
	denied := binary.LittleEndian.AppendUint32(nil, current.Entry|0x80000000)
	x.deliver(t, opcode.SMSGGameobjectQueryResponse, denied)
	// A duplicate different answer for the now-consumed entry cannot change
	// this observed object's request or become evidence for a new instance.
	x.deliver(t, opcode.SMSGGameobjectQueryResponse, shipQueryReply(current.Entry, 15, 3015, 999))
	if got := queryShip(t, x, current); !got.TransportWritten || got.Template != nil || len(x.f.packets) != 2 {
		t.Fatal("cross-visit or unavailable template reused", got)
	}
}
