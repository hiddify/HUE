package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// DB represents a SQLite database connection
type DB struct {
	*sql.DB
	path string
	mu   sync.RWMutex
}

// NewDB creates a new SQLite database connection
func NewDB(dbURL string) (*DB, error) {
	// Parse database URL
	// Format: sqlite://./path/to/db.db or sqlite::memory:
	path := strings.TrimPrefix(dbURL, "sqlite://")
	if path == "" {
		path = "./hue.db"
	}

	// Open connection
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrent performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(1) // SQLite works best with single writer
	db.SetMaxIdleConns(1)

	return &DB{
		DB:   db,
		path: path,
	}, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.DB.Close()
}

// Path returns the database file path
func (db *DB) Path() string {
	return db.path
}

// Transaction executes a function within a transaction
func (db *DB) Transaction(fn func(tx *sql.Tx) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("tx error: %v, rollback error: %w", err, rbErr)
		}
		return err
	}

	return tx.Commit()
}
