// Package geo resolves a client IP to country/city/ASN via MaxMind's
// GeoLite2 database, then drops the IP. The IP must NOT be persisted or
// logged after Lookup returns — that's the whole privacy guarantee
// (PRD §3.1, §5).
package geo

import (
	"errors"
	"net"

	"github.com/oschwald/geoip2-golang"
)

// Result is the geo metadata extracted from an IP. It contains no IP.
type Result struct {
	Country string // ISO-3166 two-letter
	City    string // English name
	ASN     uint32
}

// Resolver wraps a MaxMind reader. A nil-db Resolver returns empty
// results from Lookup — useful when MAXMIND_DB_PATH is unset and you
// don't want every report to fail.
type Resolver struct {
	db *geoip2.Reader
}

// New opens the MaxMind DB at path. An empty path returns a no-op resolver.
func New(path string) (*Resolver, error) {
	if path == "" {
		return &Resolver{}, nil
	}
	db, err := geoip2.Open(path)
	if err != nil {
		return nil, err
	}
	return &Resolver{db: db}, nil
}

// Ready reports whether a real database is loaded.
func (r *Resolver) Ready() bool { return r != nil && r.db != nil }

// Lookup parses ipStr, queries the DB, and returns the geo metadata. The
// caller is responsible for not retaining ipStr after this function
// returns. The function itself does not log or persist it.
func (r *Resolver) Lookup(ipStr string) (Result, error) {
	if r == nil || r.db == nil {
		return Result{}, nil
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return Result{}, errors.New("invalid ip")
	}
	rec, err := r.db.City(ip)
	if err != nil {
		return Result{}, err
	}
	// ASN is not present in GeoLite2-City.mmdb; it lives in GeoLite2-ASN.mmdb.
	// We only support the city DB for now — ASN can be plumbed in later
	// by accepting a second MaxMind reader.
	return Result{
		Country: rec.Country.IsoCode,
		City:    rec.City.Names["en"],
	}, nil
}

// Close releases the MaxMind DB handle. Safe on nil/empty Resolver.
func (r *Resolver) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
