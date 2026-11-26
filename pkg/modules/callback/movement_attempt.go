package modcb

import (
	"context"
	"encoding/hex"
	"google.golang.org/grpc/metadata"
)

const movementAttemptKey = "agentwow-own-movement-attempt"

// Movement attempt IDs correlate transport evidence only; they grant no authority.
func ValidMovementAttempt(id string) bool {
	if len(id) != 64 {
		return false
	}
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == id
}
func WithMovementAttempt(ctx context.Context, id string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, movementAttemptKey, id)
}
func IncomingMovementAttempt(ctx context.Context) string {
	m, _ := metadata.FromIncomingContext(ctx)
	v := m.Get(movementAttemptKey)
	if len(v) != 1 || !ValidMovementAttempt(v[0]) {
		return ""
	}
	return v[0]
}

const routeBodyKey = "agentwow-route-body-input"

// WithRouteBodyInput marks frames selected by the native Actions route owner.
// A marker is usable only on that module's scoped callback with an exact
// attempt. An existing policy token remains mandatory for a Lua travel call.
func WithRouteBodyInput(ctx context.Context, followsOwner bool) context.Context {
	mode := "destination"
	if followsOwner {
		mode = "follow"
	}
	return metadata.AppendToOutgoingContext(ctx, routeBodyKey, mode)
}

func IncomingRouteBodyInput(ctx context.Context) (followsOwner, valid, supplied bool) {
	values := metadata.ValueFromIncomingContext(ctx, routeBodyKey)
	if len(values) != 1 {
		return false, false, len(values) != 0
	}
	return values[0] == "follow", values[0] == "follow" || values[0] == "destination", true
}
