package world

import (
	"encoding/binary"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/proto"
)

type areaTriggerAttempt struct {
	connection          string
	self, visit, motion uint64
	trigger             uint32
}

// Actions validates the actual trigger geometry and verified ground route.
// This transport-owner fence preserves its captured body/visit/motion until
// the one ID-only wire request enters transport. It does not choose a trigger
// or infer admission, a map destination, or successful instance entry.
func (t *transportScopeState) reserveAreaTrigger(payload []byte, requested *modv1.TransportScope) error {
	current := t.scope
	if current == nil || t.exhausted || current.ConnectionId == "" || current.SelfGuid == 0 || !current.VisitBound ||
		current.VisitRevision == 0 || current.MotionRevision == 0 || !proto.Equal(requested, current) {
		return modcb.PacketNotDispatchedError("area trigger transport scope is absent or no longer current")
	}
	if len(payload) != 4 || binary.LittleEndian.Uint32(payload) == 0 {
		return modcb.PacketNotDispatchedError("area trigger requires exactly one nonzero uint32 ID")
	}
	key := areaTriggerAttempt{current.ConnectionId, current.SelfGuid, current.VisitRevision, current.MotionRevision, binary.LittleEndian.Uint32(payload)}
	if _, attempted := t.areaTriggerAttempts[key]; attempted {
		return modcb.PacketNotDispatchedError("this exact area trigger source already has a transport attempt")
	}
	if t.areaTriggerAttempts == nil {
		t.areaTriggerAttempts = make(map[areaTriggerAttempt]struct{})
	}
	// Retain the reservation on both a known write and any ambiguous transport
	// error. New motion is not permission to retry an unresolved Actions row.
	t.areaTriggerAttempts[key] = struct{}{}
	return nil
}
