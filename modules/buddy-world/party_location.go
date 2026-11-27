package buddyworld

import (
	"github.com/agent-wow/agent-wow/pkg/maps"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/partystats"
)

// A remote group report has no nonce. Only the physical socket ordering after
// this exact owner's observed departure can supply a different map.
type ownerPartyLocation struct {
	source *modv1.TransportScope
	last   uint64
	living bool
}

func isPartyLocationOpcode(op uint32) bool {
	return op == opcode.SMSGPartyMemberStats || op == opcode.SMSGPartyMemberStatsFull || op == opcode.SMSGGroupList || op == opcode.SMSGGroupDestroyed || op == opcode.SMSGGroupUninvite
}

func (s *observations) observeOwnerDeparture(previous *observations, p *modv1.WorldPacket) {
	own, owner := &s.players[0], &s.players[1]
	if owner.present || !own.present || !own.mapKnown || s.visitRevision != previous.visitRevision {
		s.partyLocation = ownerPartyLocation{}
		return
	}
	if previous.players[1].present && (p.Opcode == opcode.SMSGDestroyObject || p.Opcode == opcode.SMSGUpdateObject || p.Opcode == opcode.SMSGCompressedUpdateObject) {
		s.partyLocation = ownerPartyLocation{}
		if scopeMatchesSelf(p.TransportScope, *own, s.visitRevision) && previous.players[1].mapKnown && previous.players[1].mapID == own.mapID {
			s.partyLocation = ownerPartyLocation{source: cloneMotionScope(p.TransportScope), last: p.TransportScope.MotionRevision}
		}
	}
}

func (s *observations) observePartyLocation(p *modv1.WorldPacket) error {
	owner := &s.players[1]
	state := &s.partyLocation
	scope := p.TransportScope
	if owner.guid == 0 || owner.present || !scopeMatchesSelf(scope, s.players[0], s.visitRevision) || !sameMotionVisit(scope, state.source) || scope.MotionRevision <= state.last {
		return nil
	}
	if p.Opcode == opcode.SMSGGroupList || p.Opcode == opcode.SMSGGroupDestroyed || p.Opcode == opcode.SMSGGroupUninvite {
		state.last, state.living, owner.mapKnown = scope.MotionRevision, false, false
		return nil
	}
	m, complete := partystats.Decode(p.Opcode, p.Payload)
	if m.GUID != owner.guid && (m.GUID != 0 || complete) {
		return nil
	}
	if !complete {
		state.last, state.living, owner.mapKnown = scope.MotionRevision, false, false
		return errLayout
	}
	if !m.LocationUpdate() {
		return nil
	}
	state.last = scope.MotionRevision
	if m.Unavailable() {
		state.living, owner.mapKnown = false, false
		return nil
	}
	if m.ConfirmsLife() {
		state.living = true
	}
	if m.HasZone() {
		mapID, known := maps.DungeonMapForZone(uint32(m.Zone))
		owner.mapKnown = known && state.living
		if owner.mapKnown {
			owner.mapID = mapID
		}
	}
	return nil
}
