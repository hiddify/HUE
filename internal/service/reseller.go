package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hiddify/hue/internal/ent"
	entreseller "github.com/hiddify/hue/internal/ent/reseller"
	entresellerplan "github.com/hiddify/hue/internal/ent/resellerplan"
)

// ResellerHierarchy enforces the multi-level reseller policy described
// in PRD §6:
//   * Children's limits must never exceed any ancestor's limits.
//   * Usage propagates from leaf to root.
//   * A breach at any level rejects the operation before any DB write
//     is committed (projected-usage check).
//
// Locks serialize concurrent updates against the same hierarchy node;
// reads stay unlocked since ent uses a connection pool and Postgres
// provides MVCC.
type ResellerHierarchy struct {
	db    *ent.Client
	locks *LockManager
}

func NewResellerHierarchy(db *ent.Client, locks *LockManager) *ResellerHierarchy {
	return &ResellerHierarchy{db: db, locks: locks}
}

// Ancestors returns the chain from the given reseller to root,
// inclusive of the start. Order: [self, parent, grandparent, ..., root].
func (rh *ResellerHierarchy) Ancestors(ctx context.Context, resellerID uuid.UUID) ([]*ent.Reseller, error) {
	chain := make([]*ent.Reseller, 0, 4)
	cur := resellerID
	for {
		r, err := rh.db.Reseller.Query().Where(entreseller.ID(cur)).Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("reseller %s: %w", cur, err)
		}
		chain = append(chain, r)
		if r.ParentID == nil {
			return chain, nil
		}
		cur = *r.ParentID
		if len(chain) > 32 {
			return nil, fmt.Errorf("reseller hierarchy too deep at %s — likely a cycle", resellerID)
		}
	}
}

// CheckUsageDelta projects the proposed (uploadDelta, downloadDelta)
// onto every ancestor's plan. Returns the first ancestor whose limit
// would be exceeded, or nil if all pass.
func (rh *ResellerHierarchy) CheckUsageDelta(
	ctx context.Context,
	resellerID uuid.UUID,
	uploadDelta, downloadDelta int64,
) (breach *ent.Reseller, reason string, err error) {
	if resellerID == uuid.Nil {
		return nil, "", nil
	}
	ancestors, err := rh.Ancestors(ctx, resellerID)
	if err != nil {
		return nil, "", err
	}
	totalDelta := uploadDelta + downloadDelta
	for _, r := range ancestors {
		plan, err := rh.db.ResellerPlan.Query().Where(entresellerplan.ResellerID(r.ID)).Only(ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("plan for %s: %w", r.ID, err)
		}
		if plan.TotalLimit > 0 && plan.CurrentTotal+totalDelta > plan.TotalLimit {
			return r, "reseller total limit", nil
		}
		if plan.UploadLimit > 0 && plan.CurrentUpload+uploadDelta > plan.UploadLimit {
			return r, "reseller upload limit", nil
		}
		if plan.DownloadLimit > 0 && plan.CurrentDownload+downloadDelta > plan.DownloadLimit {
			return r, "reseller download limit", nil
		}
	}
	return nil, "", nil
}

// ApplyUsageDelta increments each ancestor's plan counters atomically.
// Caller must have already called CheckUsageDelta and decided to commit.
func (rh *ResellerHierarchy) ApplyUsageDelta(
	ctx context.Context,
	tx *ent.Tx,
	resellerID uuid.UUID,
	uploadDelta, downloadDelta int64,
) error {
	if resellerID == uuid.Nil || (uploadDelta == 0 && downloadDelta == 0) {
		return nil
	}
	ancestors, err := rh.ancestorsTx(ctx, tx, resellerID)
	if err != nil {
		return err
	}
	totalDelta := uploadDelta + downloadDelta
	for _, r := range ancestors {
		_, err := tx.ResellerPlan.Update().
			Where(entresellerplan.ResellerID(r.ID)).
			AddCurrentTotal(totalDelta).
			AddCurrentUpload(uploadDelta).
			AddCurrentDownload(downloadDelta).
			Save(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return fmt.Errorf("apply delta to %s: %w", r.ID, err)
		}
	}
	return nil
}

func (rh *ResellerHierarchy) ancestorsTx(ctx context.Context, tx *ent.Tx, resellerID uuid.UUID) ([]*ent.Reseller, error) {
	chain := make([]*ent.Reseller, 0, 4)
	cur := resellerID
	for {
		r, err := tx.Reseller.Query().Where(entreseller.ID(cur)).Only(ctx)
		if err != nil {
			return nil, err
		}
		chain = append(chain, r)
		if r.ParentID == nil {
			return chain, nil
		}
		cur = *r.ParentID
		if len(chain) > 32 {
			return nil, fmt.Errorf("reseller hierarchy too deep at %s", resellerID)
		}
	}
}

// DescendantIDs returns the set of all reseller ids in the subtree
// rooted at root, inclusive. Used by ResellerClientService to scope
// queries: "this caller can only see clients whose reseller_id is in
// this set".
//
// BFS walk. Caps at 100k to avoid runaway recursion if the tree is
// pathological.
func (rh *ResellerHierarchy) DescendantIDs(ctx context.Context, root uuid.UUID) (map[uuid.UUID]struct{}, error) {
	out := map[uuid.UUID]struct{}{root: {}}
	frontier := []uuid.UUID{root}
	for len(frontier) > 0 && len(out) < 100_000 {
		next, err := rh.db.Reseller.Query().
			Where(entreseller.ParentIDIn(frontier...)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("descendants: %w", err)
		}
		frontier = frontier[:0]
		for _, r := range next {
			if _, seen := out[r.ID]; seen {
				continue
			}
			out[r.ID] = struct{}{}
			frontier = append(frontier, r.ID)
		}
	}
	return out, nil
}
