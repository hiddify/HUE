package server

import (
	"log/slog"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/ent"
)

// NodeServer implements hue.v1.NodeService.
//
// AuthenticateNode and Heartbeat are intentionally left as Unimplemented
// for this iteration — the node-bootstrap flow (one-time provisioning
// token → long-lived session token) is its own design decision and gets
// implemented in a follow-up. The embed below provides the default
// "Unimplemented" responses, which is correct behavior until the design
// is settled.
type NodeServer struct {
	huev1.UnimplementedNodeServiceServer

	db     *ent.Client
	logger *slog.Logger
}
