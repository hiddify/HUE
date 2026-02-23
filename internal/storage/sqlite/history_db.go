package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
)

// HistoryDB handles historical event and usage data
type HistoryDB struct {
	*DB
}

// NewHistoryDB creates a new HistoryDB instance
func NewHistoryDB(dbURL string) (*HistoryDB, error) {
	// Use a separate database file for history data
	historyURL := dbURL
	if dbURL != ":memory:" && !containsHistorySuffix(dbURL) {
		historyURL = replaceDBNameWithSuffix(dbURL, "_history")
	}

	db, err := NewDB(historyURL)
	if err != nil {
		return nil, err
	}

	historyDB := &HistoryDB{DB: db}

	// Create tables
	if err := historyDB.createTables(); err != nil {
		return nil, err
	}

	return historyDB, nil
}

func (db *HistoryDB) createTables() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			user_id TEXT,
			package_id TEXT,
			node_id TEXT,
			service_id TEXT,
			tags TEXT,
			metadata BLOB,
			timestamp DATETIME NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS usage_history (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			package_id TEXT,
			node_id TEXT NOT NULL,
			service_id TEXT NOT NULL,
			upload INTEGER NOT NULL,
			download INTEGER NOT NULL,
			session_id TEXT,
			country TEXT,
			city TEXT,
			isp TEXT,
			tags TEXT,
			timestamp DATETIME NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_type ON events(type)`,
		`CREATE INDEX IF NOT EXISTS idx_events_user_id ON events(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_history_user_id ON usage_history(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_history_timestamp ON usage_history(timestamp)`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}

	return nil
}

// StoreEvent stores an event in the history
func (db *HistoryDB) StoreEvent(event *domain.Event) error {
	tags, _ := json.Marshal(event.Tags)

	_, err := db.Exec(`
		INSERT INTO events (id, type, user_id, package_id, node_id, service_id, tags, metadata, timestamp, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.Type, event.UserID, event.PackageID, event.NodeID, event.ServiceID,
		string(tags), event.Metadata, event.Timestamp, time.Now())

	return err
}

// GetEvents retrieves events with optional filtering
func (db *HistoryDB) GetEvents(eventType *domain.EventType, userID *string, start, end *time.Time, limit int) ([]*domain.Event, error) {
	query := `SELECT id, type, user_id, package_id, node_id, service_id, tags, metadata, timestamp FROM events WHERE 1=1`
	args := []interface{}{}

	if start != nil {
		query += " AND timestamp >= ?"
		args = append(args, *start)
	}
	if end != nil {
		query += " AND timestamp <= ?"
		args = append(args, *end)
	}

	if eventType != nil {
		query += " AND type = ?"
		args = append(args, *eventType)
	}
	if userID != nil {
		query += " AND user_id = ?"
		args = append(args, *userID)
	}

	query += " ORDER BY timestamp DESC"

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []*domain.Event{}
	for rows.Next() {
		event := &domain.Event{}
		var userID, packageID, nodeID, serviceID sql.NullString
		var tags sql.NullString
		var metadata []byte

		err := rows.Scan(
			&event.ID, &event.Type, &userID, &packageID, &nodeID, &serviceID,
			&tags, &metadata, &event.Timestamp,
		)
		if err != nil {
			return nil, err
		}

		if userID.Valid {
			event.UserID = &userID.String
		}
		if packageID.Valid {
			event.PackageID = &packageID.String
		}
		if nodeID.Valid {
			event.NodeID = &nodeID.String
		}
		if serviceID.Valid {
			event.ServiceID = &serviceID.String
		}
		if tags.Valid {
			json.Unmarshal([]byte(tags.String), &event.Tags)
		}
		if metadata != nil {
			event.Metadata = metadata
		}

		events = append(events, event)
	}

	return events, nil
}

// StoreUsageHistory stores aggregated usage history
func (db *HistoryDB) StoreUsageHistory(
	userID, packageID, nodeID, serviceID string,
	upload, download int64,
	sessionID string,
	geoData *domain.GeoData,
	tags []string,
	timestamp time.Time,
) error {
	id := generateID()
	tagsJSON, _ := json.Marshal(tags)

	_, err := db.Exec(`
		INSERT INTO usage_history (id, user_id, package_id, node_id, service_id, upload, download, session_id, country, city, isp, tags, timestamp, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, userID, packageID, nodeID, serviceID, upload, download, sessionID,
		geoData.Country, geoData.City, geoData.ISP, string(tagsJSON), timestamp, time.Now())

	return err
}

// GetUsageHistory retrieves usage history for a user
func (db *HistoryDB) GetUsageHistory(userID string, start, end time.Time, limit int) ([]*UsageHistoryEntry, error) {
	query := `
		SELECT id, user_id, package_id, node_id, service_id, upload, download, session_id, country, city, isp, tags, timestamp
		FROM usage_history
		WHERE user_id = ? AND timestamp >= ? AND timestamp <= ?
		ORDER BY timestamp DESC
	`
	args := []interface{}{userID, start, end}

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []*UsageHistoryEntry{}
	for rows.Next() {
		entry := &UsageHistoryEntry{}
		var packageID, nodeID, serviceID, sessionID sql.NullString
		var country, city, isp sql.NullString
		var tags sql.NullString

		err := rows.Scan(
			&entry.ID, &entry.UserID, &packageID, &nodeID, &serviceID,
			&entry.Upload, &entry.Download, &sessionID,
			&country, &city, &isp, &tags, &entry.Timestamp,
		)
		if err != nil {
			return nil, err
		}

		if packageID.Valid {
			entry.PackageID = packageID.String
		}
		if nodeID.Valid {
			entry.NodeID = nodeID.String
		}
		if serviceID.Valid {
			entry.ServiceID = serviceID.String
		}
		if sessionID.Valid {
			entry.SessionID = sessionID.String
		}
		if country.Valid {
			entry.Country = country.String
		}
		if city.Valid {
			entry.City = city.String
		}
		if isp.Valid {
			entry.ISP = isp.String
		}
		if tags.Valid {
			json.Unmarshal([]byte(tags.String), &entry.Tags)
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// DeleteOldHistory deletes history older than the retention period
func (db *HistoryDB) DeleteOldHistory(olderThan time.Time) error {
	_, err := db.Exec(`DELETE FROM events WHERE timestamp < ?`, olderThan)
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM usage_history WHERE timestamp < ?`, olderThan)
	return err
}

// UsageHistoryEntry represents a usage history entry
type UsageHistoryEntry struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	PackageID string    `json:"package_id,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
	ServiceID string    `json:"service_id,omitempty"`
	Upload    int64     `json:"upload"`
	Download  int64     `json:"download"`
	SessionID string    `json:"session_id,omitempty"`
	Country   string    `json:"country,omitempty"`
	City      string    `json:"city,omitempty"`
	ISP       string    `json:"isp,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

func containsHistorySuffix(url string) bool {
	return len(url) > 9 && url[len(url)-9:] == "_history"
}

func replaceDBNameWithSuffix(url string, suffix string) string {
	if len(url) > 3 && url[len(url)-3:] == ".db" {
		return url[:len(url)-3] + suffix + ".db"
	}
	return url + suffix
}

func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
