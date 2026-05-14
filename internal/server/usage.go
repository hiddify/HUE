package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/service"
)

// UsageServer implements hue.v1.UsageService — Agent-only data plane.
type UsageServer struct {
	huev1.UnimplementedUsageServiceServer

	engine *service.Engine
	logger *slog.Logger
}

func (s *UsageServer) ReportUsage(ctx context.Context, req *huev1.ReportUsageRequest) (*huev1.ReportUsageResponse, error) {
	in := req.GetReport()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "report is required")
	}
	clientID, err := uuid.Parse(in.GetClientId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "client_id: %v", err)
	}
	var nodeID uuid.UUID
	if v := in.GetNodeId(); v != "" {
		if nodeID, err = uuid.Parse(v); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "node_id: %v", err)
		}
	}
	var agentID uuid.UUID
	if v := in.GetAgentId(); v != "" {
		if agentID, err = uuid.Parse(v); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "agent_id: %v", err)
		}
	}
	at := time.Now().UTC()
	if ts := in.GetTimestamp(); ts != nil && ts.IsValid() {
		at = ts.AsTime()
	}

	d, err := s.engine.ReportUsage(ctx, service.ReportInput{
		ClientID:  clientID,
		NodeID:    nodeID,
		AgentID:   agentID,
		Upload:    in.GetUpload(),
		Download:  in.GetDownload(),
		SessionID: in.GetSessionId(),
		ClientIP:  in.GetClientIp(),
		Tags:      in.GetTags(),
		At:        at,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "engine: %v", err)
	}
	return &huev1.ReportUsageResponse{Decision: decisionToProto(d)}, nil
}

func (s *UsageServer) BatchReportUsage(ctx context.Context, req *huev1.BatchReportUsageRequest) (*huev1.BatchReportUsageResponse, error) {
	reports := req.GetReports()
	out := &huev1.BatchReportUsageResponse{Decisions: make([]*huev1.UsageDecision, 0, len(reports))}
	for _, r := range reports {
		resp, err := s.ReportUsage(ctx, &huev1.ReportUsageRequest{Report: r})
		if err != nil {
			out.Decisions = append(out.Decisions, &huev1.UsageDecision{
				Accepted: false,
				Reason:   err.Error(),
			})
			out.Rejected++
			continue
		}
		dec := resp.GetDecision()
		out.Decisions = append(out.Decisions, dec)
		if dec.GetAccepted() {
			out.Accepted++
		} else {
			out.Rejected++
		}
	}
	return out, nil
}

func decisionToProto(d service.Decision) *huev1.UsageDecision {
	out := &huev1.UsageDecision{
		Accepted:         d.Accepted,
		QuotaExceeded:    d.QuotaExceeded,
		SessionLimitHit:  d.SessionLimitHit,
		ShouldDisconnect: d.ShouldDisconnect,
		Reason:           d.Reason,
	}
	if !d.PenaltyUntil.IsZero() {
		out.PenaltyUntil = timestamppb.New(d.PenaltyUntil)
	}
	return out
}
