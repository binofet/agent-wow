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
