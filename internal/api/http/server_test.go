package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/engine"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	"go.uber.org/zap"
)

type httpFixture struct {
	router *gin.Engine
	userDB *sqlite.UserDB
	secret string
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "http-api.db")
	userDB, err := sqlite.NewUserDB("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("new user db: %v", err)
	}
	t.Cleanup(func() { _ = userDB.Close() })

	if err := userDB.Migrate(); err != nil {
		t.Fatalf("migrate user db: %v", err)
	}

	cache := cache.NewMemoryCache()
	quota := engine.NewQuotaEngine(userDB, nil, cache, zap.NewNop())
	secret := "test-secret"
	router := NewServer(userDB, nil, quota, zap.NewNop(), secret)

	return &httpFixture{router: router, userDB: userDB, secret: secret}
}

func (f *httpFixture) doJSON(t *testing.T, method, path string, body any, auth bool) *httptest.ResponseRecorder {
	t.Helper()

	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.Header.Set("Hue-API-Key", f.secret)
	}

	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func decodeBodyMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func TestHTTPHealthAndAuth(t *testing.T) {
	fx := newHTTPFixture(t)

	health := fx.doJSON(t, http.MethodGet, "/health", nil, false)
	if health.Code != http.StatusOK {
		t.Fatalf("expected 200 for health, got %d", health.Code)
	}

	unauth := fx.doJSON(t, http.MethodGet, "/api/v1/users", nil, false)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth secret, got %d", unauth.Code)
	}
}

func TestHTTPOwnerDBAuthKey(t *testing.T) {
	fx := newHTTPFixture(t)

	if err := fx.userDB.UpsertOwnerAuthKey("db-owner-key"); err != nil {
		t.Fatalf("upsert owner auth key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.Header.Set("Hue-API-Key", "db-owner-key")
	rr := httptest.NewRecorder()
	fx.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with db-backed owner auth key, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHTTPUserPackageNodeServiceFlow(t *testing.T) {
	fx := newHTTPFixture(t)

	createUser := fx.doJSON(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "api-user",
		"password": "p@ss",
		"groups":   []string{"premium"},
	}, true)
	if createUser.Code != http.StatusCreated {
		t.Fatalf("expected 201 create user, got %d body=%s", createUser.Code, createUser.Body.String())
	}
	createdUser := decodeBodyMap(t, createUser)
	userID := createdUser["id"].(string)

	getUser := fx.doJSON(t, http.MethodGet, "/api/v1/users/"+userID, nil, true)
	if getUser.Code != http.StatusOK {
		t.Fatalf("expected 200 get user, got %d", getUser.Code)
	}

	updateUser := fx.doJSON(t, http.MethodPut, "/api/v1/users/"+userID, map[string]any{
		"username": "api-user-updated",
		"groups":   []string{"premium", "vip"},
	}, true)
	if updateUser.Code != http.StatusOK {
		t.Fatalf("expected 200 update user, got %d", updateUser.Code)
	}

	createNode := fx.doJSON(t, http.MethodPost, "/api/v1/nodes", map[string]any{
		"name":               "node-1",
		"secret_key":         "node-secret",
		"allowed_ips":        []string{"10.0.0.0/8"},
		"traffic_multiplier": 1.0,
		"reset_mode":         string(domain.ResetModeNoReset),
	}, true)
	if createNode.Code != http.StatusCreated {
		t.Fatalf("expected 201 create node, got %d body=%s", createNode.Code, createNode.Body.String())
	}
	createdNode := decodeBodyMap(t, createNode)
	nodeID := createdNode["id"].(string)

	listNodes := fx.doJSON(t, http.MethodGet, "/api/v1/nodes", nil, true)
	if listNodes.Code != http.StatusOK {
		t.Fatalf("expected 200 list nodes, got %d", listNodes.Code)
	}

	createService := fx.doJSON(t, http.MethodPost, "/api/v1/services", map[string]any{
		"node_id":              nodeID,
		"secret_key":           "svc-secret",
		"name":                 "svc-1",
		"protocol":             "vless",
		"allowed_auth_methods": []string{"uuid", "password"},
	}, true)
	if createService.Code != http.StatusCreated {
		t.Fatalf("expected 201 create service, got %d body=%s", createService.Code, createService.Body.String())
	}
	createdService := decodeBodyMap(t, createService)
	serviceID := createdService["id"].(string)

	createPackage := fx.doJSON(t, http.MethodPost, "/api/v1/packages", map[string]any{
		"user_id":        userID,
		"total_traffic":  10_000,
		"upload_limit":   0,
		"download_limit": 0,
		"reset_mode":     string(domain.ResetModeMonthly),
		"duration":       3600,
		"max_concurrent": 2,
	}, true)
	if createPackage.Code != http.StatusCreated {
		t.Fatalf("expected 201 create package, got %d body=%s", createPackage.Code, createPackage.Body.String())
	}
	createdPackage := decodeBodyMap(t, createPackage)
	pkgID := createdPackage["id"].(string)

	_, err := fx.userDB.Exec(`UPDATE users SET active_package_id = ? WHERE id = ?`, pkgID, userID)
	if err != nil {
		t.Fatalf("attach package to user: %v", err)
	}

	userPkg := fx.doJSON(t, http.MethodGet, "/api/v1/users/"+userID+"/package", nil, true)
	if userPkg.Code != http.StatusOK {
		t.Fatalf("expected 200 get user package, got %d body=%s", userPkg.Code, userPkg.Body.String())
	}

	stats := fx.doJSON(t, http.MethodGet, "/api/v1/stats", nil, true)
	if stats.Code != http.StatusOK {
		t.Fatalf("expected 200 stats, got %d", stats.Code)
	}

	deleteService := fx.doJSON(t, http.MethodDelete, "/api/v1/services/"+serviceID, nil, true)
	if deleteService.Code != http.StatusOK {
		t.Fatalf("expected 200 delete service, got %d", deleteService.Code)
	}

	deleteNode := fx.doJSON(t, http.MethodDelete, "/api/v1/nodes/"+nodeID, nil, true)
	if deleteNode.Code != http.StatusOK {
		t.Fatalf("expected 200 delete node, got %d", deleteNode.Code)
	}

	deleteUser := fx.doJSON(t, http.MethodDelete, "/api/v1/users/"+userID, nil, true)
	if deleteUser.Code != http.StatusOK {
		t.Fatalf("expected 200 delete user, got %d", deleteUser.Code)
	}
}
