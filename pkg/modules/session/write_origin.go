package modsession

import (
	"context"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
)

// WriteOrigin associates a transport attempt with a runtime dispatch. The
// runtime sets these values from its own action and writer state. They are
// diagnostic metadata, never authority to send a packet.
type WriteOrigin struct {
	MovementAttempt string `json:"movementAttempt,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	Module          string `json:"module,omitempty"`
	Writer          string `json:"writer,omitempty"`
	Generation      uint64 `json:"generation,string"`
	PolicyRevision  string `json:"policyRevision,omitempty"`
	ActionID        string `json:"actionId,omitempty"`
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

// Only Runtime's authenticated measured body-input callback marks a write for
// complete-World-parser continuity. This marker grants no write by itself.
type bodyInputKey struct{}
type selfBodyInputKey struct{}

// Independent forward input and an already committed fall have no remaining
// destination dependency. Physical changes to this body still fence them.
func WithSelfBodyInput(ctx context.Context) context.Context {
	return context.WithValue(WithBodyInput(ctx), selfBodyInputKey{}, true)
}
func IsSelfBodyInput(ctx context.Context) bool {
	self, _ := ctx.Value(selfBodyInputKey{}).(bool)
	return self
}

func WithBodyInput(ctx context.Context) context.Context {
	return context.WithValue(ctx, bodyInputKey{}, true)
}
func IsBodyInput(ctx context.Context) bool { yes, _ := ctx.Value(bodyInputKey{}).(bool); return yes }

type routeBodyInputKey struct{}
type followPositionKey struct{}

// Only a normal scoped follow callback can supply its selected destination.
func WithFollowPosition(ctx context.Context, p movementinfo.FollowPosition) context.Context {
	return context.WithValue(ctx, followPositionKey{}, p)
}

func FollowPosition(ctx context.Context) (movementinfo.FollowPosition, bool) {
	p, ok := ctx.Value(followPositionKey{}).(movementinfo.FollowPosition)
	return p, ok && IsRouteBodyInput(ctx) && !IsSelfBodyInput(ctx) && p.Valid()
}

// Only the authenticated native route callback enables full surface travel.
func WithRouteBodyInput(ctx context.Context, followsOwner bool) context.Context {
	if followsOwner {
		ctx = WithBodyInput(ctx)
	} else {
		ctx = WithSelfBodyInput(ctx)
	}
	return context.WithValue(ctx, routeBodyInputKey{}, true)
}
func IsRouteBodyInput(ctx context.Context) bool {
	route, _ := ctx.Value(routeBodyInputKey{}).(bool)
	return route
}

type routeHullKey struct{}

func WithRouteHull(ctx context.Context, h modcb.RouteHull) context.Context {
	return context.WithValue(ctx, routeHullKey{}, h)
}

func RouteHull(ctx context.Context) (modcb.RouteHull, bool) {
	h, ok := ctx.Value(routeHullKey{}).(modcb.RouteHull)
	return h, ok
}

type settledBodySourceKey struct{}

func WithSettledBodySource(ctx context.Context) context.Context {
	return context.WithValue(ctx, settledBodySourceKey{}, true)
}
func IsSettledBodySource(ctx context.Context) bool {
	enabled, _ := ctx.Value(settledBodySourceKey{}).(bool)
	return enabled
}

type flightpathWorldCheckKey struct{}

// The Runtime binds the count from World to this session's ordered input.
// Only its in-process writer can supply the final socket check.
func WithFlightpathWorldCheck(ctx context.Context, current func() bool) context.Context {
	return context.WithValue(ctx, flightpathWorldCheckKey{}, current)
}

func FlightpathWorldCurrent(ctx context.Context) bool {
	current, ok := ctx.Value(flightpathWorldCheckKey{}).(func() bool)
	return ok && current != nil && current()
}
