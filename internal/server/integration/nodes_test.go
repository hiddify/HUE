//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestNode_CreateWithConfig_RoundTrips(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := ownerCtx(env)

	cfg := map[string]*structpb.Value{
		"xray.numeric_version.25003007000": structpb.NewStringValue(`{"inbounds":[{"port":443}]}`),
		"xray.api_endpoint":                structpb.NewStringValue("127.0.0.1:10085"),
	}
	created, err := env.Admin.CreateNode(ctx, &huev1.CreateNodeRequest{
		Node: &huev1.Node{
			Name:                "fra-1",
			Ips:                 []string{"203.0.113.10"},
			BandwidthLimitBytes: 2 * 1024 * 1024 * 1024 * 1024, // 2 TiB
			Config:              cfg,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "fra-1", created.GetName())
	assert.EqualValues(t, 2199023255552, created.GetBandwidthLimitBytes())

	got, err := env.Admin.GetNode(ctx, &huev1.GetNodeRequest{Id: created.GetId()})
	require.NoError(t, err)
	assert.Contains(t, got.GetConfig(), "xray.numeric_version.25003007000")
	assert.Contains(t, got.GetConfig(), "xray.api_endpoint")
}

func TestAgent_CreateUnderNode(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ctx := ownerCtx(env)

	n, err := env.Admin.CreateNode(ctx, &huev1.CreateNodeRequest{
		Node: &huev1.Node{Name: "fra-1"},
	})
	require.NoError(t, err)

	resp, err := env.Admin.CreateAgent(ctx, &huev1.CreateAgentRequest{
		Agent: &huev1.Agent{
			NodeId:  n.GetId(),
			Name:    "fra-1-xray",
			Kind:    huev1.AgentKind_AGENT_KIND_XRAY,
			Version: "25.3.7",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetAgent().GetId())
	assert.Equal(t, huev1.AgentKind_AGENT_KIND_XRAY, resp.GetAgent().GetKind())
}
