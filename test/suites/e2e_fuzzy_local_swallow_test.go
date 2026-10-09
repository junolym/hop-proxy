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
// #57 复现：本地代理模糊子域名兜底吞并其他客户端的精确子域名应用
//
// 服务端解析（GetAppClientsWithTarget）是全局精确优先：只要全局存在精确
// 子域名应用，就永远不会落到模糊模式。而客户端本地代理只看自己的应用列表：
// 精确未命中时，本地模糊模式（如 `*`）会兜底吞并一切子域名——包括挂在
// 其他客户端上的精确子域名应用。两端解析结果不一致导致：
//   1. 误路由：请求被本地模糊应用接管，发给错误的目标
//   2. SSO 死循环：本地按模糊应用的 appID 找 cookie（hopproxy_sso_{wid}_sub），
//      服务端 /sso 按精确应用的 appID 种 cookie（hopproxy_sso_{eid}_sub），
//      永远对不上 → 302 → /sso → 302 → 无限重定向
// =============================================================================

// createSwallowApp 创建复现用应用：允许指定 subdomain（精确或模糊模式）、
// 归属客户端、认证方式。先清理同 subdomain 残留，注册 t.Cleanup 删除。
func createSwallowApp(t *testing.T, c *harness.Client, subdomain, clientID, authMethod, allowedUsers string, customHeaders map[string]string) int64 {
	t.Helper()

	// 清理同 subdomain 残留应用
	resp := c.Get(t, "/api/apps")
	defer resp.Body.Close()
	var list listAppsResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &list))
	for _, app := range list.Data {
		if app.Subdomain == subdomain {
			c.Delete(t, fmt.Sprintf("/api/apps/%d", app.ID))
		}
	}

	body := map[string]interface{}{
		"name":           "swallow-" + subdomain,
		"subdomain":      subdomain,
		"target_url":     "http://test-backend:8000",
		"client_ids":     []string{clientID},
		"auth_method":    authMethod,
		"allowed_users":  allowedUsers,
		"custom_headers": customHeaders,
	}
	resp2 := c.Post(t, "/api/apps", body)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		var errRes struct {
			Error string `json:"error"`
		}
		harness.DecodeJSON(resp2.Body, &errRes)
		t.Fatalf("create swallow app %q failed (status %d): %s", subdomain, resp2.StatusCode, errRes.Error)
	}

	var result singleAppResponse
	require.NoError(t, harness.DecodeJSON(resp2.Body, &result))
	appID := result.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })
	return appID
}

// decodeMarkerHeader 解析 /headers 响应中的指定 header，用于区分请求最终由哪个 app 服务
func decodeMarkerHeader(t *testing.T, resp *http.Response, header string) string {
	t.Helper()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status from /headers")
	var hdrs struct {
		Headers map[string]string `json:"headers"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &hdrs))
	return hdrs.Headers[header]
}

// TestFuzzyLocalSwallow_Routing 验证误路由：
// client-1 挂模糊应用 swallow-*，client-2 挂精确应用 swallow-exact。
// 经 client-1 本地代理访问 swallow-exact 时，应转发服务端由精确应用（client-2）服务，
// 而不是被本地模糊应用吞并。
func TestFuzzyLocalSwallow_Routing(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	cid1 := firstClientID(t, c)
	cid2 := secondClientID(t, c)

	// 模糊应用挂 client-1（marker 区分最终服务方）
	createSwallowApp(t, c, "swallow-*", cid1, "none", "all",
		map[string]string{"X-Marker": "wildcard-local"})
	// 精确应用挂 client-2，子域名落在模糊模式覆盖范围内
	createSwallowApp(t, c, "swallow-exact", cid2, "none", "all",
		map[string]string{"X-Marker": "exact-remote"})

	// 等待 app 变更通知 → client-1 刷新本地应用列表
	// （本地命中与转发服务端的响应无法区分，只能等同步完成后再断言）
	time.Sleep(3 * time.Second)
	lpc1 := localProxyClient1()

	// sanity：公网路径由服务端解析，全局精确优先 → exact-remote
	respPub := rawGet(t, serverURL, "swallow-exact."+proxyDomain, "/headers", "")
	assert.Equal(t, "exact-remote", decodeMarkerHeader(t, respPub, "X-Marker"),
		"公网路径应命中精确应用（服务端全局精确优先）")

	// 核心断言：经 client-1 本地代理访问精确应用子域名，
	// 应转发服务端（Path 5）由 client-2 上的精确应用服务
	respLoc := lpc1.DoGet(t, "swallow-exact", "/headers")
	assert.Equal(t, "exact-remote", decodeMarkerHeader(t, respLoc, "X-Marker"),
		"本地代理访问其他客户端的精确子域名应用应转发服务端解析，而非被本地模糊应用吞并")

	// 反向 sanity：无精确应用的纯模糊子域名仍应本地命中（Path 3）
	respWild := lpc1.DoGet(t, "swallow-probe", "/headers")
	assert.Equal(t, "wildcard-local", decodeMarkerHeader(t, respWild, "X-Marker"),
		"无精确应用时本地模糊模式应正常命中（Path 3 直连）")
}

// TestFuzzyLocalSwallow_FuzzyPriorityAcrossClients 验证跨客户端模糊优先级：
// client-1 挂宽模式 swallowfz-*（lit=10），client-2 挂更具体模式 swallowfz-a-*（lit=13）。
// 服务端全局排序会选更具体的 swallowfz-a-*；client-1 本地代理必须同步让位转发，
// 而不是用本地宽模式吞并。
func TestFuzzyLocalSwallow_FuzzyPriorityAcrossClients(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	cid1 := firstClientID(t, c)
	cid2 := secondClientID(t, c)

	// 宽模式挂 client-1，更具体的模式挂 client-2
	createSwallowApp(t, c, "swallowfz-*", cid1, "none", "all",
		map[string]string{"X-Marker": "wide-local"})
	createSwallowApp(t, c, "swallowfz-a-*", cid2, "none", "all",
		map[string]string{"X-Marker": "specific-remote"})

	time.Sleep(3 * time.Second)
	lpc1 := localProxyClient1()

	// swallowfz-a-foo 同时命中两个模式，全局应选更具体的 swallowfz-a-*
	resp := lpc1.DoGet(t, "swallowfz-a-foo", "/headers")
	assert.Equal(t, "specific-remote", decodeMarkerHeader(t, resp, "X-Marker"),
		"跨客户端模糊优先级应与服务端一致：更具体的模式（其他客户端）赢，本地宽模式让位转发")

	// swallowfz-b-foo 只命中宽模式 → 本地处理
	resp2 := lpc1.DoGet(t, "swallowfz-b-foo", "/headers")
	assert.Equal(t, "wide-local", decodeMarkerHeader(t, resp2, "X-Marker"),
		"仅命中本地宽模式时应本地处理（Path 3）")
}

// TestFuzzyLocalSwallow_SSO_InfiniteRedirect 验证 SSO 无限重定向（#57 原始现象）：
// client-1 挂模糊应用（sso 认证），client-2 挂精确应用（sso 认证）。
// 浏览器持有服务端为精确应用签发的 SSO cookie（hopproxy_sso_{exactID}_{sub}），
// 经 client-1 本地代理访问时应 200（Path 5 服务端鉴权）。
// 修复前：本地被模糊应用吞并，按模糊应用 appID 找 cookie 永远找不到 → 302 死循环。
func TestFuzzyLocalSwallow_SSO_InfiniteRedirect(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	cid1 := firstClientID(t, c)
	cid2 := secondClientID(t, c)

	// 模糊应用挂 client-1，sso 认证（复刻生产：nas 上的 * 应用）
	createSwallowApp(t, c, "swallowsso-*", cid1, "sso", "all", nil)
	// 精确应用挂 client-2，sso 认证（复刻生产：lab 上的 lab-8080）
	exactID := createSwallowApp(t, c, "swallowsso-app", cid2, "sso", "all", nil)

	// 等待 app 变更通知 → client-1 刷新本地应用列表
	time.Sleep(3 * time.Second)
	lpc1 := localProxyClient1()

	// 服务端为精确应用签发 SSO cookie（等价于用户走公网 /sso 授权后拿到的 cookie）
	exactSub := "swallowsso-app"
	ssoCookieValue := authorizeSSO(t, c, exactID, exactSub)
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", exactID, exactSub)

	// 无 cookie → 302/401（正常：服务端要求认证）
	respNoCookie := lpc1.DoGet(t, exactSub, "/")
	defer respNoCookie.Body.Close()
	assert.True(t, respNoCookie.StatusCode == http.StatusFound || respNoCookie.StatusCode == http.StatusUnauthorized,
		"无 cookie 应被要求认证，got %d", respNoCookie.StatusCode)

	// 核心断言：带精确应用的 SSO cookie 经本地代理访问 → 200
	respWithCookie := lpc1.DoGet(t, exactSub, "/", map[string]string{
		cookieName: ssoCookieValue,
	})
	defer respWithCookie.Body.Close()
	require.NotEqual(t, http.StatusFound, respWithCookie.StatusCode,
		"带有效 SSO cookie 不应被重定向（模糊吞并导致的 302 死循环，#57）")
	require.Equal(t, http.StatusOK, respWithCookie.StatusCode,
		"带精确应用的 SSO cookie 经本地代理应 200（Path 5 服务端鉴权放行）")
}
