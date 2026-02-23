package domain

import (
	"time"
)

// AuthMethod represents supported authentication methods
type AuthMethod string

const (
	AuthMethodUUID     AuthMethod = "uuid"
	AuthMethodPassword AuthMethod = "password"
	AuthMethodPubKey   AuthMethod = "pubkey"
	AuthMethodCert     AuthMethod = "cert"
)

// Service represents a protocol instance on a Node
type Service struct {
	ID              string      `json:"id" db:"id"`
	SecretKey       string      `json:"-" db:"secret_key"` // Omit from JSON responses
	NodeID          string      `json:"node_id" db:"node_id"`
	Name            string      `json:"name" db:"name"`
	Protocol        string      `json:"protocol" db:"protocol"` // vless, trojan, wireguard, etc.
	AllowedAuthMethods []AuthMethod `json:"allowed_auth_methods" db:"allowed_auth_methods"`
	CallbackURL     string      `json:"callback_url,omitempty" db:"callback_url"`
	CurrentUpload   int64       `json:"current_upload" db:"current_upload"`
	CurrentDownload int64       `json:"current_download" db:"current_download"`
	CreatedAt       time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at" db:"updated_at"`
}

// ServiceCreate represents the input for creating a new service
type ServiceCreate struct {
	NodeID            string      `json:"node_id" validate:"required"`
	SecretKey         string      `json:"secret_key" validate:"required"`
	Name              string      `json:"name" validate:"required"`
	Protocol          string      `json:"protocol" validate:"required"`
	AllowedAuthMethods []AuthMethod `json:"allowed_auth_methods" validate:"required"`
	CallbackURL       string      `json:"callback_url,omitempty"`
}

// ServiceUpdate represents the input for updating a service
type ServiceUpdate struct {
	Name              *string     `json:"name,omitempty"`
	SecretKey         *string    `json:"secret_key,omitempty"`
	AllowedAuthMethods *[]AuthMethod `json:"allowed_auth_methods,omitempty"`
	CallbackURL       *string    `json:"callback_url,omitempty"`
}

// AddUsage adds upload and download bytes to the service counters
func (s *Service) AddUsage(upload, download int64) {
	s.CurrentUpload += upload
	s.CurrentDownload += download
	s.UpdatedAt = time.Now()
}

// SupportsAuthMethod returns true if the service supports the given auth method
func (s *Service) SupportsAuthMethod(method AuthMethod) bool {
	for _, m := range s.AllowedAuthMethods {
		if m == method {
			return true
		}
	}
	return false
}
