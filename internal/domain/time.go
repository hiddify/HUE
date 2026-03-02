package domain

import (
	"time"
)

// ParseTime converts a Unix timestamp to time.Time
func ParseTime(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

// FormatTime converts time.Time to Unix timestamp
func FormatTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
