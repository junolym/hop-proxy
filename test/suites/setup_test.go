package suites

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/robin/hop-proxy/test/harness"
)

// TestMain manages the full Docker Compose environment lifecycle.
// It performs two-phase startup (server + backends, then clients),
// waits for all services to be ready, runs all tests, and tears down.
func TestMain(m *testing.M) {
	ctx := context.Background()
	env := harness.NewTestEnv(dockerDir())

	// If the environment is already running (e.g. developer started it manually),
	// skip the startup phase and just run the tests.
	if !env.IsRunning(ctx) {
		fmt.Println("=== Starting test environment ===")
		if err := env.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: failed to start test environment: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== Test environment started ===")
	} else {
		fmt.Println("=== Test environment already running ===")
	}

	// Wait for clients to come online (they connect via WebSocket after starting).
	// We need to login first to get a session token for the API.
	if !harness.IsInitialized(serverURL, adminDomain) {
		fmt.Fprintf(os.Stderr, "FATAL: server is running but not initialized\n")
		os.Exit(1)
	}

	// Login and wait for at least 2 clients online.
	fmt.Println("=== Waiting for clients to come online ===")
	sessionToken, _, err := loginGetTokens(serverURL, adminDomain)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: login failed: %v\n", err)
		os.Exit(1)
	}

	if err := harness.WaitForAllClients(serverURL, adminDomain, sessionToken, 2, 60*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: not all clients came online: %v\n", err)
		fmt.Fprintf(os.Stderr, "Some client-dependent tests may be skipped or fail.\n")
	} else {
		fmt.Println("=== All clients online ===")
	}

	// Run the test suite.
	fmt.Println("=== Running tests ===")
	code := m.Run()

	// On failure, capture Docker logs before teardown for post-mortem diagnosis.
	if code != 0 {
		fmt.Println("=== Test failed, capturing Docker logs ===")
		captureDockerLogs()
	}

	// Cleanup: tear down the environment.
	fmt.Println("=== Tearing down test environment ===")
	if err := env.StopWithVolumes(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to tear down: %v\n", err)
	}

	os.Exit(code)
}

// captureDockerLogs saves server and client logs to /tmp for post-mortem diagnosis.
func captureDockerLogs() {
	services := []string{"server", "client-1", "client-2"}
	for _, svc := range services {
		cmd := exec.Command("docker", "compose", "-f", dockerDir()+"/docker-compose.yml",
			"logs", "--no-log-prefix", svc)
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: failed to capture %s logs: %v\n", svc, err)
			continue
		}
		path := "/tmp/diag-" + svc + ".log"
		if writeErr := os.WriteFile(path, out, 0644); writeErr != nil {
			fmt.Fprintf(os.Stderr, "WARN: failed to write %s: %v\n", path, writeErr)
			continue
		}
		fmt.Printf("  saved %s logs to %s (%d bytes)\n", svc, path, len(out))
	}
}

// loginGetTokens logs in and returns session + csrf tokens.
// This mirrors the internal harness function for use in TestMain.
func loginGetTokens(baseURL, adminDomain string) (string, string, error) {
	c := harness.NewClient(baseURL, adminDomain)
	csrfToken := "testmain-csrf"
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
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("login returned %d", resp.StatusCode)
	}

	var sessionToken string
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
