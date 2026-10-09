package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TestEnv manages the Docker Compose test environment lifecycle.
type TestEnv struct {
	ComposeDir  string // Path to the directory containing docker-compose.yml
	ProjectName string // Docker Compose project name (avoids conflicts)

	ServerURL   string // e.g. http://localhost:18080
	AdminDomain string
	ProxyDomain string

	clientIDs []string // populated by Start() after API registration

	agentKeyUUID string // populated by Start() after agent_key creation
}

// NewTestEnv creates a new test environment.
// composeDir should be the directory containing docker-compose.yml.
func NewTestEnv(composeDir string) *TestEnv {
	return &TestEnv{
		ComposeDir:  composeDir,
		ProjectName: "hopproxy-integration-test",
		ServerURL:   "http://localhost:18080",
		AdminDomain: "hopproxy-admin.test",
		ProxyDomain: "hopproxy.test",
	}
}

// ComposeArgs returns base arguments for docker compose commands.
func (e *TestEnv) ComposeArgs() []string {
	return []string{
		"compose",
		"-f", e.ComposeDir + "/docker-compose.yml",
		"-p", e.ProjectName,
	}
}

// ComposeCmd creates an *exec.Cmd for a docker compose subcommand.
func (e *TestEnv) ComposeCmd(ctx context.Context, args ...string) *exec.Cmd {
	allArgs := append(e.ComposeArgs(), args...)
	cmd := exec.CommandContext(ctx, "docker", allArgs...)
	return cmd
}

// composeCmdEnv creates a docker compose command with additional env vars injected.
func (e *TestEnv) composeCmdEnv(ctx context.Context, env []string, args ...string) *exec.Cmd {
	cmd := e.ComposeCmd(ctx, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

// Start performs two-phase environment startup:
//
//	Phase 1: Start server + backends, wait for server readiness.
//	Phase 2: Register two clients via API, then start client containers
//	         with the assigned UUIDs injected as HP_CLIENT_ID_1 / HP_CLIENT_ID_2.
func (e *TestEnv) Start(ctx context.Context) error {
	// Phase 1: start server + backends (no client containers yet)
	cmd := e.ComposeCmd(ctx, "up", "-d", "--build", "--wait",
		"server", "test-backend", "test-nginx")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("phase 1 up failed: %w\n%s", err, out)
	}

	// Wait for server HTTP endpoint to be ready.
	if err := WaitForServer(e.ServerURL, e.AdminDomain, 60*time.Second); err != nil {
		return fmt.Errorf("server not ready: %w", err)
	}

	// Ensure system is initialized.
	if !IsInitialized(e.ServerURL, e.AdminDomain) {
		if err := setupInit(e.ServerURL, e.AdminDomain, e.ProxyDomain); err != nil {
			return fmt.Errorf("setup init failed: %w", err)
		}
	}

	// Login to get a session for the API calls.
	sessionToken, csrfToken, err := loginGetTokens(e.ServerURL, e.AdminDomain)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	// Phase 2: register two client records, capture UUIDs.
	id1, err := createClient(e.ServerURL, e.AdminDomain, sessionToken, csrfToken, "test-client-1")
	if err != nil {
		return fmt.Errorf("create client-1 failed: %w", err)
	}
	id2, err := createClient(e.ServerURL, e.AdminDomain, sessionToken, csrfToken, "test-client-2")
	if err != nil {
		return fmt.Errorf("create client-2 failed: %w", err)
	}
	e.clientIDs = []string{id1, id2}

	// Create an agent key for the agent integration test.
	agentUUID, err := createAgentKey(e.ServerURL, e.AdminDomain, sessionToken, csrfToken, "test-agent-key")
	if err != nil {
		return fmt.Errorf("create agent key failed: %w", err)
	}
	e.agentKeyUUID = agentUUID

	// Start client containers + test-agent container with the assigned UUIDs.
	// --build 必不可少：client 镜像只 COPY bin/，不重建会一直跑旧二进制
	// （phase 1 只 build server/backends）。
	env := []string{
		"HP_CLIENT_ID_1=" + id1,
		"HP_CLIENT_ID_2=" + id2,
		"HP_AGENT_UUID=" + agentUUID,
	}
	cmd2 := e.composeCmdEnv(ctx, env, "up", "-d", "--build", "--wait", "client-1", "client-2")
	if out, err := cmd2.CombinedOutput(); err != nil {
		return fmt.Errorf("phase 2 up clients failed: %w\n%s", err, out)
	}

	// Start test-agent separately (no --wait: it has no healthcheck and exits if it can't fetch pubkey)
	cmd3 := e.composeCmdEnv(ctx, env, "up", "-d", "--build", "test-agent")
	if out, err := cmd3.CombinedOutput(); err != nil {
		return fmt.Errorf("phase 2 up test-agent failed: %w\n%s", err, out)
	}
	// Give agent a moment to fetch pubkey and start listening
	time.Sleep(2 * time.Second)

	return nil
}

// Stop stops and removes all containers and networks (preserves volumes).
func (e *TestEnv) Stop(ctx context.Context) error {
	cmd := e.ComposeCmd(ctx, "down")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down failed: %w\n%s", err, string(out))
	}
	return nil
}

// StopWithVolumes stops and removes all containers, networks, and volumes.
func (e *TestEnv) StopWithVolumes(ctx context.Context) error {
	cmd := e.ComposeCmd(ctx, "down", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down -v failed: %w\n%s", err, string(out))
	}
	return nil
}

// RestartService restarts a specific service.
func (e *TestEnv) RestartService(ctx context.Context, serviceName string) error {
	cmd := e.ComposeCmd(ctx, "restart", serviceName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart %s failed: %w\n%s", serviceName, err, string(out))
	}
	return nil
}

// Logs returns the logs for a specific service (last N lines).
func (e *TestEnv) Logs(serviceName string, tail int) (string, error) {
	ctx := context.Background()
	args := []string{"logs", serviceName}
	if tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tail))
	}
	cmd := e.ComposeCmd(ctx, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("logs %s failed: %w\n%s", serviceName, err, string(out))
	}
	return string(out), nil
}

// ClientIDs returns the UUIDs of the two registered test clients.
// Only valid after Start() has been called.
func (e *TestEnv) ClientIDs() []string {
	return e.clientIDs
}

// AgentKeyUUID returns the UUID of the registered agent key.
// Only valid after Start() has been called.
func (e *TestEnv) AgentKeyUUID() string {
	return e.agentKeyUUID
}

// createAgentKey creates an agent key via the management API.
func createAgentKey(serverURL, adminDomain, sessionToken, csrfToken, name string) (string, error) {
	c := NewClient(serverURL, adminDomain)
	c.SetCookie("hopproxy_session", sessionToken)
	c.SetCookie("hopproxy_csrf", csrfToken)

	req, err := c.NewRequest("POST", "/api/agent-keys", map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	req.Header.Set("X-CSRF-Token", csrfToken)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("create agent key: status %d", resp.StatusCode)
	}
	var result struct {
		Data struct {
			UUID string `json:"uuid"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Data.UUID == "" {
		return "", fmt.Errorf("agent key UUID empty")
	}
	return result.Data.UUID, nil
}

// IsRunning checks if the compose environment is up (any containers running).
func (e *TestEnv) IsRunning(ctx context.Context) bool {
	cmd := e.ComposeCmd(ctx, "ps", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// DirectBackendURL returns the URL for directly accessing the test backend
// from the host machine, bypassing the HopProxy server.
func (e *TestEnv) DirectBackendURL() string {
	return "http://localhost:18081"
}

// ServiceURLs returns the internal Docker network URLs for services.
func (e *TestEnv) ServiceURLs() map[string]string {
	return map[string]string{
		"server":       "http://server:8080",
		"test-backend": "http://test-backend:8000",
		"test-nginx":   "http://test-nginx:80",
	}
}

// =============================================================================
// Internal helpers for two-phase startup
// =============================================================================

func setupInit(baseURL, adminDomain, proxyDomain string) error {
	c := NewClient(baseURL, adminDomain)
	c.SetCookie("hopproxy_csrf", "setup-csrf")

	req, err := c.NewRequest("POST", "/api/setup/init", map[string]string{
		"username":     "admin",
		"password":     "admin123",
		"admin_domain": adminDomain,
		"proxy_domain": proxyDomain,
	})
	if err != nil {
		return err
	}
	req.Header.Set("X-CSRF-Token", "setup-csrf")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("setup/init returned %d", resp.StatusCode)
	}
	return nil
}

func loginGetTokens(baseURL, adminDomain string) (sessionToken, csrfToken string, err error) {
	c := NewClient(baseURL, adminDomain)
	csrfToken = "login-csrf"
	c.SetCookie("hopproxy_csrf", csrfToken)

	req, err := c.NewRequest("POST", "/api/auth/login", map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	if err != nil {
		return "", "", err
	}
	req.Header.Set("X-CSRF-Token", csrfToken)
	resp, err := c.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("login returned %d", resp.StatusCode)
	}

	// Extract session cookie set by the server.
	for _, ck := range resp.Cookies() {
		if ck.Name == "hopproxy_session" {
			sessionToken = ck.Value
		}
		if ck.Name == "hopproxy_csrf" {
			csrfToken = ck.Value
		}
	}
	if sessionToken == "" {
		return "", "", fmt.Errorf("no hopproxy_session cookie in login response")
	}
	return sessionToken, csrfToken, nil
}

func createClient(baseURL, adminDomain, sessionToken, csrfToken, name string) (string, error) {
	c := NewClient(baseURL, adminDomain)
	c.SetCookie("hopproxy_session", sessionToken)
	c.SetCookie("hopproxy_csrf", csrfToken)

	req, err := c.NewRequest("POST", "/api/clients", map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	req.Header.Set("X-CSRF-Token", csrfToken)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST /api/clients returned %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode client response: %w", err)
	}
	if result.Data.ID == "" {
		return "", fmt.Errorf("empty client ID in response")
	}
	return result.Data.ID, nil
}
