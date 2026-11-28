package world

import (
	"math"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// World and the physical reader count the same ordered hull inputs within
// the visit. Foreign rider heartbeats do not advance self's motion revision.
type routeHullState struct {
	pending, parsed uint64
	scope           *modv1.TransportScope
	visible         map[uint64]modcb.RouteHull
	riders          map[uint64]uint64
	pendingAll      bool
	pendingShips    map[uint64]bool
}

func (s *Session) observeRouteHullPacket(op uint16, wire []byte, scope *modv1.TransportScope) {
	switch op {
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld, opcode.SMSGTransferPending:
		s.mu.Lock()
		s.routeHulls = routeHullState{}
		s.mu.Unlock()
		return
	}
	subject, relevant := movementinfo.RouteHullSubject(uint32(op), wire)
	if !relevant {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.routeHulls
	if h.pending < math.MaxUint64 {
		h.pending++
	}
	if subject == 0 || op == opcode.SMSGDestroyObject && subject == scope.GetSelfGuid() {
		h.pendingAll = true
	}
	if h.pendingAll || len(h.visible) == 0 {
		return
	}
	// A stand-alone rider can leave their old ship and join the new one in
	// one frame. The last known image of an unrelated ship stays usable.
	var boarding uint64
	if !movementinfo.MovingTransportGUID(subject) {
		boarding = movementinfo.IncomingPassengerHull(uint32(op), wire)
	}
	for _, guid := range [...]uint64{subject, h.riders[subject], boarding} {
		if _, visible := h.visible[guid]; visible {
			if h.pendingShips == nil {
				h.pendingShips = make(map[uint64]bool)
			}
			h.pendingShips[guid] = true
		}
	}
}

func (h *routeHullState) waitingFor(guid uint64) bool {
	return h.pending != h.parsed && (h.pendingAll || h.pendingShips[guid])
}

func (s *Session) recordRouteHulls(scope *modv1.TransportScope, rows []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.routeHulls
	if len(rows) == 0 || h.parsed == h.pending || !sameBodyScope(scope, s.transportScopeSnapshot) {
		return
	}
	seq, riders, ok := modcb.ReadRouteHullHeader(rows[0])
	if !ok || seq != h.pending {
		return
	}
	rows = rows[1:]
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
	h.visible, h.riders, h.scope, h.parsed = visible, riders, cloneTransportScope(scope), seq
	h.pendingAll, h.pendingShips = false, nil
	s.bodyUpdates.signal()
}

func (s *Session) currentRouteHull(guid uint64, scope *modv1.TransportScope) (modcb.RouteHull, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := &s.routeHulls
	r, present := h.visible[guid]
	return r, present && !h.waitingFor(guid) && sameBodyScope(h.scope, scope)
}
