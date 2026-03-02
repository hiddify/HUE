package engine

import (
	"fmt"
	"net"

	"github.com/hiddify/hue-go/internal/domain"
	"github.com/oschwald/geoip2-golang"
)

// GeoHandler handles GeoIP extraction with zero raw IP retention
type GeoHandler struct {
	db *geoip2.Reader
}

// NewGeoHandler creates a new GeoHandler instance
func NewGeoHandler(dbPath string) (*GeoHandler, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("maxmind db path not configured")
	}

	db, err := geoip2.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open maxmind db: %w", err)
	}

	return &GeoHandler{db: db}, nil
}

// ExtractGeo extracts geo information from an IP and immediately discards the IP
// This enforces the Zero Raw-IP Retention policy
func (h *GeoHandler) ExtractGeo(ipStr string) *domain.GeoData {
	if h.db == nil {
		return &domain.GeoData{}
	}

	// Parse IP
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return &domain.GeoData{}
	}

	// Lookup geo data
	city, err := h.db.City(ip)
	if err != nil {
		return &domain.GeoData{}
	}

	// Extract data
	geoData := &domain.GeoData{
		Country: h.getEnglishName(city.Country.Names),
		City:    h.getEnglishName(city.City.Names),
		ISP:     "", // ISP requires ASN database
		ASN:     0,  // ASN requires separate database
	}

	// IP is discarded here - no storage, no logging
	// The geoData is returned without any IP reference

	return geoData
}

// ExtractGeoWithISP extracts geo information including ISP (requires ASN database)
func (h *GeoHandler) ExtractGeoWithISP(ipStr string) *domain.GeoData {
	geoData := h.ExtractGeo(ipStr)

	// ISP extraction would require a separate ASN database
	// For now, we leave ISP empty

	return geoData
}

// Close closes the GeoIP database
func (h *GeoHandler) Close() error {
	if h.db != nil {
		return h.db.Close()
	}
	return nil
}

// IsReady returns true if the handler is ready to use
func (h *GeoHandler) IsReady() bool {
	return h.db != nil
}

// getEnglishName gets the English name from a map of names
func (h *GeoHandler) getEnglishName(names map[string]string) string {
	if names == nil {
		return ""
	}
	if name, ok := names["en"]; ok {
		return name
	}
	// Return first available name if English not found
	for _, name := range names {
		return name
	}
	return ""
}
