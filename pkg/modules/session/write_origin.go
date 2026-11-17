package modsession

import "context"

// WriteOrigin associates a transport attempt with a runtime dispatch. The
// runtime sets these values from its own action and writer state. They are
// diagnostic metadata, never authority to send a packet.
type WriteOrigin struct {
	SessionID      string `json:"sessionId,omitempty"`
	Module         string `json:"module,omitempty"`
	Writer         string `json:"writer,omitempty"`
	Generation     uint64 `json:"generation,string"`
	PolicyRevision string `json:"policyRevision,omitempty"`
	ActionID       string `json:"actionId,omitempty"`
}

type writeOriginKey struct{}

// WithWriteOrigin attaches trusted in-process dispatch metadata. RPC clients
// cannot set this context value through request fields or gRPC metadata.
func WithWriteOrigin(ctx context.Context, origin WriteOrigin) context.Context {
	return context.WithValue(ctx, writeOriginKey{}, origin)
}

// WriteOriginFromContext returns a zero value when the write is unattributed.
func WriteOriginFromContext(ctx context.Context) WriteOrigin {
	origin, _ := ctx.Value(writeOriginKey{}).(WriteOrigin)
	return origin
}
