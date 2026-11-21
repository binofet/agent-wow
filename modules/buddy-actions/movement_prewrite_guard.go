package buddyactions

import (
	"context"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

type movementPrewriteGuardKey struct{}
type movementPrewriteGuard func(context.Context, *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool)

// A caller's finite movement intent can retain its own authority through the
// final clock/World callbacks without installing standing movement ownership.
func withMovementPrewriteGuard(ctx context.Context, guard movementPrewriteGuard) context.Context {
	return context.WithValue(ctx, movementPrewriteGuardKey{}, guard)
}
