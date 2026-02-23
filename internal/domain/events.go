package domain

import (
	"time"
)

// EventType represents the type of event
type EventType string

const (
	EventUserConnected    EventType = "USER_CONNECTED"
	EventUserDisconnected EventType = "USER_DISCONNECTED"
	EventUsageRecorded    EventType = "USAGE_RECORDED"
	EventPackageExpired   EventType = "PACKAGE_EXPIRED"
	EventPackageReset     EventType = "PACKAGE_RESET"
	EventNodeReset        EventType = "NODE_RESET"
	EventUserSuspended    EventType = "USER_SUSPENDED"
	EventUserActivated    EventType = "USER_ACTIVATED"
	EventPenaltyApplied   EventType = "PENALTY_APPLIED"
	EventPenaltyExpired   EventType = "PENALTY_EXPIRED"
)

// Event represents an immutable event in the system
type Event struct {
	ID          string      `json:"id" db:"id"`
	Type        EventType   `json:"type" db:"type"`
	UserID      *string     `json:"user_id,omitempty" db:"user_id"`
	PackageID   *string     `json:"package_id,omitempty" db:"package_id"`
	NodeID      *string     `json:"node_id,omitempty" db:"node_id"`
	ServiceID   *string     `json:"service_id,omitempty" db:"service_id"`
	Tags        []string    `json:"tags,omitempty" db:"tags"`
	Metadata    []byte      `json:"metadata,omitempty" db:"metadata"` // JSON encoded additional data
	Timestamp   time.Time   `json:"timestamp" db:"timestamp"`
}

// UsageReport represents a usage report from a service/node
type UsageReport struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id" validate:"required"`
	NodeID       string    `json:"node_id" validate:"required"`
	ServiceID    string    `json:"service_id" validate:"required"`
	Upload       int64     `json:"upload" validate:"min=0"`
	Download     int64     `json:"download" validate:"min=0"`
	SessionID    string    `json:"session_id,omitempty"`
	ClientIP     string    `json:"client_ip,omitempty"` // Will be deleted after geo extraction
	Tags         []string  `json:"tags,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

// UsageReportResult represents the result of processing a usage report
type UsageReportResult struct {
	UserID         string `json:"user_id"`
	PackageID      string `json:"package_id"`
	Accepted       bool   `json:"accepted"`
	QuotaExceeded  bool   `json:"quota_exceeded"`
	SessionLimitHit bool  `json:"session_limit_hit"`
	PenaltyApplied bool   `json:"penalty_applied"`
	ShouldDisconnect bool `json:"should_disconnect"`
	Reason         string `json:"reason,omitempty"`
}

// SessionInfo represents information about an active session
type SessionInfo struct {
	UserID     string    `json:"user_id"`
	SessionID  string    `json:"session_id"`
	IPHash     string    `json:"ip_hash"` // Hashed IP for privacy
	Country    string    `json:"country,omitempty"`
	City       string    `json:"city,omitempty"`
	ISP        string    `json:"isp,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// GeoData represents extracted geo information
type GeoData struct {
	Country string `json:"country,omitempty"`
	City    string `json:"city,omitempty"`
	ISP     string `json:"isp,omitempty"`
	ASN     uint   `json:"asn,omitempty"`
}

// NewEvent creates a new event with the current timestamp
func NewEvent(eventType EventType, userID, packageID, nodeID, serviceID *string, tags []string, metadata []byte) *Event {
	return &Event{
		Type:      eventType,
		UserID:    userID,
		PackageID: packageID,
		NodeID:    nodeID,
		ServiceID: serviceID,
		Tags:      tags,
		Metadata:  metadata,
		Timestamp: time.Now(),
	}
}
