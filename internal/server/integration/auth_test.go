//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
)

// AdminService rejects calls with no Authorization metadata.
func TestAuth_MissingToken_Rejected(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	_, err := env.Admin.ListUsers(context.Background(), &huev1.ListUsersRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

// AdminService rejects unknown bearer tokens (DB lookup fails).
func TestAuth_UnknownToken_Rejected(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	_, err := env.Admin.ListUsers(authCtxWith("mgr_thisistotallymadeup1234567890"),
		&huev1.ListUsersRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

// AdminService accepts the bootstrap token (Argon2id verify against DB).
func TestAuth_BootstrapToken_Accepted(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	resp, err := env.Admin.ListUsers(authCtx(env), &huev1.ListUsersRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.GetTotal())
}

// Newly created API keys authenticate successfully and are honored
// straight away (no caching layer between issuance and verification).
func TestAuth_FreshKey_AuthenticatesSameSecond(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	// Need an owner (manager) to attach the new service key to.
	mgr, err := env.Admin.CreateManager(ctx, &huev1.CreateManagerRequest{
		Manager: &huev1.Manager{Name: "owner"},
	})
	require.NoError(t, err)

	created, err := env.Admin.CreateApiKey(ctx, &huev1.CreateApiKeyRequest{
		ApiKey: &huev1.ApiKey{
			Kind:    huev1.ApiKeyKind_API_KEY_KIND_SERVICE,
			OwnerId: mgr.GetId(),
			Name:    "fresh",
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.GetToken())

	// Use the just-issued token.
	_, err = env.Admin.ListUsers(authCtxWith(created.GetToken()),
		&huev1.ListUsersRequest{})
	require.NoError(t, err, "freshly issued key must authenticate")
}

// Revoked tokens are rejected on the next request.
func TestAuth_RevokedKey_Rejected(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	mgr, err := env.Admin.CreateManager(ctx,
		&huev1.CreateManagerRequest{Manager: &huev1.Manager{Name: "owner"}})
	require.NoError(t, err)

	created, err := env.Admin.CreateApiKey(ctx, &huev1.CreateApiKeyRequest{
		ApiKey: &huev1.ApiKey{
			Kind:    huev1.ApiKeyKind_API_KEY_KIND_SERVICE,
			OwnerId: mgr.GetId(),
			Name:    "soon-revoked",
		},
	})
	require.NoError(t, err)

	// Works before revocation.
	_, err = env.Admin.ListUsers(authCtxWith(created.GetToken()), &huev1.ListUsersRequest{})
	require.NoError(t, err)

	// Revoke.
	_, err = env.Admin.RevokeApiKey(ctx, &huev1.RevokeApiKeyRequest{Id: created.GetApiKey().GetId()})
	require.NoError(t, err)

	// Rejected after revocation.
	_, err = env.Admin.ListUsers(authCtxWith(created.GetToken()), &huev1.ListUsersRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

// Health check is reachable without auth (whitelisted in interceptors).
func TestAuth_HealthCheck_NoAuthRequired(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := env.Admin.HealthCheck(ctx, &huev1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, huev1.HealthStatus_HEALTH_STATUS_SERVING, resp.GetStatus())
}
