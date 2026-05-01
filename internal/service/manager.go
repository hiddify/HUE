package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hiddify/hue/internal/ent"
	entmanager "github.com/hiddify/hue/internal/ent/manager"
	entmplan "github.com/hiddify/hue/internal/ent/managerplan"
)

// ManagerHierarchy enforces the multi-level reseller policy described in
// PRD §6:
//   * Children's limits must never exceed any ancestor's limits.
//   * Usage propagates from leaf to root.
//   * A breach at any level rejects the operation before any DB write
//     is committed (projected-usage check).
//
// It uses the per-manager LockManager to serialize concurrent updates
// against the same hierarchy node, but reads are unlocked since ent uses a
// connection pool and Postgres provides MVCC.
type ManagerHierarchy struct {
	db    *ent.Client
	locks *LockManager
}

func NewManagerHierarchy(db *ent.Client, locks *LockManager) *ManagerHierarchy {
	return &ManagerHierarchy{db: db, locks: locks}
}

// Ancestors returns the chain from the given manager to the root,
// inclusive of the start. Order: [self, parent, grandparent, ..., root].
func (mh *ManagerHierarchy) Ancestors(ctx context.Context, managerID uuid.UUID) ([]*ent.Manager, error) {
	chain := make([]*ent.Manager, 0, 4)
	cur := managerID
	for {
		m, err := mh.db.Manager.Query().Where(entmanager.ID(cur)).Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("manager %s: %w", cur, err)
		}
		chain = append(chain, m)
		if m.ParentID == nil {
			return chain, nil
		}
		cur = *m.ParentID
		if len(chain) > 32 {
			return nil, fmt.Errorf("manager hierarchy too deep at %s — likely a cycle", managerID)
		}
	}
}

// CheckUsageDelta projects the proposed (uploadDelta, downloadDelta) onto
// every ancestor's plan. Returns the first ancestor whose limit would be
// exceeded, or nil if all pass.
func (mh *ManagerHierarchy) CheckUsageDelta(
	ctx context.Context,
	managerID uuid.UUID,
	uploadDelta, downloadDelta int64,
) (breach *ent.Manager, reason string, err error) {
	if managerID == uuid.Nil {
		return nil, "", nil
	}
	ancestors, err := mh.Ancestors(ctx, managerID)
	if err != nil {
		return nil, "", err
	}
	totalDelta := uploadDelta + downloadDelta
	for _, m := range ancestors {
		plan, err := mh.db.ManagerPlan.Query().Where(entmplan.ManagerID(m.ID)).Only(ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("plan for %s: %w", m.ID, err)
		}
		if plan.TotalLimit > 0 && plan.CurrentTotal+totalDelta > plan.TotalLimit {
			return m, "manager total limit", nil
		}
		if plan.UploadLimit > 0 && plan.CurrentUpload+uploadDelta > plan.UploadLimit {
			return m, "manager upload limit", nil
		}
		if plan.DownloadLimit > 0 && plan.CurrentDownload+downloadDelta > plan.DownloadLimit {
			return m, "manager download limit", nil
		}
	}
	return nil, "", nil
}

// ApplyUsageDelta increments each ancestor's plan counters atomically.
// Caller must have already called CheckUsageDelta and decided to commit.
// Counter underflow is prevented at the SQL level via GREATEST(0, ...) on
// the negative path; positive deltas use plain addition.
func (mh *ManagerHierarchy) ApplyUsageDelta(
	ctx context.Context,
	tx *ent.Tx,
	managerID uuid.UUID,
	uploadDelta, downloadDelta int64,
) error {
	if managerID == uuid.Nil || (uploadDelta == 0 && downloadDelta == 0) {
		return nil
	}
	ancestors, err := mh.ancestorsTx(ctx, tx, managerID)
	if err != nil {
		return err
	}
	totalDelta := uploadDelta + downloadDelta
	for _, m := range ancestors {
		_, err := tx.ManagerPlan.Update().
			Where(entmplan.ManagerID(m.ID)).
			AddCurrentTotal(totalDelta).
			AddCurrentUpload(uploadDelta).
			AddCurrentDownload(downloadDelta).
			Save(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return fmt.Errorf("apply delta to %s: %w", m.ID, err)
		}
	}
	return nil
}

func (mh *ManagerHierarchy) ancestorsTx(ctx context.Context, tx *ent.Tx, managerID uuid.UUID) ([]*ent.Manager, error) {
	chain := make([]*ent.Manager, 0, 4)
	cur := managerID
	for {
		m, err := tx.Manager.Query().Where(entmanager.ID(cur)).Only(ctx)
		if err != nil {
			return nil, err
		}
		chain = append(chain, m)
		if m.ParentID == nil {
			return chain, nil
		}
		cur = *m.ParentID
		if len(chain) > 32 {
			return nil, fmt.Errorf("manager hierarchy too deep at %s", managerID)
		}
	}
}
