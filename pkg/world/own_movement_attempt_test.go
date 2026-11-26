package world

import (
	"context"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"strings"
	"testing"
)

func TestOwnMovementAttemptNeverBorrowsAnotherWrite(t *testing.T) {
	e := scopeEngine(t, &scopeWriteConn{})
	e.publishTransportScope()
	id := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	ctx := modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{MovementAttempt: id})
	body := ownReportPacket(t, 0)
	if _, _, err := e.writeContext(ctx, opcode.MSGMoveStop, body); err != nil {
		t.Fatal(err)
	}
	if e.s.OwnMovementAttempt(id) == nil || e.s.OwnMovementAttempt(other) != nil || e.s.OwnMovementAttempt("") != nil {
		t.Fatal("attempt correlation lost")
	}
	if _, _, err := e.writeContext(context.Background(), opcode.CMSGAttackstop, nil); err != nil {
		t.Fatal(err)
	}
	if e.s.OwnMovementAttempt(id) == nil {
		t.Fatal("unrelated write relabeled movement")
	}
	if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, body); err != nil {
		t.Fatal(err)
	}
	if e.s.OwnMovementAttempt(id) != nil {
		t.Fatal("identical later packet borrowed old identity")
	}
	if _, _, err := e.writeContext(ctx, opcode.MSGMoveStop, body); err != nil {
		t.Fatal(err)
	}
	e.transport.scope.VisitRevision++
	e.publishTransportScope()
	if e.s.OwnMovementAttempt(id) != nil {
		t.Fatal("old attempt crossed visit")
	}
}

func TestOwnMovementAttemptRetainsActualFacingImage(t *testing.T) {
	e := scopeEngine(t, &scopeWriteConn{})
	e.publishTransportScope()
	id := strings.Repeat("c", 64)
	ctx := modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{MovementAttempt: id})
	if _, _, err := e.writeContext(ctx, opcode.MSGMoveSetFacing, ownReportPacket(t, 0)); err != nil {
		t.Fatal(err)
	}
	if r := e.s.OwnMovementAttempt(id); r == nil || r.Opcode != opcode.MSGMoveSetFacing {
		t.Fatal("actual normal-facing wire image lost", r)
	}
}
