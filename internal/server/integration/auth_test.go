//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
)

func TestAuth_HealthAnonymous(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	resp, err := env.Admin.HealthCheck(context.Background(), &huev1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, huev1.HealthStatus_HEALTH_STATUS_SERVING, resp.GetStatus())
}

func TestAuth_OwnerCanCreateReseller(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	r, err := env.Resellers.CreateReseller(ownerCtx(env), &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{
			Name:     "acme",
			Password: "p4ssw0rd-acme",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, r.GetId())
	assert.Equal(t, "acme", r.GetName())
	assert.Empty(t, r.GetPassword(), "password must not be echoed back")
}

func TestAuth_ResellerLoginRoundTrip(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	_, err := env.Resellers.CreateReseller(ownerCtx(env), &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{Name: "acme", Password: "p4ssw0rd-acme"},
	})
	require.NoError(t, err)

	login, err := env.Auth.Login(context.Background(), &huev1.LoginRequest{
		Username: "acme",
		Password: "p4ssw0rd-acme",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, login.GetAccessToken())
	assert.NotEmpty(t, login.GetRefreshToken())
	assert.Equal(t, huev1.PrincipalKind_PRINCIPAL_KIND_RESELLER, login.GetKind())

	// Reseller JWT can list resellers (positive-authorization permits Reseller).
	_, err = env.Resellers.ListResellers(jwtCtx(login.GetAccessToken()), &huev1.ListResellersRequest{})
	require.NoError(t, err)
}

func TestAuth_WrongPassword_Unauthenticated(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, err := env.Resellers.CreateReseller(ownerCtx(env), &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{Name: "acme", Password: "right"},
	})
	require.NoError(t, err)
	_, err = env.Auth.Login(context.Background(), &huev1.LoginRequest{
		Username: "acme", Password: "wrong",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuth_OwnerSudoBypassesPassword(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, err := env.Resellers.CreateReseller(ownerCtx(env), &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{Name: "acme", Password: "real-pass"},
	})
	require.NoError(t, err)

	// Owner key + Login(username, "anything") → sudo JWT.
	login, err := env.Auth.Login(ownerCtx(env), &huev1.LoginRequest{
		Username: "acme",
		Password: "anything",
	})
	require.NoError(t, err, "Owner sudo should accept any password")
	assert.NotEmpty(t, login.GetAccessToken())
	assert.Equal(t, huev1.PrincipalKind_PRINCIPAL_KIND_RESELLER, login.GetKind())
}

func TestAuth_RejectedKindMismatch(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	// Reseller logs in...
	_, err := env.Resellers.CreateReseller(ownerCtx(env), &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{Name: "acme", Password: "ok"},
	})
	require.NoError(t, err)
	login, err := env.Auth.Login(context.Background(), &huev1.LoginRequest{
		Username: "acme", Password: "ok",
	})
	require.NoError(t, err)

	// ...then tries an Owner-only endpoint → PermissionDenied.
	_, err = env.Admin.CreateNode(jwtCtx(login.GetAccessToken()), &huev1.CreateNodeRequest{
		Node: &huev1.Node{Name: "should-fail"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}
