package domain

import (
	"time"
)

// UserStatus represents the current state of a user
type UserStatus string

const (
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
	UserStatusExpired   UserStatus = "expired"
	UserStatusFinish    UserStatus = "finish"
	UserStatusInactive  UserStatus = "inactive"
)

// User represents a user entity in the system
type User struct {
	ID             string     `json:"id" db:"id"`
	ManagerID      *string    `json:"manager_id,omitempty" db:"manager_id"`
	Username       string     `json:"username" db:"username"`
	Password       string     `json:"-" db:"password"` // Omit from JSON responses
	PublicKey      string     `json:"public_key,omitempty" db:"public_key"`
	PrivateKey     string     `json:"-" db:"private_key"` // Omit from JSON responses
	CACertList     []string   `json:"ca_cert_list,omitempty" db:"ca_cert_list"`
	Groups         []string   `json:"groups,omitempty" db:"groups"`
	AllowedDevices []string   `json:"allowed_devices,omitempty" db:"allowed_devices"`
	Status         UserStatus `json:"status" db:"status"`
	ActivePackageID *string   `json:"active_package_id,omitempty" db:"active_package_id"`
	FirstConnectionAt *time.Time `json:"first_connection_at,omitempty" db:"first_connection_at"`
	LastConnectionAt  *time.Time `json:"last_connection_at,omitempty" db:"last_connection_at"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" db:"updated_at"`
}

// UserCreate represents the input for creating a new user
type UserCreate struct {
	Username       string   `json:"username" validate:"required"`
	ManagerID      *string  `json:"manager_id,omitempty"`
	Password       string   `json:"password" validate:"required"`
	PublicKey      string   `json:"public_key,omitempty"`
	PrivateKey     string   `json:"private_key,omitempty"`
	CACertList     []string `json:"ca_cert_list,omitempty"`
	Groups         []string `json:"groups,omitempty"`
	AllowedDevices []string `json:"allowed_devices,omitempty"`
	ActivePackageID *string `json:"active_package_id,omitempty"`
}

// UserUpdate represents the input for updating a user
type UserUpdate struct {
	Username       *string   `json:"username,omitempty"`
	ManagerID      *string   `json:"manager_id,omitempty"`
	Password       *string   `json:"password,omitempty"`
	PublicKey      *string   `json:"public_key,omitempty"`
	PrivateKey     *string   `json:"private_key,omitempty"`
	CACertList     *[]string `json:"ca_cert_list,omitempty"`
	Groups         *[]string `json:"groups,omitempty"`
	AllowedDevices *[]string `json:"allowed_devices,omitempty"`
	Status         *UserStatus `json:"status,omitempty"`
	ActivePackageID *string  `json:"active_package_id,omitempty"`
}

// UserFilter represents filters for listing users
type UserFilter struct {
	Status  *UserStatus `json:"status,omitempty"`
	Group   *string     `json:"group,omitempty"`
	Search  *string     `json:"search,omitempty"`
	Limit   int         `json:"limit,omitempty"`
	Offset  int         `json:"offset,omitempty"`
}

// IsActive returns true if the user is in active status
func (u *User) IsActive() bool {
	return u.Status == UserStatusActive
}

// CanConnect returns true if the user can establish a connection
func (u *User) CanConnect() bool {
	return u.IsActive() && u.ActivePackageID != nil
}
