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

// loginAsReseller helper: create reseller via Owner, log in as them,
// return the JWT context.
func loginAsReseller(t *testing.T, env *testEnv, name, password string) (string, context.Context) {
	t.Helper()
	r, err := env.Resellers.CreateReseller(ownerCtx(env), &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{Name: name, Password: password},
	})
	require.NoError(t, err)
	login, err := env.Auth.Login(context.Background(), &huev1.LoginRequest{
		Username: name, Password: password,
	})
	require.NoError(t, err)
	return r.GetId(), jwtCtx(login.GetAccessToken())
}

// Two siblings can each see their own clients, never each other's,
// even by direct GetClient on the right id.
func TestScope_ResellerSeesOwnClientsOnly(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	aID, aCtx := loginAsReseller(t, env, "reseller-a", "pwA")
	_, bCtx := loginAsReseller(t, env, "reseller-b", "pwB")

	// Reseller A creates a client (no reseller_id → defaults to caller).
	aliceA, err := env.ResellerClients.CreateClient(aCtx, &huev1.CreateClientRequest{
		Client: &huev1.Client{
			AuthMethod: &huev1.ClientAuthMethod{Username: "alice"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, aID, aliceA.GetInfo().GetResellerId(),
		"new client should inherit caller's reseller id")

	// Reseller B's list is empty — A's client is invisible.
	bList, err := env.ResellerClients.ListClients(bCtx, &huev1.ListClientsRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(0), bList.GetTotal())

	// Direct GetClient by A's client id is also NotFound for B.
	_, err = env.ResellerClients.GetClient(bCtx, &huev1.GetClientRequest{Id: aliceA.GetInfo().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))

	// B can't delete A's client either.
	_, err = env.ResellerClients.DeleteClient(bCtx, &huev1.DeleteClientRequest{Id: aliceA.GetInfo().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))

	// A still sees alice.
	aList, err := env.ResellerClients.ListClients(aCtx, &huev1.ListClientsRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), aList.GetTotal())

	// Owner sees everything.
	allList, err := env.ResellerClients.ListClients(ownerCtx(env), &huev1.ListClientsRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), allList.GetTotal())
}

// Reseller cannot create a client assigned to another reseller.
func TestScope_ResellerCannotAssignOutsideSubtree(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, aCtx := loginAsReseller(t, env, "reseller-a", "pw")
	bID, _ := loginAsReseller(t, env, "reseller-b", "pw")

	_, err := env.ResellerClients.CreateClient(aCtx, &huev1.CreateClientRequest{
		Client: &huev1.Client{
			Info:       &huev1.ClientInfo{ResellerId: bID},
			AuthMethod: &huev1.ClientAuthMethod{Username: "intruder"},
		},
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// A reseller creates a sub-reseller; that sub-reseller's clients are
// visible to the parent (the subtree includes descendants).
func TestScope_ParentSeesSubResellerClients(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	parentID, parentCtx := loginAsReseller(t, env, "parent", "pp")

	// Parent creates a sub-reseller. parent_id defaults to caller.
	sub, err := env.Resellers.CreateReseller(parentCtx, &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{Name: "sub", Password: "sp"},
	})
	require.NoError(t, err)
	assert.Equal(t, parentID, sub.GetParentId())

	// Sub logs in, creates a client.
	subLogin, err := env.Auth.Login(context.Background(), &huev1.LoginRequest{
		Username: "sub", Password: "sp",
	})
	require.NoError(t, err)
	_, err = env.ResellerClients.CreateClient(jwtCtx(subLogin.GetAccessToken()), &huev1.CreateClientRequest{
		Client: &huev1.Client{AuthMethod: &huev1.ClientAuthMethod{Username: "subclient"}},
	})
	require.NoError(t, err)

	// Parent's list includes the sub-reseller's client (subtree visibility).
	parentList, err := env.ResellerClients.ListClients(parentCtx, &huev1.ListClientsRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), parentList.GetTotal())
}

// Reseller cannot view a sibling reseller via Management.
func TestScope_ResellerCannotSeeSiblingReseller(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, aCtx := loginAsReseller(t, env, "r-a", "x")
	bID, _ := loginAsReseller(t, env, "r-b", "x")

	_, err := env.Resellers.GetReseller(aCtx, &huev1.GetResellerRequest{Id: bID})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))

	list, err := env.Resellers.ListResellers(aCtx, &huev1.ListResellersRequest{})
	require.NoError(t, err)
	// Reseller A only sees themselves in the subtree.
	assert.Equal(t, 1, len(list.GetResellers()))
}
