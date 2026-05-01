//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hiddify/hue/internal/ent"
	entuser "github.com/hiddify/hue/internal/ent/user"
	"github.com/hiddify/hue/internal/service"
)

// withPlan creates a user + an active plan with the given limits and
// returns both. Tests use the engine directly to bypass the gRPC layer
// where it's not what's under test.
func withPlan(t *testing.T, env *testEnv, totalLimit int64, maxConcurrent int32) (*ent.User, *ent.UsagePlan) {
	t.Helper()
	ctx := context.Background()

	u, err := env.DB.User.Create().
		SetUsername("u-" + uuid.NewString()[:8]).
		Save(ctx)
	require.NoError(t, err)

	p, err := env.DB.UsagePlan.Create().
		SetUserID(u.ID).
		SetTotalLimit(totalLimit).
		SetMaxConcurrent(maxConcurrent).
		Save(ctx)
	require.NoError(t, err)

	_, err = env.DB.User.UpdateOneID(u.ID).SetActivePlanID(p.ID).Save(ctx)
	require.NoError(t, err)

	return u, p
}

func TestReportUsage_HappyPath(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	u, p := withPlan(t, env, 0, 0) // unlimited

	d, err := env.Engine.ReportUsage(context.Background(), service.ReportInput{
		UserID:   u.ID,
		Upload:   1024,
		Download: 4096,
		At:       time.Now(),
	})
	require.NoError(t, err)
	assert.True(t, d.Accepted)

	// Counters advanced.
	got, err := env.DB.UsagePlan.Get(context.Background(), p.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1024, got.CurrentUpload)
	assert.EqualValues(t, 4096, got.CurrentDownload)
	assert.EqualValues(t, 5120, got.CurrentTotal)
}

func TestReportUsage_NoActivePlan_Rejected(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	u, err := env.DB.User.Create().SetUsername("orphan").Save(context.Background())
	require.NoError(t, err)

	d, err := env.Engine.ReportUsage(context.Background(), service.ReportInput{
		UserID: u.ID, Upload: 1024,
	})
	require.NoError(t, err)
	assert.False(t, d.Accepted)
	assert.Contains(t, d.Reason, "no active plan")
}

func TestReportUsage_QuotaExceeded_SuspendsUser(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	u, p := withPlan(t, env, 1024 /*total_limit*/, 0)

	d, err := env.Engine.ReportUsage(context.Background(), service.ReportInput{
		UserID:   u.ID,
		Upload:   2048, // exceeds total_limit
		At:       time.Now(),
	})
	require.NoError(t, err)
	assert.False(t, d.Accepted)
	assert.True(t, d.QuotaExceeded)
	assert.True(t, d.ShouldDisconnect)

	// Engine flips both User.status and UsagePlan.status to quota_used.
	gotUser, err := env.DB.User.Query().Where(entuser.ID(u.ID)).Only(context.Background())
	require.NoError(t, err)
	assert.Equal(t, entuser.StatusQuotaUsed, gotUser.Status)

	gotPlan, err := env.DB.UsagePlan.Get(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, "quota_used", string(gotPlan.Status))
}

func TestReportUsage_InactiveUser_Rejected(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	u, _ := withPlan(t, env, 0, 0)

	_, err := env.DB.User.UpdateOneID(u.ID).SetStatus(entuser.StatusInactive).Save(context.Background())
	require.NoError(t, err)

	d, err := env.Engine.ReportUsage(context.Background(), service.ReportInput{
		UserID: u.ID, Upload: 100,
	})
	require.NoError(t, err)
	assert.False(t, d.Accepted)
	assert.True(t, d.ShouldDisconnect)
}

func TestReportUsage_PenaltyShortCircuits(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	u, p := withPlan(t, env, 0, 0)

	// Force a penalty.
	until := env.Penalty.Apply(u.ID, time.Now())

	d, err := env.Engine.ReportUsage(context.Background(), service.ReportInput{
		UserID: u.ID, Upload: 500,
	})
	require.NoError(t, err)
	assert.False(t, d.Accepted)
	assert.True(t, d.ShouldDisconnect)
	assert.Equal(t, until.UTC().Truncate(time.Second), d.PenaltyUntil.UTC().Truncate(time.Second))

	// No counters advanced — penalty short-circuits before any DB work.
	got, err := env.DB.UsagePlan.Get(context.Background(), p.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, got.CurrentTotal)
}

func TestReportUsage_SessionLimitTriggersPenalty(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	u, _ := withPlan(t, env, 0 /*unlimited*/, 1 /*max_concurrent=1*/)

	// First IP — accepted.
	d, err := env.Engine.ReportUsage(context.Background(), service.ReportInput{
		UserID: u.ID, Upload: 100, ClientIP: "10.0.0.1", SessionID: "s1",
	})
	require.NoError(t, err)
	assert.True(t, d.Accepted)

	// Second IP within window — over limit, penalty applied.
	d, err = env.Engine.ReportUsage(context.Background(), service.ReportInput{
		UserID: u.ID, Upload: 100, ClientIP: "10.0.0.2", SessionID: "s2",
	})
	require.NoError(t, err)
	assert.False(t, d.Accepted)
	assert.True(t, d.SessionLimitHit)
	assert.True(t, d.ShouldDisconnect)
	assert.False(t, d.PenaltyUntil.IsZero())

	// Verify the engine actually recorded the penalty in memory.
	active, _ := env.Penalty.Active(u.ID, time.Now())
	assert.True(t, active)
}

// Concurrent ReportUsage calls for the same user must serialize through
// the per-user lock and produce a consistent counter — REVIEW.md H2 +
// the missing race-flagged concurrency test from the audit.
func TestReportUsage_ConcurrentSameUser_NoLost(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	u, p := withPlan(t, env, 0, 0)

	const goroutines = 50
	const eachUpload = 100

	errs := make(chan error, goroutines)
	for range goroutines {
		go func() {
			_, err := env.Engine.ReportUsage(context.Background(), service.ReportInput{
				UserID: u.ID, Upload: eachUpload,
			})
			errs <- err
		}()
	}
	for range goroutines {
		require.NoError(t, <-errs)
	}

	got, err := env.DB.UsagePlan.Get(context.Background(), p.ID)
	require.NoError(t, err)
	assert.EqualValues(t, goroutines*eachUpload, got.CurrentUpload,
		"all reports must accumulate exactly — no lost updates under contention")
}
