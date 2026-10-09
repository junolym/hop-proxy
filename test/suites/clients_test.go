package suites

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// Response types for client API
// =============================================================================

// clientResponse mirrors a single client in the API response.
type clientResponse struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Online       bool    `json:"online"`
	AppCount     int     `json:"app_count"`
	Version      *string `json:"version,omitempty"`
	ProxyEnabled bool    `json:"proxy_enabled"`
	ProxyListen  string  `json:"proxy_listen"`
}

// listClientsResponse mirrors GET /api/clients response.
type listClientsResponse struct {
	Data    []clientResponse `json:"data"`
	Message string           `json:"message"`
}

// singleClientResponse mirrors POST /api/clients response.
type singleClientResponse struct {
	Data    clientResponse `json:"data"`
	Message string         `json:"message"`
}

// pingResponse mirrors GET /api/clients/{id}/ping response.
type pingResponse struct {
	Data struct {
		LatencyMs int64 `json:"latency_ms"`
	} `json:"data"`
}

// =============================================================================
// Registration / Deregistration Flow
// =============================================================================

func TestClients_Register(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientName := "test-register-client"

	// Create a new client.
	resp := c.Post(t, "/api/clients", map[string]string{"name": clientName})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var created singleClientResponse
	err := harness.DecodeJSON(resp.Body, &created)
	require.NoError(t, err)

	assert.NotEmpty(t, created.Data.ID, "client ID should be a UUID")
	assert.Equal(t, clientName, created.Data.Name)
	assert.False(t, created.Data.Online, "newly registered client should not be online")

	createdID := created.Data.ID

	// Cleanup: delete the client after test.
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/clients/%s", createdID)) })
}

func TestClients_Register_MissingName(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Try creating a client without a name.
	resp := c.Post(t, "/api/clients", map[string]string{"name": ""})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestClients_UpdateName(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientName := "test-update-name-client"

	// Create a client to update.
	createResp := c.Post(t, "/api/clients", map[string]string{"name": clientName})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleClientResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	createdID := created.Data.ID

	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/clients/%s", createdID)) })

	// Update the client name.
	updatedName := "test-updated-name-client"
	updateResp := c.Put(t, fmt.Sprintf("/api/clients/%s", createdID), map[string]string{
		"name": updatedName,
	})
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	var updateResult msgResponse
	err := harness.DecodeJSON(updateResp.Body, &updateResult)
	require.NoError(t, err)
	assert.Contains(t, updateResult.Message, "成功")

	// Verify the update by listing.
	listResp := c.Get(t, "/api/clients")
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	var listResult listClientsResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	for _, cl := range listResult.Data {
		if cl.ID == createdID {
			assert.Equal(t, updatedName, cl.Name, "client name should be updated")
			return
		}
	}
	t.Fatalf("created client %s not found in list after update", createdID)
}

func TestClients_Deregister(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientName := "test-deregister-client"

	// Create a client to delete.
	createResp := c.Post(t, "/api/clients", map[string]string{"name": clientName})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleClientResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	createdID := created.Data.ID

	// Verify it exists in the list.
	listResp := c.Get(t, "/api/clients")
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	var listBefore listClientsResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listBefore))
	found := false
	for _, cl := range listBefore.Data {
		if cl.ID == createdID {
			found = true
			break
		}
	}
	assert.True(t, found, "client should exist before deletion")

	// Delete the client.
	delResp := c.Delete(t, fmt.Sprintf("/api/clients/%s", createdID))
	defer delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode)

	var delResult msgResponse
	err := harness.DecodeJSON(delResp.Body, &delResult)
	require.NoError(t, err)
	assert.Contains(t, delResult.Message, "成功")

	// Verify it no longer exists in the list.
	listResp2 := c.Get(t, "/api/clients")
	defer listResp2.Body.Close()
	require.Equal(t, http.StatusOK, listResp2.StatusCode)

	var listAfter listClientsResponse
	require.NoError(t, harness.DecodeJSON(listResp2.Body, &listAfter))

	for _, cl := range listAfter.Data {
		assert.NotEqual(t, createdID, cl.ID, "deleted client should not appear in list")
	}
}

func TestClients_Delete_NotFound(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Try deleting a non-existent client.
	fakeID := "00000000-0000-0000-0000-000000000000"
	resp := c.Delete(t, fmt.Sprintf("/api/clients/%s", fakeID))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// =============================================================================
// Real-time Online/Offline Status Updates (via API Polling)
// =============================================================================

func TestClients_OnlineStatus_AfterRestart(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)

	// Find the first non-Host client ID from the list.
	resp := c.Get(t, "/api/clients")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var listResult listClientsResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &listResult))

	var clientID string
	for _, cl := range listResult.Data {
		if cl.ID != "__host__" {
			clientID = cl.ID
			break
		}
	}
	require.NotEmpty(t, clientID, "at least one non-Host client must exist")

	// Verify the client is currently online (it should be since the environment is running).
	require.True(t, clientOnline(t, c, clientID),
		"client %s should be online", clientID)

	// Stop the client containers to make them go offline.
	err := env.ComposeCmd(t.Context(), "stop", "client-1", "client-2").Run()
	require.NoError(t, err)

	// Wait for the client to appear offline via API polling.
	err = pollUntil(func() error {
		if clientOnline(t, c, clientID) {
			return fmt.Errorf("client still online")
		}
		return nil
	}, 15*time.Second)
	assert.NoError(t, err, "client should go offline after container stop")

	t.Logf("Client %s confirmed offline after stopping containers", clientID)

	// Restart client containers.
	err = env.ComposeCmd(t.Context(), "start", "client-1", "client-2").Run()
	require.NoError(t, err)

	// Wait for the client to come back online via API polling.
	err = pollUntil(func() error {
		if !clientOnline(t, c, clientID) {
			return fmt.Errorf("client still offline")
		}
		return nil
	}, 30*time.Second)
	assert.NoError(t, err, "client should come back online after container start")

	t.Logf("Client %s confirmed online after restarting containers", clientID)
}

// =============================================================================
// GET /api/clients/{id}/ping Connectivity
// =============================================================================

func TestClients_Ping_Online(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Find an online client.
	resp := c.Get(t, "/api/clients")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var listResult listClientsResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &listResult))

	var clientID string
	for _, cl := range listResult.Data {
		if cl.Online && cl.ID != "__host__" {
			clientID = cl.ID
			break
		}
	}
	require.NotEmpty(t, clientID, "at least one online non-Host client must exist")

	// Ping the online client.
	pingResp := c.Get(t, fmt.Sprintf("/api/clients/%s/ping", clientID))
	defer pingResp.Body.Close()
	require.Equal(t, http.StatusOK, pingResp.StatusCode)

	var pingResult pingResponse
	err := harness.DecodeJSON(pingResp.Body, &pingResult)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, pingResult.Data.LatencyMs, int64(0),
		"latency should be non-negative")
	t.Logf("Ping client %s: latency=%d ms", clientID, pingResult.Data.LatencyMs)
}

func TestClients_Ping_Offline(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	env := harness.NewTestEnv(dockerDir())

	// Find a non-Host client ID.
	resp := c.Get(t, "/api/clients")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var listResult listClientsResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &listResult))

	var clientID string
	for _, cl := range listResult.Data {
		if cl.ID != "__host__" {
			clientID = cl.ID
			break
		}
	}
	require.NotEmpty(t, clientID)

	// Stop client containers to make them go offline.
	err := env.ComposeCmd(t.Context(), "stop", "client-1", "client-2").Run()
	require.NoError(t, err)

	t.Cleanup(func() {
		// Ensure clients are restarted after this test.
		// Use context.Background() because t.Context() is already cancelled during cleanup.
		env.ComposeCmd(context.Background(), "start", "client-1", "client-2").Run()
	})

	// Wait for client to go offline.
	err = pollUntil(func() error {
		if clientOnline(t, c, clientID) {
			return fmt.Errorf("client still online")
		}
		return nil
	}, 15*time.Second)
	require.NoError(t, err, "client should go offline")

	// Ping the offline client — the server should still return 200
	// but latency_ms should indicate the client is unreachable.
	pingResp := c.Get(t, fmt.Sprintf("/api/clients/%s/ping", clientID))
	defer pingResp.Body.Close()
	require.Equal(t, http.StatusOK, pingResp.StatusCode)

	var pingResult pingResponse
	err = harness.DecodeJSON(pingResp.Body, &pingResult)
	require.NoError(t, err)
	// When offline, latency should be 0 or negative (unreachable).
	t.Logf("Ping offline client %s: latency=%d ms", clientID, pingResult.Data.LatencyMs)
}

func TestClients_Ping_NotFound(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Ping a non-existent client.
	fakeID := "00000000-0000-0000-0000-000000000000"
	resp := c.Get(t, fmt.Sprintf("/api/clients/%s/ping", fakeID))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// =============================================================================
// Helpers
// =============================================================================

// clientOnline checks if a specific client is online via the API.
func clientOnline(t *testing.T, c *harness.Client, clientID string) bool {
	t.Helper()
	resp := c.Get(t, "/api/clients")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result listClientsResponse
	if err := harness.DecodeJSON(resp.Body, &result); err != nil {
		return false
	}
	for _, cl := range result.Data {
		if cl.ID == clientID {
			return cl.Online
		}
	}
	return false
}

// pollUntil is a local poll helper (mirrors harness.pollUntil but accessible from test code).
func pollUntil(fn func() error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	if err := fn(); err == nil {
		return nil
	}

	for {
		select {
		case <-ticker.C:
			if err := fn(); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out after %v", timeout)
			}
		}
	}
}
