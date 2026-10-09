package suites

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// Cookie 隔离测试 — HopProxy 内部 Cookie 不应转发给后端应用 (#31)
//
// 验证 hopproxy_* 前缀的 Cookie（SSO 会话、管理会话、CSRF、TOTP 等）在代理转发时
// 被剥离，不会泄漏到后端。同时验证非 hopproxy_* 的业务 Cookie 仍正常透传。
// =============================================================================

// cookieEchoResponse 是 test-backend /cookies 端点的响应结构
type cookieEchoResponse struct {
	Status  string            `json:"status"`
	Cookies map[string]string `json:"cookies"`
}

// fetchBackendCookies 通过代理访问 /cookies 端点，返回后端实际收到的 Cookie map。
// 对 503（隧道短暂离线，常见于前置测试重启客户端后）自动重试。
func fetchBackendCookies(t *testing.T, proxy *harness.ProxyClient, subdomain string, cookies map[string]string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var resp *http.Response
	for {
		resp = proxy.Do(t, harness.ProxyRequest{
			Method:    http.MethodGet,
			Subdomain: subdomain,
			Path:      "/cookies",
			Cookies:   cookies,
		})
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			break
		}
		resp.Body.Close()
		time.Sleep(500 * time.Millisecond)
	}
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "proxy /cookies should return 200")

	var result cookieEchoResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &result), "decode /cookies response")
	return result.Cookies
}

// assertNoHopProxyCookies 断言后端收到的 Cookie 中不包含任何 hopproxy_* 前缀的 Cookie
func assertNoHopProxyCookies(t *testing.T, backendCookies map[string]string) {
	t.Helper()
	for name := range backendCookies {
		assert.False(t, strings.HasPrefix(strings.ToLower(name), "hopproxy_"),
			"后端不应收到 HopProxy 内部 Cookie，发现 %q = %q", name, backendCookies[name])
	}
}

// =============================================================================
// P1（Server-Local __host__）— SSO 模式
// =============================================================================

func TestCookieStrip_P1_Host_SSO(t *testing.T) {
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

	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	backendCookies := fetchBackendCookies(t, proxy, subdomain, map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
		"hopproxy_session":      "fake-admin-session",
		"hopproxy_csrf":         "fake-csrf-token",
		"hopproxy_totp_pending": "fake-totp",
		"myapp_session":         "user-business-session",
	})

	assertNoHopProxyCookies(t, backendCookies)
	assert.Equal(t, "user-business-session", backendCookies["myapp_session"],
		"业务 Cookie myapp_session 应正常透传到后端")
}

// =============================================================================
// P2（Tunnel）— SSO 模式
// =============================================================================

func TestCookieStrip_P2_Tunnel_SSO(t *testing.T) {
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

	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	backendCookies := fetchBackendCookies(t, proxy, subdomain, map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
		"hopproxy_session": "fake-admin-session",
		"hopproxy_csrf":    "fake-csrf-token",
		"app_token":        "business-token-xyz",
	})

	assertNoHopProxyCookies(t, backendCookies)
	assert.Equal(t, "business-token-xyz", backendCookies["app_token"],
		"业务 Cookie app_token 应正常透传到后端")
}

// =============================================================================
// P3（LocalProxy）— SSO 模式
// =============================================================================

func TestCookieStrip_P3_LocalProxy_SSO(t *testing.T) {
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

	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	backendCookies := fetchBackendCookies(t, lpc, subdomain, map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
		"hopproxy_session": "fake-admin-session",
		"hopproxy_csrf":    "fake-csrf-token",
		"customer_pref":    "dark-mode",
	})

	assertNoHopProxyCookies(t, backendCookies)
	assert.Equal(t, "dark-mode", backendCookies["customer_pref"],
		"业务 Cookie customer_pref 应正常透传到后端")
}

// =============================================================================
// P1（Server-Local __host__）— auth_method=none，验证无认证场景也剥离内部 Cookie
// =============================================================================

func TestCookieStrip_P1_Host_NoAuth(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	_, subdomain := harness.SetupHostApp(t, c, serverURL, adminDomain, "http://test-backend:8000")

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	// 即使应用无认证，浏览器可能携带其他应用的 hopproxy_* Cookie
	backendCookies := fetchBackendCookies(t, proxy, subdomain, map[string]string{
		"hopproxy_session":      "leaked-admin-session",
		"hopproxy_csrf":         "leaked-csrf",
		"hopproxy_sso_999":      "leaked-other-app-sso",
		"hopproxy_totp_pending": "leaked-totp",
		"normal_cookie":         "should-pass-through",
	})

	assertNoHopProxyCookies(t, backendCookies)
	assert.Equal(t, "should-pass-through", backendCookies["normal_cookie"],
		"业务 Cookie normal_cookie 应正常透传到后端")
}

// =============================================================================
// P5（Client-Server Relay）— SSO 模式，验证 relay 路径也剥离 Cookie
// =============================================================================

func TestCookieStrip_P5_Relay_SSO(t *testing.T) {
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

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	ssoCookieValue := authorizeSSO(t, c, appID, subdomain)

	// P5 从 client-1 本地代理发起，但应用关联的是 client-2，会走 relay 到 server 再到 client-2
	backendCookies := fetchBackendCookies(t, lpc1, subdomain, map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookieValue,
		"hopproxy_session": "fake-admin-session",
		"hopproxy_csrf":    "fake-csrf-token",
		"relay_data":       "passthrough-value",
	})

	assertNoHopProxyCookies(t, backendCookies)
	assert.Equal(t, "passthrough-value", backendCookies["relay_data"],
		"业务 Cookie relay_data 应正常透传到后端")
}

// =============================================================================
// 边界场景：只有 hopproxy_* Cookie 时，Cookie 头应被完全移除
// =============================================================================

func TestCookieStrip_OnlyHopProxyCookies(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	_, subdomain := harness.SetupHostApp(t, c, serverURL, adminDomain, "http://test-backend:8000")

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	// 只发 hopproxy_* Cookie，后端应收到空 Cookie
	backendCookies := fetchBackendCookies(t, proxy, subdomain, map[string]string{
		"hopproxy_session": "only-admin-session",
		"hopproxy_csrf":    "only-csrf",
	})

	assert.Empty(t, backendCookies, "只有 hopproxy_* Cookie 时，后端不应收到任何 Cookie")
}
