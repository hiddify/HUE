# HUE-GO Testing Walkthrough

This guide walks you through testing the Hiddify Usage Engine (HUE) in Go.

---

## Prerequisites

1. **Built binary**: Run `make build` or `go build -o bin/hue.exe ./cmd/hue`
2. **Environment variables**: Set `HUE_AUTH_SECRET` (required)
3. **Optional**: MaxMind GeoLite2 database for geo-location features

---

## Quick Start

### 1. Set Environment Variables

```powershell
# Windows PowerShell
$env:HUE_AUTH_SECRET = "test-secret-key-12345"
$env:HUE_PORT = "50051"
$env:HUE_HTTP_PORT = "50052"
$env:HUE_LOG_LEVEL = "debug"
```

```bash
# Linux/macOS
export HUE_AUTH_SECRET="test-secret-key-12345"
export HUE_PORT="50051"
export HUE_HTTP_PORT="50052"
export HUE_LOG_LEVEL="debug"
```

### 2. Start the Server

```powershell
.\bin\hue.exe
```

You should see:
```
{"level":"info","msg":"starting HUE server","port":"50051","http_port":"50052"}
{"level":"info","msg":"gRPC server started","port":"50051"}
{"level":"info","msg":"HTTP server started","port":"50052"}
```

---

## Testing HTTP REST API

All HTTP endpoints require the `secret` query parameter.

### Health Check

```powershell
# Health check (no auth required)
Invoke-WebRequest -Uri "http://localhost:50052/health" -Method GET
```

```bash
curl http://localhost:50052/health
```

**Expected Response:**
```json
{"status": "ok"}
```

---

### User Management

#### Create a User

```powershell
$headers = @{
    "Content-Type" = "application/json"
}
$body = @{
    username = "testuser"
    password = "testpass123"
    public_key = "ssh-rsa AAAA..."
    groups = @("premium", "vpn")
    allowed_devices = @("device1", "device2")
    active_package_id = ""
} | ConvertTo-Json

Invoke-WebRequest -Uri "http://localhost:50052/api/v1/users?secret=test-secret-key-12345" -Method POST -Headers $headers -Body $body
```

```bash
curl -X POST "http://localhost:50052/api/v1/users?secret=test-secret-key-12345" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "testuser",
    "password": "testpass123",
    "public_key": "ssh-rsa AAAA...",
    "groups": ["premium", "vpn"],
    "allowed_devices": ["device1", "device2"]
  }'
```

**Expected Response:**
```json
{
  "id": "uuid-here",
  "username": "testuser",
  "public_key": "ssh-rsa AAAA...",
  "groups": ["premium", "vpn"],
  "status": "active",
  "created_at": 1700000000
}
```

#### List Users

```powershell
Invoke-WebRequest -Uri "http://localhost:50052/api/v1/users?secret=test-secret-key-12345" -Method GET
```

```bash
curl "http://localhost:50052/api/v1/users?secret=test-secret-key-12345"
```

#### Get a Specific User

```powershell
$userId = "uuid-from-create-response"
Invoke-WebRequest -Uri "http://localhost:50052/api/v1/users/$userId?secret=test-secret-key-12345" -Method GET
```

```bash
curl "http://localhost:50052/api/v1/users/{user-id}?secret=test-secret-key-12345"
```

#### Update a User

```powershell
$body = @{
    username = "testuser_updated"
    status = "active"
    groups = @("premium", "vpn", "admin")
} | ConvertTo-Json

Invoke-WebRequest -Uri "http://localhost:50052/api/v1/users/$userId?secret=test-secret-key-12345" -Method PUT -Headers $headers -Body $body
```

```bash
curl -X PUT "http://localhost:50052/api/v1/users/{user-id}?secret=test-secret-key-12345" \
  -H "Content-Type: application/json" \
  -d '{"username": "testuser_updated", "status": "active", "groups": ["premium", "vpn", "admin"]}'
```

#### Delete a User

```powershell
Invoke-WebRequest -Uri "http://localhost:50052/api/v1/users/$userId?secret=test-secret-key-12345" -Method DELETE
```

```bash
curl -X DELETE "http://localhost:50052/api/v1/users/{user-id}?secret=test-secret-key-12345"
```

---

### Package Management

#### Create a Package for a User

```powershell
$body = @{
    user_id = $userId
    total_traffic = 107374182400  # 100GB in bytes
    upload_limit = 53687091200    # 50GB
    download_limit = 53687091200  # 50GB
    reset_mode = "monthly"
    duration = 2592000           # 30 days in seconds
    max_concurrent = 3
} | ConvertTo-Json

Invoke-WebRequest -Uri "http://localhost:50052/api/v1/packages?secret=test-secret-key-12345" -Method POST -Headers $headers -Body $body
```

```bash
curl -X POST "http://localhost:50052/api/v1/packages?secret=test-secret-key-12345" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "{user-id}",
    "total_traffic": 107374182400,
    "upload_limit": 53687091200,
    "download_limit": 53687091200,
    "reset_mode": "monthly",
    "duration": 2592000,
    "max_concurrent": 3
  }'
```

---

### Node Management

#### Create a Node (Server)

```powershell
$body = @{
    name = "node-us-east-1"
    secret_key = "node-secret-key-abc"
    allowed_ips = @("192.168.1.0/24", "10.0.0.0/8")
    traffic_multiplier = 1.0
    reset_mode = "monthly"
    reset_day = 1
    country = "US"
    city = "New York"
    isp = "AWS"
} | ConvertTo-Json

Invoke-WebRequest -Uri "http://localhost:50052/api/v1/nodes?secret=test-secret-key-12345" -Method POST -Headers $headers -Body $body
```

```bash
curl -X POST "http://localhost:50052/api/v1/nodes?secret=test-secret-key-12345" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "node-us-east-1",
    "secret_key": "node-secret-key-abc",
    "allowed_ips": ["192.168.1.0/24", "10.0.0.0/8"],
    "traffic_multiplier": 1.0,
    "reset_mode": "monthly",
    "reset_day": 1,
    "country": "US",
    "city": "New York",
    "isp": "AWS"
  }'
```

#### List All Nodes

```powershell
Invoke-WebRequest -Uri "http://localhost:50052/api/v1/nodes?secret=test-secret-key-12345" -Method GET
```

```bash
curl "http://localhost:50052/api/v1/nodes?secret=test-secret-key-12345"
```

---

### Service Management

#### Create a Service on a Node

```powershell
$body = @{
    node_id = $nodeId
    secret_key = "service-secret-key-xyz"
    name = "vless-service"
    protocol = "vless"
    allowed_auth_methods = @("uuid", "password")
    callback_url = "https://example.com/callback"
} | ConvertTo-Json

Invoke-WebRequest -Uri "http://localhost:50052/api/v1/services?secret=test-secret-key-12345" -Method POST -Headers $headers -Body $body
```

```bash
curl -X POST "http://localhost:50052/api/v1/services?secret=test-secret-key-12345" \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "{node-id}",
    "secret_key": "service-secret-key-xyz",
    "name": "vless-service",
    "protocol": "vless",
    "allowed_auth_methods": ["uuid", "password"],
    "callback_url": "https://example.com/callback"
  }'
```

---

### Statistics

```powershell
Invoke-WebRequest -Uri "http://localhost:50052/api/v1/stats?secret=test-secret-key-12345" -Method GET
```

```bash
curl "http://localhost:50052/api/v1/stats?secret=test-secret-key-12345"
```

**Expected Response:**
```json
{
  "total_users": 10,
  "active_users": 5,
  "total_nodes": 3,
  "total_services": 5,
  "total_upload": 1073741824,
  "total_download": 2147483648
}
```

---

## Testing gRPC API

### Using grpcurl

Install grpcurl: https://github.com/fullstorydev/grpcurl

```powershell
# List available services
grpcurl -plaintext localhost:50051 list

# Describe a service
grpcurl -plaintext localhost:50051 describe hue.UsageService

# Report usage
grpcurl -plaintext -d '{
  "report": {
    "user_id": "{user-id}",
    "node_id": "{node-id}",
    "service_id": "{service-id}",
    "upload": 1048576,
    "download": 2097152,
    "session_id": "session-123",
    "client_ip": "192.168.1.100",
    "timestamp": 1700000000
  }
}' localhost:50051 hue.UsageService/ReportUsage
```

```bash
# List available services
grpcurl -plaintext localhost:50051 list

# Report usage
grpcurl -plaintext -d '{
  "report": {
    "user_id": "{user-id}",
    "node_id": "{node-id}",
    "service_id": "{service-id}",
    "upload": 1048576,
    "download": 2097152,
    "session_id": "session-123",
    "client_ip": "192.168.1.100",
    "timestamp": 1700000000
  }
}' localhost:50051 hue.UsageService/ReportUsage
```

### Using a Go Client

Create a test client:

```go
package main

import (
    "context"
    "log"
    "time"

    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    pb "github.com/hiddify/hue-go/pkg/proto"
)

func main() {
    conn, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()

    client := pb.NewUsageServiceClient(conn)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    resp, err := client.ReportUsage(ctx, &pb.ReportUsageRequest{
        Report: &pb.UsageReport{
            UserId:    "user-uuid",
            NodeId:    "node-uuid",
            ServiceId: "service-uuid",
            Upload:    1048576,
            Download:  2097152,
            SessionId: "session-123",
            Timestamp: time.Now().Unix(),
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    log.Printf("Result: accepted=%v, quota_exceeded=%v", resp.Result.Accepted, resp.Result.QuotaExceeded)
}
```

---

## Testing with Docker

### Build Docker Image

```powershell
docker build -t hue -f deployments/docker/Dockerfile .
```

```bash
docker build -t hue -f deployments/docker/Dockerfile .
```

### Run with Docker

```powershell
docker run -d `
  -p 50051:50051 `
  -p 50052:50052 `
  -e HUE_AUTH_SECRET=test-secret-key-12345 `
  -e HUE_LOG_LEVEL=debug `
  -v hue-data:/data `
  --name hue-test `
  hue
```

```bash
docker run -d \
  -p 50051:50051 \
  -p 50052:50052 \
  -e HUE_AUTH_SECRET=test-secret-key-12345 \
  -e HUE_LOG_LEVEL=debug \
  -v hue-data:/data \
  --name hue-test \
  hue
```

### Check Logs

```powershell
docker logs hue-test -f
```

```bash
docker logs -f hue-test
```

### Using Docker Compose

```powershell
cd deployments/docker
docker-compose up -d
docker-compose logs -f
```

```bash
cd deployments/docker
docker-compose up -d
docker-compose logs -f
```

---

## Unit Tests

### Run All Tests

```powershell
go test ./... -v
```

```bash
go test ./... -v
```

### Run Specific Package Tests

```powershell
go test ./internal/engine -v
go test ./internal/storage/sqlite -v
```

```bash
go test ./internal/engine -v
go test ./internal/storage/sqlite -v
```

### Run with Coverage

```powershell
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

---

## Integration Test Script

Create a PowerShell script `test-integration.ps1`:

```powershell
# Integration test script for HUE-GO

$SECRET = "test-secret-key-12345"
$BASE_URL = "http://localhost:50052"

Write-Host "=== HUE-GO Integration Test ===" -ForegroundColor Green

# Test 1: Health Check
Write-Host "`n[Test 1] Health Check..." -ForegroundColor Yellow
$response = Invoke-WebRequest -Uri "$BASE_URL/health" -Method GET
Write-Host "Response: $($response.Content)" -ForegroundColor Cyan

# Test 2: Create User
Write-Host "`n[Test 2] Create User..." -ForegroundColor Yellow
$body = @{
    username = "testuser_$(Get-Random)"
    password = "testpass123"
    groups = @("test")
} | ConvertTo-Json

$response = Invoke-WebRequest -Uri "$BASE_URL/api/v1/users?secret=$SECRET" -Method POST -Headers @{"Content-Type"="application/json"} -Body $body
$user = $response.Content | ConvertFrom-Json
$userId = $user.id
Write-Host "Created user: $userId" -ForegroundColor Cyan

# Test 3: Get User
Write-Host "`n[Test 3] Get User..." -ForegroundColor Yellow
$response = Invoke-WebRequest -Uri "$BASE_URL/api/v1/users/$userId`?secret=$SECRET" -Method GET
Write-Host "Response: $($response.Content)" -ForegroundColor Cyan

# Test 4: Create Package
Write-Host "`n[Test 4] Create Package..." -ForegroundColor Yellow
$body = @{
    user_id = $userId
    total_traffic = 107374182400
    reset_mode = "monthly"
    duration = 2592000
    max_concurrent = 3
} | ConvertTo-Json

$response = Invoke-WebRequest -Uri "$BASE_URL/api/v1/packages?secret=$SECRET" -Method POST -Headers @{"Content-Type"="application/json"} -Body $body
$pkg = $response.Content | ConvertFrom-Json
Write-Host "Created package: $($pkg.id)" -ForegroundColor Cyan

# Test 5: Create Node
Write-Host "`n[Test 5] Create Node..." -ForegroundColor Yellow
$body = @{
    name = "test-node-$(Get-Random)"
    secret_key = "node-secret-$(Get-Random)"
    traffic_multiplier = 1.0
} | ConvertTo-Json

$response = Invoke-WebRequest -Uri "$BASE_URL/api/v1/nodes?secret=$SECRET" -Method POST -Headers @{"Content-Type"="application/json"} -Body $body
$node = $response.Content | ConvertFrom-Json
$nodeId = $node.id
Write-Host "Created node: $nodeId" -ForegroundColor Cyan

# Test 6: List Nodes
Write-Host "`n[Test 6] List Nodes..." -ForegroundColor Yellow
$response = Invoke-WebRequest -Uri "$BASE_URL/api/v1/nodes?secret=$SECRET" -Method GET
Write-Host "Response: $($response.Content)" -ForegroundColor Cyan

# Test 7: Get Stats
Write-Host "`n[Test 7] Get Stats..." -ForegroundColor Yellow
$response = Invoke-WebRequest -Uri "$BASE_URL/api/v1/stats?secret=$SECRET" -Method GET
Write-Host "Response: $($response.Content)" -ForegroundColor Cyan

# Test 8: Delete User
Write-Host "`n[Test 8] Delete User..." -ForegroundColor Yellow
$response = Invoke-WebRequest -Uri "$BASE_URL/api/v1/users/$userId`?secret=$SECRET" -Method DELETE
Write-Host "User deleted" -ForegroundColor Cyan

Write-Host "`n=== All Tests Passed ===" -ForegroundColor Green
```

---

## Expected Behavior

### Quota Enforcement

1. Create a user with a package that has `total_traffic = 10485760` (10MB)
2. Report usage that exceeds the limit
3. The response should have `quota_exceeded = true` and `should_disconnect = true`

### Concurrent Session Limits

1. Create a user with a package that has `max_concurrent = 2`
2. Start 3 sessions with different session IDs
3. The 3rd session should be rejected with `session_limit_hit = true`

### Penalty System

1. User exceeds concurrent session limit
2. Penalty is applied for configured duration
3. User cannot connect until penalty expires or is cleared

---

## Troubleshooting

### "Unauthorized" Response

- Ensure `secret` query parameter matches `HUE_AUTH_SECRET`
- Check that the secret is URL-encoded if it contains special characters

### "User not found" Response

- Verify the user ID is correct
- Check if the user was deleted

### Database Errors

- Check file permissions for SQLite database files
- Ensure the data directory is writable

### Port Already in Use

```powershell
# Find process using port
netstat -ano | findstr :50051
netstat -ano | findstr :50052

# Kill process (replace PID)
taskkill /PID <pid> /F
```

```bash
# Find process using port
lsof -i :50051
lsof -i :50052

# Kill process
kill -9 <pid>
```

---

## Environment Variables Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `HUE_DB_URL` | `sqlite://./hue.db` | Database connection string |
| `HUE_PORT` | `50051` | gRPC server port |
| `HUE_HTTP_PORT` | `50052` | HTTP REST API port |
| `HUE_AUTH_SECRET` | *required* | Authentication secret key |
| `HUE_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |
| `HUE_DB_FLUSH_INTERVAL` | `5m` | Buffered write flush interval |
| `HUE_CONCURRENT_WINDOW` | `5m` | Session counting window |
| `HUE_PENALTY_DURATION` | `10m` | Penalty duration for violations |
| `HUE_MAXMIND_DB_PATH` | `""` | Path to MaxMind GeoLite2 database |
| `HUE_EVENT_STORE_TYPE` | `db` | Event storage: db, file, none |
