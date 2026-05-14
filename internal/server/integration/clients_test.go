//go:build integration

package integration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	entsubscriber "github.com/hiddify/hue/internal/ent/subscriber"
)

func TestClient_CreateEncryptsPasswordAtRest(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	created, err := env.ResellerClients.CreateClient(ownerCtx(env), &huev1.CreateClientRequest{
		Client: &huev1.Client{
			Info: &huev1.ClientInfo{Groups: []string{"premium"}},
			AuthMethod: &huev1.ClientAuthMethod{
				Username: "alice",
				Password: "alice-secret",
			},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.GetInfo().GetId())
	assert.Empty(t, created.GetAuthMethod().GetPassword(), "password must not be echoed")

	// Pull the raw ciphertext from the DB and confirm it's not plaintext.
	id, err := uuid.Parse(created.GetInfo().GetId())
	require.NoError(t, err)
	row, err := env.DB.Subscriber.Query().Where(entsubscriber.ID(id)).Only(ownerCtx(env))
	require.NoError(t, err)
	assert.NotEqual(t, "alice-secret", string(row.PasswordCiphertext), "stored must not be plaintext")

	plain, err := auth.Decrypt(row.PasswordCiphertext, row.PasswordKeyID)
	require.NoError(t, err)
	assert.Equal(t, "alice-secret", string(plain), "decrypt must round-trip")
}

func TestClient_ListByStatus(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := ownerCtx(env)

	for _, name := range []string{"a", "b", "c"} {
		req := &huev1.CreateClientRequest{Client: &huev1.Client{
			Info:       &huev1.ClientInfo{},
			AuthMethod: &huev1.ClientAuthMethod{Username: name},
		}}
		if name == "c" {
			req.Client.Info.Status = huev1.ClientStatus_CLIENT_STATUS_SUSPENDED
		}
		_, err := env.ResellerClients.CreateClient(ctx, req)
		require.NoError(t, err)
	}

	all, err := env.ResellerClients.ListClients(ctx, &huev1.ListClientsRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(3), all.GetTotal())

	active, err := env.ResellerClients.ListClients(ctx, &huev1.ListClientsRequest{
		Status: huev1.ClientStatus_CLIENT_STATUS_ACTIVE,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), active.GetTotal())
}

func TestClient_DuplicateUsernameRejected(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := ownerCtx(env)
	req := &huev1.CreateClientRequest{Client: &huev1.Client{
		AuthMethod: &huev1.ClientAuthMethod{Username: "dup"},
	}}
	_, err := env.ResellerClients.CreateClient(ctx, req)
	require.NoError(t, err)
	_, err = env.ResellerClients.CreateClient(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
}

func TestClient_DeleteRemoves(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := ownerCtx(env)
	created, err := env.ResellerClients.CreateClient(ctx, &huev1.CreateClientRequest{
		Client: &huev1.Client{AuthMethod: &huev1.ClientAuthMethod{Username: "drop"}},
	})
	require.NoError(t, err)
	_, err = env.ResellerClients.DeleteClient(ctx, &huev1.DeleteClientRequest{Id: created.GetInfo().GetId()})
	require.NoError(t, err)
	_, err = env.ResellerClients.GetClient(ctx, &huev1.GetClientRequest{Id: created.GetInfo().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
