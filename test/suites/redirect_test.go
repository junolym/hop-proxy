package suites

import (
	"net/http"
	"testing"

	"github.com/robin/hop-proxy/test/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppRedirects 验证应用跳转路径规则：精确匹配优先于正则、正则捕获组替换、
// 未命中走正常代理、状态码 301/302 正确。
func TestAppRedirects(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)

	appSubdomain := "redirect-test"
	appID := createTestApp(t, c, appSubdomain, "none")
	t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

	proxyHost := appSubdomain + "." + proxyDomain

	// 精确规则：/old → /new（302 临时）
	resp := c.Post(t, "/api/apps/"+itoa(appID)+"/redirects", map[string]interface{}{
		"match_type":      "exact",
		"match_path":      "/old",
		"redirect_target": "/new",
		"status_code":     302,
		"priority":        0,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "创建精确跳转规则失败")

	// 正则规则：^/api/(.*)$ → /v2/api/$1（301 永久）
	resp2 := c.Post(t, "/api/apps/"+itoa(appID)+"/redirects", map[string]interface{}{
		"match_type":      "regex",
		"match_path":      `^/api/(.*)$`,
		"redirect_target": "/v2/api/$1",
		"status_code":     301,
		"priority":        0,
	})
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode, "创建正则跳转规则失败")

	// 1. 精确命中：/old → 302 Location: /new
	r := rawGet(t, serverURL, proxyHost, "/old", "")
	defer r.Body.Close()
	assert.Equal(t, http.StatusFound, r.StatusCode, "/old 应 302 跳转")
	assert.Equal(t, "/new", r.Header.Get("Location"), "/old 跳转目标应为 /new")

	// 2. 正则命中：/api/users → 301 Location: /v2/api/users
	r2 := rawGet(t, serverURL, proxyHost, "/api/users", "")
	defer r2.Body.Close()
	assert.Equal(t, http.StatusMovedPermanently, r2.StatusCode, "/api/users 应 301 跳转")
	assert.Equal(t, "/v2/api/users", r2.Header.Get("Location"), "正则捕获组替换应得到 /v2/api/users")

	// 3. 未命中：/anything 走正常代理（test-backend echo 返回 200）
	r3 := rawGet(t, serverURL, proxyHost, "/anything", "")
	defer r3.Body.Close()
	assert.NotEqual(t, http.StatusFound, r3.StatusCode, "/anything 不应 302")
	assert.NotEqual(t, http.StatusMovedPermanently, r3.StatusCode, "/anything 不应 301")

	// 4. 站外跳转：精确 /ext → https://example.com
	c.Post(t, "/api/apps/"+itoa(appID)+"/redirects", map[string]interface{}{
		"match_type":          "exact",
		"match_path":          "/ext",
		"match_include_query": false,
		"redirect_target":     "https://example.com/path",
		"status_code":         302,
		"priority":            0,
	}).Body.Close()
	r4 := rawGet(t, serverURL, proxyHost, "/ext", "")
	defer r4.Body.Close()
	assert.Equal(t, http.StatusFound, r4.StatusCode)
	assert.Equal(t, "https://example.com/path", r4.Header.Get("Location"))

	// 5. match_include_query=true 防死循环：/ → /?token=xxx
	//    第一次 GET /（无 query）命中跳转到 /?token=xxx；
	//    第二次 GET /?token=xxx（含 query）不再命中，走正常代理（test-backend echo 200）。
	c.Post(t, "/api/apps/"+itoa(appID)+"/redirects", map[string]interface{}{
		"match_type":          "exact",
		"match_path":          "/",
		"match_include_query": true,
		"redirect_target":     "/?token=xxx",
		"status_code":         302,
		"priority":            0,
	}).Body.Close()

	// 5.1 GET /（无 query）应 302 跳转到 /?token=xxx
	r5 := rawGet(t, serverURL, proxyHost, "/", "")
	defer r5.Body.Close()
	assert.Equal(t, http.StatusFound, r5.StatusCode, "match_include_query=true 时 GET / 应 302 跳转")
	assert.Equal(t, "/?token=xxx", r5.Header.Get("Location"), "跳转目标应为 /?token=xxx")

	// 5.2 GET /?token=xxx（含 query）不应跳转，走正常代理
	r6 := rawGet(t, serverURL, proxyHost, "/?token=xxx", "")
	defer r6.Body.Close()
	assert.NotEqual(t, http.StatusFound, r6.StatusCode, "含 query 时不应再 302（避免死循环）")
	assert.NotEqual(t, http.StatusMovedPermanently, r6.StatusCode, "含 query 时不应再 301（避免死循环）")
}

// itoa 是 int64→string 的本地辅助（避免引入 strconv）
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
