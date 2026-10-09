package harness

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Setup performs the system initialization POST /api/setup/init.
// It uses a fresh Client so it works before any session is established.
func Setup(t *testing.T, baseURL, adminDomain, proxyDomain string) {
	t.Helper()

	c := NewClient(baseURL, adminDomain)
	c.SetCookie("hopproxy_csrf", "setup-csrf-token")

	resp := c.Post(t, "/api/setup/init", map[string]string{
		"username":     "admin",
		"password":     "admin123",
		"admin_domain": adminDomain,
		"proxy_domain": proxyDomain,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "setup init should succeed")

	var result struct {
		Message string `json:"message"`
	}
	err := DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	assert.Contains(t, result.Message, "成功")
}

// IsInitialized checks whether the server has been initialized.
func IsInitialized(baseURL, host string) bool {
	c := NewClient(baseURL, host)
	resp, err := c.Client.Do(mustNewRequest("GET", baseURL+"/api/setup/status", host))
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Initialized bool `json:"initialized"`
		} `json:"data"`
	}
	if err := DecodeJSON(resp.Body, &result); err != nil {
		return false
	}
	return result.Data.Initialized
}

// Login performs POST /api/auth/login and returns an authenticated Client.
// If the system is not yet initialized, Setup is called first.
func Login(t *testing.T, baseURL, adminDomain string) *Client {
	t.Helper()

	if !IsInitialized(baseURL, adminDomain) {
		// Default proxy domain: replace "admin." prefix with empty
		proxyDomain := adminDomain
		Setup(t, baseURL, adminDomain, proxyDomain)
	}

	c := NewClient(baseURL, adminDomain)
	c.SetCookie("hopproxy_csrf", "login-csrf-token")

	resp := c.Post(t, "/api/auth/login", map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			OK bool `json:"ok"`
		} `json:"data"`
	}
	err := DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	assert.True(t, result.Data.OK)

	return c
}

// mustNewRequest is a helper to create a plain GET request without t.
func mustNewRequest(method, url, host string) *http.Request {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		panic(err)
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Accept-Encoding", "identity")
	return req
}
