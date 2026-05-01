package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hiddify/hue/internal/ent"
	entnode "github.com/hiddify/hue/internal/ent/node"
	entservice "github.com/hiddify/hue/internal/ent/service"
	entuser "github.com/hiddify/hue/internal/ent/user"
	entusageplan "github.com/hiddify/hue/internal/ent/usageplan"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/geo"
)

// Engine orchestrates ReportUsage from raw input to the persisted decision.
//
// Critical-section discipline (REVIEW.md H2): math runs under the per-user
// lock; all DB writes happen inside one transaction; the lock is released
// as soon as the transaction commits.
type Engine struct {
	db        *ent.Client
	geo       *geo.Resolver
	locks     *LockManager
	sessions  *SessionTracker
	penalties *PenaltyTracker
	managers  *ManagerHierarchy
	events    *eventstore.Store
	now       func() time.Time // injectable for tests
}

// EngineDeps groups Engine dependencies for the constructor. Pass real
// values from main.go; pass test doubles from tests.
type EngineDeps struct {
	DB        *ent.Client
	Geo       *geo.Resolver
	Locks     *LockManager
	Sessions  *SessionTracker
	Penalties *PenaltyTracker
	Managers  *ManagerHierarchy
	Events    *eventstore.Store
	Now       func() time.Time // optional; defaults to time.Now
}

func NewEngine(d EngineDeps) *Engine {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Engine{
		db:        d.DB,
		geo:       d.Geo,
		locks:     d.Locks,
		sessions:  d.Sessions,
		penalties: d.Penalties,
		managers:  d.Managers,
		events:    d.Events,
		now:       now,
	}
}

// ReportInput is what the gRPC layer translates an inbound UsageReport
// into. ClientIP is nulled by the engine immediately after geo + session
// processing; do not retain it in the caller.
type ReportInput struct {
	UserID    uuid.UUID
	NodeID    uuid.UUID
	ServiceID uuid.UUID
	Upload    int64
	Download  int64
	SessionID string
	ClientIP  string
	Tags      []string
	At        time.Time // zero = engine.now()
}

// Decision mirrors proto's UsageDecision but lives in the service package
// so the engine has no proto dependencies.
type Decision struct {
	Accepted         bool
	QuotaExceeded    bool
	SessionLimitHit  bool
	ShouldDisconnect bool
	Reason           string
	PenaltyUntil     time.Time
}

// ReportUsage is the hot path. Decision flow:
//   1. Drop the IP into geo + session as early as possible.
//   2. Reject on penalty.
//   3. Acquire per-user lock.
//   4. Load user + active plan; reject if user inactive or plan expired.
//   5. Apply node multiplier; project new counters; reject on quota.
//   6. Project against manager hierarchy; reject on ancestor limit.
//   7. Commit user + node + service + manager updates in one tx.
//   8. Emit USAGE_RECORDED. On suspension, also emit USER_SUSPENDED.
func (e *Engine) ReportUsage(ctx context.Context, in ReportInput) (Decision, error) {
	if in.At.IsZero() {
		in.At = e.now()
	}

	// Step 1: geo + session, then drop the IP.
	var (
		geoData geo.Result
		ipHash  string
	)
	if in.ClientIP != "" {
		geoData, _ = e.geo.Lookup(in.ClientIP)
		ipHash = e.sessions.HashIP(in.ClientIP)
		in.ClientIP = ""
	}

	// Step 2: penalty short-circuit.
	if active, until := e.penalties.Active(in.UserID, in.At); active {
		return Decision{
			Accepted:         false,
			ShouldDisconnect: true,
			Reason:           "user is in penalty",
			PenaltyUntil:     until,
		}, nil
	}

	// Step 3: per-user lock — all subsequent reads + writes are serialized
	// for this user but parallel for everyone else.
	release := e.locks.Acquire(in.UserID)
	defer release()

	// Step 4: load user + plan.
	u, err := e.db.User.Query().
		Where(entuser.ID(in.UserID)).
		WithActivePlan().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return Decision{Reason: "user not found", ShouldDisconnect: true}, nil
		}
		return Decision{}, fmt.Errorf("load user: %w", err)
	}
	if u.Status != entuser.StatusActive {
		return Decision{
			ShouldDisconnect: true,
			Reason:           fmt.Sprintf("user status: %s", u.Status),
		}, nil
	}
	plan := u.Edges.ActivePlan
	if plan == nil {
		return Decision{Reason: "no active plan"}, nil
	}
	if plan.ExpiresAt != nil && !plan.ExpiresAt.After(in.At) {
		return Decision{
			QuotaExceeded:    true,
			ShouldDisconnect: true,
			Reason:           "plan expired",
		}, nil
	}

	// Step 5: apply node multiplier; project new counters.
	upload, download := in.Upload, in.Download
	var node *ent.Node
	if in.NodeID != uuid.Nil {
		node, err = e.db.Node.Query().Where(entnode.ID(in.NodeID)).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return Decision{}, fmt.Errorf("load node: %w", err)
		}
		if node != nil && node.TrafficMultiplier > 0 {
			upload = int64(float64(upload) * node.TrafficMultiplier)
			download = int64(float64(download) * node.TrafficMultiplier)
		}
	}
	total := upload + download

	newTotal := plan.CurrentTotal + total
	newUpload := plan.CurrentUpload + upload
	newDownload := plan.CurrentDownload + download

	switch {
	case plan.TotalLimit > 0 && newTotal > plan.TotalLimit:
		return e.suspendForQuota(ctx, u, plan, "total limit"), nil
	case plan.UploadLimit > 0 && newUpload > plan.UploadLimit:
		return e.suspendForQuota(ctx, u, plan, "upload limit"), nil
	case plan.DownloadLimit > 0 && newDownload > plan.DownloadLimit:
		return e.suspendForQuota(ctx, u, plan, "download limit"), nil
	}

	// Step 5b: session check (post-IP drop, hash-based).
	sessionCount := e.sessions.Touch(u.ID, ipHash, in.At)
	if plan.MaxConcurrent > 0 && int32(sessionCount) > plan.MaxConcurrent {
		until := e.penalties.Apply(u.ID, in.At)
		_ = e.events.Append(ctx, eventstore.Event{
			Type:      "penalty_applied",
			UserID:    u.ID.String(),
			PlanID:    plan.ID.String(),
			Timestamp: in.At,
			Metadata: map[string]any{
				"reason":      "max_concurrent_exceeded",
				"observed":    sessionCount,
				"limit":       plan.MaxConcurrent,
				"penalty_end": until,
			},
		})
		return Decision{
			SessionLimitHit:  true,
			ShouldDisconnect: true,
			Reason:           "concurrent session limit",
			PenaltyUntil:     until,
		}, nil
	}

	// Step 6: manager hierarchy projection.
	if u.ManagerID != nil {
		breach, reason, err := e.managers.CheckUsageDelta(ctx, *u.ManagerID, upload, download)
		if err != nil {
			return Decision{}, fmt.Errorf("manager check: %w", err)
		}
		if breach != nil {
			return Decision{
				QuotaExceeded:    true,
				ShouldDisconnect: true,
				Reason:           fmt.Sprintf("%s on %s", reason, breach.Name),
			}, nil
		}
	}

	// Step 7: persist in one transaction.
	tx, err := e.db.Tx(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("begin tx: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.UsagePlan.Update().
		Where(entusageplan.ID(plan.ID)).
		AddCurrentTotal(total).
		AddCurrentUpload(upload).
		AddCurrentDownload(download).
		Save(ctx); err != nil {
		return Decision{}, fmt.Errorf("update plan: %w", err)
	}
	if _, err := tx.User.UpdateOneID(u.ID).
		SetLastConnectionAt(in.At).
		Save(ctx); err != nil {
		return Decision{}, fmt.Errorf("update user: %w", err)
	}
	if node != nil {
		if _, err := tx.Node.UpdateOneID(node.ID).
			AddCurrentTotal(total).
			AddCurrentUpload(upload).
			AddCurrentDownload(download).
			Save(ctx); err != nil {
			return Decision{}, fmt.Errorf("update node: %w", err)
		}
	}
	if in.ServiceID != uuid.Nil {
		if _, err := tx.Service.Update().
			Where(entservice.ID(in.ServiceID)).
			AddCurrentTotal(total).
			AddCurrentUpload(upload).
			AddCurrentDownload(download).
			Save(ctx); err != nil && !ent.IsNotFound(err) {
			return Decision{}, fmt.Errorf("update service: %w", err)
		}
	}
	if u.ManagerID != nil {
		if err := e.managers.ApplyUsageDelta(ctx, tx, *u.ManagerID, upload, download); err != nil {
			return Decision{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, fmt.Errorf("commit: %w", err)
	}
	commit = true

	// Step 8: emit event after commit (best-effort; do not fail the report).
	_ = e.events.Append(ctx, eventstore.Event{
		Type:      "usage_recorded",
		UserID:    u.ID.String(),
		PlanID:    plan.ID.String(),
		NodeID:    nodeIDStr(node),
		ServiceID: serviceIDStr(in.ServiceID),
		Tags:      in.Tags,
		Timestamp: in.At,
		Metadata: map[string]any{
			"upload":   upload,
			"download": download,
			"geo":      geoData,
		},
	})

	return Decision{Accepted: true}, nil
}

// suspendForQuota marks user as quota-used + emits the suspension event.
// On any DB error here we still report the decision — the goal is to
// disconnect the user; an internal write failure shouldn't let them keep
// going.
func (e *Engine) suspendForQuota(ctx context.Context, u *ent.User, plan *ent.UsagePlan, reason string) Decision {
	_, err := e.db.User.UpdateOneID(u.ID).SetStatus(entuser.StatusQuotaUsed).Save(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		// Log only — return decision regardless.
	}
	_, _ = e.db.UsagePlan.UpdateOneID(plan.ID).SetStatus(entusageplan.StatusQuotaUsed).Save(ctx)
	_ = e.events.Append(ctx, eventstore.Event{
		Type:      "user_suspended",
		UserID:    u.ID.String(),
		PlanID:    plan.ID.String(),
		Timestamp: e.now(),
		Metadata:  map[string]any{"reason": reason},
	})
	return Decision{
		QuotaExceeded:    true,
		ShouldDisconnect: true,
		Reason:           reason,
	}
}

func nodeIDStr(n *ent.Node) string {
	if n == nil {
		return ""
	}
	return n.ID.String()
}

func serviceIDStr(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}
