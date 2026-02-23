package sqlite

import (
	"database/sql"
	"encoding/json"
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
		`CREATE INDEX IF NOT EXISTS idx_users_status ON users(status)`,
		`CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)`,
		`CREATE INDEX IF NOT EXISTS idx_packages_user_id ON packages(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_packages_status ON packages(status)`,
		`CREATE INDEX IF NOT EXISTS idx_services_node_id ON services(node_id)`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w", err)
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
		INSERT INTO users (id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, user.ID, user.Username, user.Password, user.PublicKey, user.PrivateKey, string(caCerts), string(groups), string(devices), user.Status, user.ActivePackageID, now, now)

	return err
}

// GetUser retrieves a user by ID
func (db *UserDB) GetUser(id string) (*domain.User, error) {
	user := &domain.User{}
	var caCerts, groups, devices sql.NullString
	var activePackageID sql.NullString
	var firstConn, lastConn sql.NullTime
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, first_connection_at, last_connection_at, created_at, updated_at
		FROM users WHERE id = ?
	`, id).Scan(
		&user.ID, &user.Username, &user.Password, &user.PublicKey, &user.PrivateKey,
		&caCerts, &groups, &devices, &user.Status, &activePackageID,
		&firstConn, &lastConn, &createdAtRaw, &updatedAtRaw,
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
	if activePackageID.Valid {
		user.ActivePackageID = &activePackageID.String
	}
	if firstConn.Valid {
		user.FirstConnectionAt = &firstConn.Time
	}
	if lastConn.Valid {
		user.LastConnectionAt = &lastConn.Time
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
	var activePackageID sql.NullString
	var firstConn, lastConn sql.NullTime
	var createdAtRaw, updatedAtRaw string

	err := db.QueryRow(`
		SELECT id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, first_connection_at, last_connection_at, created_at, updated_at
		FROM users WHERE username = ?
	`, username).Scan(
		&user.ID, &user.Username, &user.Password, &user.PublicKey, &user.PrivateKey,
		&caCerts, &groups, &devices, &user.Status, &activePackageID,
		&firstConn, &lastConn, &createdAtRaw, &updatedAtRaw,
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
	if activePackageID.Valid {
		user.ActivePackageID = &activePackageID.String
	}
	if firstConn.Valid {
		user.FirstConnectionAt = &firstConn.Time
	}
	if lastConn.Valid {
		user.LastConnectionAt = &lastConn.Time
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
	query := `SELECT id, username, password, public_key, private_key, ca_cert_list, groups, allowed_devices, status, active_package_id, first_connection_at, last_connection_at, created_at, updated_at FROM users`
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
		var activePackageID sql.NullString
		var firstConn, lastConn sql.NullTime
		var createdAtRaw, updatedAtRaw string

		err := rows.Scan(
			&user.ID, &user.Username, &user.Password, &user.PublicKey, &user.PrivateKey,
			&caCerts, &groups, &devices, &user.Status, &activePackageID,
			&firstConn, &lastConn, &createdAtRaw, &updatedAtRaw,
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
		if activePackageID.Valid {
			user.ActivePackageID = &activePackageID.String
		}
		if firstConn.Valid {
			user.FirstConnectionAt = &firstConn.Time
		}
		if lastConn.Valid {
			user.LastConnectionAt = &lastConn.Time
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
			username = ?, password = ?, public_key = ?, private_key = ?,
			ca_cert_list = ?, groups = ?, allowed_devices = ?,
			status = ?, active_package_id = ?, first_connection_at = ?,
			last_connection_at = ?, updated_at = ?
		WHERE id = ?
	`, user.Username, user.Password, user.PublicKey, user.PrivateKey,
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

	err := db.QueryRow(`
		SELECT id, secret_key, name, allowed_ips, traffic_multiplier, reset_mode, reset_day, current_upload, current_download, country, city, isp, created_at, updated_at
		FROM nodes WHERE id = ?
	`, id).Scan(
		&node.ID, &node.SecretKey, &node.Name, &allowedIPs, &node.TrafficMultiplier,
		&node.ResetMode, &node.ResetDay, &node.CurrentUpload, &node.CurrentDownload,
		&node.Country, &node.City, &node.ISP, &node.CreatedAt, &node.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if allowedIPs.Valid {
		json.Unmarshal([]byte(allowedIPs.String), &node.AllowedIPs)
	}

	return node, nil
}

// GetNodeBySecretKey retrieves a node by secret key
func (db *UserDB) GetNodeBySecretKey(secretKey string) (*domain.Node, error) {
	node := &domain.Node{}
	var allowedIPs sql.NullString

	err := db.QueryRow(`
		SELECT id, secret_key, name, allowed_ips, traffic_multiplier, reset_mode, reset_day, current_upload, current_download, country, city, isp, created_at, updated_at
		FROM nodes WHERE secret_key = ?
	`, secretKey).Scan(
		&node.ID, &node.SecretKey, &node.Name, &allowedIPs, &node.TrafficMultiplier,
		&node.ResetMode, &node.ResetDay, &node.CurrentUpload, &node.CurrentDownload,
		&node.Country, &node.City, &node.ISP, &node.CreatedAt, &node.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if allowedIPs.Valid {
		json.Unmarshal([]byte(allowedIPs.String), &node.AllowedIPs)
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

		err := rows.Scan(
			&node.ID, &node.SecretKey, &node.Name, &allowedIPs, &node.TrafficMultiplier,
			&node.ResetMode, &node.ResetDay, &node.CurrentUpload, &node.CurrentDownload,
			&node.Country, &node.City, &node.ISP, &node.CreatedAt, &node.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if allowedIPs.Valid {
			json.Unmarshal([]byte(allowedIPs.String), &node.AllowedIPs)
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
	authMethods, _ := json.Marshal(service.AllowedAuthMethods)
	now := time.Now()

	_, err := db.Exec(`
		INSERT INTO services (id, secret_key, node_id, name, protocol, allowed_auth_methods, callback_url, current_upload, current_download, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, service.ID, service.SecretKey, service.NodeID, service.Name, service.Protocol,
		string(authMethods), service.CallbackURL, service.CurrentUpload, service.CurrentDownload, now, now)

	return err
}

// GetService retrieves a service by ID
func (db *UserDB) GetService(id string) (*domain.Service, error) {
	service := &domain.Service{}
	var authMethods sql.NullString

	err := db.QueryRow(`
		SELECT id, secret_key, node_id, name, protocol, allowed_auth_methods, callback_url, current_upload, current_download, created_at, updated_at
		FROM services WHERE id = ?
	`, id).Scan(
		&service.ID, &service.SecretKey, &service.NodeID, &service.Name, &service.Protocol,
		&authMethods, &service.CallbackURL, &service.CurrentUpload, &service.CurrentDownload,
		&service.CreatedAt, &service.UpdatedAt,
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

	return service, nil
}

// GetServiceBySecretKey retrieves a service by secret key
func (db *UserDB) GetServiceBySecretKey(secretKey string) (*domain.Service, error) {
	service := &domain.Service{}
	var authMethods sql.NullString

	err := db.QueryRow(`
		SELECT id, secret_key, node_id, name, protocol, allowed_auth_methods, callback_url, current_upload, current_download, created_at, updated_at
		FROM services WHERE secret_key = ?
	`, secretKey).Scan(
		&service.ID, &service.SecretKey, &service.NodeID, &service.Name, &service.Protocol,
		&authMethods, &service.CallbackURL, &service.CurrentUpload, &service.CurrentDownload,
		&service.CreatedAt, &service.UpdatedAt,
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
