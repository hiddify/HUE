package server

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hiddify/hue/internal/auth"
)

// kindSet is a compact lookup for allowed PrincipalKinds per RPC.
type kindSet map[auth.PrincipalKind]struct{}

func allow(kinds ...auth.PrincipalKind) kindSet {
	m := make(kindSet, len(kinds))
	for _, k := range kinds {
		m[k] = struct{}{}
	}
	return m
}

// methodAllowList maps gRPC FullMethod → allowed PrincipalKinds.
//
// Methods missing from this map fall back to "any authenticated kind
// passes" (current auth interceptor already gates anonymous). The
// list is exhaustive for OWNER- and AGENT-only routes today; phase
// 2.3 adds Reseller/Subscriber routes as JWT wiring lands.
var methodAllowList = map[string]kindSet{
	// AdminService — Owner only.
	"/hue.v1.AdminService/CreateNode":           allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/GetNode":              allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/ListNodes":            allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/UpdateNode":           allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/DeleteNode":           allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/ResetNodeUsage":       allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/CreateAgent":          allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/GetAgent":             allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/ListAgents":           allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/UpdateAgent":          allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/DeleteAgent":          allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/ListEvents":           allow(auth.PrincipalKindOwner),
	"/hue.v1.AdminService/StreamEvents":         allow(auth.PrincipalKindOwner),

	// AuthAdminService — Owner only.
	"/hue.v1.AuthAdminService/CreateApiKey": allow(auth.PrincipalKindOwner),
	"/hue.v1.AuthAdminService/ListApiKeys":  allow(auth.PrincipalKindOwner),
	"/hue.v1.AuthAdminService/RevokeApiKey": allow(auth.PrincipalKindOwner),

	// ResellerClientService — Reseller or Owner.
	"/hue.v1.ResellerClientService/CreateClient":          allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/GetClient":             allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/ListClients":           allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/UpdateClient":          allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/DeleteClient":          allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/ResetClientUsage":      allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/GetUsagePlan":          allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/ListUsagePlans":        allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/GetActiveUsagePlan":    allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerClientService/GetAvailableNodesInfo": allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),

	// ResellerManagementService — Reseller or Owner.
	"/hue.v1.ResellerManagementService/CreateReseller":     allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerManagementService/GetReseller":        allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerManagementService/ListResellers":      allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerManagementService/UpdateReseller":     allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerManagementService/DeleteReseller":     allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.ResellerManagementService/ResetResellerUsage": allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),

	// ConfigService — Agent only.
	"/hue.v1.ConfigService/SyncConfig": allow(auth.PrincipalKindAgent),
	"/hue.v1.ConfigService/Heartbeat":  allow(auth.PrincipalKindAgent),

	// UsageService — Agent only.
	"/hue.v1.UsageService/ReportUsage":      allow(auth.PrincipalKindAgent),
	"/hue.v1.UsageService/BatchReportUsage": allow(auth.PrincipalKindAgent),
	"/hue.v1.UsageService/SyncClients":      allow(auth.PrincipalKindAgent),

	// DomainCertificateService — writes Owner, reads Reseller+Owner.
	"/hue.v1.DomainCertificateService/AddCertificate":    allow(auth.PrincipalKindOwner),
	"/hue.v1.DomainCertificateService/DeleteCertificate": allow(auth.PrincipalKindOwner),
	"/hue.v1.DomainCertificateService/RequestACME":       allow(auth.PrincipalKindOwner),
	"/hue.v1.DomainCertificateService/GetCertificate":    allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
	"/hue.v1.DomainCertificateService/ListCertificates":  allow(auth.PrincipalKindReseller, auth.PrincipalKindOwner),
}

// AuthorizeUnary enforces methodAllowList. Runs after the auth
// interceptor (which attaches the Actor) and before validation. Methods
// missing from the table pass through — the auth interceptor's own
// AllowMethods set covers anonymous routes.
func AuthorizeUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		allowed, tracked := methodAllowList[info.FullMethod]
		if !tracked {
			return handler(ctx, req)
		}
		actor, ok := auth.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing principal")
		}
		if _, permitted := allowed[actor.Kind]; !permitted {
			return nil, status.Errorf(codes.PermissionDenied,
				"%s requires one of the allowed principal kinds", info.FullMethod)
		}
		return handler(ctx, req)
	}
}

// AuthorizeStream is the streaming counterpart.
func AuthorizeStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		allowed, tracked := methodAllowList[info.FullMethod]
		if !tracked {
			return handler(srv, ss)
		}
		actor, ok := auth.FromContext(ss.Context())
		if !ok {
			return status.Error(codes.Unauthenticated, "missing principal")
		}
		if _, permitted := allowed[actor.Kind]; !permitted {
			return status.Errorf(codes.PermissionDenied,
				"%s requires one of the allowed principal kinds", info.FullMethod)
		}
		return handler(srv, ss)
	}
}
