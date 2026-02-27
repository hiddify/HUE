package domain

import (
	"time"
)

// PackageStatus represents the current state of a package
type PackageStatus string

const (
	PackageStatusActive    PackageStatus = "active"
	PackageStatusExpired   PackageStatus = "expired"
	PackageStatusFinish    PackageStatus = "finish"
	PackageStatusSuspended PackageStatus = "suspended"
)

// ResetMode defines how usage counters are reset
type ResetMode string

const (
	ResetModeNoReset ResetMode = "no-reset"
	ResetModeHourly  ResetMode = "hourly"
	ResetModeDaily   ResetMode = "daily"
	ResetModeWeekly  ResetMode = "weekly"
	ResetModeMonthly ResetMode = "monthly"
	ResetModeYearly  ResetMode = "yearly"
)

// Package represents a subscription package
type Package struct {
	ID              string        `json:"id" db:"id"`
	UserID          string        `json:"user_id" db:"user_id"`
	TotalLimit      int64         `json:"total_limit" db:"total_traffic"`
	TotalTraffic    int64         `json:"total_traffic" db:"total_traffic"`       // Bytes
	UploadLimit     int64         `json:"upload_limit,omitempty" db:"upload_limit"`   // Bytes, 0 = unlimited
	DownloadLimit   int64         `json:"download_limit,omitempty" db:"download_limit"` // Bytes, 0 = unlimited
	ResetMode       ResetMode     `json:"reset_mode" db:"reset_mode"`
	Duration        int64         `json:"duration" db:"duration"` // Seconds
	StartAt         *time.Time    `json:"start_at,omitempty" db:"start_at"`
	MaxConcurrent   int           `json:"max_concurrent" db:"max_concurrent"`
	Status          PackageStatus `json:"status" db:"status"`
	CurrentUpload   int64         `json:"current_upload" db:"current_upload"`
	CurrentDownload int64         `json:"current_download" db:"current_download"`
	CurrentTotal    int64         `json:"current_total" db:"current_total"`
	ExpiresAt       *time.Time    `json:"expires_at,omitempty" db:"expires_at"`
	CreatedAt       time.Time     `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at" db:"updated_at"`
}

// PackageCreate represents the input for creating a new package
type PackageCreate struct {
	UserID        string     `json:"user_id" validate:"required"`
	TotalLimit    int64      `json:"total_limit"`
	TotalTraffic  int64      `json:"total_traffic" validate:"min=0"`
	UploadLimit   int64      `json:"upload_limit,omitempty"`
	DownloadLimit int64      `json:"download_limit,omitempty"`
	ResetMode     ResetMode  `json:"reset_mode" validate:"required"`
	Duration      int64      `json:"duration" validate:"required,min=1"` // Seconds
	StartAt       *time.Time `json:"start_at,omitempty"`
	MaxConcurrent int        `json:"max_concurrent" validate:"min=1"`
}

// PackageUpdate represents the input for updating a package
type PackageUpdate struct {
	TotalTraffic    *int64        `json:"total_traffic,omitempty"`
	UploadLimit     *int64        `json:"upload_limit,omitempty"`
	DownloadLimit   *int64        `json:"download_limit,omitempty"`
	ResetMode       *ResetMode    `json:"reset_mode,omitempty"`
	Duration        *int64        `json:"duration,omitempty"`
	MaxConcurrent   *int          `json:"max_concurrent,omitempty"`
	Status          *PackageStatus `json:"status,omitempty"`
}

// IsActive returns true if the package is active
func (p *Package) IsActive() bool {
	return p.Status == PackageStatusActive
}

// IsExpired returns true if the package has expired
func (p *Package) IsExpired() bool {
	if p.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*p.ExpiresAt)
}

// HasTrafficRemaining returns true if there is traffic quota remaining
func (p *Package) HasTrafficRemaining() bool {
	total := p.TotalLimit
	if total == 0 {
		total = p.TotalTraffic
	}
	if total == 0 {
		return true // Unlimited
	}
	return p.CurrentTotal < total
}

// HasUploadRemaining returns true if upload quota is remaining
func (p *Package) HasUploadRemaining() bool {
	if p.UploadLimit == 0 {
		return true // Unlimited
	}
	return p.CurrentUpload < p.UploadLimit
}

// HasDownloadRemaining returns true if download quota is remaining
func (p *Package) HasDownloadRemaining() bool {
	if p.DownloadLimit == 0 {
		return true // Unlimited
	}
	return p.CurrentDownload < p.DownloadLimit
}

// CanUse returns true if the package can be used (active, not expired, has quota)
func (p *Package) CanUse() bool {
	return p.IsActive() && !p.IsExpired() && p.HasTrafficRemaining()
}

// AddUsage adds upload and download bytes to the current counters
func (p *Package) AddUsage(upload, download int64) {
	if p.TotalLimit == 0 && p.TotalTraffic > 0 {
		p.TotalLimit = p.TotalTraffic
	}
	if p.TotalTraffic == 0 && p.TotalLimit > 0 {
		p.TotalTraffic = p.TotalLimit
	}

	p.CurrentUpload += upload
	p.CurrentDownload += download
	p.CurrentTotal += upload + download
	p.UpdatedAt = time.Now()
}

// CalculateNextReset returns the next reset time based on reset mode
func (p *Package) CalculateNextReset() *time.Time {
	now := time.Now()
	
	switch p.ResetMode {
	case ResetModeHourly:
		next := now.Add(time.Hour)
		return &next
	case ResetModeDaily:
		next := now.AddDate(0, 0, 1)
		return &next
	case ResetModeWeekly:
		next := now.AddDate(0, 0, 7)
		return &next
	case ResetModeMonthly:
		next := now.AddDate(0, 1, 0)
		return &next
	case ResetModeYearly:
		next := now.AddDate(1, 0, 0)
		return &next
	default:
		return nil
	}
}
