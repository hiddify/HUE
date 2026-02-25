package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type Scope uint32

const (
	ScopeFull Scope = 1 << iota
	ScopeServiceUpdate
	ScopeReadOnly
)

type ServiceAPIKey struct {
	ServiceID  string
	HashedKey  string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	Revoked    bool
}

type OwnerAPIKey struct {
	HashedKey  string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	Revoked    bool
}

// Authenticator handles authentication for gRPC and HTTP
type Authenticator struct {
	secret         string
	allowedNodeIPs []*net.IPNet
	tlsConfig      *tls.Config
}

// NewAuthenticator creates a new Authenticator instance
func NewAuthenticator(secret, tlsCertPath, tlsKeyPath string, allowedNodeIPs []string) (*Authenticator, error) {
	auth := &Authenticator{
		secret:         secret,
		allowedNodeIPs: make([]*net.IPNet, 0),
	}

	// Parse allowed IP CIDRs
	for _, cidr := range allowedNodeIPs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			// Try as single IP
			ip := net.ParseIP(cidr)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP/CIDR: %s", cidr)
			}
			// Convert to /32 or /128 CIDR
			if ip.To4() != nil {
				_, ipNet, _ = net.ParseCIDR(ip.String() + "/32")
			} else {
				_, ipNet, _ = net.ParseCIDR(ip.String() + "/128")
			}
		}
		auth.allowedNodeIPs = append(auth.allowedNodeIPs, ipNet)
	}

	// Load TLS config if provided
	if tlsCertPath != "" && tlsKeyPath != "" {
		tlsConfig, err := loadTLSConfig(tlsCertPath, tlsKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS config: %w", err)
		}
		auth.tlsConfig = tlsConfig
	}

	return auth, nil
}

// loadTLSConfig loads TLS certificate and key
func loadTLSConfig(certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ValidateSecret validates the auth secret
func (a *Authenticator) ValidateSecret(providedSecret string) bool {
	return providedSecret == a.secret
}

// IsIPAllowed checks if an IP is in the allowed list
func (a *Authenticator) IsIPAllowed(ipStr string) bool {
	if len(a.allowedNodeIPs) == 0 {
		return true // No restrictions
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	for _, ipNet := range a.allowedNodeIPs {
		if ipNet.Contains(ip) {
			return true
		}
	}

	return false
}

// GetClientIP extracts the client IP from a context
func (a *Authenticator) GetClientIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ""
	}

	addr := p.Addr.String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// GetTLSConfig returns the TLS configuration
func (a *Authenticator) GetTLSConfig() *tls.Config {
	return a.tlsConfig
}

// HasTLS returns true if TLS is configured
func (a *Authenticator) HasTLS() bool {
	return a.tlsConfig != nil
}

// GRPCServerOptions returns gRPC server options for authentication
func (a *Authenticator) GRPCServerOptions() []grpc.ServerOption {
	opts := []grpc.ServerOption{}

	if a.HasTLS() {
		opts = append(opts, grpc.Creds(credentials.NewTLS(a.tlsConfig)))
	}

	// Add auth interceptor
	unaryInterceptor := grpc.UnaryInterceptor(a.unaryAuthInterceptor)
	streamInterceptor := grpc.StreamInterceptor(a.streamAuthInterceptor)

	opts = append(opts, unaryInterceptor, streamInterceptor)

	return opts
}

// unaryAuthInterceptor is a gRPC unary interceptor for authentication
func (a *Authenticator) unaryAuthInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	// Skip auth for health checks
	if strings.HasSuffix(info.FullMethod, "Health") {
		return handler(ctx, req)
	}

	// Check IP restriction for node services
	if strings.Contains(info.FullMethod, "NodeService") || strings.Contains(info.FullMethod, "UsageService") {
		clientIP := a.GetClientIP(ctx)
		if !a.IsIPAllowed(clientIP) {
			return nil, status.Errorf(codes.PermissionDenied, "IP %s not allowed", clientIP)
		}
	}

	return handler(ctx, req)
}

// streamAuthInterceptor is a gRPC stream interceptor for authentication
func (a *Authenticator) streamAuthInterceptor(
	srv interface{},
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	// Skip auth for health checks
	if strings.HasSuffix(info.FullMethod, "Health") {
		return handler(srv, ss)
	}

	// Check IP restriction for node services
	if strings.Contains(info.FullMethod, "NodeService") || strings.Contains(info.FullMethod, "UsageService") {
		clientIP := a.GetClientIP(ss.Context())
		if !a.IsIPAllowed(clientIP) {
			return status.Errorf(codes.PermissionDenied, "IP %s not allowed", clientIP)
		}
	}

	return handler(srv, ss)
}

// LoadCACerts loads CA certificates for mTLS
func LoadCACerts(caPath string) (*x509.CertPool, error) {
	caCert, err := ioutil.ReadFile(caPath)
	if err != nil {
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to append CA certificate")
	}

	return caCertPool, nil
}
