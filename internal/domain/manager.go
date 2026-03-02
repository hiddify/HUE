package domain

import "time"

type EnforcementMode string

const (
	EnforcementModeSoft    EnforcementMode = "soft"
	EnforcementModeDefault EnforcementMode = "default"
	EnforcementModeHard    EnforcementMode = "hard"
)

type ManagerPackageStatus string

const (
	ManagerPackageStatusInactive ManagerPackageStatus = "inactive"
	ManagerPackageStatusActive   ManagerPackageStatus = "active"
)

type ManagerPackage struct {
	ManagerID       string               `json:"manager_id" db:"manager_id"`
	TotalLimit      int64                `json:"total_limit" db:"total_limit"`
	UploadLimit     int64                `json:"upload_limit" db:"upload_limit"`
	DownloadLimit   int64                `json:"download_limit" db:"download_limit"`
	ResetMode       ResetMode            `json:"reset_mode" db:"reset_mode"`
	Duration        int64                `json:"duration" db:"duration"`
	StartAt         *time.Time           `json:"start_at,omitempty" db:"start_at"`
	MaxSessions     int                  `json:"max_sessions" db:"max_sessions"`
	MaxOnlineUsers  int                  `json:"max_online_users" db:"max_online_users"`
	MaxActiveUsers  int                  `json:"max_active_users" db:"max_active_users"`
	Status          ManagerPackageStatus `json:"status" db:"status"`
	CurrentUpload   int64                `json:"current_upload" db:"current_upload"`
	CurrentDownload int64                `json:"current_download" db:"current_download"`
	CurrentTotal    int64                `json:"current_total" db:"current_total"`
	CurrentSessions int64                `json:"current_sessions" db:"current_sessions"`
	CurrentOnline   int64                `json:"current_online_users" db:"current_online_users"`
	CurrentActive   int64                `json:"current_active_users" db:"current_active_users"`
	CreatedAt       time.Time            `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at" db:"updated_at"`
}

func (p *ManagerPackage) IsActive() bool {
	return p != nil && p.Status == ManagerPackageStatusActive
}

type Manager struct {
	ID        string                 `json:"id" db:"id"`
	Name      string                 `json:"name" db:"name"`
	ParentID  *string                `json:"parent_id,omitempty" db:"parent_id"`
	Metadata  map[string]interface{} `json:"metadata,omitempty" db:"metadata"`
	Package   *ManagerPackage        `json:"package,omitempty"`
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}

func (m *Manager) HasParent() bool {
	return m != nil && m.ParentID != nil && *m.ParentID != ""
}
