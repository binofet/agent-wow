package modrt

import (
	"context"
	"encoding/binary"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The previous ordinary TravelTo request consists entirely of scalar fields.
// An untagged call may use exactly that installed schema. A flightmaster leg
// always requires the new descriptor, so no older server can ignore its guard.
func observedRequestMatches(got protoreflect.MessageDescriptor, request proto.Message) bool {
	want := request.ProtoReflect().Descriptor()
	if instanceEntryDescriptorMatches(got, want, map[protoreflect.FullName]bool{}) {
		return true
	}
	travel, ok := request.(*actionsv1.TravelToRequest)
	if !ok || travel.Flightmaster != nil || got.FullName() != want.FullName() || got.Fields().ByNumber(7) != nil {
		return false
	}
	legacy := protodesc.ToDescriptorProto(want)
	if len(legacy.Field) != 7 || legacy.Field[6].GetName() != "flightmaster" || legacy.Field[6].GetNumber() != 7 {
		return false
	}
	legacy.Field = legacy.Field[:6]
	return proto.Equal(protodesc.ToDescriptorProto(got), legacy)
}

// One authenticated Lua reservation may send only the free query it selected.
// A ground-arrival query retains the normal World count and exact physical
// STOP scope already verified by moduleScopedPacketWriter and the socket.
func (m *Manager) writePolicyFlightpath(ctx context.Context, token, sid, module string, code uint32, data []byte, scope *modv1.TransportScope) (bool, error) {
	registry := &m.policySpells
	registry.mu.Lock()
	op := registry.active[token]
	if op == nil || op.flightpath == nil {
		registry.mu.Unlock()
		return false, nil
	}
	q := op.flightpath
	expected := uint32(opcode.CMSGTaxinodeStatusQuery)
	if q.Mode == "discover" {
		expected = opcode.CMSGTaxiqueryavailablenodes
	}
	if !op.admitted || op.attempted || op.origin.SessionID != sid || q.SessionId != sid || op.origin.Module != module ||
		op.origin.Writer != "lua" || op.origin.ActionID == "" || module != actionsModule || code != expected ||
		!modcb.FlightpathQueryPacket(code, data) || binary.LittleEndian.Uint64(data) != q.NpcGuid ||
		scope != nil && (scope.SelfGuid != q.SelfGuid || !routeScopeBound(scope)) || ctx.Err() != nil {
		registry.mu.Unlock()
		return true, modcb.PacketNotDispatchedError("callback is outside the selected free flightmaster query")
	}
	// Consume the single-packet reservation even if Core later proves that
	// it declined this write. Another selection needs its own revision.
	op.admitted, op.attempted = false, true
	origin := op.origin
	registry.mu.Unlock()
	ctx = modsession.WithWriteOrigin(ctx, origin)
	var err error
	if scope != nil {
		err = m.session.SendScopedPacket(ctx, code, data, scope)
	} else {
		err = m.session.SendPacket(ctx, code, data)
	}
	registry.mu.Lock()
	op.written = err == nil
	if modcb.PacketWasNotDispatched(err) {
		op.attempted = false
	}
	registry.mu.Unlock()
	return true, err
}
