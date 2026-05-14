//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/ent"
	entsubscriber "github.com/hiddify/hue/internal/ent/subscriber"
)

// TestFullSpeedtest is the real-binary end-to-end check the phase-2
// plan calls for. It:
//
//  1. Boots an in-process HUE against a testcontainers Postgres.
//  2. Provisions Owner → Reseller → 2 Clients via gRPC.
//  3. Creates a Node (with service_hostnames + an xray config template
//     pinned to the binary's numeric_version) + an Agent under it,
//     capturing the plaintext Agent API key.
//  4. Calls ConfigService.SyncConfig as the Agent and asserts the
//     response carries an etag + the active Clients.
//  5. Starts a real xray-core binary configured with the same JSON
//     template — an HTTP-forward-proxy inbound + a freedom outbound —
//     and HTTP-roundtrips through it to a local httptest.Server.
//  6. Reports usage for one Client as the Agent; asserts the engine
//     accepts the report and Node bandwidth advances.
//
// The xray binary is supplied by `make e2e-deps` under
// test/e2e/testdata/xray/<os>-<arch>/xray. The test is skipped when
// the binary is missing.
func TestFullSpeedtest(t *testing.T) {
	// Ensure the binary is on disk before any HUE setup so the skip
	// path is cheap.
	_ = EnsureXrayAvailable(t)

	env := newHueEnv(t)
	ctx, _ := WithContextTimeout(t, 60*time.Second)

	owner := env.ownerCtx()

	// --- Reseller + login (exercises JWT path) ---
	_, err := env.Resellers.CreateReseller(owner, &huev1.CreateResellerRequest{
		Reseller: &huev1.Reseller{
			Name:     "acme",
			Password: "acmePassword1!",
			Status:   huev1.ResellerStatus_RESELLER_STATUS_ACTIVE,
		},
	})
	require.NoError(t, err, "CreateReseller")

	loginResp, err := env.Auth.Login(ctx, &huev1.LoginRequest{
		Username: "acme",
		Password: "acmePassword1!",
	})
	require.NoError(t, err, "Login as reseller")
	require.NotEmpty(t, loginResp.GetAccessToken(), "access token issued")
	resellerCtx := env.tokenCtx(loginResp.GetAccessToken())

	// --- 2 Clients under the reseller ---
	alice, err := env.ResellerClients.CreateClient(resellerCtx, &huev1.CreateClientRequest{
		Client: &huev1.Client{
			Info:       &huev1.ClientInfo{Groups: []string{"premium"}},
			AuthMethod: &huev1.ClientAuthMethod{Username: "alice", Password: "alicePw!"},
		},
	})
	require.NoError(t, err, "CreateClient alice")

	bob, err := env.ResellerClients.CreateClient(resellerCtx, &huev1.CreateClientRequest{
		Client: &huev1.Client{
			Info:       &huev1.ClientInfo{Groups: []string{"basic"}},
			AuthMethod: &huev1.ClientAuthMethod{Username: "bob", Password: "bobPw!"},
		},
	})
	require.NoError(t, err, "CreateClient bob")

	// CreateClient doesn't auto-mint a UsagePlan (CreateUsagePlan RPC is
	// roadmap). The engine rejects ReportUsage when the Client has no
	// active plan, so attach one directly via ent. Alice gets generous
	// limits (won't trip quota), Bob gets a tiny one for the future
	// exhaustion test.
	attachUsagePlan(ctx, t, env.DB, alice.GetInfo().GetId(), 1<<30) // 1 GiB
	attachUsagePlan(ctx, t, env.DB, bob.GetInfo().GetId(), 4096)    // 4 KiB

	// --- Node + xray config template ---
	xrayPort := FreePort(t)
	configJSON := xrayHTTPProxyConfig(xrayPort)

	cfg := map[string]*structpb.Value{
		"xray.numeric_version.0":  structpb.NewStringValue(configJSON),
		"xray.api_endpoint":       structpb.NewStringValue("127.0.0.1:10085"),
		"xray.inbound_http_port":  structpb.NewNumberValue(float64(xrayPort)),
	}
	node, err := env.Admin.CreateNode(owner, &huev1.CreateNodeRequest{
		Node: &huev1.Node{
			Name:                "e2e-fra-1",
			Ips:                 []string{"127.0.0.1"},
			ServiceHostnames:    []string{"e2e.invalid"},
			BandwidthLimitBytes: 1 << 40, // 1 TiB
			Config:              cfg,
		},
	})
	require.NoError(t, err, "CreateNode")

	// --- Agent under the Node ---
	agentResp, err := env.Admin.CreateAgent(owner, &huev1.CreateAgentRequest{
		Agent: &huev1.Agent{
			NodeId:  node.GetId(),
			Name:    "e2e-fra-1-xray",
			Kind:    huev1.AgentKind_AGENT_KIND_XRAY,
			Version: "25.3.7",
		},
	})
	require.NoError(t, err, "CreateAgent")
	agentID := agentResp.GetAgent().GetId()

	// AuthAdminService mints the Agent's API key separately; the
	// plaintext token is in the response exactly once.
	keyResp, err := env.AuthAdmin.CreateApiKey(owner, &huev1.CreateApiKeyRequest{
		ApiKey: &huev1.ApiKey{
			Kind:    huev1.ApiKeyKind_API_KEY_KIND_AGENT,
			Name:    "e2e-fra-1-xray",
			AgentId: agentID,
		},
	})
	require.NoError(t, err, "CreateApiKey (Agent)")
	require.NotEmpty(t, keyResp.GetToken(), "agent api key returned plaintext")
	agentCtx := env.tokenCtx(keyResp.GetToken())

	// --- SyncConfig as the agent ---
	sync, err := env.Config.SyncConfig(agentCtx, &huev1.SyncConfigRequest{
		NodeId:       node.GetId(),
		AgentKind:    huev1.AgentKind_AGENT_KIND_XRAY,
		AgentVersion: "25.3.7",
	})
	require.NoError(t, err, "SyncConfig")
	assert.NotEmpty(t, sync.GetEtag(), "etag set")
	assert.True(t, sync.GetChanged(), "first sync is a change")
	assert.GreaterOrEqual(t, len(sync.GetClients()), 2, "alice + bob in snapshot")

	// Idempotent second call with the same etag should report unchanged.
	sync2, err := env.Config.SyncConfig(agentCtx, &huev1.SyncConfigRequest{
		NodeId:       node.GetId(),
		AgentKind:    huev1.AgentKind_AGENT_KIND_XRAY,
		AgentVersion: "25.3.7",
		CurrentEtag:  sync.GetEtag(),
	})
	require.NoError(t, err, "SyncConfig (etag match)")
	assert.False(t, sync2.GetChanged(), "matching etag => unchanged")

	// --- Boot real xray-core with the template we stored ---
	runner := StartXray(t, []byte(configJSON), xrayPort)
	_ = runner

	// --- httptest server inside the test ---
	want := "hue-e2e-ok"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, want)
	}))
	t.Cleanup(upstream.Close)

	// --- HTTP forward-proxy round-trip through xray ---
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", xrayPort))
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyURL(proxyURL),
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}

	resp, err := client.Get(upstream.URL)
	require.NoError(t, err, "GET through xray proxy")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "200 via proxy")
	assert.Equal(t, want, string(body), "body round-trips through xray")

	// --- ReportUsage for one Client; assert acceptance + bandwidth advance ---
	report, err := env.Usage.ReportUsage(agentCtx, &huev1.ReportUsageRequest{
		Report: &huev1.UsageReport{
			ClientId:  alice.GetInfo().GetId(),
			NodeId:    node.GetId(),
			AgentId:   agentID,
			Upload:    1024,
			Download:  4096,
			SessionId: "e2e-sess-1",
			ClientIp:  "203.0.113.5",
		},
	})
	require.NoError(t, err, "ReportUsage")
	require.NotNil(t, report.GetDecision())
	assert.True(t, report.GetDecision().GetAccepted(), "accepted")
	assert.False(t, report.GetDecision().GetShouldDisconnect(), "no disconnect")

	// Node counters should reflect the report.
	gotNode, err := env.Admin.GetNode(owner, &huev1.GetNodeRequest{Id: node.GetId()})
	require.NoError(t, err)
	current := gotNode.GetCurrent()
	require.NotNil(t, current)
	assert.GreaterOrEqual(t, current.GetTotalBytes(), int64(1024+4096),
		"node total advanced past at least the report's bytes")

	_ = bob // Reserved for an exhaustion test in a follow-up commit.
}

// attachUsagePlan creates a UsagePlan row for the given client UUID and
// sets it as the Client's active plan. Done via direct ent because no
// CreateUsagePlan RPC exists yet (roadmap).
func attachUsagePlan(ctx context.Context, t *testing.T, db *ent.Client, clientIDStr string, totalLimit int64) {
	t.Helper()
	clientID, err := uuid.Parse(clientIDStr)
	require.NoError(t, err, "parse client id")
	plan, err := db.UsagePlan.Create().
		SetClientID(clientID).
		SetTotalLimit(totalLimit).
		Save(ctx)
	require.NoError(t, err, "create usage plan")
	_, err = db.Subscriber.UpdateOneID(clientID).
		SetActivePlanID(plan.ID).
		Save(ctx)
	require.NoError(t, err, "set active plan")
	// Silence the entsubscriber import — only kept here so a future
	// status-flip assertion has the constant available.
	_ = entsubscriber.StatusActive
}

// xrayHTTPProxyConfig returns a minimal xray-core configuration that
// listens for HTTP forward-proxy traffic on 127.0.0.1:<port> and
// forwards via a freedom outbound. The shape is verbatim what the
// E2E test pins under `xray.numeric_version.0` on the Node.
func xrayHTTPProxyConfig(port int) string {
	type inbound struct {
		Tag      string `json:"tag"`
		Listen   string `json:"listen"`
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
		Settings struct {
			AllowTransparent bool `json:"allowTransparent"`
		} `json:"settings"`
	}
	type outbound struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
	}
	type cfg struct {
		Log struct {
			Loglevel string `json:"loglevel"`
		} `json:"log"`
		Inbounds  []inbound  `json:"inbounds"`
		Outbounds []outbound `json:"outbounds"`
	}
	var c cfg
	c.Log.Loglevel = "warning"
	in := inbound{Tag: "http-in", Listen: "127.0.0.1", Port: port, Protocol: "http"}
	in.Settings.AllowTransparent = false
	c.Inbounds = []inbound{in}
	c.Outbounds = []outbound{{Tag: "direct", Protocol: "freedom"}}
	b, _ := json.MarshalIndent(c, "", "  ")
	return string(b)
}
