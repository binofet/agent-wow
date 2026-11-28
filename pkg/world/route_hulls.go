package world

import (
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Object batches publish their complete authoritative hull set from World.
// A standalone removal can arrive while an earlier batch is still queued.
// Keep that removal until the next ordered batch includes it.
type routeHullState struct {
	pending, parsed uint64
	scope           *modv1.TransportScope
	visible         map[uint64]modcb.RouteHull
	removed         map[uint64]bool
}

func (s *Session) observeRouteHullPacket(op uint16, wire []byte, scope *modv1.TransportScope) {
	var guid uint64
	switch op {
	case opcode.SMSGUpdateObject, opcode.SMSGCompressedUpdateObject:
		s.mu.Lock()
		s.routeHulls.pending = scope.GetMotionRevision()
		s.routeHulls.removed = nil
		s.mu.Unlock()
		return
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld, opcode.SMSGTransferPending:
	default:
		var relevant bool
		guid, relevant = movementinfo.HullPacketSubject(uint32(op), wire)
		if !relevant {
			return
		}
		if op == opcode.SMSGDestroyObject && guid == scope.GetSelfGuid() {
			guid = 0
		}
	}
	if guid != 0 && !movementinfo.MovingTransportGUID(guid) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.routeHulls
	if guid == 0 {
		*h = routeHullState{}
	} else {
		delete(h.visible, guid)
		if h.pending != h.parsed {
			// World retains at most 128 GameObjects in one visit. A burst of
			// more unclassified removals withdraws the pending image entirely.
			if len(h.removed) >= 128 {
				*h = routeHullState{}
				return
			}
			if h.removed == nil {
				h.removed = make(map[uint64]bool)
			}
			h.removed[guid] = true
		}
	}
}

func (s *Session) recordRouteHulls(scope *modv1.TransportScope, rows []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.routeHulls
	if h.pending == 0 || scope.GetMotionRevision() != h.pending || h.parsed == h.pending || !sameBodyScope(scope, s.transportScopeSnapshot) {
		return
	}
	var visible map[uint64]modcb.RouteHull
	if len(rows) > 0 && len(rows) <= 128 {
		visible = make(map[uint64]modcb.RouteHull, len(rows))
		for _, row := range rows {
			r, ok := modcb.ReadRouteHull(row)
			if !ok || visible[r.GUID].GUID != 0 {
				visible = nil
				break
			}
			visible[r.GUID] = r
		}
	}
	for guid := range h.removed {
		delete(visible, guid)
	}
	h.visible, h.removed, h.scope, h.parsed = visible, nil, cloneTransportScope(scope), h.pending
	s.bodyUpdates.signal()
}

func (s *Session) currentRouteHull(guid uint64, scope *modv1.TransportScope) (modcb.RouteHull, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := &s.routeHulls
	r, present := h.visible[guid]
	return r, present && h.pending == h.parsed && sameBodyScope(h.scope, scope)
}
