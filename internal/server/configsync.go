package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/ent"
	entagent "github.com/hiddify/hue/internal/ent/agent"
	entsubscriber "github.com/hiddify/hue/internal/ent/subscriber"
	"github.com/hiddify/hue/pkg/version"
)

// ConfigServer — Agent-only. SyncConfig + Heartbeat per Node.
//
// Identification: the calling agent's API key resolves to Actor with
// Kind=Agent and SubjectID=Agent.id. The agent passes its own node_id
// in the request; SyncConfig verifies it matches the Agent's stored
// node_id. Mismatch = PermissionDenied.
type ConfigServer struct {
	huev1.UnimplementedConfigServiceServer

	db     *ent.Client
	logger *slog.Logger
}

func (s *ConfigServer) SyncConfig(ctx context.Context, req *huev1.SyncConfigRequest) (*huev1.SyncConfigResponse, error) {
	actor, ok := auth.FromContext(ctx)
	if !ok || actor.Kind != auth.PrincipalKindAgent {
		return nil, status.Error(codes.PermissionDenied, "agent principal required")
	}
	agentRow, err := s.db.Agent.Get(ctx, actor.SubjectID)
	if err != nil {
		return nil, mapEntError(err)
	}
	reqNodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "node_id: %v", err)
	}
	if agentRow.NodeID != reqNodeID {
		return nil, status.Error(codes.PermissionDenied, "node_id does not match calling agent")
	}

	node, err := s.db.Node.Get(ctx, reqNodeID)
	if err != nil {
		return nil, mapEntError(err)
	}

	// Pick the right config key for this agent's kind + version.
	kindPrefix := agentKindPrefix(req.GetAgentKind())
	if kindPrefix == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_kind is required")
	}
	encodedV, err := version.Encode(req.GetAgentVersion())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "agent_version: %v", err)
	}

	keys := mapKeys(node.Config)
	best := version.PickHighestKey(keys, kindPrefix, encodedV)

	// Build response config map: drop everything not relevant to this
	// agent kind. Include only the picked numeric_version value
	// (renamed to a plain "template" key) plus other top-level keys
	// under the kind prefix (e.g. "xray.api_endpoint").
	cfg := pickConfigForKind(node.Config, kindPrefix, best)

	resp := &huev1.SyncConfigResponse{
		Config:    cfg,
		UpdatedAt: timestamppb.New(node.UpdatedAt),
	}

	// Active clients on this server (currently all active across HUE;
	// scoping by reseller hierarchy lands in 2.7's E2E suite).
	subs, err := s.db.Subscriber.Query().
		Where(entsubscriber.StatusEQ(entsubscriber.StatusActive)).
		Limit(10000).
		All(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	resp.Clients = make([]*huev1.PrincipalSnapshot, 0, len(subs))
	for _, sub := range subs {
		plain, err := auth.Decrypt(sub.PasswordCiphertext, sub.PasswordKeyID)
		if err != nil {
			// Skip clients whose password can't be decrypted (rotated
			// key, corrupt row); operator-visible via metrics.
			continue
		}
		resp.Clients = append(resp.Clients, &huev1.PrincipalSnapshot{
			ClientId:  sub.ID.String(),
			Username:  sub.Username,
			Password:  string(plain),
			PublicKey: sub.PublicKey,
			Groups:    sub.Groups,
			Status:    clientStatusFromEnt(sub.Status),
		})
	}

	// Certs are populated by phase 2.5 once DomainCertificateService
	// is implemented. Empty slice today.

	resp.Etag = computeEtag(node.UpdatedAt, len(resp.Clients), len(resp.Certs), best)
	resp.Changed = req.GetCurrentEtag() != resp.Etag
	if !resp.Changed {
		// Match the SyncConfig contract: changed=false means caller is
		// up-to-date; trim the response to the bare minimum.
		return &huev1.SyncConfigResponse{Etag: resp.Etag, UpdatedAt: resp.UpdatedAt}, nil
	}
	return resp, nil
}

func (s *ConfigServer) Heartbeat(ctx context.Context, req *huev1.HeartbeatRequest) (*huev1.HeartbeatResponse, error) {
	actor, ok := auth.FromContext(ctx)
	if !ok || actor.Kind != auth.PrincipalKindAgent {
		return nil, status.Error(codes.PermissionDenied, "agent principal required")
	}
	_, err := s.db.Agent.UpdateOneID(actor.SubjectID).
		SetLastSeenAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	// disconnect_client_ids = clients in QUOTA_USED / SUSPENDED status
	// the agent should drop. Computed in phase 2.7's engine integration;
	// today returns empty list (the per-ReportUsage decision already
	// returns ShouldDisconnect).
	return &huev1.HeartbeatResponse{}, nil
}

// agentKindPrefix maps an AgentKind to its config-key prefix.
// Matches the proto enum to the dotted-key convention used in
// Node.config.
func agentKindPrefix(k huev1.AgentKind) string {
	switch k {
	case huev1.AgentKind_AGENT_KIND_XRAY:
		return "xray.numeric_version."
	case huev1.AgentKind_AGENT_KIND_SINGBOX:
		return "singbox.numeric_version."
	case huev1.AgentKind_AGENT_KIND_WIREGUARD:
		return "wireguard.numeric_version."
	case huev1.AgentKind_AGENT_KIND_OPENVPN:
		return "openvpn.numeric_version."
	case huev1.AgentKind_AGENT_KIND_IPSEC:
		return "ipsec.numeric_version."
	case huev1.AgentKind_AGENT_KIND_RADIUS:
		return "radius.numeric_version."
	case huev1.AgentKind_AGENT_KIND_SSH:
		return "ssh.numeric_version."
	}
	return ""
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pickConfigForKind returns the SyncConfig response config map:
//   * the chosen versioned template under the key "template"
//   * any non-versioned siblings under the same kind (e.g.
//     "xray.api_endpoint", "xray.log_level") forwarded verbatim
func pickConfigForKind(src map[string]any, kindPrefix, bestKey string) map[string]*structpb.Value {
	out := map[string]*structpb.Value{}
	if bestKey != "" {
		if v, ok := src[bestKey]; ok {
			if pv, err := structpb.NewValue(v); err == nil {
				out["template"] = pv
			}
		}
	}
	// Forward siblings: same root prefix (e.g. "xray.") but not under
	// "<root>.numeric_version.*". Agent-specific knobs live there.
	root := kindPrefix
	if idx := indexOfDot(root); idx >= 0 {
		root = root[:idx+1]
	}
	for k, v := range src {
		if !hasPrefix(k, root) || hasPrefix(k, kindPrefix) {
			continue
		}
		if pv, err := structpb.NewValue(v); err == nil {
			out[k] = pv
		}
	}
	return out
}

func indexOfDot(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func computeEtag(updatedAt time.Time, nClients, nCerts int, bestKey string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%d|%s", updatedAt.UTC().Format(time.RFC3339Nano), nClients, nCerts, bestKey)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Touch entagent so its import isn't dropped if future refactors remove
// the only direct use. Cheap and explicit.
var _ = entagent.ID
