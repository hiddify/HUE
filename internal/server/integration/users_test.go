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
	entuser "github.com/hiddify/hue/internal/ent/user"
)

func TestCreateUser_HashesPassword(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	got, err := env.Admin.CreateUser(ctx, &huev1.CreateUserRequest{
		User: &huev1.User{
			Info: &huev1.UserInfo{Groups: []string{"crud"}},
			AuthMethod: &huev1.UserAuthMethod{
				Username: "alice",
				Password: "p4ssw0rd",
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, got.GetInfo().GetId())

	// The plaintext is never echoed back on the response.
	assert.Empty(t, got.GetAuthMethod().GetPassword(), "password must not appear in response")

	// Persisted hash must verify against the original plaintext.
	id, err := uuid.Parse(got.GetInfo().GetId())
	require.NoError(t, err)
	row, err := env.DB.User.Query().Where(entuser.ID(id)).Only(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, "p4ssw0rd", row.PasswordHash, "stored value must not be plaintext")
	assert.NoError(t, auth.VerifyToken("p4ssw0rd", row.PasswordHash))
}

func TestGetUser_NotFound(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	_, err := env.Admin.GetUser(ctx, &huev1.GetUserRequest{Id: uuid.New().String()})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetUser_BadID(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	_, err := env.Admin.GetUser(ctx, &huev1.GetUserRequest{Id: "not-a-uuid"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestListUsers_FiltersByStatus(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	// Two ACTIVE, one SUSPENDED.
	for _, name := range []string{"a", "b", "c"} {
		req := &huev1.CreateUserRequest{User: &huev1.User{
			Info:       &huev1.UserInfo{},
			AuthMethod: &huev1.UserAuthMethod{Username: name},
		}}
		if name == "c" {
			req.User.Info.Status = huev1.UserStatus_USER_STATUS_SUSPENDED
		}
		_, err := env.Admin.CreateUser(ctx, req)
		require.NoError(t, err)
	}

	all, err := env.Admin.ListUsers(ctx, &huev1.ListUsersRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(3), all.GetTotal())

	active, err := env.Admin.ListUsers(ctx, &huev1.ListUsersRequest{
		Status: huev1.UserStatus_USER_STATUS_ACTIVE,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), active.GetTotal())

	suspended, err := env.Admin.ListUsers(ctx, &huev1.ListUsersRequest{
		Status: huev1.UserStatus_USER_STATUS_SUSPENDED,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), suspended.GetTotal())
}

func TestUpdateUser_ChangesGroupsAndPassword(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	created, err := env.Admin.CreateUser(ctx, &huev1.CreateUserRequest{
		User: &huev1.User{
			Info:       &huev1.UserInfo{Groups: []string{"old"}},
			AuthMethod: &huev1.UserAuthMethod{Username: "u", Password: "old-pw"},
		},
	})
	require.NoError(t, err)

	updated, err := env.Admin.UpdateUser(ctx, &huev1.UpdateUserRequest{
		Id: created.GetInfo().GetId(),
		User: &huev1.User{
			Info:       &huev1.UserInfo{Groups: []string{"new", "vip"}},
			AuthMethod: &huev1.UserAuthMethod{Password: "new-pw"},
		},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"new", "vip"}, updated.GetInfo().GetGroups())

	// Stored hash now matches the new password, not the old.
	id, _ := uuid.Parse(created.GetInfo().GetId())
	row, err := env.DB.User.Query().Where(entuser.ID(id)).Only(ctx)
	require.NoError(t, err)
	assert.NoError(t, auth.VerifyToken("new-pw", row.PasswordHash))
	assert.Error(t, auth.VerifyToken("old-pw", row.PasswordHash))
}

func TestDeleteUser_Removes(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	created, err := env.Admin.CreateUser(ctx, &huev1.CreateUserRequest{
		User: &huev1.User{
			AuthMethod: &huev1.UserAuthMethod{Username: "ephemeral"},
		},
	})
	require.NoError(t, err)

	_, err = env.Admin.DeleteUser(ctx, &huev1.DeleteUserRequest{Id: created.GetInfo().GetId()})
	require.NoError(t, err)

	_, err = env.Admin.GetUser(ctx, &huev1.GetUserRequest{Id: created.GetInfo().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestCreateUser_DuplicateUsername_Rejected(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := authCtx(env)

	req := &huev1.CreateUserRequest{User: &huev1.User{
		AuthMethod: &huev1.UserAuthMethod{Username: "dup"},
	}}
	_, err := env.Admin.CreateUser(ctx, req)
	require.NoError(t, err)

	_, err = env.Admin.CreateUser(ctx, req)
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
}
