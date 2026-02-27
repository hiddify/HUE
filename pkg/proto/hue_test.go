package proto

import (
	"testing"
)

func TestProtoUsageReportFieldsAndGetters(t *testing.T) {
	report := &UsageReport{
		Id:        "rep-1",
		UserId:    "user-1",
		NodeId:    "node-1",
		ServiceId: "service-1",
		Upload:    123,
		Download:  456,
		SessionId: "sess-1",
		ClientIp:  "1.2.3.4",
		Tags:      []string{"vless", "edge"},
		Timestamp: 1700000000,
	}

	if report.GetId() != "rep-1" || report.GetUserId() != "user-1" || report.GetUpload() != 123 || report.GetDownload() != 456 {
		t.Fatalf("unexpected usage report getter values")
	}
	if len(report.GetTags()) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(report.GetTags()))
	}
}

func TestProtoServiceDescriptors(t *testing.T) {
	if UsageService_ServiceDesc.ServiceName != "hue.UsageService" {
		t.Fatalf("unexpected usage service name: %s", UsageService_ServiceDesc.ServiceName)
	}
	if AdminService_ServiceDesc.ServiceName != "hue.AdminService" {
		t.Fatalf("unexpected admin service name: %s", AdminService_ServiceDesc.ServiceName)
	}
	if NodeService_ServiceDesc.ServiceName != "hue.NodeService" {
		t.Fatalf("unexpected node service name: %s", NodeService_ServiceDesc.ServiceName)
	}
	if len(UsageService_ServiceDesc.Methods) == 0 || len(AdminService_ServiceDesc.Methods) == 0 || len(NodeService_ServiceDesc.Methods) == 0 {
		t.Fatalf("expected generated gRPC methods in service descriptors")
	}
}

func TestProtoBasicMessages(t *testing.T) {
	user := &User{Id: "u1", Username: "alice", Status: "active"}
	if user.GetUsername() != "alice" || user.GetStatus() != "active" {
		t.Fatalf("unexpected user getters output")
	}

	errResp := &ErrorResponse{Code: "E1", Message: "bad request"}
	if errResp.GetCode() != "E1" || errResp.GetMessage() != "bad request" {
		t.Fatalf("unexpected error response getters output")
	}
}
