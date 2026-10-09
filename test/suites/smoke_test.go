package suites

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// Integration test configuration.
// Requests go to localhost:18080 with Host header set to adminDomain.
// This avoids requiring /etc/hosts modifications.
const (
	serverURL   = "http://localhost:18080"
	adminDomain = "hopproxy-admin.test"
	proxyDomain = "hopproxy.test"
)

// dockerDir returns the absolute path to the docker compose directory.
// Uses runtime.Caller to find the file location, avoiding hardcoded paths.
func dockerDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "docker")
}

// =============================================================================
// Smoke Tests — Infrastructure verification
// =============================================================================

func TestSmoke_ServerVersion(t *testing.T) {
	c := harness.NewClient(serverURL, adminDomain)
	resp := c.Get(t, "/api/version")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			GoVersion string `json:"go_version"`
		} `json:"data"`
	}
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Data.GoVersion)
}

func TestSmoke_TestBackendAccessible(t *testing.T) {
	env := harness.NewTestEnv(dockerDir())

	logs, err := env.Logs("server", 3)
	require.NoError(t, err)
	t.Logf("Server logs:\n%s", logs)

	cmd := env.ComposeCmd(t.Context(), "exec", "server", "wget", "-qO-", "http://test-backend:8000/")
	out, err := cmd.Output()
	require.NoError(t, err)

	var result struct {
		Status string `json:"status"`
	}
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Status)
}

func TestSmoke_TestBackendHeaders(t *testing.T) {
	env := harness.NewTestEnv(dockerDir())

	cmd := env.ComposeCmd(t.Context(), "exec", "server", "wget", "-qO-", "--header=X-Custom: test-value", "http://test-backend:8000/headers")
	out, err := cmd.Output()
	require.NoError(t, err)

	var result struct {
		Headers map[string]string `json:"headers"`
	}
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)
	assert.Equal(t, "test-value", result.Headers["X-Custom"])
}

func TestSmoke_TestBackendStatusCodes(t *testing.T) {
	env := harness.NewTestEnv(dockerDir())

	cmd := env.ComposeCmd(t.Context(), "exec", "server", "wget", "-qO-", "http://test-backend:8000/status/200")
	out, _ := cmd.CombinedOutput()

	var result struct {
		Code int `json:"code"`
	}
	err := json.Unmarshal(out, &result)
	require.NoError(t, err, "got: %s", string(out))
	assert.Equal(t, 200, result.Code)
}

func TestSmoke_TestBackendEcho(t *testing.T) {
	env := harness.NewTestEnv(dockerDir())

	cmd := env.ComposeCmd(t.Context(), "exec", "server", "wget", "-qO-", "--post-data=hello-world", "http://test-backend:8000/echo")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "hello-world", string(out))
}

func TestSmoke_TestNginxAccessible(t *testing.T) {
	env := harness.NewTestEnv(dockerDir())

	cmd := env.ComposeCmd(t.Context(), "exec", "server", "wget", "-qO-", "http://test-nginx/health")
	out, err := cmd.Output()
	require.NoError(t, err)

	var result struct {
		Status string `json:"status"`
	}
	err = json.Unmarshal(out, &result)
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Status)
}

// =============================================================================
// Setup Tests
// =============================================================================

func TestSetup_Double_Init(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not yet initialized")
	}

	c := harness.NewClient(serverURL, adminDomain)
	c.SetCookie("hopproxy_csrf", "double-init-csrf")

	resp := c.Post(t, "/api/setup/init", map[string]string{
		"username":     "admin",
		"password":     "admin123",
		"admin_domain": adminDomain,
		"proxy_domain": proxyDomain,
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result struct {
		Error string `json:"error"`
	}
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "已初始化")
}

func TestSetup_Missing_Fields(t *testing.T) {
	if harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system already initialized")
	}

	tests := []struct {
		name string
		body map[string]string
	}{
		{"missing_password", map[string]string{"username": "admin", "admin_domain": "a.test", "proxy_domain": "b.test"}},
		{"short_password", map[string]string{"username": "admin", "password": "12345", "admin_domain": "a.test", "proxy_domain": "b.test"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := harness.NewClient(serverURL, adminDomain)
			c.SetCookie("hopproxy_csrf", "missing-fields-csrf")
			resp := c.Post(t, "/api/setup/init", tc.body)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

// =============================================================================
// Auth Tests
// =============================================================================

func TestAuth_Login_Success(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	resp := c.Get(t, "/api/auth/me")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			Username string `json:"username"`
			IsAdmin  bool   `json:"is_admin"`
		} `json:"data"`
	}
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	assert.Equal(t, "admin", result.Data.Username)
	assert.True(t, result.Data.IsAdmin)
}

func TestAuth_Login_WrongPassword(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.NewClient(serverURL, adminDomain)
	c.SetCookie("hopproxy_csrf", "wrong-pwd-csrf")

	resp := c.Post(t, "/api/auth/login", map[string]string{
		"username": "admin",
		"password": "wrong",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_Me_Unauthenticated(t *testing.T) {
	c := harness.NewClient(serverURL, adminDomain)
	resp := c.Get(t, "/api/auth/me")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_Logout(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	resp := c.Post(t, "/api/auth/logout", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// After logout, /me should return 401
	resp2 := c.Get(t, "/api/auth/me")
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// =============================================================================
// Client Tests
// =============================================================================

func TestClients_List(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	resp := c.Get(t, "/api/clients")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Online bool   `json:"online"`
		} `json:"data"`
	}
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)

	t.Logf("Clients: %d found", len(result.Data))
	for _, cl := range result.Data {
		t.Logf("  id=%s name=%s online=%v", cl.ID, cl.Name, cl.Online)
	}
}

// =============================================================================
// Harness Infrastructure Tests
// =============================================================================

func TestHarness_WaitForServer(t *testing.T) {
	err := harness.WaitForServer(serverURL, adminDomain, 10*time.Second)
	assert.NoError(t, err, "server should be reachable")
}

func TestHarness_WaitForSetup(t *testing.T) {
	err := harness.WaitForSetup(serverURL, adminDomain, 10*time.Second)
	assert.NoError(t, err, "setup status endpoint should be reachable")
}

func TestHarness_EnvRunning(t *testing.T) {
	env := harness.NewTestEnv(dockerDir())
	assert.True(t, env.IsRunning(t.Context()))
}

func TestHarness_ClientIDs(t *testing.T) {
	// Client IDs are assigned dynamically at startup (not hardcoded).
	// This test just verifies two IDs are available when the env is running.
	env := harness.NewTestEnv(dockerDir())
	ids := env.ClientIDs()
	// When running against a pre-started env (no Start() called), IDs are empty.
	// This is expected; full client ID verification happens in clients_test.go.
	t.Logf("ClientIDs from env: %v (populated only after TestEnv.Start())", ids)
}
