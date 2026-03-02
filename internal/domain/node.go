package domain

import (
	"time"
)

// Node represents a server hosting services
type Node struct {
	ID               string     `json:"id" db:"id"`
	SecretKey        string     `json:"-" db:"secret_key"` // Omit from JSON responses
	Name             string     `json:"name" db:"name"`
	IPs              []string   `json:"ips,omitempty" db:"allowed_ips"`
	AllowedIPs       []string   `json:"allowed_ips,omitempty" db:"allowed_ips"`
	TrafficMultiplier float64   `json:"traffic_multiplier" db:"traffic_multiplier"`
	ResetMode        ResetMode  `json:"reset_mode" db:"reset_mode"`
	ResetDay         int        `json:"reset_day,omitempty" db:"reset_day"` // Day of week/month for reset
	CurrentUpload    int64      `json:"current_upload" db:"current_upload"`
	CurrentDownload  int64      `json:"current_download" db:"current_download"`
	CurrentTotal     int64      `json:"current_total" db:"-"`
	Country          string     `json:"country,omitempty" db:"country"`
	City             string     `json:"city,omitempty" db:"city"`
	ISP              string     `json:"isp,omitempty" db:"isp"`
	CreatedAt        time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at" db:"updated_at"`
}

// NodeCreate represents the input for creating a new node
type NodeCreate struct {
	Name              string    `json:"name" validate:"required"`
	SecretKey         string    `json:"secret_key" validate:"required"`
	AllowedIPs        []string  `json:"allowed_ips,omitempty"`
	TrafficMultiplier float64   `json:"traffic_multiplier" validate:"min=0.1"`
	ResetMode         ResetMode `json:"reset_mode"`
	ResetDay          int       `json:"reset_day,omitempty"`
	Country           string    `json:"country,omitempty"`
	City              string    `json:"city,omitempty"`
	ISP               string    `json:"isp,omitempty"`
}

// NodeUpdate represents the input for updating a node
type NodeUpdate struct {
	Name              *string   `json:"name,omitempty"`
	SecretKey         *string   `json:"secret_key,omitempty"`
	AllowedIPs        *[]string `json:"allowed_ips,omitempty"`
	TrafficMultiplier *float64  `json:"traffic_multiplier,omitempty"`
	ResetMode         *ResetMode `json:"reset_mode,omitempty"`
	ResetDay          *int      `json:"reset_day,omitempty"`
	Country           *string   `json:"country,omitempty"`
	City              *string   `json:"city,omitempty"`
	ISP               *string   `json:"isp,omitempty"`
}

// AddUsage adds upload and download bytes to the node counters
func (n *Node) AddUsage(upload, download int64) {
	n.CurrentUpload += upload
	n.CurrentDownload += download
	n.CurrentTotal += upload + download
	n.syncIPs()
	n.UpdatedAt = time.Now()
}

// ApplyMultiplier applies the traffic multiplier to usage values
func (n *Node) ApplyMultiplier(upload, download int64) (int64, int64) {
	n.syncIPs()
	if n.TrafficMultiplier == 0 || n.TrafficMultiplier == 1 {
		return upload, download
	}
	return int64(float64(upload) * n.TrafficMultiplier), 
	       int64(float64(download) * n.TrafficMultiplier)
}

func (n *Node) syncIPs() {
	if len(n.IPs) == 0 && len(n.AllowedIPs) > 0 {
		n.IPs = append([]string(nil), n.AllowedIPs...)
	}
	if len(n.AllowedIPs) == 0 && len(n.IPs) > 0 {
		n.AllowedIPs = append([]string(nil), n.IPs...)
	}
}
