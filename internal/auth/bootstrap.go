package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/hiddify/hue/internal/ent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
	entmanager "github.com/hiddify/hue/internal/ent/manager"
)

// Bootstrap provisions an initial root manager and its API key on a fresh
// database, so the operator has a way in. Called from main.go when
// HUE_BOOTSTRAP_TOKEN is set:
//
//   * If any active manager API key already exists, returns nil (no-op).
//   * Otherwise, creates a "root" manager (or finds the existing one),
//     stores the supplied plaintext token's Argon2id hash + lookup
//     prefix, and logs a one-line confirmation. The plaintext is never
//     written to disk.
//
// The token must already follow the "<kind>_<body>" shape for KindManager;
// callers can use GenerateKey(KindManager) to produce one and inject it
// via the env var.
func Bootstrap(ctx context.Context, db *ent.Client, plaintext string, logger *slog.Logger) error {
	if plaintext == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	if !strings.HasPrefix(plaintext, prefixManager+"_") {
		return errors.New("auth: bootstrap token must be a manager key (prefix mgr_)")
	}
	prefix := LookupPrefix(plaintext)
	if prefix == "" {
		return errors.New("auth: bootstrap token is malformed")
	}

	// Idempotency: if any non-revoked manager key exists, leave the DB alone.
	existing, err := db.ApiKey.Query().
		Where(entapikey.KindEQ(entapikey.KindManager), entapikey.RevokedAtIsNil()).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("auth: check existing keys: %w", err)
	}
	if existing != nil {
		logger.Info("bootstrap skipped: a manager API key already exists",
			"existing_prefix", existing.Prefix)
		return nil
	}

	// Find or create a root manager.
	var rootID uuid.UUID
	root, err := db.Manager.Query().
		Where(entmanager.ParentIDIsNil(), entmanager.NameEQ("root")).
		First(ctx)
	switch {
	case err == nil:
		rootID = root.ID
	case ent.IsNotFound(err):
		created, err := db.Manager.Create().
			SetName("root").
			SetStatus(entmanager.StatusActive).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("auth: create root manager: %w", err)
		}
		rootID = created.ID
	default:
		return fmt.Errorf("auth: lookup root manager: %w", err)
	}

	hash, err := HashToken(plaintext)
	if err != nil {
		return err
	}
	_, err = db.ApiKey.Create().
		SetKind(entapikey.KindManager).
		SetOwnerID(rootID.String()).
		SetName("bootstrap").
		SetPrefix(prefix).
		SetHash(hash).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("auth: persist bootstrap key: %w", err)
	}

	logger.Info("bootstrap manager API key provisioned",
		"manager_id", rootID,
		"prefix", prefix,
	)
	return nil
}
