package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hiddify/hue/internal/ent"
	entagent "github.com/hiddify/hue/internal/ent/agent"
	entnode "github.com/hiddify/hue/internal/ent/node"
	entsubscriber "github.com/hiddify/hue/internal/ent/subscriber"
	entusageplan "github.com/hiddify/hue/internal/ent/usageplan"
	"github.com/hiddify/hue/internal/eventstore"
	"github.com/hiddify/hue/internal/geo"
)

// Engine orchestrates ReportUsage from raw input to the persisted decision.
//
// Critical-section discipline (REVIEW.md H2): math runs under the
// per-subscriber lock; all DB writes happen inside one transaction;
// the lock is released as soon as the transaction commits.
//
// Phase-2 vocabulary: Subscriber (was User) is the ent type; Client is
// the API noun. Agent (was Service) is the wire-side process on a Node.
// Reseller (was Manager) is the admin tree.
type Engine struct {
	db        *ent.Client
	geo       *geo.Resolver
	locks     *LockManager
	sessions  *SessionTracker
	penalties *PenaltyTracker
	resellers *ResellerHierarchy
	events    *eventstore.Store
	now       func() time.Time
}

// EngineDeps groups Engine dependencies for the constructor.
type EngineDeps struct {
	DB        *ent.Client
	Geo       *geo.Resolver
	Locks     *LockManager
	Sessions  *SessionTracker
	Penalties *PenaltyTracker
	Resellers *ResellerHierarchy
	Events    *eventstore.Store
	Now       func() time.Time
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
		resellers: d.Resellers,
		events:    d.Events,
		now:       now,
	}
}

// ReportInput is what the gRPC layer translates an inbound UsageReport
// into. ClientIP is nulled by the engine immediately after geo + session
// processing.
type ReportInput struct {
	ClientID  uuid.UUID
	NodeID    uuid.UUID
	AgentID   uuid.UUID
	Upload    int64
	Download  int64
	SessionID string
	ClientIP  string
	Tags      []string
	At        time.Time // zero = engine.now()
}

// Decision mirrors proto's UsageDecision but lives in the service package.
type Decision struct {
	Accepted         bool
	QuotaExceeded    bool
	SessionLimitHit  bool
	ShouldDisconnect bool
	Reason           string
	PenaltyUntil     time.Time
}

// ReportUsage decision flow:
//   1. geo + session, then drop IP.
//   2. Reject on penalty.
//   3. Acquire per-subscriber lock.
//   4. Load subscriber + active plan; reject if inactive or plan expired.
//   5. Apply node multiplier; project new counters; reject on quota.
//   6. Project against reseller hierarchy.
//   7. Commit subscriber + node + agent + reseller updates in one tx.
//   8. Emit USAGE_RECORDED; on suspension also CLIENT_SUSPENDED.
func (e *Engine) ReportUsage(ctx context.Context, in ReportInput) (Decision, error) {
	if in.At.IsZero() {
		in.At = e.now()
	}

	var (
		geoData geo.Result
		ipHash  string
	)
	if in.ClientIP != "" {
		geoData, _ = e.geo.Lookup(in.ClientIP)
		ipHash = e.sessions.HashIP(in.ClientIP)
		in.ClientIP = ""
	}

	if active, until := e.penalties.Active(in.ClientID, in.At); active {
		return Decision{
			ShouldDisconnect: true,
			Reason:           "client is in penalty",
			PenaltyUntil:     until,
		}, nil
	}

	release := e.locks.Acquire(in.ClientID)
	defer release()

	sub, err := e.db.Subscriber.Query().
		Where(entsubscriber.ID(in.ClientID)).
		WithActivePlan().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return Decision{Reason: "client not found", ShouldDisconnect: true}, nil
		}
		return Decision{}, fmt.Errorf("load subscriber: %w", err)
	}
	if sub.Status != entsubscriber.StatusActive {
		return Decision{
			ShouldDisconnect: true,
			Reason:           fmt.Sprintf("client status: %s", sub.Status),
		}, nil
	}
	plan := sub.Edges.ActivePlan
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
		return e.suspendForQuota(ctx, sub, plan, "total limit"), nil
	case plan.UploadLimit > 0 && newUpload > plan.UploadLimit:
		return e.suspendForQuota(ctx, sub, plan, "upload limit"), nil
	case plan.DownloadLimit > 0 && newDownload > plan.DownloadLimit:
		return e.suspendForQuota(ctx, sub, plan, "download limit"), nil
	}

	// Node bandwidth ceiling — sum across all agents on this node.
	if node != nil && node.BandwidthLimitBytes > 0 &&
		node.CurrentTotal+total > node.BandwidthLimitBytes {
		_ = e.events.Append(ctx, eventstore.Event{
			Type:      "node_quota_reached",
			NodeID:    node.ID.String(),
			Timestamp: in.At,
		})
		return Decision{
			QuotaExceeded:    true,
			ShouldDisconnect: true,
			Reason:           "node bandwidth limit",
		}, nil
	}

	sessionCount := e.sessions.Touch(sub.ID, ipHash, in.At)
	if plan.MaxConcurrent > 0 && int32(sessionCount) > plan.MaxConcurrent {
		until := e.penalties.Apply(sub.ID, in.At)
		_ = e.events.Append(ctx, eventstore.Event{
			Type:      "penalty_applied",
			ClientID:  sub.ID.String(),
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

	if sub.ResellerID != nil {
		breach, reason, err := e.resellers.CheckUsageDelta(ctx, *sub.ResellerID, upload, download)
		if err != nil {
			return Decision{}, fmt.Errorf("reseller check: %w", err)
		}
		if breach != nil {
			return Decision{
				QuotaExceeded:    true,
				ShouldDisconnect: true,
				Reason:           fmt.Sprintf("%s on %s", reason, breach.Name),
			}, nil
		}
	}

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
	if _, err := tx.Subscriber.UpdateOneID(sub.ID).
		SetLastConnectionAt(in.At).
		Save(ctx); err != nil {
		return Decision{}, fmt.Errorf("update subscriber: %w", err)
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
	if in.AgentID != uuid.Nil {
		if _, err := tx.Agent.Update().
			Where(entagent.ID(in.AgentID)).
			AddCurrentTotal(total).
			AddCurrentUpload(upload).
			AddCurrentDownload(download).
			Save(ctx); err != nil && !ent.IsNotFound(err) {
			return Decision{}, fmt.Errorf("update agent: %w", err)
		}
	}
	if sub.ResellerID != nil {
		if err := e.resellers.ApplyUsageDelta(ctx, tx, *sub.ResellerID, upload, download); err != nil {
			return Decision{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, fmt.Errorf("commit: %w", err)
	}
	commit = true

	_ = e.events.Append(ctx, eventstore.Event{
		Type:      "usage_recorded",
		ClientID:  sub.ID.String(),
		PlanID:    plan.ID.String(),
		NodeID:    nodeIDStr(node),
		AgentID:   nilableIDStr(in.AgentID),
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

func (e *Engine) suspendForQuota(ctx context.Context, sub *ent.Subscriber, plan *ent.UsagePlan, reason string) Decision {
	_, err := e.db.Subscriber.UpdateOneID(sub.ID).SetStatus(entsubscriber.StatusQuotaUsed).Save(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		// Log only — return decision regardless.
	}
	_, _ = e.db.UsagePlan.UpdateOneID(plan.ID).SetStatus(entusageplan.StatusQuotaUsed).Save(ctx)
	_ = e.events.Append(ctx, eventstore.Event{
		Type:      "client_suspended",
		ClientID:  sub.ID.String(),
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

func nilableIDStr(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}
