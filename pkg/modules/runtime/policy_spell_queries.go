package modrt

import (
	"context"
	"encoding/binary"

	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A selected Sap can acquire missing creature type proof through the ordinary
// client query. This read never consumes its one cast-packet reservation.
func (m *Manager) writePolicyReadQuery(ctx context.Context, token, sessionID, module string, code uint32, payload []byte) (bool, error) {
	if code != opcode.CMSGCreatureQuery {
		return false, nil
	}
	spells := &m.policySpells
	spells.mu.Lock()
	op := spells.active[token]
	valid := op != nil && op.admitted && !op.attempted && op.origin.SessionID == sessionID &&
		op.origin.Module == module && op.capability == "sap" && op.target>>48 == 0xf130 &&
		len(payload) == 12 && binary.LittleEndian.Uint32(payload) == uint32(op.target>>24)&0xffffff &&
		binary.LittleEndian.Uint64(payload[4:]) == op.target
	var origin modsession.WriteOrigin
	if valid {
		origin = op.origin
	}
	spells.mu.Unlock()
	if !valid {
		return true, status.Error(codes.PermissionDenied, "policy creature query must match the selected Sap target")
	}
	return true, m.session.SendPacket(modsession.WithWriteOrigin(ctx, origin), code, payload)
}
