package auth

import (
	"context"

	"github.com/google/uuid"
)

// Actor identifies the authenticated principal that made the request.
// Service-layer code uses this to enforce per-actor authorization (e.g.,
// "manager X can only modify users where manager_id = X").
type Actor struct {
	Kind    ActorKind
	OwnerID uuid.UUID // manager_id / service_id / node_id
	KeyID   uuid.UUID // api_key.id used (audit trail)
}

type actorCtxKey struct{}

// WithActor returns a new context carrying a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// FromContext extracts the Actor; the second return is false when the
// request is unauthenticated (e.g., health check).
func FromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorCtxKey{}).(Actor)
	return a, ok
}

// MustFromContext is the convenience accessor used by handlers that have
// already passed through the auth interceptor; it panics otherwise.
// Reserve for code paths that are unreachable on unauthenticated routes.
func MustFromContext(ctx context.Context) Actor {
	a, ok := FromContext(ctx)
	if !ok {
		panic("auth: missing Actor on context — was the auth interceptor wired?")
	}
	return a
}
