package modcb

import (
	"context"
	"encoding/hex"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
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

func OutgoingMovementAttempt(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	values := md.Get(movementAttemptKey)
	if len(values) != 1 || !ValidMovementAttempt(values[0]) {
		return ""
	}
	return values[0]
}

const routeBodyKey = "agentwow-route-body-input"
const followPositionKey = "agentwow-route-follow-position-bin"

// WithRouteBodyInput marks frames selected by the native Actions route owner.
// A marker is usable only on that module's scoped callback with an exact
// attempt. An existing policy token remains mandatory for a Lua travel call.
func WithRouteBodyInput(ctx context.Context, followsOwner bool, anchor ...movementinfo.FollowPosition) context.Context {
	mode := "destination"
	if followsOwner {
		mode = "follow"
		if len(anchor) == 1 {
			ctx = metadata.AppendToOutgoingContext(ctx, followPositionKey, anchor[0].Wire())
		}
	}
	return metadata.AppendToOutgoingContext(ctx, routeBodyKey, mode)
}

func IncomingRouteBodyInput(ctx context.Context) (followsOwner, valid, supplied bool) {
	values := metadata.ValueFromIncomingContext(ctx, routeBodyKey)
	anchors := metadata.ValueFromIncomingContext(ctx, followPositionKey)
	if len(anchors) != 0 {
		if _, ok := IncomingFollowPosition(ctx); !ok || len(values) != 1 || values[0] != "follow" {
			return false, false, true
		}
	}
	if len(values) != 1 {
		return false, false, len(values) != 0
	}
	return values[0] == "follow", values[0] == "follow" || values[0] == "destination", true
}

func IncomingFollowPosition(ctx context.Context) (movementinfo.FollowPosition, bool) {
	rows := metadata.ValueFromIncomingContext(ctx, followPositionKey)
	if len(rows) != 1 {
		return movementinfo.FollowPosition{}, false
	}
	return movementinfo.ReadFollowPosition(rows[0])
}
