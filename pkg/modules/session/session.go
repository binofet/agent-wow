// Package modsession defines the identity and callbacks supplied by a gameplay session.
package modsession

import (
	"context"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
)

// Session supplies character identity, packet transport, and the shared monotonic
// clock. Both callbacks are required and must support concurrent calls.
type Session struct {
	Identity   Identity
	SendPacket func(context.Context, uint32, []byte) error
	Clock      func() uint32
	// Optional transport-owned fencing for new typed native mechanics. Legacy
	// hosts leave these nil; a scoped call must never fall back to SendPacket.
	SendScopedPacket func(context.Context, uint32, []byte, *modv1.TransportScope) error
	TransportScope   func() *modv1.TransportScope
}
