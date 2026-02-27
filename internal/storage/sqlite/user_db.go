package sqlite

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
)

func parseSQLiteTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if idx := strings.Index(value, " m="); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700 -0700",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700 -0700",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}

	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported sqlite datetime format: %q", value)
}

// UserDB handles user-related database operations
type UserDB struct {
	*DB
}

// NewUserDB creates a new UserDB instance
func NewUserDB(dbURL string) (*UserDB, error) {
	db, err := NewDB(dbURL)
	if err != nil {
		return nil, err
	}
	return &UserDB{DB: db}, nil
}

// Migrate runs database migrations for user tables
func (db *UserDB) Migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			manager_id TEXT,
			username TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			public_key TEXT,
			private_key TEXT,
			ca_cert_list TEXT DEFAULT '[]',
			groups TEXT DEFAULT '[]',
			allowed_devices TEXT DEFAULT '[]',
			status TEXT NOT NULL DEFAULT 'active',
			active_package_id TEXT,
			first_connection_at DATETIME,
			last_connection_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS packages (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			total_traffic INTEGER NOT NULL DEFAULT 0,
			upload_limit INTEGER NOT NULL DEFAULT 0,
			download_limit INTEGER NOT NULL DEFAULT 0,
			reset_mode TEXT NOT NULL DEFAULT 'no-reset',
			duration INTEGER NOT NULL,
			start_at DATETIME,
			max_concurrent INTEGER NOT NULL DEFAULT 1,
			status TEXT NOT NULL DEFAULT 'active',
			current_upload INTEGER NOT NULL DEFAULT 0,
			current_download INTEGER NOT NULL DEFAULT 0,
			current_total INTEGER NOT NULL DEFAULT 0,
			expires_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			secret_key TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			allowed_ips TEXT DEFAULT '[]',
			traffic_multiplier REAL NOT NULL DEFAULT 1.0,
			reset_mode TEXT NOT NULL DEFAULT 'no-reset',
			reset_day INTEGER DEFAULT 0,
			current_upload INTEGER NOT NULL DEFAULT 0,
			current_download INTEGER NOT NULL DEFAULT 0,
			country TEXT,
			city TEXT,
			isp TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS services (
			id TEXT PRIMARY KEY,
			secret_key TEXT NOT NULL UNIQUE,
			node_id TEXT NOT NULL,
			name TEXT NOT NULL,
			protocol TEXT NOT NULL,
			allowed_auth_methods TEXT NOT NULL DEFAULT '["password"]',
			callback_url TEXT,
			current_upload INTEGER NOT NULL DEFAULT 0,
			current_download INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS managers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			parent_id TEXT,
			metadata TEXT DEFAULT '{}',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (parent_id) REFERENCES managers(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS manager_packages (
			manager_id TEXT PRIMARY KEY,
			total_limit INTEGER NOT NULL DEFAULT 0,
			upload_limit INTEGER NOT NULL DEFAULT 0,
			download_limit INTEGER NOT NULL DEFAULT 0,
			reset_mode TEXT NOT NULL DEFAULT 'no-reset',
			duration INTEGER NOT NULL DEFAULT 0,
			start_at DATETIME,
			max_sessions INTEGER NOT NULL DEFAULT 0,
			max_online_users INTEGER NOT NULL DEFAULT 0,
			max_active_users INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'inactive',
			current_upload INTEGER NOT NULL DEFAULT 0,
			current_download INTEGER NOT NULL DEFAULT 0,
			current_total INTEGER NOT NULL DEFAULT 0,
			current_sessions INTEGER NOT NULL DEFAULT 0,
			current_online_users INTEGER NOT NULL DEFAULT 0,
			current_active_users INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (manager_id) REFERENCES managers(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS owner_auth_key (
			key_id INTEGER PRIMARY KEY CHECK (key_id = 1),
			hashed_key TEXT NOT NULL,
			revoked INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS service_auth_keys (
			service_id TEXT PRIMARY KEY,
			hashed_key TEXT NOT NULL,
			revoked INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (service_id) REFERENCES services(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_status ON users(status)`,
		`CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)`,
		`CREATE INDEX IF NOT EXISTS idx_users_manager_id ON users(manager_id)`,
		`CREATE INDEX IF NOT EXISTS idx_packages_user_id ON packages(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_packages_status ON packages(status)`,
		`CREATE INDEX IF NOT EXISTS idx_services_node_id ON services(node_id)`,
		`CREATE INDEX IF NOT EXISTS idx_managers_parent_id ON managers(parent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_manager_packages_status ON manager_packages(status)`,
		`CREATE INDEX IF NOT EXISTS idx_service_auth_keys_revoked ON service_auth_keys(revoked)`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN manager_id TEXT`); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return fmt.Errorf("failed to ensure users.manager_id column: %w", err)
		}
	}

	return nil
}

// User operations

// CreateUser creates a new user
func (db *UserDB) CreateUser(user *domain.User) error {
	caCerts, _ := json.Marshal(user.CACertList)
	groups, _ := json.Marshal(user.Groups)
	devices, _ := json.Marshal(user.AllowedDevices)

	now := time.Now()
	_, err := db.Exec(`
		INSERT INTO users (id, manager_id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, user.ID, user.ManagerID, user.Username, user.Password, user.PublicKey, user.PrivateKey, string(caCerts), string(groups), string(devices), user.Status, user.ActivePackageID, now, now)

	return err
}

// GetUser retrieves a user by ID
func (db *UserDB) GetUser(id string) (*domain.User, error) {
	user := &domain.User{}
	var caCerts, groups, devices sql.NullString
	var managerID sql.NullString
	var activePackageID sql.NullString
	var firstConnRaw, lastConnRaw sql.NullString
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, manager_id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, first_connection_at, last_connection_at, created_at, updated_at
		FROM users WHERE id = ?
	`, id).Scan(
		&user.ID, &managerID, &user.Username, &user.Password, &user.PublicKey, &user.PrivateKey,
		&caCerts, &groups, &devices, &user.Status, &activePackageID,
		&firstConnRaw, &lastConnRaw, &createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Parse JSON arrays
	if caCerts.Valid {
		json.Unmarshal([]byte(caCerts.String), &user.CACertList)
	}
	if groups.Valid {
		json.Unmarshal([]byte(groups.String), &user.Groups)
	}
	if devices.Valid {
		json.Unmarshal([]byte(devices.String), &user.AllowedDevices)
	}
	if managerID.Valid {
		user.ManagerID = &managerID.String
	}
	if activePackageID.Valid {
		user.ActivePackageID = &activePackageID.String
	}
	if firstConnRaw.Valid && firstConnRaw.String != "" {
		parsed, parseErr := parseSQLiteTime(firstConnRaw.String)
		if parseErr != nil {
			return nil, parseErr
		}
		user.FirstConnectionAt = &parsed
	}
	if lastConnRaw.Valid && lastConnRaw.String != "" {
		parsed, parseErr := parseSQLiteTime(lastConnRaw.String)
		if parseErr != nil {
			return nil, parseErr
		}
		user.LastConnectionAt = &parsed
	}

	user.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}

	user.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return user, nil
}

// GetUserByUsername retrieves a user by username
func (db *UserDB) GetUserByUsername(username string) (*domain.User, error) {
	user := &domain.User{}
	var caCerts, groups, devices sql.NullString
	var managerID sql.NullString
	var activePackageID sql.NullString
	var firstConnRaw, lastConnRaw sql.NullString
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, manager_id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, first_connection_at, last_connection_at, created_at, updated_at
		FROM users WHERE username = ?
	`, username).Scan(
		&user.ID, &managerID, &user.Username, &user.Password, &user.PublicKey, &user.PrivateKey,
		&caCerts, &groups, &devices, &user.Status, &activePackageID,
		&firstConnRaw, &lastConnRaw, &createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if caCerts.Valid {
		json.Unmarshal([]byte(caCerts.String), &user.CACertList)
	}
	if groups.Valid {
		json.Unmarshal([]byte(groups.String), &user.Groups)
	}
	if devices.Valid {
		json.Unmarshal([]byte(devices.String), &user.AllowedDevices)
	}
	if managerID.Valid {
		user.ManagerID = &managerID.String
	}
	if activePackageID.Valid {
		user.ActivePackageID = &activePackageID.String
	}
	if firstConnRaw.Valid && firstConnRaw.String != "" {
		parsed, parseErr := parseSQLiteTime(firstConnRaw.String)
		if parseErr != nil {
			return nil, parseErr
		}
		user.FirstConnectionAt = &parsed
	}
	if lastConnRaw.Valid && lastConnRaw.String != "" {
		parsed, parseErr := parseSQLiteTime(lastConnRaw.String)
		if parseErr != nil {
			return nil, parseErr
		}
		user.LastConnectionAt = &parsed
	}

	user.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}

	user.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return user, nil
}

// ListUsers retrieves users with optional filtering
func (db *UserDB) ListUsers(filter *domain.UserFilter) ([]*domain.User, error) {
	query := `SELECT id, manager_id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, first_connection_at, last_connection_at, created_at, updated_at FROM users`
	args := []interface{}{}
	conditions := []string{}

	if filter != nil {
		if filter.Status != nil {
			conditions = append(conditions, "status = ?")
			args = append(args, *filter.Status)
		}
		if filter.Search != nil {
			conditions = append(conditions, "username LIKE ?")
			args = append(args, "%"+*filter.Search+"%")
		}
	}

	if len(conditions) > 0 {
		query += " WHERE " + joinConditions(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"

	if filter != nil && filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", filter.Offset)
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []*domain.User{}
	for rows.Next() {
		user := &domain.User{}
		var caCerts, groups, devices sql.NullString
		var managerID sql.NullString
		var activePackageID sql.NullString
		var firstConnRaw, lastConnRaw sql.NullString
		var createdAtRaw, updatedAtRaw string

		err := rows.Scan(
			&user.ID, &managerID, &user.Username, &user.Password, &user.PublicKey, &user.PrivateKey,
			&caCerts, &groups, &devices, &user.Status, &activePackageID,
			&firstConnRaw, &lastConnRaw, &createdAtRaw, &updatedAtRaw,
		)
		if err != nil {
			return nil, err
		}

		if caCerts.Valid {
			json.Unmarshal([]byte(caCerts.String), &user.CACertList)
		}
		if groups.Valid {
			json.Unmarshal([]byte(groups.String), &user.Groups)
		}
		if devices.Valid {
			json.Unmarshal([]byte(devices.String), &user.AllowedDevices)
		}
		if managerID.Valid {
			user.ManagerID = &managerID.String
		}
		if activePackageID.Valid {
			user.ActivePackageID = &activePackageID.String
		}
		if firstConnRaw.Valid && firstConnRaw.String != "" {
			parsed, parseErr := parseSQLiteTime(firstConnRaw.String)
			if parseErr != nil {
				return nil, parseErr
			}
			user.FirstConnectionAt = &parsed
		}
		if lastConnRaw.Valid && lastConnRaw.String != "" {
			parsed, parseErr := parseSQLiteTime(lastConnRaw.String)
			if parseErr != nil {
				return nil, parseErr
			}
			user.LastConnectionAt = &parsed
		}

		user.CreatedAt, err = parseSQLiteTime(createdAtRaw)
		if err != nil {
			return nil, err
		}

		user.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
		if err != nil {
			return nil, err
		}

		users = append(users, user)
	}

	return users, nil
}

// UpdateUser updates a user
func (db *UserDB) UpdateUser(user *domain.User) error {
	caCerts, _ := json.Marshal(user.CACertList)
	groups, _ := json.Marshal(user.Groups)
	devices, _ := json.Marshal(user.AllowedDevices)

	_, err := db.Exec(`
		UPDATE users SET
			manager_id = ?, username = ?, password = ?, public_key = ?, private_key = ?,
			ca_cert_list = ?, groups = ?, allowed_devices = ?,
			status = ?, active_package_id = ?, first_connection_at = ?,
			last_connection_at = ?, updated_at = ?
		WHERE id = ?
	`, user.ManagerID, user.Username, user.Password, user.PublicKey, user.PrivateKey,
		string(caCerts), string(groups), string(devices),
		user.Status, user.ActivePackageID, user.FirstConnectionAt,
		user.LastConnectionAt, time.Now(), user.ID)

	return err
}

// UpdateUserStatus updates only the user status
func (db *UserDB) UpdateUserStatus(id string, status domain.UserStatus) error {
	_, err := db.Exec(`UPDATE users SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now(), id)
	return err
}

// UpdateUserLastConnection updates the last connection timestamp
func (db *UserDB) UpdateUserLastConnection(id string) error {
	now := time.Now()
	_, err := db.Exec(`
		UPDATE users SET last_connection_at = ?, updated_at = ? WHERE id = ?
	`, now, now, id)
	return err
}

// DeleteUser deletes a user
func (db *UserDB) DeleteUser(id string) error {
	_, err := db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// Package operations

// CreatePackage creates a new package
func (db *UserDB) CreatePackage(pkg *domain.Package) error {
	if pkg.TotalLimit == 0 && pkg.TotalTraffic > 0 {
		pkg.TotalLimit = pkg.TotalTraffic
	}
	if pkg.TotalTraffic == 0 && pkg.TotalLimit > 0 {
		pkg.TotalTraffic = pkg.TotalLimit
	}

	now := time.Now()
	_, err := db.Exec(`
		INSERT INTO packages (id, user_id, total_traffic, upload_limit, download_limit, reset_mode, duration, start_at, max_concurrent, status, current_upload, current_download, current_total, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, pkg.ID, pkg.UserID, pkg.TotalTraffic, pkg.UploadLimit, pkg.DownloadLimit,
		pkg.ResetMode, pkg.Duration, pkg.StartAt, pkg.MaxConcurrent, pkg.Status,
		pkg.CurrentUpload, pkg.CurrentDownload, pkg.CurrentTotal, pkg.ExpiresAt, now, now)

	return err
}

// GetPackage retrieves a package by ID
func (db *UserDB) GetPackage(id string) (*domain.Package, error) {
	pkg := &domain.Package{}
	var startAt, expiresAt sql.NullTime
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, user_id, total_traffic, upload_limit, download_limit, reset_mode, duration, start_at, max_concurrent, status, current_upload, current_download, current_total, expires_at, created_at, updated_at
		FROM packages WHERE id = ?
	`, id).Scan(
		&pkg.ID, &pkg.UserID, &pkg.TotalTraffic, &pkg.UploadLimit, &pkg.DownloadLimit,
		&pkg.ResetMode, &pkg.Duration, &startAt, &pkg.MaxConcurrent, &pkg.Status,
		&pkg.CurrentUpload, &pkg.CurrentDownload, &pkg.CurrentTotal, &expiresAt,
		&createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if startAt.Valid {
		pkg.StartAt = &startAt.Time
	}
	if expiresAt.Valid {
		pkg.ExpiresAt = &expiresAt.Time
	}
	pkg.TotalLimit = pkg.TotalTraffic

	pkg.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}

	pkg.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return pkg, nil
}

// GetPackageByUserID retrieves the active package for a user
func (db *UserDB) GetPackageByUserID(userID string) (*domain.Package, error) {
	pkg := &domain.Package{}
	var startAt, expiresAt sql.NullTime
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT p.id, p.user_id, p.total_traffic, p.upload_limit, p.download_limit, p.reset_mode, p.duration, p.start_at, p.max_concurrent, p.status, p.current_upload, p.current_download, p.current_total, p.expires_at, p.created_at, p.updated_at
		FROM packages p
		JOIN users u ON u.active_package_id = p.id
		WHERE u.id = ?
	`, userID).Scan(
		&pkg.ID, &pkg.UserID, &pkg.TotalTraffic, &pkg.UploadLimit, &pkg.DownloadLimit,
		&pkg.ResetMode, &pkg.Duration, &startAt, &pkg.MaxConcurrent, &pkg.Status,
		&pkg.CurrentUpload, &pkg.CurrentDownload, &pkg.CurrentTotal, &expiresAt,
		&createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if startAt.Valid {
		pkg.StartAt = &startAt.Time
	}
	if expiresAt.Valid {
		pkg.ExpiresAt = &expiresAt.Time
	}
	pkg.TotalLimit = pkg.TotalTraffic

	pkg.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}

	pkg.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return pkg, nil
}

// UpdatePackageUsage updates the current usage counters
func (db *UserDB) UpdatePackageUsage(id string, upload, download int64) error {
	_, err := db.Exec(`
		UPDATE packages SET
			current_upload = current_upload + ?,
			current_download = current_download + ?,
			current_total = current_total + ?,
			updated_at = ?
		WHERE id = ?
	`, upload, download, upload+download, time.Now(), id)
	return err
}

// UpdatePackageStatus updates the package status
func (db *UserDB) UpdatePackageStatus(id string, status domain.PackageStatus) error {
	_, err := db.Exec(`UPDATE packages SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now(), id)
	return err
}

// ResetPackageUsage resets the usage counters
func (db *UserDB) ResetPackageUsage(id string) error {
	_, err := db.Exec(`
		UPDATE packages SET
			current_upload = 0,
			current_download = 0,
			current_total = 0,
			updated_at = ?
		WHERE id = ?
	`, time.Now(), id)
	return err
}

// Node operations

// CreateNode creates a new node
func (db *UserDB) CreateNode(node *domain.Node) error {
	if len(node.IPs) == 0 && len(node.AllowedIPs) > 0 {
		node.IPs = append([]string(nil), node.AllowedIPs...)
	}
	if len(node.AllowedIPs) == 0 && len(node.IPs) > 0 {
		node.AllowedIPs = append([]string(nil), node.IPs...)
	}

	allowedIPs, _ := json.Marshal(node.AllowedIPs)
	now := time.Now()

	_, err := db.Exec(`
		INSERT INTO nodes (id, secret_key, name, allowed_ips, traffic_multiplier, reset_mode, reset_day, current_upload, current_download, country, city, isp, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, node.ID, node.SecretKey, node.Name, string(allowedIPs), node.TrafficMultiplier,
		node.ResetMode, node.ResetDay, node.CurrentUpload, node.CurrentDownload,
		node.Country, node.City, node.ISP, now, now)

	return err
}

// GetNode retrieves a node by ID
func (db *UserDB) GetNode(id string) (*domain.Node, error) {
	node := &domain.Node{}
	var allowedIPs sql.NullString
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, secret_key, name, allowed_ips, traffic_multiplier, reset_mode, reset_day, current_upload, current_download, country, city, isp, created_at, updated_at
		FROM nodes WHERE id = ?
	`, id).Scan(
		&node.ID, &node.SecretKey, &node.Name, &allowedIPs, &node.TrafficMultiplier,
		&node.ResetMode, &node.ResetDay, &node.CurrentUpload, &node.CurrentDownload,
		&node.Country, &node.City, &node.ISP, &createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if allowedIPs.Valid {
		json.Unmarshal([]byte(allowedIPs.String), &node.AllowedIPs)
		node.IPs = append([]string(nil), node.AllowedIPs...)
	}
	node.CurrentTotal = node.CurrentUpload + node.CurrentDownload

	node.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}
	node.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return node, nil
}

// GetNodeBySecretKey retrieves a node by secret key
func (db *UserDB) GetNodeBySecretKey(secretKey string) (*domain.Node, error) {
	node := &domain.Node{}
	var allowedIPs sql.NullString
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, secret_key, name, allowed_ips, traffic_multiplier, reset_mode, reset_day, current_upload, current_download, country, city, isp, created_at, updated_at
		FROM nodes WHERE secret_key = ?
	`, secretKey).Scan(
		&node.ID, &node.SecretKey, &node.Name, &allowedIPs, &node.TrafficMultiplier,
		&node.ResetMode, &node.ResetDay, &node.CurrentUpload, &node.CurrentDownload,
		&node.Country, &node.City, &node.ISP, &createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if allowedIPs.Valid {
		json.Unmarshal([]byte(allowedIPs.String), &node.AllowedIPs)
		node.IPs = append([]string(nil), node.AllowedIPs...)
	}
	node.CurrentTotal = node.CurrentUpload + node.CurrentDownload

	node.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}
	node.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return node, nil
}

// ListNodes retrieves all nodes
func (db *UserDB) ListNodes() ([]*domain.Node, error) {
	rows, err := db.Query(`
		SELECT id, secret_key, name, allowed_ips, traffic_multiplier, reset_mode, reset_day, current_upload, current_download, country, city, isp, created_at, updated_at
		FROM nodes ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := []*domain.Node{}
	for rows.Next() {
		node := &domain.Node{}
		var allowedIPs sql.NullString
		var createdAtRaw, updatedAtRaw string

		err := rows.Scan(
			&node.ID, &node.SecretKey, &node.Name, &allowedIPs, &node.TrafficMultiplier,
			&node.ResetMode, &node.ResetDay, &node.CurrentUpload, &node.CurrentDownload,
			&node.Country, &node.City, &node.ISP, &createdAtRaw, &updatedAtRaw,
		)
		if err != nil {
			return nil, err
		}

		if allowedIPs.Valid {
			json.Unmarshal([]byte(allowedIPs.String), &node.AllowedIPs)
			node.IPs = append([]string(nil), node.AllowedIPs...)
		}
		node.CurrentTotal = node.CurrentUpload + node.CurrentDownload

		node.CreatedAt, err = parseSQLiteTime(createdAtRaw)
		if err != nil {
			return nil, err
		}
		node.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
		if err != nil {
			return nil, err
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// UpdateNodeUsage updates the node usage counters
func (db *UserDB) UpdateNodeUsage(id string, upload, download int64) error {
	_, err := db.Exec(`
		UPDATE nodes SET
			current_upload = current_upload + ?,
			current_download = current_download + ?,
			updated_at = ?
		WHERE id = ?
	`, upload, download, time.Now(), id)
	return err
}

// DeleteNode deletes a node
func (db *UserDB) DeleteNode(id string) error {
	_, err := db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}

// Service operations

// CreateService creates a new service
func (db *UserDB) CreateService(service *domain.Service) error {
	if service.SecretKey == "" && service.AccessToken != "" {
		service.SecretKey = service.AccessToken
	}
	if service.AccessToken == "" && service.SecretKey != "" {
		service.AccessToken = service.SecretKey
	}

	authMethods, _ := json.Marshal(service.AllowedAuthMethods)
	now := time.Now()

	return db.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO services (id, secret_key, node_id, name, protocol, allowed_auth_methods, callback_url, current_upload, current_download, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, service.ID, service.SecretKey, service.NodeID, service.Name, service.Protocol,
			string(authMethods), service.CallbackURL, service.CurrentUpload, service.CurrentDownload, now, now); err != nil {
			return err
		}

		if service.SecretKey != "" {
			hashed := hashAuthKey(service.SecretKey)
			if _, err := tx.Exec(`
				INSERT INTO service_auth_keys (service_id, hashed_key, revoked, created_at, updated_at)
				VALUES (?, ?, 0, ?, ?)
				ON CONFLICT(service_id) DO UPDATE SET
					hashed_key = excluded.hashed_key,
					revoked = 0,
					updated_at = excluded.updated_at
			`, service.ID, hashed, now, now); err != nil {
				return err
			}
		}

		return nil
	})
}

// GetService retrieves a service by ID
func (db *UserDB) GetService(id string) (*domain.Service, error) {
	service := &domain.Service{}
	var authMethods sql.NullString
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, secret_key, node_id, name, protocol, allowed_auth_methods, callback_url, current_upload, current_download, created_at, updated_at
		FROM services WHERE id = ?
	`, id).Scan(
		&service.ID, &service.SecretKey, &service.NodeID, &service.Name, &service.Protocol,
		&authMethods, &service.CallbackURL, &service.CurrentUpload, &service.CurrentDownload,
		&createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if authMethods.Valid {
		json.Unmarshal([]byte(authMethods.String), &service.AllowedAuthMethods)
	}
	if service.AccessToken == "" && service.SecretKey != "" {
		service.AccessToken = service.SecretKey
	}

	service.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}
	service.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return service, nil
}

// GetServiceBySecretKey retrieves a service by secret key
func (db *UserDB) GetServiceBySecretKey(secretKey string) (*domain.Service, error) {
	service := &domain.Service{}
	var authMethods sql.NullString
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, secret_key, node_id, name, protocol, allowed_auth_methods, callback_url, current_upload, current_download, created_at, updated_at
		FROM services WHERE secret_key = ?
	`, secretKey).Scan(
		&service.ID, &service.SecretKey, &service.NodeID, &service.Name, &service.Protocol,
		&authMethods, &service.CallbackURL, &service.CurrentUpload, &service.CurrentDownload,
		&createdAtRaw, &updatedAtRaw,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if authMethods.Valid {
		json.Unmarshal([]byte(authMethods.String), &service.AllowedAuthMethods)
	}
	if service.AccessToken == "" && service.SecretKey != "" {
		service.AccessToken = service.SecretKey
	}

	service.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}
	service.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return service, nil
}

// UpdateServiceUsage updates the service usage counters
func (db *UserDB) UpdateServiceUsage(id string, upload, download int64) error {
	_, err := db.Exec(`
		UPDATE services SET
			current_upload = current_upload + ?,
			current_download = current_download + ?,
			updated_at = ?
		WHERE id = ?
	`, upload, download, time.Now(), id)
	return err
}

// DeleteService deletes a service
func (db *UserDB) DeleteService(id string) error {
	_, err := db.Exec(`DELETE FROM services WHERE id = ?`, id)
	return err
}

func (db *UserDB) UpsertOwnerAuthKey(rawKey string) error {
	if rawKey == "" {
		return nil
	}

	now := time.Now()
	hashed := hashAuthKey(rawKey)
	_, err := db.Exec(`
		INSERT INTO owner_auth_key (key_id, hashed_key, revoked, created_at, updated_at)
		VALUES (1, ?, 0, ?, ?)
		ON CONFLICT(key_id) DO UPDATE SET
			hashed_key = excluded.hashed_key,
			revoked = 0,
			updated_at = excluded.updated_at
	`, hashed, now, now)
	return err
}

func (db *UserDB) ValidateOwnerAuthKey(rawKey string) (bool, error) {
	if rawKey == "" {
		return false, nil
	}

	var hashed string
	var revoked int
	err := db.QueryRow(`SELECT hashed_key, revoked FROM owner_auth_key WHERE key_id = 1`).Scan(&hashed, &revoked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if revoked != 0 {
		return false, nil
	}

	inputHash := hashAuthKey(rawKey)
	return subtle.ConstantTimeCompare([]byte(inputHash), []byte(hashed)) == 1, nil
}

func (db *UserDB) UpsertServiceAuthKey(serviceID, rawKey string) error {
	if serviceID == "" || rawKey == "" {
		return nil
	}

	now := time.Now()
	hashed := hashAuthKey(rawKey)
	_, err := db.Exec(`
		INSERT INTO service_auth_keys (service_id, hashed_key, revoked, created_at, updated_at)
		VALUES (?, ?, 0, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET
			hashed_key = excluded.hashed_key,
			revoked = 0,
			updated_at = excluded.updated_at
	`, serviceID, hashed, now, now)
	return err
}

func (db *UserDB) ValidateServiceAuthKey(serviceID, rawKey string) (bool, error) {
	if serviceID == "" || rawKey == "" {
		return false, nil
	}

	var hashed string
	var revoked int
	err := db.QueryRow(`SELECT hashed_key, revoked FROM service_auth_keys WHERE service_id = ?`, serviceID).Scan(&hashed, &revoked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if revoked != 0 {
		return false, nil
	}

	inputHash := hashAuthKey(rawKey)
	return subtle.ConstantTimeCompare([]byte(inputHash), []byte(hashed)) == 1, nil
}

func hashAuthKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type ManagerLimitCheckResult struct {
	Allowed   bool
	ManagerID string
	Reason    string
}

func (db *UserDB) CreateManager(manager *domain.Manager) error {
	if manager == nil || manager.Package == nil {
		return fmt.Errorf("manager and manager package are required")
	}

	if manager.ParentID != nil && *manager.ParentID != "" {
		parentPkg, err := db.GetManagerPackage(*manager.ParentID)
		if err != nil {
			return err
		}
		if parentPkg == nil {
			return fmt.Errorf("parent manager package not found")
		}
		if err := validateChildPackageAgainstParent(manager.Package, parentPkg); err != nil {
			return err
		}
	}

	metadata, _ := json.Marshal(manager.Metadata)
	now := time.Now()

	return db.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO managers (id, name, parent_id, metadata, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, manager.ID, manager.Name, manager.ParentID, string(metadata), now, now); err != nil {
			return err
		}

		pkg := manager.Package
		_, err := tx.Exec(`
			INSERT INTO manager_packages (
				manager_id, total_limit, upload_limit, download_limit, reset_mode, duration, start_at,
				max_sessions, max_online_users, max_active_users, status,
				current_upload, current_download, current_total,
				current_sessions, current_online_users, current_active_users,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			manager.ID, pkg.TotalLimit, pkg.UploadLimit, pkg.DownloadLimit, pkg.ResetMode, pkg.Duration, pkg.StartAt,
			pkg.MaxSessions, pkg.MaxOnlineUsers, pkg.MaxActiveUsers, pkg.Status,
			pkg.CurrentUpload, pkg.CurrentDownload, pkg.CurrentTotal,
			pkg.CurrentSessions, pkg.CurrentOnline, pkg.CurrentActive,
			now, now,
		)
		return err
	})
}

func (db *UserDB) GetManager(id string) (*domain.Manager, error) {
	manager := &domain.Manager{}
	var parentID sql.NullString
	var metadata sql.NullString
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, name, parent_id, metadata, created_at, updated_at
		FROM managers
		WHERE id = ?
	`, id).Scan(&manager.ID, &manager.Name, &parentID, &metadata, &createdAtRaw, &updatedAtRaw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if parentID.Valid {
		manager.ParentID = &parentID.String
	}
	if metadata.Valid && metadata.String != "" {
		_ = json.Unmarshal([]byte(metadata.String), &manager.Metadata)
	}

	manager.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}
	manager.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	pkg, err := db.GetManagerPackage(id)
	if err != nil {
		return nil, err
	}
	manager.Package = pkg

	return manager, nil
}

func (db *UserDB) GetManagerPackage(managerID string) (*domain.ManagerPackage, error) {
	pkg := &domain.ManagerPackage{}
	var startAt sql.NullTime
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT manager_id, total_limit, upload_limit, download_limit, reset_mode, duration, start_at,
			max_sessions, max_online_users, max_active_users, status,
			current_upload, current_download, current_total,
			current_sessions, current_online_users, current_active_users,
			created_at, updated_at
		FROM manager_packages WHERE manager_id = ?
	`, managerID).Scan(
		&pkg.ManagerID, &pkg.TotalLimit, &pkg.UploadLimit, &pkg.DownloadLimit, &pkg.ResetMode, &pkg.Duration, &startAt,
		&pkg.MaxSessions, &pkg.MaxOnlineUsers, &pkg.MaxActiveUsers, &pkg.Status,
		&pkg.CurrentUpload, &pkg.CurrentDownload, &pkg.CurrentTotal,
		&pkg.CurrentSessions, &pkg.CurrentOnline, &pkg.CurrentActive,
		&createdAtRaw, &updatedAtRaw,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if startAt.Valid {
		pkg.StartAt = &startAt.Time
	}
	pkg.CreatedAt, err = parseSQLiteTime(createdAtRaw)
	if err != nil {
		return nil, err
	}
	pkg.UpdatedAt, err = parseSQLiteTime(updatedAtRaw)
	if err != nil {
		return nil, err
	}

	return pkg, nil
}

func (db *UserDB) GetManagerAncestors(managerID string) ([]string, error) {
	ids := make([]string, 0, 4)
	current := managerID
	for current != "" {
		ids = append(ids, current)
		var parent sql.NullString
		err := db.QueryRow(`SELECT parent_id FROM managers WHERE id = ?`, current).Scan(&parent)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			return nil, err
		}
		if !parent.Valid || parent.String == "" {
			break
		}
		current = parent.String
	}
	return ids, nil
}

func (db *UserDB) CheckManagerLimits(managerID string, upload, download, sessionDelta, onlineUsersDelta, activeUsersDelta int64) (*ManagerLimitCheckResult, error) {
	if managerID == "" {
		return &ManagerLimitCheckResult{Allowed: true}, nil
	}

	ancestors, err := db.GetManagerAncestors(managerID)
	if err != nil {
		return nil, err
	}

	for _, id := range ancestors {
		pkg, err := db.GetManagerPackage(id)
		if err != nil {
			return nil, err
		}
		if pkg == nil || !pkg.IsActive() {
			continue
		}

		projectedUpload := pkg.CurrentUpload + upload
		projectedDownload := pkg.CurrentDownload + download
		projectedTotal := pkg.CurrentTotal + upload + download
		projectedSessions := pkg.CurrentSessions + sessionDelta
		projectedOnline := pkg.CurrentOnline + onlineUsersDelta
		projectedActive := pkg.CurrentActive + activeUsersDelta

		if pkg.TotalLimit > 0 && projectedTotal > pkg.TotalLimit {
			return &ManagerLimitCheckResult{Allowed: false, ManagerID: id, Reason: "manager total limit reached"}, nil
		}
		if pkg.UploadLimit > 0 && projectedUpload > pkg.UploadLimit {
			return &ManagerLimitCheckResult{Allowed: false, ManagerID: id, Reason: "manager upload limit reached"}, nil
		}
		if pkg.DownloadLimit > 0 && projectedDownload > pkg.DownloadLimit {
			return &ManagerLimitCheckResult{Allowed: false, ManagerID: id, Reason: "manager download limit reached"}, nil
		}
		if pkg.MaxSessions > 0 && projectedSessions > int64(pkg.MaxSessions) {
			return &ManagerLimitCheckResult{Allowed: false, ManagerID: id, Reason: "manager max sessions reached"}, nil
		}
		if pkg.MaxOnlineUsers > 0 && projectedOnline > int64(pkg.MaxOnlineUsers) {
			return &ManagerLimitCheckResult{Allowed: false, ManagerID: id, Reason: "manager max online users reached"}, nil
		}
		if pkg.MaxActiveUsers > 0 && projectedActive > int64(pkg.MaxActiveUsers) {
			return &ManagerLimitCheckResult{Allowed: false, ManagerID: id, Reason: "manager max active users reached"}, nil
		}
	}

	return &ManagerLimitCheckResult{Allowed: true}, nil
}

func (db *UserDB) ApplyManagerUsageDelta(managerID string, upload, download, sessionDelta, onlineUsersDelta, activeUsersDelta int64) error {
	if managerID == "" {
		return nil
	}

	ancestors, err := db.GetManagerAncestors(managerID)
	if err != nil {
		return err
	}

	return db.Transaction(func(tx *sql.Tx) error {
		now := time.Now()
		for _, id := range ancestors {
			_, err := tx.Exec(`
				UPDATE manager_packages
				SET
					current_upload = MAX(0, current_upload + ?),
					current_download = MAX(0, current_download + ?),
					current_total = MAX(0, current_total + ?),
					current_sessions = MAX(0, current_sessions + ?),
					current_online_users = MAX(0, current_online_users + ?),
					current_active_users = MAX(0, current_active_users + ?),
					updated_at = ?
				WHERE manager_id = ?
			`,
				upload,
				download,
				upload+download,
				sessionDelta,
				onlineUsersDelta,
				activeUsersDelta,
				now,
				id,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func validateChildPackageAgainstParent(child, parent *domain.ManagerPackage) error {
	if child == nil || parent == nil {
		return nil
	}

	if parent.TotalLimit > 0 && child.TotalLimit > parent.TotalLimit {
		return fmt.Errorf("child total_limit exceeds parent")
	}
	if parent.UploadLimit > 0 && child.UploadLimit > parent.UploadLimit {
		return fmt.Errorf("child upload_limit exceeds parent")
	}
	if parent.DownloadLimit > 0 && child.DownloadLimit > parent.DownloadLimit {
		return fmt.Errorf("child download_limit exceeds parent")
	}
	if parent.MaxSessions > 0 && child.MaxSessions > parent.MaxSessions {
		return fmt.Errorf("child max_sessions exceeds parent")
	}
	if parent.MaxOnlineUsers > 0 && child.MaxOnlineUsers > parent.MaxOnlineUsers {
		return fmt.Errorf("child max_online_users exceeds parent")
	}
	if parent.MaxActiveUsers > 0 && child.MaxActiveUsers > parent.MaxActiveUsers {
		return fmt.Errorf("child max_active_users exceeds parent")
	}

	return nil
}

func joinConditions(conditions []string, sep string) string {
	result := ""
	for i, c := range conditions {
		if i > 0 {
			result += sep
		}
		result += c
	}
	return result
}
