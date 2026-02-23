package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
)

// ActiveDB handles temporary usage data with buffered writes
type ActiveDB struct {
	*DB
	buffer     []*domain.UsageReport
	bufferMu   sync.Mutex
	flushSize  int
}

// NewActiveDB creates a new ActiveDB instance
func NewActiveDB(dbURL string) (*ActiveDB, error) {
	// Use a separate database file for active data
	activeURL := dbURL
	if dbURL != ":memory:" && !containsActiveSuffix(dbURL) {
		activeURL = replaceDBName(dbURL, "_active")
	}

	db, err := NewDB(activeURL)
	if err != nil {
		return nil, err
	}

	activeDB := &ActiveDB{
		DB:        db,
		buffer:    make([]*domain.UsageReport, 0, 1000),
		flushSize: 100,
	}

	// Create tables
	if err := activeDB.createTables(); err != nil {
		return nil, err
	}

	return activeDB, nil
}

func (db *ActiveDB) createTables() error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS usage_reports (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			service_id TEXT NOT NULL,
			upload INTEGER NOT NULL,
			download INTEGER NOT NULL,
			session_id TEXT,
			tags TEXT,
			timestamp DATETIME NOT NULL,
			processed INTEGER DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_reports_user_id ON usage_reports(user_id)`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_reports_timestamp ON usage_reports(timestamp)`)
	return err
}

// BufferUsage adds a usage report to the in-memory buffer
func (db *ActiveDB) BufferUsage(report *domain.UsageReport) error {
	db.bufferMu.Lock()
	defer db.bufferMu.Unlock()

	db.buffer = append(db.buffer, report)

	// Auto-flush if buffer is full
	if len(db.buffer) >= db.flushSize {
		return db.flushBuffer()
	}

	return nil
}

// Flush writes all buffered data to the database
func (db *ActiveDB) Flush() error {
	db.bufferMu.Lock()
	defer db.bufferMu.Unlock()

	return db.flushBuffer()
}

func (db *ActiveDB) flushBuffer() error {
	if len(db.buffer) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO usage_reports (id, user_id, node_id, service_id, upload, download, session_id, tags, timestamp, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	now := time.Now()
	for _, report := range db.buffer {
		tags, _ := json.Marshal(report.Tags)
		_, err := stmt.Exec(
			report.ID, report.UserID, report.NodeID, report.ServiceID,
			report.Upload, report.Download, report.SessionID,
			string(tags), report.Timestamp, now,
		)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to insert usage report: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Clear buffer
	db.buffer = db.buffer[:0]
	return nil
}

// GetUnprocessedReports retrieves unprocessed usage reports
func (db *ActiveDB) GetUnprocessedReports(limit int) ([]*domain.UsageReport, error) {
	rows, err := db.Query(`
		SELECT id, user_id, node_id, service_id, upload, download, session_id, tags, timestamp
		FROM usage_reports
		WHERE processed = 0
		ORDER BY timestamp ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reports := []*domain.UsageReport{}
	for rows.Next() {
		report := &domain.UsageReport{}
		var tags sql.NullString
		var sessionID sql.NullString

		err := rows.Scan(
			&report.ID, &report.UserID, &report.NodeID, &report.ServiceID,
			&report.Upload, &report.Download, &sessionID, &tags, &report.Timestamp,
		)
		if err != nil {
			return nil, err
		}

		if sessionID.Valid {
			report.SessionID = sessionID.String
		}
		if tags.Valid {
			json.Unmarshal([]byte(tags.String), &report.Tags)
		}

		reports = append(reports, report)
	}

	return reports, nil
}

// MarkProcessed marks usage reports as processed
func (db *ActiveDB) MarkProcessed(ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}

	stmt, err := tx.Prepare(`UPDATE usage_reports SET processed = 1 WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

// DeleteOldReports deletes processed reports older than the retention period
func (db *ActiveDB) DeleteOldReports(olderThan time.Time) error {
	_, err := db.Exec(`DELETE FROM usage_reports WHERE processed = 1 AND timestamp < ?`, olderThan)
	return err
}

// GetAggregatedUsage returns aggregated usage for a user within a time range
func (db *ActiveDB) GetAggregatedUsage(userID string, start, end time.Time) (upload, download int64, err error) {
	err = db.QueryRow(`
		SELECT COALESCE(SUM(upload), 0), COALESCE(SUM(download), 0)
		FROM usage_reports
		WHERE user_id = ? AND timestamp >= ? AND timestamp <= ?
	`, userID, start, end).Scan(&upload, &download)
	return
}

func containsActiveSuffix(url string) bool {
	return len(url) > 7 && url[len(url)-7:] == "_active"
}

func replaceDBName(url string, suffix string) string {
	// Simple replacement for sqlite://./hue.db -> sqlite://./hue_active.db
	if len(url) > 3 && url[len(url)-3:] == ".db" {
		return url[:len(url)-3] + suffix + ".db"
	}
	return url + suffix
}
