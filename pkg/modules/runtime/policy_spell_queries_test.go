package modrt

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/creaturequery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPolicySapReadQueryDoesNotConsumeCastReservation(t *testing.T) {
	m := New(nil, nil, nil)
	t.Cleanup(m.cancel)
	const target = uint64(0xf13000022b000001)
	origin := modsession.WriteOrigin{SessionID: "body", Module: actionsModule, Writer: "lua", ActionID: "1"}
	op := &policySpell{capability: "sap", target: target, admitted: true, origin: origin}
	m.policySpells.active = map[string]*policySpell{"token": op}
	writes := 0
	m.session.SendPacket = func(ctx context.Context, code uint32, payload []byte) error {
		if code != opcode.CMSGCreatureQuery || len(payload) != 12 || modsession.WriteOriginFromContext(ctx) != origin {
			t.Fatal("query identity changed", code, payload)
		}
		writes++
		return nil
	}
	body := creaturequery.Request(555, target)
	if handled, err := m.writePolicyReadQuery(context.Background(), "token", "body", actionsModule, opcode.CMSGCreatureQuery, body); !handled || err != nil || op.attempted || writes != 1 {
		t.Fatal("ordinary observation consumed or bypassed the cast reservation", handled, err, op.attempted, writes)
	}
	for _, mode := range []string{"unknown_token", "other_target", "other_entry", "trailing", "other_capability", "already_cast"} {
		t.Run(mode, func(t *testing.T) {
			op.capability, op.attempted = "sap", false
			payload, token := append([]byte(nil), body...), "token"
			switch mode {
			case "unknown_token":
				token = "other"
			case "other_target":
				binary.LittleEndian.PutUint64(payload[4:], target+1)
			case "other_entry":
				binary.LittleEndian.PutUint32(payload, 556)
			case "trailing":
				payload = append(payload, 0)
			case "other_capability":
				op.capability = "polymorph"
			case "already_cast":
				op.attempted = true
			}
			handled, err := m.writePolicyReadQuery(context.Background(), token, "body", actionsModule, opcode.CMSGCreatureQuery, payload)
			if !handled || status.Code(err) != codes.PermissionDenied || writes != 1 {
				t.Fatal("query escaped selected action scope", handled, err, writes)
			}
		})
	}
}
