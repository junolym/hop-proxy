package suites

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// TestWildcardCookieIsolation 验证通配符应用的 SSO cookie 跨子域名隔离 (#40)。
//
// 同一通配符模式（如 *-wci）匹配的两个具体子域名（a-wci / b-wci）各自 SSO 授权后，
// 浏览器会同时把两个 cookie 发到任一子域名（Domain=.proxy_domain），
// 但 server 按 cookie name 中的子域名后缀精确匹配当前 host，
// a-wci 的 cookie 不能访问 b-wci，反之亦然。
func TestWildcardCookieIsolation(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	appID, _ := createFuzzySSOApp(t, c, "*-wci")

	subA := "a-wci"
	subB := "b-wci"

	// 各自 SSO 授权
	cookieA := authorizeSSO(t, c, appID, subA)
	require.NotEmpty(t, cookieA, "a-wci SSO cookie should be set")
	cookieB := authorizeSSO(t, c, appID, subB)
	require.NotEmpty(t, cookieB, "b-wci SSO cookie should be set")

	// 验证 cookie name 含子域名后缀
	cookieNameA := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subA)
	cookieNameB := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subB)
	assert.NotEqual(t, cookieNameA, cookieNameB,
		"两个子域名的 cookie name 必须不同（含子域名后缀）")

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 正向：a-wci 的 cookie 访问 a-wci → 200
	respA := proxy.DoGet(t, subA, "/", map[string]string{
		cookieNameA: cookieA,
	})
	defer respA.Body.Close()
	assert.Equal(t, http.StatusOK, respA.StatusCode,
		"a-wci cookie 访问 a-wci 应放行（200）")

	// 正向：b-wci 的 cookie 访问 b-wci → 200
	respB := proxy.DoGet(t, subB, "/", map[string]string{
		cookieNameB: cookieB,
	})
	defer respB.Body.Close()
	assert.Equal(t, http.StatusOK, respB.StatusCode,
		"b-wci cookie 访问 b-wci 应放行（200）")

	// 反向：a-wci 的 cookie 访问 b-wci → 非 200（被重定向到 SSO 登录）
	// 浏览器会同时发送 a-wci 和 b-wci 的 cookie，但 server 按 b-wci 的 cookie name 读取，
	// a-wci 的 cookie name 在 b-wci 请求中匹配不上 → 读不到 → 重定向 SSO
	respCrossAB := proxy.DoGet(t, subB, "/", map[string]string{
		cookieNameA: cookieA, // 故意只带 a 的 cookie 访问 b
	})
	defer respCrossAB.Body.Close()
	assert.True(t, respCrossAB.StatusCode == http.StatusFound || respCrossAB.StatusCode == http.StatusUnauthorized,
		"a-wci cookie 访问 b-wci 应被拒绝（302 重定向 SSO 或 401），got: %d", respCrossAB.StatusCode)

	// 反向：b-wci 的 cookie 访问 a-wci → 非 200
	respCrossBA := proxy.DoGet(t, subA, "/", map[string]string{
		cookieNameB: cookieB, // 故意只带 b 的 cookie 访问 a
	})
	defer respCrossBA.Body.Close()
	assert.True(t, respCrossBA.StatusCode == http.StatusFound || respCrossBA.StatusCode == http.StatusUnauthorized,
		"b-wci cookie 访问 a-wci 应被拒绝（302 重定向 SSO 或 401），got: %d", respCrossBA.StatusCode)

	// 混合：同时带两个 cookie 访问 a-wci → 200（server 按 a-wci name 读到 a 的 cookie）
	respBothA := proxy.DoGet(t, subA, "/", map[string]string{
		cookieNameA: cookieA,
		cookieNameB: cookieB,
	})
	defer respBothA.Body.Close()
	assert.Equal(t, http.StatusOK, respBothA.StatusCode,
		"同时带两个 cookie 访问 a-wci 应放行（server 按 a-wci name 读到 a 的 cookie）")

	// 混合：同时带两个 cookie 访问 b-wci → 200（server 按 b-wci name 读到 b 的 cookie）
	respBothB := proxy.DoGet(t, subB, "/", map[string]string{
		cookieNameA: cookieA,
		cookieNameB: cookieB,
	})
	defer respBothB.Body.Close()
	assert.Equal(t, http.StatusOK, respBothB.StatusCode,
		"同时带两个 cookie 访问 b-wci 应放行（server 按 b-wci name 读到 b 的 cookie）")
}
