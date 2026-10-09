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
// 分享码 CRUD 测试
// =============================================================================

// shareCodeResponse 镜像分享码 API 响应
type shareCodeResponse struct {
	ID                int64  `json:"id"`
	UserID            int64  `json:"user_id"`
	AppID             int64  `json:"app_id"`
	Code              string `json:"code"`
	MaxUses           int    `json:"max_uses"`
	UseCount          int    `json:"use_count"`
	ExpiresAt         string `json:"expires_at"`
	CookieTTL         int    `json:"cookie_ttl"`
	RedirectPath      string `json:"redirect_path"`
	ConcreteSubdomain string `json:"concrete_subdomain"`
	Enabled           bool   `json:"enabled"`
	AppName           string `json:"app_name"`
	Subdomain         string `json:"subdomain"`
}

// createShareCodeForTest 创建测试用 SSO 应用 + 分享码，自动清理
func createShareCodeForTest(t *testing.T, c *harness.Client, maxUses int) (appID int64, subdomain string, code string) {
	t.Helper()

	// 创建 SSO 认证应用
	appID, subdomain = harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	// 创建分享码
	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":          appID,
		"max_uses":        maxUses,
		"cookie_ttl":      86400,
		"expires_in_secs": 3600,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create share code should succeed")

	var result struct {
		Data shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	code = result.Data.Code
	require.NotEmpty(t, code, "share code should not be empty")
	assert.Equal(t, maxUses, result.Data.MaxUses)
	assert.Equal(t, 0, result.Data.UseCount)

	// 注册清理
	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/share-codes/%d", result.Data.ID))
	})

	return appID, subdomain, code
}

func TestShareCodes_Create(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	appID, _, _ := createShareCodeForTest(t, c, 5)
	assert.Greater(t, appID, int64(0))
}

func TestShareCodes_List(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	_, _, _ = createShareCodeForTest(t, c, 3)

	resp := c.Get(t, "/api/share-codes")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	assert.NotEmpty(t, result.Data, "share codes list should not be empty")
}

func TestShareCodes_Update(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 创建分享码
	appID, _, code := createShareCodeForTest(t, c, 3)

	// 查找创建的分享码 ID
	listResp := c.Get(t, "/api/share-codes")
	defer listResp.Body.Close()
	var listResult struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	var scID int64
	for _, sc := range listResult.Data {
		if sc.Code == code {
			scID = sc.ID
			break
		}
	}
	require.NotZero(t, scID, "created share code not found in list")

	// 更新：修改 max_uses 并禁用
	resp := c.Put(t, fmt.Sprintf("/api/share-codes/%d", scID), map[string]interface{}{
		"max_uses": 10,
		"enabled":  false,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// 验证更新
	listResp2 := c.Get(t, "/api/share-codes")
	defer listResp2.Body.Close()
	var listResult2 struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp2.Body, &listResult2))

	for _, sc := range listResult2.Data {
		if sc.ID == scID {
			assert.Equal(t, 10, sc.MaxUses, "max_uses should be updated")
			assert.False(t, sc.Enabled, "should be disabled")
			return
		}
	}
	t.Fatal("share code not found after update")
	_ = appID
}

func TestShareCodes_Delete(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 创建分享码
	_, _, code := createShareCodeForTest(t, c, 3)

	// 查找 ID
	listResp := c.Get(t, "/api/share-codes")
	defer listResp.Body.Close()
	var listResult struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	var scID int64
	for _, sc := range listResult.Data {
		if sc.Code == code {
			scID = sc.ID
			break
		}
	}
	require.NotZero(t, scID)

	// 删除
	resp := c.Delete(t, fmt.Sprintf("/api/share-codes/%d", scID))
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// 验证列表中不再有该分享码
	listResp2 := c.Get(t, "/api/share-codes")
	defer listResp2.Body.Close()
	var listResult2 struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp2.Body, &listResult2))
	for _, sc := range listResult2.Data {
		assert.NotEqual(t, code, sc.Code, "deleted share code should not appear in list")
	}
}

func TestShareCodes_Create_NonSSOApp(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 创建无需认证的应用
	appID, _ := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "none",
	})

	// 尝试为 none 认证应用创建分享码 → 应失败
	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":          appID,
		"max_uses":        3,
		"cookie_ttl":      86400,
		"expires_in_secs": 3600,
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// =============================================================================
// 分享码兑换端点测试 (/s/{code})
// =============================================================================

// newNoRedirectClient 创建不跟随重定向的客户端（用于测试 /s/ 端点）
func newNoRedirectClient() *harness.Client {
	c := harness.NewClient(serverURL, adminDomain)
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}

// redeemShareCode 访问 /s/{code}，返回响应
func redeemShareCode(t *testing.T, c *harness.Client, code string) *http.Response {
	t.Helper()
	resp := c.Get(t, "/s/"+code)
	return resp
}

// extractSSOCookie 从响应中提取 SSO cookie 值。
// subdomain 用于构造含子域名后缀的 cookie name (#40)。
func extractSSOCookie(t *testing.T, resp *http.Response, appID int64, subdomain string) string {
	t.Helper()
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	for _, cookie := range resp.Cookies() {
		if cookie.Name == cookieName {
			return cookie.Value
		}
	}
	t.Fatalf("SSO cookie %s not found in response", cookieName)
	return ""
}

func TestShareCodes_Redeem_Valid(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	appID, subdomain, code := createShareCodeForTest(t, c, 3)

	// 用无重定向客户端访问分享链接
	rc := newNoRedirectClient()
	resp := redeemShareCode(t, rc, code)
	defer resp.Body.Close()

	// 应返回 302 重定向
	require.Equal(t, http.StatusFound, resp.StatusCode, "should redirect")

	// 验证 Location 指向代理域名
	location := resp.Header.Get("Location")
	assert.Contains(t, location, subdomain, "redirect URL should contain subdomain")
	assert.Contains(t, location, proxyDomain, "redirect URL should contain proxy domain")

	// 验证设置了 SSO cookie
	ssoCookie := extractSSOCookie(t, resp, appID, subdomain)
	assert.NotEmpty(t, ssoCookie, "SSO cookie should be set")

	// 验证 use_count 递增到 1
	listResp := c.Get(t, "/api/share-codes")
	defer listResp.Body.Close()
	var listResult struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))
	for _, sc := range listResult.Data {
		if sc.Code == code {
			assert.Equal(t, 1, sc.UseCount, "use_count should be 1 after redemption")
			break
		}
	}

	// 验证 SSO cookie 可访问代理应用（不被重定向到 SSO 登录）
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	proxyResp := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookie,
	})
	defer proxyResp.Body.Close()
	assert.NotEqual(t, http.StatusFound, proxyResp.StatusCode,
		"proxy should not redirect to SSO when valid cookie present")
}

func TestShareCodes_Redeem_WithCookie_SkipCount(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	appID, subdomain, code := createShareCodeForTest(t, c, 1)

	// 第一次访问：消耗一次跳转
	rc := newNoRedirectClient()
	resp1 := redeemShareCode(t, rc, code)
	defer resp1.Body.Close()
	require.Equal(t, http.StatusFound, resp1.StatusCode)

	ssoCookie := extractSSOCookie(t, resp1, appID, subdomain)

	// 第二次访问：带 cookie，应直接跳转且不计次数
	rc2 := newNoRedirectClient()
	rc2.SetCookie(fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain), ssoCookie)
	resp2 := redeemShareCode(t, rc2, code)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusFound, resp2.StatusCode, "should redirect when cookie valid")

	// 验证 use_count 仍为 1（未递增）
	listResp := c.Get(t, "/api/share-codes")
	defer listResp.Body.Close()
	var listResult struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))
	for _, sc := range listResult.Data {
		if sc.Code == code {
			assert.Equal(t, 1, sc.UseCount, "use_count should still be 1 (cookie skip)")
			break
		}
	}
}

func TestShareCodes_Redeem_Exhausted(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	_, _, code := createShareCodeForTest(t, c, 1)

	// 第一次访问：消耗唯一一次跳转
	rc := newNoRedirectClient()
	resp1 := redeemShareCode(t, rc, code)
	defer resp1.Body.Close()
	require.Equal(t, http.StatusFound, resp1.StatusCode)

	// 第二次访问（无 cookie）：应 302 跳转到错误页（#39 改前端渲染）
	resp2 := redeemShareCode(t, rc, code)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusFound, resp2.StatusCode, "exhausted code should redirect to error page")
	assert.Equal(t, "/share-error", resp2.Header.Get("Location"), "should redirect to /share-error")
}

func TestShareCodes_Redeem_Disabled(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	_, _, code := createShareCodeForTest(t, c, 3)

	// 查找 ID 并禁用
	listResp := c.Get(t, "/api/share-codes")
	defer listResp.Body.Close()
	var listResult struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	var scID int64
	for _, sc := range listResult.Data {
		if sc.Code == code {
			scID = sc.ID
			break
		}
	}
	require.NotZero(t, scID)

	disableResp := c.Put(t, fmt.Sprintf("/api/share-codes/%d", scID), map[string]interface{}{
		"enabled": false,
	})
	defer disableResp.Body.Close()
	require.Equal(t, http.StatusOK, disableResp.StatusCode)

	// 访问已禁用的分享码 → 302 跳转到错误页（#39 改前端渲染）
	rc := newNoRedirectClient()
	resp := redeemShareCode(t, rc, code)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode, "disabled code should redirect to error page")
	assert.Equal(t, "/share-error", resp.Header.Get("Location"), "should redirect to /share-error")
}

func TestShareCodes_Redeem_InvalidCode(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	rc := newNoRedirectClient()
	resp := rc.Get(t, "/s/INVALIDxx")
	defer resp.Body.Close()

	// 无效码 → 302 跳转到错误页（#39 改前端渲染）
	assert.Equal(t, http.StatusFound, resp.StatusCode, "invalid code should redirect to error page")
	assert.Equal(t, "/share-error", resp.Header.Get("Location"), "should redirect to /share-error")
}

func TestShareCodes_Redeem_Expired(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 创建 SSO 应用
	appID, _ := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	// 创建 1 秒过期的分享码
	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":          appID,
		"max_uses":        5,
		"cookie_ttl":      86400,
		"expires_in_secs": 1,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	code := result.Data.Code

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/share-codes/%d", result.Data.ID))
	})

	// 等待分享码过期
	time.Sleep(2 * time.Second)

	// 访问过期分享码 → 302 跳转到错误页（#39 改前端渲染）
	rc := newNoRedirectClient()
	resp2 := redeemShareCode(t, rc, code)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusFound, resp2.StatusCode, "expired code should redirect to error page")
	assert.Equal(t, "/share-error", resp2.Header.Get("Location"), "should redirect to /share-error")
}

// =============================================================================
// 分享码跳转路径测试
// =============================================================================

func TestShareCodes_RedirectPath_Default(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	_, subdomain, code := createShareCodeForTest(t, c, 3)

	// 不设置 redirect_path → 跳转到 https://subdomain.proxyDomain/
	rc := newNoRedirectClient()
	resp := redeemShareCode(t, rc, code)
	defer resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	location := resp.Header.Get("Location")
	// 默认应跳到根路径（以 / 结尾，无额外 path）
	assert.True(t, strings.HasSuffix(location, subdomain+"."+proxyDomain+"/") ||
		strings.HasSuffix(location, subdomain+"."+proxyDomain),
		"default redirect should point to app root, got: %s", location)
}

func TestShareCodes_RedirectPath_Custom(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 创建带自定义跳转路径的分享码
	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	customPath := "/dashboard/overview"
	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":          appID,
		"max_uses":        3,
		"cookie_ttl":      86400,
		"expires_in_secs": 3600,
		"redirect_path":   customPath,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	code := result.Data.Code
	require.NotEmpty(t, code)
	assert.Equal(t, customPath, result.Data.RedirectPath, "created share code should carry redirect_path")

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/share-codes/%d", result.Data.ID))
	})

	// 兑换 → 应跳转到 https://subdomain.proxyDomain/dashboard/overview
	rc := newNoRedirectClient()
	redeemResp := redeemShareCode(t, rc, code)
	defer redeemResp.Body.Close()

	require.Equal(t, http.StatusFound, redeemResp.StatusCode)
	location := redeemResp.Header.Get("Location")
	expectedSuffix := subdomain + "." + proxyDomain + customPath
	assert.True(t, strings.HasSuffix(location, expectedSuffix),
		"redirect should append custom path, got: %s, expected suffix: %s", location, expectedSuffix)

	// SSO cookie 也应正常下发
	ssoCookie := extractSSOCookie(t, redeemResp, appID, subdomain)
	assert.NotEmpty(t, ssoCookie)
}

func TestShareCodes_RedirectPath_Invalid(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	appID, _ := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	// 不以 / 开头 → 400
	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":          appID,
		"max_uses":        3,
		"cookie_ttl":      86400,
		"expires_in_secs": 3600,
		"redirect_path":   "foo/bar",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "non-slash path should be rejected")

	// // 开头 → 400（协议相对 URL）
	resp2 := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":          appID,
		"max_uses":        3,
		"cookie_ttl":      86400,
		"expires_in_secs": 3600,
		"redirect_path":   "//evil.com",
	})
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode, "// prefix should be rejected")
}

func TestShareCodes_RedirectPath_Update(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	_, subdomain, code := createShareCodeForTest(t, c, 3)

	// 查 ID
	listResp := c.Get(t, "/api/share-codes")
	defer listResp.Body.Close()
	var listResult struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	var scID int64
	for _, sc := range listResult.Data {
		if sc.Code == code {
			scID = sc.ID
			break
		}
	}
	require.NotZero(t, scID)

	// 更新跳转路径
	newPath := "/profile"
	updResp := c.Put(t, fmt.Sprintf("/api/share-codes/%d", scID), map[string]interface{}{
		"redirect_path": newPath,
	})
	defer updResp.Body.Close()
	require.Equal(t, http.StatusOK, updResp.StatusCode)

	// 兑换 → 验证跳转到新路径
	rc := newNoRedirectClient()
	redeemResp := redeemShareCode(t, rc, code)
	defer redeemResp.Body.Close()
	require.Equal(t, http.StatusFound, redeemResp.StatusCode)

	location := redeemResp.Header.Get("Location")
	expectedSuffix := subdomain + "." + proxyDomain + newPath
	assert.True(t, strings.HasSuffix(location, expectedSuffix),
		"redirect after update should use new path, got: %s, expected suffix: %s", location, expectedSuffix)
}

// =============================================================================
// 模糊匹配应用的分享码测试（issue #24 后续需求）
// =============================================================================

// createFuzzySSOApp 创建一个使用模糊子域名模式的 SSO 应用，自动清理。
// 返回 appID 与子域名模式（如 *-fuzzyshare）。
func createFuzzySSOApp(t *testing.T, c *harness.Client, pattern string) (appID int64, subdomain string) {
	t.Helper()
	subdomain = pattern

	// 清理同 pattern 残留
	listResp := c.Get(t, "/api/apps")
	defer listResp.Body.Close()
	var list listAppsResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &list))
	for _, app := range list.Data {
		if app.Subdomain == pattern {
			c.Delete(t, fmt.Sprintf("/api/apps/%d", app.ID))
		}
	}

	body := map[string]interface{}{
		"name":        "fuzzy-share-" + pattern,
		"subdomain":   pattern,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{"__host__"},
		"auth_method": "sso",
	}
	resp := c.Post(t, "/api/apps", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create fuzzy SSO app should succeed")

	var result singleAppResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	appID = result.Data.ID
	require.NotZero(t, appID)

	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })
	return appID, subdomain
}

// TestShareCodes_FuzzyApp_NoConcreteSubdomain 模糊应用未指定具体子域名应被拒绝
func TestShareCodes_FuzzyApp_NoConcreteSubdomain(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	appID, _ := createFuzzySSOApp(t, c, "*-fzshare1")

	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":          appID,
		"max_uses":        3,
		"cookie_ttl":      86400,
		"expires_in_secs": 3600,
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"模糊应用未指定 concrete_subdomain 应返回 400")
}

// TestShareCodes_FuzzyApp_ConcreteNotMatch 模糊应用的具体子域名不匹配模式应被拒绝
func TestShareCodes_FuzzyApp_ConcreteNotMatch(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	appID, _ := createFuzzySSOApp(t, c, "*-fzshare2")

	// *-fzshare2 模式要求 [a-z0-9-]+-fzshare2；abc-fzshare3 后缀不符，不应命中
	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":             appID,
		"max_uses":           3,
		"cookie_ttl":         86400,
		"expires_in_secs":    3600,
		"concrete_subdomain": "abc-fzshare3",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"不匹配模式的具体子域名应返回 400")
}

// TestShareCodes_NonFuzzyApp_ConcreteRejected 精确匹配应用指定具体子域名应被拒绝
func TestShareCodes_NonFuzzyApp_ConcreteRejected(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	appID, _ := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":             appID,
		"max_uses":           3,
		"cookie_ttl":         86400,
		"expires_in_secs":    3600,
		"concrete_subdomain": "should-be-rejected",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"精确匹配应用指定 concrete_subdomain 应返回 400")
}

// TestShareCodes_FuzzyApp_Redeem 模糊应用的分享码兑换跳转到具体子域名
func TestShareCodes_FuzzyApp_Redeem(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	appID, _ := createFuzzySSOApp(t, c, "*-fzshare3")

	// 创建带具体子域名 abc-fzshare3 的分享码
	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":             appID,
		"max_uses":           3,
		"cookie_ttl":         86400,
		"expires_in_secs":    3600,
		"concrete_subdomain": "abc-fzshare3",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "创建模糊应用分享码应成功")

	var result struct {
		Data shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	code := result.Data.Code
	require.NotEmpty(t, code)
	assert.Equal(t, "abc-fzshare3", result.Data.ConcreteSubdomain,
		"返回的 concrete_subdomain 应为 abc-fzshare3")

	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/share-codes/%d", result.Data.ID)) })

	// 兑换 → 跳转 Location 应包含 abc-fzshare3.proxyDomain
	rc := newNoRedirectClient()
	redeemResp := redeemShareCode(t, rc, code)
	defer redeemResp.Body.Close()
	require.Equal(t, http.StatusFound, redeemResp.StatusCode, "应 302 跳转")

	location := redeemResp.Header.Get("Location")
	expectedSuffix := "abc-fzshare3." + proxyDomain + "/"
	assert.True(t, strings.HasSuffix(location, expectedSuffix),
		"跳转 URL 应使用具体子域名 abc-fzshare3，got: %s", location)
	assert.NotContains(t, location, "*-fzshare3",
		"跳转 URL 不应包含模式字符串 *-fzshare3")

	// SSO cookie 也应正常下发（cookie 名基于 appID+具体子域名 #40）
	ssoCookie := extractSSOCookie(t, redeemResp, appID, "abc-fzshare3")
	assert.NotEmpty(t, ssoCookie, "SSO cookie 应正常下发")
}

// TestShareCodes_FuzzyApp_Redeem_WithCookie_SkipCount 模糊应用已有 SSO cookie 直接跳转
func TestShareCodes_FuzzyApp_Redeem_WithCookie_SkipCount(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	appID, _ := createFuzzySSOApp(t, c, "*-fzshare4")

	resp := c.Post(t, "/api/share-codes", map[string]interface{}{
		"app_id":             appID,
		"max_uses":           1,
		"cookie_ttl":         86400,
		"expires_in_secs":    3600,
		"concrete_subdomain": "xyz-fzshare4",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	code := result.Data.Code
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/share-codes/%d", result.Data.ID)) })

	// 第一次兑换：消耗一次跳转
	rc := newNoRedirectClient()
	resp1 := redeemShareCode(t, rc, code)
	defer resp1.Body.Close()
	require.Equal(t, http.StatusFound, resp1.StatusCode)
	ssoCookie := extractSSOCookie(t, resp1, appID, "xyz-fzshare4")

	// 第二次兑换：带 cookie 应直接跳转且不计次数
	rc2 := newNoRedirectClient()
	rc2.SetCookie(fmt.Sprintf("hopproxy_sso_%d_%s", appID, "xyz-fzshare4"), ssoCookie)
	resp2 := redeemShareCode(t, rc2, code)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusFound, resp2.StatusCode, "有 cookie 应直接跳转")

	// 验证 use_count 仍为 1
	listResp := c.Get(t, "/api/share-codes")
	defer listResp.Body.Close()
	var listResult struct {
		Data []shareCodeResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))
	for _, sc := range listResult.Data {
		if sc.Code == code {
			assert.Equal(t, 1, sc.UseCount, "use_count 应仍为 1（cookie 跳过计数）")
			break
		}
	}
}
