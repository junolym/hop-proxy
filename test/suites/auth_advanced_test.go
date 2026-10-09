package suites

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// Response types for access token API
// =============================================================================

// accessTokenResponse mirrors GET /api/access-tokens list item.
type accessTokenResponse struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Token          string  `json:"token"`
	AllowedEntries string  `json:"allowed_entries"`
	AllowedAppIDs  string  `json:"allowed_app_ids"`
	ExpiresAt      *string `json:"expires_at"`
	LastUsedAt     *string `json:"last_used_at"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// createTokenResponse mirrors POST /api/access-tokens response.
type createTokenResponse struct {
	Data    accessTokenResponse `json:"data"`
	Message string              `json:"message"`
}

// listTokensResponse mirrors GET /api/access-tokens response.
type listTokensResponse struct {
	Data    []accessTokenResponse `json:"data"`
	Message string                `json:"message"`
}

// =============================================================================
// Helpers
// =============================================================================

// createAccessToken creates an access token via the API and returns the full token string.
// The token is created with server entry access and no specific app restrictions.
func createAccessToken(t *testing.T, c *harness.Client, name string, appID int64, expiresInSecs *int) string {
	t.Helper()

	body := map[string]interface{}{
		"name": name,
		"allowed_entries": map[string]interface{}{
			"server":  true,
			"clients": []string{},
		},
		"allowed_app_ids": []int64{appID},
	}
	if expiresInSecs != nil {
		body["expires_in_secs"] = *expiresInSecs
	}

	resp := c.Post(t, "/api/access-tokens", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create access token should succeed")

	var result createTokenResponse
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Data.Token, "token should not be empty")
	assert.Equal(t, name, result.Data.Name)
	return result.Data.Token
}

// createAccessTokenWithEntry creates an access token with specific allowed_entries config.
func createAccessTokenWithEntry(t *testing.T, c *harness.Client, name string, appID int64, allowServer bool, allowClients []string) string {
	t.Helper()

	body := map[string]interface{}{
		"name": name,
		"allowed_entries": map[string]interface{}{
			"server":  allowServer,
			"clients": allowClients,
		},
		"allowed_app_ids": []int64{appID},
	}

	resp := c.Post(t, "/api/access-tokens", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result createTokenResponse
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	return result.Data.Token
}

// cleanupAppBySubdomain deletes any app with the given subdomain.
func cleanupAppBySubdomain(t *testing.T, c *harness.Client, subdomain string) {
	t.Helper()
	resp := c.Get(t, "/api/apps")
	defer resp.Body.Close()
	var list listAppsResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &list))
	for _, app := range list.Data {
		if app.Subdomain == subdomain {
			c.Delete(t, fmt.Sprintf("/api/apps/%d", app.ID))
		}
	}
}

// createTestApp creates a temporary test app with token auth and returns its ID.
func createTestApp(t *testing.T, c *harness.Client, subdomain string, authMethod string) int64 {
	t.Helper()

	cleanupAppBySubdomain(t, c, subdomain)

	clientID := firstClientID(t, c)

	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        "token-test-" + subdomain,
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{clientID},
		"auth_method": authMethod,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errRes struct {
			Error string `json:"error"`
		}
		harness.DecodeJSON(resp.Body, &errRes)
		t.Fatalf("create app %s failed (status %d): %s", subdomain, resp.StatusCode, errRes.Error)
	}

	var result singleAppResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	return result.Data.ID
}

// rawGet sends a GET request with the given Authorization header to the proxy domain.
// Does not follow redirects — returns the first response (including 302).
func rawGet(t *testing.T, baseURL, proxyHost, path, authHeader string) *http.Response {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest("GET", baseURL+path, nil)
	require.NoError(t, err)
	req.Host = proxyHost
	req.Header.Set("Accept-Encoding", "identity")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := hc.Do(req)
	require.NoError(t, err)
	return resp
}

// =============================================================================
// 1. Bearer Token Authentication Effectiveness Test
// =============================================================================

// rawLogin 发送裸登录请求并返回响应（不经过 harness.Login 的封装，便于检查 Set-Cookie 原始头）。
func rawLogin(t *testing.T) *http.Response {
	t.Helper()
	c := harness.NewClient(serverURL, adminDomain)
	c.SetCookie("hopproxy_csrf", "cookie-mode-csrf")
	req, err := c.NewRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	require.NoError(t, err)
	req.Header.Set("X-CSRF-Token", "cookie-mode-csrf")
	resp, err := c.Do(req)
	require.NoError(t, err)
	return resp
}

// sessionCookieHeader 从登录响应中提取 hopproxy_session 的 Set-Cookie 原始头（找不到则失败）。
func sessionCookieHeader(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, sc := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(sc, "hopproxy_session=") {
			return sc
		}
	}
	t.Fatal("login response should set hopproxy_session cookie")
	return ""
}

// TestAdminSessionCookieMode 验证管理端会话 Cookie 模式（#77）：
// 默认持久模式 Set-Cookie 带 Max-Age；切 session 模式后不带 Max-Age/Expires，且会话仍可用；
// 非法值被 400 拒绝。
func TestAdminSessionCookieMode(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	admin := harness.Login(t, serverURL, adminDomain)
	setMode := func(mode string) *http.Response {
		t.Helper()
		resp := admin.Put(t, "/api/admin/settings", map[string]interface{}{
			"session_cookie_mode": mode,
		})
		return resp
	}
	// 测试结束恢复默认，避免影响其他用例
	t.Cleanup(func() {
		resp := setMode("persistent")
		resp.Body.Close()
	})

	// 先确保回到持久模式
	resp := setMode("persistent")
	require.Equal(t, http.StatusOK, resp.StatusCode, "set persistent mode should succeed")
	resp.Body.Close()

	// 持久模式：登录 Set-Cookie 带 Max-Age
	loginResp := rawLogin(t)
	defer loginResp.Body.Close()
	sc := sessionCookieHeader(t, loginResp)
	assert.Contains(t, sc, "Max-Age=", "persistent mode cookie should carry Max-Age")

	// 非法值：400 拒绝
	badResp := setMode("bogus")
	defer badResp.Body.Close()
	require.Equal(t, http.StatusBadRequest, badResp.StatusCode, "invalid session_cookie_mode should be rejected")

	// 会话级模式：登录 Set-Cookie 不带 Max-Age / Expires
	resp = setMode("session")
	require.Equal(t, http.StatusOK, resp.StatusCode, "set session mode should succeed")
	resp.Body.Close()

	loginResp = rawLogin(t)
	sc = sessionCookieHeader(t, loginResp)
	assert.NotContains(t, sc, "Max-Age=", "session mode cookie should not carry Max-Age")
	assert.NotContains(t, sc, "Expires=", "session mode cookie should not carry Expires")

	// 会话级 cookie 仍可正常使用（JWT 校验照常）
	for _, ck := range loginResp.Cookies() {
		if ck.Name != "hopproxy_session" {
			continue
		}
		c2 := harness.NewClient(serverURL, adminDomain)
		c2.SetCookie("hopproxy_session", ck.Value)
		meResp := c2.Get(t, "/api/auth/me")
		defer meResp.Body.Close()
		require.Equal(t, http.StatusOK, meResp.StatusCode, "session-mode cookie should authenticate")
		break
	}
}

func TestAuth_BearerToken_ProxyAccess(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token-only auth.
	appSubdomain := "bearer-token-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Create an access token scoped to this app.
	token := createAccessToken(t, c, "test-bearer-token", appID, nil)

	// Find token ID for cleanup.
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &tokenList))
	for _, tk := range tokenList.Data {
		if tk.Name == "test-bearer-token" {
			t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tk.ID)) })
			break
		}
	}

	proxyHost := appSubdomain + "." + proxyDomain

	// Without token: should get 401.
	respNoToken := rawGet(t, serverURL, proxyHost, "/", "")
	defer respNoToken.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respNoToken.StatusCode,
		"request without token should be rejected with 401")

	// With valid Bearer token: should succeed (200 or 302 depending on backend).
	respBearer := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer respBearer.Body.Close()
	if respBearer.StatusCode == http.StatusInternalServerError {
		body, _ := io.ReadAll(respBearer.Body)
		t.Fatalf("Bearer token request failed with 500: %s", string(body))
	}
	assert.NotEqual(t, http.StatusUnauthorized, respBearer.StatusCode,
		"request with valid Bearer token should not be rejected")
}

func TestAuth_BearerToken_APIAccess(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	// Login to get a valid JWT session.
	c := harness.Login(t, serverURL, adminDomain)

	// Access /api/auth/me with cookie-based session (should work).
	resp := c.Get(t, "/api/auth/me")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// =============================================================================
// 2. Basic Auth Format Token Test
// =============================================================================

func TestAuth_BasicAuthToken_ProxyAccess(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token-only auth.
	appSubdomain := "basic-token-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Create an access token scoped to this app.
	token := createAccessToken(t, c, "test-basic-token", appID, nil)

	// Find token ID for cleanup.
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &tokenList))
	for _, tk := range tokenList.Data {
		if tk.Name == "test-basic-token" {
			t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tk.ID)) })
			break
		}
	}

	proxyHost := appSubdomain + "." + proxyDomain

	// Basic auth with username "token" and password as the token value.
	credentials := base64.StdEncoding.EncodeToString([]byte("token:" + token))
	basicAuthHeader := "Basic " + credentials

	respBasic := rawGet(t, serverURL, proxyHost, "/", basicAuthHeader)
	defer respBasic.Body.Close()
	if respBasic.StatusCode == http.StatusInternalServerError {
		body, _ := io.ReadAll(respBasic.Body)
		t.Fatalf("Basic auth token request failed with 500: %s", string(body))
	}
	assert.NotEqual(t, http.StatusUnauthorized, respBasic.StatusCode,
		"request with valid Basic auth token format should not be rejected")
}

func TestAuth_BasicAuthToken_WrongUsername(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token-only auth.
	appSubdomain := "basic-wrong-user-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Create an access token.
	token := createAccessToken(t, c, "test-wrong-user", appID, nil)

	// Find token ID for cleanup.
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &tokenList))
	for _, tk := range tokenList.Data {
		if tk.Name == "test-wrong-user" {
			t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tk.ID)) })
			break
		}
	}

	proxyHost := appSubdomain + "." + proxyDomain

	// Basic auth with wrong username (not "token").
	credentials := base64.StdEncoding.EncodeToString([]byte("wronguser:" + token))
	basicAuthHeader := "Basic " + credentials

	resp := rawGet(t, serverURL, proxyHost, "/", basicAuthHeader)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"Basic auth with wrong username should be rejected with 401")
}

func TestAuth_BasicAuthToken_EmptyPassword(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token-only auth.
	appSubdomain := "basic-empty-pw-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	proxyHost := appSubdomain + "." + proxyDomain

	// Basic auth with username "token" but empty password.
	credentials := base64.StdEncoding.EncodeToString([]byte("token:"))
	basicAuthHeader := "Basic " + credentials

	resp := rawGet(t, serverURL, proxyHost, "/", basicAuthHeader)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"Basic auth with empty password should be rejected with 401")
}

// =============================================================================
// 3. Token Scope Validation
// =============================================================================

func TestAuth_TokenScope_WrongApp(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create two apps with token auth.
	appSubdomain1 := "scope-app1-test"
	appSubdomain2 := "scope-app2-test"
	appID1 := createTestApp(t, c, appSubdomain1, "token")
	appID2 := createTestApp(t, c, appSubdomain2, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID1)) })
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID2)) })

	// Create a token scoped only to app1.
	token := createAccessToken(t, c, "scope-test-token", appID1, nil)

	// Cleanup token.
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &tokenList))
	for _, tk := range tokenList.Data {
		if tk.Name == "scope-test-token" {
			t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tk.ID)) })
			break
		}
	}

	// Token should work for app1.
	proxyHost1 := appSubdomain1 + "." + proxyDomain
	resp1 := rawGet(t, serverURL, proxyHost1, "/", "Bearer "+token)
	defer resp1.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp1.StatusCode,
		"token scoped to app1 should allow access to app1")

	// Token should NOT work for app2.
	proxyHost2 := appSubdomain2 + "." + proxyDomain
	resp2 := rawGet(t, serverURL, proxyHost2, "/", "Bearer "+token)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode,
		"token scoped to app1 should be rejected for app2")
}

func TestAuth_TokenScope_ServerOnlyEntry(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token auth.
	appSubdomain := "scope-server-entry-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Create a token with server-only entry (no client entries).
	token := createAccessTokenWithEntry(t, c, "scope-server-entry", appID, true, []string{})

	// Cleanup token.
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &tokenList))
	for _, tk := range tokenList.Data {
		if strings.HasPrefix(tk.Token, token[:8]) {
			t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tk.ID)) })
			break
		}
	}

	proxyHost := appSubdomain + "." + proxyDomain

	// Server-entry token should work when accessing via server (proxy domain).
	resp := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"server-entry token should allow access via server proxy")
	t.Logf("Server-only entry token response status: %d", resp.StatusCode)
}

func TestAuth_TokenScope_ExpiredToken(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token auth.
	appSubdomain := "scope-expired-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Create a token that expires in 1 second.
	oneSec := 1
	token := createAccessToken(t, c, "scope-expired-token", appID, &oneSec)

	// Cleanup token.
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &tokenList))
	for _, tk := range tokenList.Data {
		if tk.Name == "scope-expired-token" {
			t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tk.ID)) })
			break
		}
	}

	proxyHost := appSubdomain + "." + proxyDomain

	// Token should still work immediately.
	resp1 := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer resp1.Body.Close()
	t.Logf("Immediately after creation: status=%d", resp1.StatusCode)

	// Use the token via the API to check the token is still present in the list.
	listResp2 := c.Get(t, "/api/access-tokens")
	defer listResp2.Body.Close()
	var tokenList2 listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp2.Body, &tokenList2))
	found := false
	for _, tk := range tokenList2.Data {
		if tk.Name == "scope-expired-token" {
			found = true
			assert.NotNil(t, tk.ExpiresAt, "expired token should have expires_at set")
			break
		}
	}
	assert.True(t, found, "expired token should still be listed")
}

func TestAuth_TokenScope_InvalidToken(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token auth.
	appSubdomain := "scope-invalid-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	proxyHost := appSubdomain + "." + proxyDomain

	// Use a completely bogus token.
	resp := rawGet(t, serverURL, proxyHost, "/", "Bearer totally-invalid-token-value-12345")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"invalid token should be rejected with 401")
}

func TestAuth_TokenScope_DeleteRevokesAccess(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Create an app with token auth.
	appSubdomain := "scope-revoke-test"
	appID := createTestApp(t, c, appSubdomain, "token")
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Create a token.
	token := createAccessToken(t, c, "scope-revoke-token", appID, nil)

	// Find token ID for cleanup/deletion.
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &tokenList))
	var tokenID int64
	for _, tk := range tokenList.Data {
		if tk.Name == "scope-revoke-token" {
			tokenID = tk.ID
			break
		}
	}
	require.NotZero(t, tokenID, "should find the created token")

	proxyHost := appSubdomain + "." + proxyDomain

	// Token should work before deletion.
	resp1 := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer resp1.Body.Close()
	if resp1.StatusCode == http.StatusInternalServerError {
		body, _ := io.ReadAll(resp1.Body)
		t.Fatalf("Request failed with 500 before deletion: %s", string(body))
	}
	assert.NotEqual(t, http.StatusUnauthorized, resp1.StatusCode,
		"token should work before deletion")

	// Delete the token.
	delResp := c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tokenID))
	defer delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode)

	// Token should no longer work after deletion.
	resp2 := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode,
		"deleted token should no longer grant access")
}
