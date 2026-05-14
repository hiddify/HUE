package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hiddify/hue/internal/ent"
	entapikey "github.com/hiddify/hue/internal/ent/apikey"
)

// Bootstrap provisions an initial Owner API key on a fresh database
// when HUE_BOOTSTRAP_TOKEN is set. Owner is the singleton root
// principal — implicit parent of every Reseller, with sudo capability
// via AuthService.Login.
//
// Idempotent: if any non-revoked Owner key already exists, leave the
// DB alone (so the env var can stay set across restarts without
// trampling).
//
// The token must already follow the "own_<body>" shape; callers can
// use GenerateKey(KindOwner) to produce one and inject it via the env
// var (or generate offline).
func Bootstrap(ctx context.Context, db *ent.Client, plaintext string, logger *slog.Logger) error {
	if plaintext == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	if !strings.HasPrefix(plaintext, prefixOwner+"_") {
		return errors.New("auth: bootstrap token must be an Owner key (prefix own_)")
	}
	prefix := LookupPrefix(plaintext)
	if prefix == "" {
		return errors.New("auth: bootstrap token is malformed")
	}

	existing, err := db.ApiKey.Query().
		Where(entapikey.KindEQ(entapikey.KindOwner), entapikey.RevokedAtIsNil()).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("auth: check existing keys: %w", err)
	}
	if existing != nil {
		logger.Info("bootstrap skipped: an Owner API key already exists",
			"existing_prefix", existing.Prefix)
		return nil
	}

	hash, err := HashToken(plaintext)
	if err != nil {
		return err
	}
	if _, err := db.ApiKey.Create().
		SetKind(entapikey.KindOwner).
		SetName("bootstrap").
		SetPrefix(prefix).
		SetHash(hash).
		Save(ctx); err != nil {
		return fmt.Errorf("auth: persist bootstrap key: %w", err)
	}

	logger.Info("bootstrap Owner API key provisioned", "prefix", prefix)
	return nil
}
