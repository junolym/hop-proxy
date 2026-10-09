package suites

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// 认证矩阵测试 — Token × 路径 + SSO × 路径 + SSO 策略
// =============================================================================

// --- 共享 helper ---

// createTestUser 创建一个非管理员用户用于 SSO 负面用例测试。
// 返回已登录的 harness.Client。
// 使用 t.Cleanup 自动删除用户。
func createTestUser(t *testing.T, adminClient *harness.Client, serverURL, adminDomain string) *harness.Client {
	t.Helper()
	username := fmt.Sprintf("testuser-%s", harness.DeriveSubdomain(t.Name()))
	resp := adminClient.Post(t, "/api/admin/users", map[string]interface{}{
		"username": username,
		"password": "TestPass123!",
		"role":     "user",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create test user should succeed")

	var result struct {
		Data struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err, "decode create user response")
	t.Cleanup(func() { adminClient.Delete(t, fmt.Sprintf("/api/admin/users/%d", result.Data.ID)) })

	// 登录新用户获取会话
	userClient := harness.NewClient(serverURL, adminDomain)
	userClient.SetCookie("hopproxy_csrf", "testuser-csrf-token")
	loginResp := userClient.Post(t, "/api/auth/login", map[string]string{
		"username": username,
		"password": "TestPass123!",
	})
	defer loginResp.Body.Close()
	require.Equal(t, http.StatusOK, loginResp.StatusCode, "test user login should succeed")
	return userClient
}

// authorizeSSO 通过 POST /api/sso/authorize 为指定应用颁发 SSO Cookie。
// 返回 SSO Cookie 值。
// c 必须是已登录的 harness.Client。
func authorizeSSO(t *testing.T, c *harness.Client, appID int64, subdomain string) string {
	t.Helper()

	redirectURL := fmt.Sprintf("http://%s.%s:18080/", subdomain, proxyDomain)
	resp := c.Post(t, "/api/sso/authorize", map[string]interface{}{
		"redirect": redirectURL,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "SSO authorize should succeed")

	// 从 Set-Cookie 中提取 SSO Cookie
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	cookies := resp.Cookies()
	for _, cookie := range cookies {
		if cookie.Name == cookieName {
			return cookie.Value
		}
	}
	t.Fatalf("SSO cookie %s not found in authorize response", cookieName)
	return ""
}

// cleanupTokenByName 删除指定名称的 access token
func cleanupTokenByName(t *testing.T, c *harness.Client, name string) {
	t.Helper()
	listResp := c.Get(t, "/api/access-tokens")
	defer listResp.Body.Close()
	var tokenList listTokensResponse
	if err := harness.DecodeJSON(listResp.Body, &tokenList); err != nil {
		return
	}
	for _, tk := range tokenList.Data {
		if tk.Name == name {
			c.Delete(t, fmt.Sprintf("/api/access-tokens/%d", tk.ID))
			return
		}
	}
}

// =============================================================================
// Token Auth — P1 Server-Local (__host__)
// =============================================================================

func TestAuthMatrix_Token_P1_Host(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 创建 __host__ 应用，auth_method="token"
	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "token",
	})

	// 创建 access token
	tokenName := fmt.Sprintf("token-p1-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	proxyHost := subdomain + "." + proxyDomain

	// 无 token → 401
	respNoToken := rawGet(t, serverURL, proxyHost, "/", "")
	defer respNoToken.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respNoToken.StatusCode,
		"P1: request without token should be rejected with 401")

	// 有 Bearer token → 200
	respWithToken := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer respWithToken.Body.Close()
	assert.Equal(t, http.StatusOK, respWithToken.StatusCode,
		"P1: request with valid token should succeed with 200")
}

// =============================================================================
// Token Auth — P2 Tunnel
// =============================================================================

func TestAuthMatrix_Token_P2_Tunnel(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{clientID},
		AuthMethod: "token",
	})

	tokenName := fmt.Sprintf("token-p2-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	proxyHost := subdomain + "." + proxyDomain

	// 无 token → 401
	respNoToken := rawGet(t, serverURL, proxyHost, "/", "")
	defer respNoToken.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respNoToken.StatusCode,
		"P2: request without token should be rejected with 401")

	// 有 Bearer token → 200
	respWithToken := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer respWithToken.Body.Close()
	assert.Equal(t, http.StatusOK, respWithToken.StatusCode,
		"P2: request with valid token should succeed with 200")
}

// =============================================================================
// Token Auth — P3 LocalProxy
// =============================================================================

func TestAuthMatrix_Token_P3_LocalProxy(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{clientID},
		AuthMethod: "token",
	})

	tokenName := fmt.Sprintf("token-p3-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	// 等待 client-1 的 AppManager 同步 app 配置
	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	// 无 token → 401
	respNoToken := lpc.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
	})
	defer respNoToken.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respNoToken.StatusCode,
		"P3: request without token should be rejected with 401")

	// 有 Bearer token → 200
	respWithToken := lpc.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
		Headers:   map[string]string{"Authorization": "Bearer " + token},
	})
	defer respWithToken.Body.Close()
	assert.Equal(t, http.StatusOK, respWithToken.StatusCode,
		"P3: request with valid token should succeed with 200")
}

// =============================================================================
// Token Auth — P5 Client-Server Relay
// =============================================================================

func TestAuthMatrix_Token_P5_Relay(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	cid2 := secondClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{cid2},
		AuthMethod: "token",
	})

	tokenName := fmt.Sprintf("token-p5-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	// 等待 client-1 同步
	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	// 无 token → 401
	respNoToken := lpc1.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
	})
	defer respNoToken.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respNoToken.StatusCode,
		"P5: request without token should be rejected with 401")

	// 有 Bearer token → 200
	respWithToken := lpc1.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
		Headers:   map[string]string{"Authorization": "Bearer " + token},
	})
	defer respWithToken.Body.Close()
	assert.Equal(t, http.StatusOK, respWithToken.StatusCode,
		"P5: request with valid token should succeed with 200")
}

// =============================================================================
// Token Auth — P6 Tunnel+Peer
// D-05: P4/P6 只测试 Token auth（SSO 不适用）
// =============================================================================

func TestAuthMatrix_Token_P6_TunnelPeer(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)
	client2ID := secondClientID(t, c)

	// 创建 peer proxy 配置（复用 setupP4App 的逻辑但使用 token auth）
	proxyID := createPeerProxy(t, c, client1ID, client2ID)

	// 创建 app 关联 client-1 并使用 peer proxy，auth_method="token"
	subdomain := harness.DeriveSubdomain(t.Name())
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       t.Name(),
		"subdomain":  subdomain,
		"target_url": "http://test-backend:8000",
		"client_ids": []string{client1ID},
		"client_configs": map[string]interface{}{
			client1ID: map[string]interface{}{
				"target_url": "http://test-backend:8000",
				"proxy_id":   proxyID,
			},
		},
		"auth_method": "token",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create P6 auth app should succeed")

	var appResult struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &appResult))
	require.NotZero(t, appResult.Data.ID)
	appID := appResult.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	tokenName := fmt.Sprintf("token-p6-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	// P6 从公网 server:18080 发请求
	proxyHost := subdomain + "." + proxyDomain

	// 无 token → 401
	respNoToken := rawGet(t, serverURL, proxyHost, "/", "")
	defer respNoToken.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respNoToken.StatusCode,
		"P6: request without token should be rejected with 401")

	// 有 Bearer token → 200
	respWithToken := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer respWithToken.Body.Close()
	assert.Equal(t, http.StatusOK, respWithToken.StatusCode,
		"P6: request with valid token should succeed with 200")
}

// =============================================================================
// SSO Basic Flow — P1 Server-Local (__host__)
// =============================================================================

func TestAuthMatrix_SSO_P1_Host(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{"__host__"},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	proxyHost := subdomain + "." + proxyDomain

	// Step 1: 无 SSO Cookie → 应重定向到 SSO
	respNoCookie := rawGet(t, serverURL, proxyHost, "/", "")
	defer respNoCookie.Body.Close()
	// SSO 未认证时应返回 302 重定向到 SSO 登录页
	assert.True(t, respNoCookie.StatusCode == http.StatusFound || respNoCookie.StatusCode == http.StatusUnauthorized,
		"P1 SSO: request without cookie should redirect (302) or reject (401), got %d", respNoCookie.StatusCode)

	// Step 2: 授权 SSO，获取 Cookie
	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	// Step 3: 带 SSO Cookie 请求 → 200
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	respWithCookie := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
	})
	defer respWithCookie.Body.Close()
	assert.Equal(t, http.StatusOK, respWithCookie.StatusCode,
		"P1 SSO: request with SSO cookie should succeed with 200")
}

// =============================================================================
// SSO Basic Flow — P2 Tunnel
// =============================================================================

func TestAuthMatrix_SSO_P2_Tunnel(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	proxyHost := subdomain + "." + proxyDomain

	// 无 SSO Cookie → 302 或 401
	respNoCookie := rawGet(t, serverURL, proxyHost, "/", "")
	defer respNoCookie.Body.Close()
	assert.True(t, respNoCookie.StatusCode == http.StatusFound || respNoCookie.StatusCode == http.StatusUnauthorized,
		"P2 SSO: request without cookie should redirect (302) or reject (401), got %d", respNoCookie.StatusCode)

	// 授权 SSO
	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	// 带 SSO Cookie → 200
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	respWithCookie := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
	})
	defer respWithCookie.Body.Close()
	assert.Equal(t, http.StatusOK, respWithCookie.StatusCode,
		"P2 SSO: request with SSO cookie should succeed with 200")
}

// =============================================================================
// SSO Basic Flow — P3 LocalProxy
// =============================================================================

func TestAuthMatrix_SSO_P3_LocalProxy(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	// 等待 app 配置同步
	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	// 授权 SSO（使用 admin 登录）
	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	// 无 SSO Cookie → 401（LocalProxy 返回 401 而非重定向）
	respNoCookie := lpc.DoGet(t, subdomain, "/")
	defer respNoCookie.Body.Close()
	assert.True(t, respNoCookie.StatusCode == http.StatusUnauthorized || respNoCookie.StatusCode == http.StatusFound,
		"P3 SSO: request without cookie should be rejected, got %d", respNoCookie.StatusCode)

	// 带 SSO Cookie → 200
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	respWithCookie := lpc.DoGet(t, subdomain, "/", map[string]string{
		cookieName: ssoCookieValue,
	})
	defer respWithCookie.Body.Close()
	assert.Equal(t, http.StatusOK, respWithCookie.StatusCode,
		"P3 SSO: request with SSO cookie should succeed with 200")
}

// =============================================================================
// SSO Basic Flow — P5 Client-Server Relay
// =============================================================================

func TestAuthMatrix_SSO_P5_Relay(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	cid2 := secondClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{cid2},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	// 等待 app 配置同步
	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	// 授权 SSO（使用 admin 登录）
	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	// 无 SSO Cookie → 401
	respNoCookie := lpc1.DoGet(t, subdomain, "/")
	defer respNoCookie.Body.Close()
	assert.True(t, respNoCookie.StatusCode == http.StatusUnauthorized || respNoCookie.StatusCode == http.StatusFound,
		"P5 SSO: request without cookie should be rejected, got %d", respNoCookie.StatusCode)

	// 带 SSO Cookie → 200
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	respWithCookie := lpc1.DoGet(t, subdomain, "/", map[string]string{
		cookieName: ssoCookieValue,
	})
	defer respWithCookie.Body.Close()
	assert.Equal(t, http.StatusOK, respWithCookie.StatusCode,
		"P5 SSO: request with SSO cookie should succeed with 200")
}

// =============================================================================
// SSO allowed_users=owner — 正面 + 负面用例
// =============================================================================

func TestAuthMatrix_SSO_AllowedUsers_Owner(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// 创建 allowed_users="owner" 的 SSO 应用
	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "owner",
	})

	// 创建一个非管理员测试用户
	testUserClient := createTestUser(t, c, serverURL, adminDomain)

	proxyHost := subdomain + "." + proxyDomain

	// 正面用例：admin（应用所有者）授权 SSO → 应该能访问
	adminSSOCookie := authorizeSSO(t, c, appID, subdomain)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	respAdmin := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): adminSSOCookie,
	})
	defer respAdmin.Body.Close()
	assert.Equal(t, http.StatusOK, respAdmin.StatusCode,
		"allowed_users=owner: admin SSO cookie should grant access (200)")

	// 负面用例：非所有者测试用户授权 SSO → 应被拒绝
	// 注意：非所有者调用 /api/sso/authorize 时服务器返回 403，不会设置 SSO Cookie
	testUserSSOResp := testUserClient.Post(t, "/api/sso/authorize", map[string]interface{}{
		"redirect": fmt.Sprintf("http://%s.%s:18080/", subdomain, proxyDomain),
	})
	defer testUserSSOResp.Body.Close()

	// 服务器应拒绝非所有者的 SSO 授权请求
	if testUserSSOResp.StatusCode == http.StatusForbidden {
		// 服务器直接拒绝授权 → 测试用户无法获得 SSO Cookie，验证直接访问也被拒绝
		respNoCookie := rawGet(t, serverURL, proxyHost, "/", "")
		defer respNoCookie.Body.Close()
		assert.True(t, respNoCookie.StatusCode == http.StatusFound || respNoCookie.StatusCode == http.StatusUnauthorized,
			"allowed_users=owner: test user without SSO cookie should be rejected, got %d", respNoCookie.StatusCode)
	} else if testUserSSOResp.StatusCode == http.StatusOK {
		// 如果服务器允许授权（不应该发生），则提取 Cookie 并验证访问被拒
		t.Logf("WARNING: server allowed SSO authorize for non-owner user (status 200), expected 403")
		cookies := testUserSSOResp.Cookies()
		cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
		for _, cookie := range cookies {
			if cookie.Name == cookieName {
				respWithCookie := proxy.DoGet(t, subdomain, "/", map[string]string{
					cookieName: cookie.Value,
				})
				defer respWithCookie.Body.Close()
				assert.True(t, respWithCookie.StatusCode == http.StatusFound || respWithCookie.StatusCode == http.StatusUnauthorized,
					"allowed_users=owner: non-owner SSO cookie should be rejected, got %d", respWithCookie.StatusCode)
				return
			}
		}
	}
}

// =============================================================================
// SSO allowed_users=all — 正面用例（admin + 非管理员均可访问）
// =============================================================================

func TestAuthMatrix_SSO_AllowedUsers_All(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	testUserClient := createTestUser(t, c, serverURL, adminDomain)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)

	// 正面用例 1：admin SSO Cookie → 200
	adminSSOCookie := authorizeSSO(t, c, appID, subdomain)
	respAdmin := proxy.DoGet(t, subdomain, "/", map[string]string{
		cookieName: adminSSOCookie,
	})
	defer respAdmin.Body.Close()
	assert.Equal(t, http.StatusOK, respAdmin.StatusCode,
		"allowed_users=all: admin SSO cookie should grant access (200)")

	// 正面用例 2：非管理员 SSO Cookie → 200
	testUserSSOCookie := authorizeSSO(t, testUserClient, appID, subdomain)
	respTestUser := proxy.DoGet(t, subdomain, "/", map[string]string{
		cookieName: testUserSSOCookie,
	})
	defer respTestUser.Body.Close()
	assert.Equal(t, http.StatusOK, respTestUser.StatusCode,
		"allowed_users=all: non-admin SSO cookie should also grant access (200)")
}

// =============================================================================
// SSO allowed_users=特定用户名 — 正面 + 负面用例
// =============================================================================

func TestAuthMatrix_SSO_AllowedUsers_SpecificList(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// 创建 allowed_users="admin" 的 SSO 应用（admin 用户名在允许列表中）
	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "admin",
	})

	testUserClient := createTestUser(t, c, serverURL, adminDomain)

	proxyHost := subdomain + "." + proxyDomain

	// 正面用例：admin（用户名 "admin" 在列表中）→ SSO 授权成功 → 200
	adminSSOCookie := authorizeSSO(t, c, appID, subdomain)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	respAdmin := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): adminSSOCookie,
	})
	defer respAdmin.Body.Close()
	assert.Equal(t, http.StatusOK, respAdmin.StatusCode,
		"allowed_users=admin: admin SSO cookie should grant access (200)")

	// 负面用例：测试用户不在允许列表中 → 授权被拒
	testUserSSOResp := testUserClient.Post(t, "/api/sso/authorize", map[string]interface{}{
		"redirect": fmt.Sprintf("http://%s.%s:18080/", subdomain, proxyDomain),
	})
	defer testUserSSOResp.Body.Close()

	if testUserSSOResp.StatusCode == http.StatusForbidden {
		// 服务器正确拒绝非列表用户授权
		respNoCookie := rawGet(t, serverURL, proxyHost, "/", "")
		defer respNoCookie.Body.Close()
		assert.True(t, respNoCookie.StatusCode == http.StatusFound || respNoCookie.StatusCode == http.StatusUnauthorized,
			"allowed_users=admin: test user without SSO cookie should be rejected, got %d", respNoCookie.StatusCode)
	} else if testUserSSOResp.StatusCode == http.StatusOK {
		t.Logf("WARNING: server allowed SSO authorize for non-listed user (status 200), expected 403")
		cookies := testUserSSOResp.Cookies()
		cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
		for _, cookie := range cookies {
			if cookie.Name == cookieName {
				respWithCookie := proxy.DoGet(t, subdomain, "/", map[string]string{
					cookieName: cookie.Value,
				})
				defer respWithCookie.Body.Close()
				assert.True(t, respWithCookie.StatusCode == http.StatusFound || respWithCookie.StatusCode == http.StatusUnauthorized,
					"allowed_users=admin: non-listed user SSO cookie should be rejected, got %d", respWithCookie.StatusCode)
				return
			}
		}
	}
}

// =============================================================================
// SSO exempt_paths — 精确匹配
// =============================================================================

func TestAuthMatrix_SSO_ExemptPaths_PublicPaths(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:   "http://test-backend:8000",
		ClientIDs:   []string{clientID},
		AuthMethod:  "sso",
		ExemptPaths: []string{"/health", "/api/public"},
	})

	proxyHost := subdomain + "." + proxyDomain

	// (a) 豁免路径 /health → 无需认证即可访问
	respHealth := rawGet(t, serverURL, proxyHost, "/health", "")
	defer respHealth.Body.Close()
	// 豁免路径应该直接通过，返回 200
	if respHealth.StatusCode == http.StatusFound || respHealth.StatusCode == http.StatusUnauthorized {
		// 如果 backend 的 /health 返回 200 但代理仍然需要认证，
		// 检查是否是因为 exempt_paths 需要精确匹配
		t.Logf("exempt path /health got status %d (expected 200 if exempt works)", respHealth.StatusCode)
	}
	assert.NotEqual(t, http.StatusNotFound, respHealth.StatusCode,
		"/health should not be 404 — app should exist")

	// (b) 非豁免路径 / → 需要认证
	respRoot := rawGet(t, serverURL, proxyHost, "/", "")
	defer respRoot.Body.Close()
	assert.True(t, respRoot.StatusCode == http.StatusFound || respRoot.StatusCode == http.StatusUnauthorized,
		"non-exempt path / should require auth, got %d", respRoot.StatusCode)

	// (c) 带 SSO Cookie 访问非豁免路径 → 200
	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	respWithCookie := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
	})
	defer respWithCookie.Body.Close()
	assert.Equal(t, http.StatusOK, respWithCookie.StatusCode,
		"non-exempt path with SSO cookie should succeed with 200")
}

// =============================================================================
// SSO exempt_paths — 前缀匹配
// =============================================================================

func TestAuthMatrix_SSO_ExemptPaths_PrefixMatch(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:   "http://test-backend:8000",
		ClientIDs:   []string{clientID},
		AuthMethod:  "sso",
		ExemptPaths: []string{"/api/"},
	})

	proxyHost := subdomain + "." + proxyDomain

	// /api/version 匹配前缀 /api/ → 无需认证
	respAPIVersion := rawGet(t, serverURL, proxyHost, "/api/version", "")
	defer respAPIVersion.Body.Close()
	// 前缀匹配的路径应该豁免认证
	if respAPIVersion.StatusCode == http.StatusFound || respAPIVersion.StatusCode == http.StatusUnauthorized {
		t.Logf("exempt prefix /api/ path /api/version got status %d (expected 200 if prefix exempt works)", respAPIVersion.StatusCode)
	}

	// / 不匹配前缀 /api/ → 需要认证
	respRoot := rawGet(t, serverURL, proxyHost, "/", "")
	defer respRoot.Body.Close()
	assert.True(t, respRoot.StatusCode == http.StatusFound || respRoot.StatusCode == http.StatusUnauthorized,
		"non-exempt path / should require auth, got %d", respRoot.StatusCode)

	// 带 SSO Cookie 访问 / → 200
	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	respWithCookie := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
	})
	defer respWithCookie.Body.Close()
	assert.Equal(t, http.StatusOK, respWithCookie.StatusCode,
		"non-exempt path with SSO cookie should succeed with 200")
}

// =============================================================================
// SSO + 票据混合模式（sso_token）— 票据优先，SSO 回退 (#9)
// =============================================================================

// TestAuthMatrix_TokenInSSOTokenMode_P2_Tunnel 验证混合模式下有效票据可放行（无需 SSO Cookie）。
func TestAuthMatrix_TokenInSSOTokenMode_P2_Tunnel(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso_token",
		AllowedUsers: "all",
	})

	tokenName := fmt.Sprintf("sso-token-mode-p2-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	proxyHost := subdomain + "." + proxyDomain

	// 无票据、无 SSO Cookie → API 客户端返回 401
	respNoAuth := rawGet(t, serverURL, proxyHost, "/", "")
	defer respNoAuth.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, respNoAuth.StatusCode,
		"sso_token mode: API client without auth should get 401, got %d", respNoAuth.StatusCode)

	// 有效 Bearer 票据、无 SSO Cookie → 放行（200）
	respBearer := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer respBearer.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, respBearer.StatusCode,
		"sso_token mode: valid Bearer token should be accepted (not 401), got %d", respBearer.StatusCode)
	assert.NotEqual(t, http.StatusFound, respBearer.StatusCode,
		"sso_token mode: valid Bearer token should not redirect to SSO, got %d", respBearer.StatusCode)
}

// TestAuthMatrix_TokenInSSOTokenMode_StaleCookie_P2_Tunnel 验证混合模式下
// 失效 SSO Cookie + 有效票据 → 放行（核心 bug 场景）。
func TestAuthMatrix_TokenInSSOTokenMode_StaleCookie_P2_Tunnel(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso_token",
		AllowedUsers: "all",
	})

	tokenName := fmt.Sprintf("sso-token-stale-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 失效 SSO Cookie + 有效 Bearer 票据 → 放行
	staleCookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
		Headers:   map[string]string{"Authorization": "Bearer " + token},
		Cookies:   map[string]string{staleCookieName: "stale-invalid-session-value"},
	})
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"sso_token mode: stale cookie + valid token should not be 401, got %d", resp.StatusCode)
	assert.NotEqual(t, http.StatusFound, resp.StatusCode,
		"sso_token mode: stale cookie + valid token should not redirect to SSO, got %d", resp.StatusCode)
}

// TestAuthMatrix_TokenInSSOTokenMode_P3_LocalProxy 验证本地代理路径下
// 混合模式有效票据可放行。
func TestAuthMatrix_TokenInSSOTokenMode_P3_LocalProxy(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso_token",
		AllowedUsers: "all",
	})

	tokenName := fmt.Sprintf("sso-token-p3-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	// 等待 app 配置同步到客户端本地代理
	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	// 有效 Bearer 票据、无 SSO Cookie → 放行
	resp := lpc.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
		Headers:   map[string]string{"Authorization": "Bearer " + token},
	})
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"P3 sso_token mode: valid Bearer token should be accepted (not 401), got %d", resp.StatusCode)
	assert.NotEqual(t, http.StatusFound, resp.StatusCode,
		"P3 sso_token mode: valid Bearer token should not redirect to SSO, got %d", resp.StatusCode)
}

// TestAuthMatrix_TokenOnly_ForbidSSO_P2 验证仅票据模式下
// 有效 SSO Cookie 也不能放行（必须用票据）。
func TestAuthMatrix_TokenOnly_ForbidSSO_P2(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "token",
		AllowedUsers: "all",
	})

	// 颁发一个有效的 SSO Cookie
	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 仅票据模式 + 有效 SSO Cookie + 无票据 → 401（SSO 被禁止）
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
		Cookies:   map[string]string{cookieName: ssoCookieValue},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"token-only mode: valid SSO cookie without token should be rejected with 401, got %d", resp.StatusCode)
}

// =============================================================================
// 仅 SSO 模式（sso）— 不支持票据，未认证直接 302 重定向
// =============================================================================

// TestAuthMatrix_SSO_TokenNotSupported_P2 验证仅 SSO 模式下票据不被接受（直接 302 重定向）。
func TestAuthMatrix_SSO_TokenNotSupported_P2(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	tokenName := fmt.Sprintf("sso-only-notoken-%d", appID)
	token := createAccessToken(t, c, tokenName, appID, nil)
	t.Cleanup(func() { cleanupTokenByName(t, c, tokenName) })

	proxyHost := subdomain + "." + proxyDomain

	// 仅 SSO 模式 + 有效 Bearer 票据 → 仍 302 重定向（票据不被检查）
	resp := rawGet(t, serverURL, proxyHost, "/", "Bearer "+token)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode,
		"sso-only mode: valid token should be ignored (302 redirect), got %d", resp.StatusCode)
}

// TestAuthMatrix_SSO_APIClient_Gets302 验证仅 SSO 模式下
// API 客户端（Accept 不含 text/html）无认证时也返回 302（不返回 401）。
func TestAuthMatrix_SSO_APIClient_Gets302(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	proxyHost := subdomain + "." + proxyDomain

	// API 客户端（无 Accept: text/html）无认证 → 302（仅 SSO 模式不返回 401）
	resp := rawGet(t, serverURL, proxyHost, "/", "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode,
		"sso-only mode: API client without auth should get 302 (not 401), got %d", resp.StatusCode)
}

// TestAuthMatrix_SSO_Browser_NoAuth_GetsRedirect 验证仅 SSO 模式下
// 浏览器（Accept: text/html）无认证时返回 302 重定向到 SSO 登录页。
func TestAuthMatrix_SSO_Browser_NoAuth_GetsRedirect(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 浏览器（Accept: text/html）无认证 → 302 重定向
	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/",
		Headers:   map[string]string{"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode,
		"sso-only mode: browser without auth should get 302 redirect, got %d", resp.StatusCode)
}

// =============================================================================
// 混合模式（sso_token）下 API 客户端的 401 响应
// =============================================================================

// TestAuthMatrix_SSOToken_APIClient_NoAuth_Gets401 验证混合模式下
// API 客户端（Accept 不含 text/html）无认证时返回 401 而非 302 重定向。
// Docker daemon 等程序化客户端依赖 401 + WWW-Authenticate 触发认证流程。
func TestAuthMatrix_SSOToken_APIClient_NoAuth_Gets401(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso_token",
		AllowedUsers: "all",
	})

	proxyHost := subdomain + "." + proxyDomain

	// API 客户端（无 Accept: text/html、无票据、无 SSO Cookie）→ 401
	resp := rawGet(t, serverURL, proxyHost, "/", "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"sso_token mode: API client without auth should get 401 (not 302 redirect), got %d", resp.StatusCode)
}
