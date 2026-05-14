package auth

import (
	"context"

	"github.com/google/uuid"
)

// PrincipalKind covers both API-key actors (Owner, Agent) and JWT
// actors (Subscriber/Client, Reseller). The auth interceptor sets it
// uniformly so service-layer code reads one field.
type PrincipalKind string

const (
	PrincipalKindOwner      PrincipalKind = "owner"
	PrincipalKindAgent      PrincipalKind = "agent"
	PrincipalKindReseller   PrincipalKind = "reseller"
	PrincipalKindSubscriber PrincipalKind = "subscriber"
)

// Actor identifies the authenticated principal that made the request.
//
//   * Kind         — which principal type.
//   * SubjectID    — the entity row id (Subscriber.id / Reseller.id /
//                    Agent.id; uuid.Nil when Kind == Owner since
//                    owner is singleton).
//   * KeyID        — ApiKey.id for API-key auth; uuid.Nil for JWT auth.
//   * SudoTargetID — when Kind == Owner and the owner sudoed via
//                    AuthService.Login, this is the impersonated
//                    principal id. Audit trail.
type Actor struct {
	Kind         PrincipalKind
	SubjectID    uuid.UUID
	KeyID        uuid.UUID
	SudoTargetID uuid.UUID
}

type actorCtxKey struct{}

// WithActor returns a new context carrying a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// FromContext extracts the Actor; ok=false on unauthenticated routes.
func FromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorCtxKey{}).(Actor)
	return a, ok
}

// MustFromContext panics if no Actor — reserve for handlers reachable
// only via the auth interceptor.
func MustFromContext(ctx context.Context) Actor {
	a, ok := FromContext(ctx)
	if !ok {
		panic("auth: missing Actor on context — was the auth interceptor wired?")
	}
	return a
}
