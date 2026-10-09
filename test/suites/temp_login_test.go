package suites

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// 临时登录（PIN + TOTP token）端点测试
// =============================================================================

// setupTempLoginForTest 为当前登录用户启用临时登录：
//   - 调用 /api/totp/reset 清空旧密钥（确保 Setup 生成全新密钥，避免跨测试 token 复用）
//   - 调用 /api/totp/setup 生成 TOTP 密钥
//   - 调用 /api/user-settings 启用临时登录并设置 PIN
//
// 返回 TOTP 密钥，供测试生成验证码。
func setupTempLoginForTest(t *testing.T, c *harness.Client, pin string) string {
	t.Helper()

	// 先重置密钥（如果存在），保证 Setup 生成全新密钥
	// 用 admin 密码验证（test harness 默认 admin/admin123）
	resetResp := c.Post(t, "/api/totp/reset", map[string]interface{}{
		"password": "admin123",
	})
	defer resetResp.Body.Close()
	// 404/400 表示无密钥可重置或未启用，忽略
	if resetResp.StatusCode != http.StatusOK && resetResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("totp reset unexpected status: %d", resetResp.StatusCode)
	}

	// 生成 TOTP 密钥
	resp := c.Get(t, "/api/totp/setup")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "totp setup should succeed")

	var setup struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &setup))
	require.NotEmpty(t, setup.Data.Secret, "totp secret should not be empty")

	// 启用临时登录 + 设置 PIN
	enableResp := c.Put(t, "/api/user-settings", map[string]interface{}{
		"temp_login_enabled": true,
		"temp_login_pin":     pin,
	})
	defer enableResp.Body.Close()
	require.Equal(t, http.StatusOK, enableResp.StatusCode, "enable temp login should succeed")

	// 显式验证：启用后 GET 必须返回 temp_login_enabled=true
	// （防止后端返回 200 但没真正写入数据库的回归）
	verifyResp := c.Get(t, "/api/user-settings")
	defer verifyResp.Body.Close()
	require.Equal(t, http.StatusOK, verifyResp.StatusCode)
	var verify struct {
		Data struct {
			TempLoginEnabled bool `json:"temp_login_enabled"`
			TOTPSecretSet    bool `json:"totp_secret_set"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(verifyResp.Body, &verify))
	require.True(t, verify.Data.TempLoginEnabled, "temp_login_enabled must be true after enable")
	require.True(t, verify.Data.TOTPSecretSet, "totp_secret_set must be true after setup")

	// 清理：关闭临时登录并重置密钥
	t.Cleanup(func() {
		c.Put(t, "/api/user-settings", map[string]interface{}{
			"temp_login_enabled": false,
		})
		c.Post(t, "/api/totp/reset", map[string]interface{}{
			"password": "admin123",
		})
	})

	return setup.Data.Secret
}

// generateTOTPCode 用 secret 生成当前可用的 6 位 TOTP 验证码
func generateTOTPCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err, "generate TOTP code")
	require.Len(t, code, 6, "TOTP code should be 6 digits")
	return code
}

// callTempLogin 调用 /api/auth/temp-login 端点
func callTempLogin(t *testing.T, c *harness.Client, username, pin, token, redirect string) *http.Response {
	t.Helper()
	resp := c.Post(t, "/api/auth/temp-login", map[string]interface{}{
		"username": username,
		"pin":      pin,
		"token":    token,
		"redirect": redirect,
	})
	return resp
}

func TestTempLogin_Success(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 启用临时登录
	secret := setupTempLoginForTest(t, c, "123456")

	// 创建 SSO 应用
	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	// 生成有效 TOTP 码
	token := generateTOTPCode(t, secret)

	// 构造 redirect URL
	redirect := fmt.Sprintf("http://%s.%s/", subdomain, proxyDomain)

	// 调用临时登录
	resp := callTempLogin(t, c, "admin", "123456", token, redirect)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "temp login should succeed")

	var result struct {
		Data struct {
			OK       bool   `json:"ok"`
			Redirect string `json:"redirect"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	assert.True(t, result.Data.OK, "response ok should be true")
	assert.Equal(t, redirect, result.Data.Redirect, "redirect should match")

	// 验证 SSO cookie 已下发
	ssoCookie := ""
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName {
			ssoCookie = ck.Value
			break
		}
	}
	require.NotEmpty(t, ssoCookie, "SSO cookie should be set")

	// 验证 SSO cookie 可访问代理应用（不被重定向）
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	proxyResp := proxy.DoGet(t, subdomain, "/", map[string]string{
		cookieName: ssoCookie,
	})
	defer proxyResp.Body.Close()
	assert.NotEqual(t, http.StatusFound, proxyResp.StatusCode,
		"proxy should not redirect to SSO when valid temp-login cookie present")
}

func TestTempLogin_WrongPIN(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	secret := setupTempLoginForTest(t, c, "123456")

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	token := generateTOTPCode(t, secret)
	redirect := fmt.Sprintf("http://%s.%s/", subdomain, proxyDomain)

	// 错误 PIN
	resp := callTempLogin(t, c, "admin", "999999", token, redirect)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "wrong PIN should return 401")

	_ = appID
}

func TestTempLogin_WrongToken(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	setupTempLoginForTest(t, c, "123456")

	_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	redirect := fmt.Sprintf("http://%s.%s/", subdomain, proxyDomain)

	// 错误 token（不在当前 30s 窗口内）
	resp := callTempLogin(t, c, "admin", "123456", "000000", redirect)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "wrong token should return 401")
}

func TestTempLogin_NotEnabled(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 不启用临时登录（默认状态），直接尝试调用
	_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	redirect := fmt.Sprintf("http://%s.%s/", subdomain, proxyDomain)

	resp := callTempLogin(t, c, "admin", "123456", "123456", redirect)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "disabled temp login should return 401")
}

func TestTempLogin_InvalidRedirect(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	secret := setupTempLoginForTest(t, c, "123456")

	// 不属于 proxy_domain 的 redirect
	redirect := "http://evil.example.com/"

	token := generateTOTPCode(t, secret)
	resp := callTempLogin(t, c, "admin", "123456", token, redirect)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "invalid redirect should return 400")
}

func TestTempLogin_NonSSOApp(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	secret := setupTempLoginForTest(t, c, "123456")

	// 无需认证的应用
	_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "none",
	})

	redirect := fmt.Sprintf("http://%s.%s/", subdomain, proxyDomain)

	token := generateTOTPCode(t, secret)
	resp := callTempLogin(t, c, "admin", "123456", token, redirect)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "non-SSO app should return 400")
}

func TestTempLogin_TokenSingleUse(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	secret := setupTempLoginForTest(t, c, "123456")

	// 创建两个 SSO 应用，验证同一 token 不能换两次。
	// SetupAuthApp 用 t.Name() 派生子域，同一测试内会冲突，因此用独立子域。
	subdomain1 := harness.DeriveSubdomain(t.Name() + "-app1")
	subdomain2 := harness.DeriveSubdomain(t.Name() + "-app2")
	createSSOAppWithSubdomain(t, c, subdomain1)
	appID2, _ := createSSOAppWithSubdomain(t, c, subdomain2)

	redirect1 := fmt.Sprintf("http://%s.%s/", subdomain1, proxyDomain)
	redirect2 := fmt.Sprintf("http://%s.%s/", subdomain2, proxyDomain)

	token := generateTOTPCode(t, secret)

	// 第一次：成功
	resp1 := callTempLogin(t, c, "admin", "123456", token, redirect1)
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp1.Body)
		t.Fatalf("first temp login should succeed: status=%d body=%s", resp1.StatusCode, string(body))
	}

	// 第二次用同一 token：应失败（单次使用，返回友好提示）
	resp2 := callTempLogin(t, c, "admin", "123456", token, redirect2)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode, "second use of same token should fail with 400")

	_ = appID2
}

// createSSOAppWithSubdomain 用指定子域创建 SSO 应用，返回 (appID, subdomain)
func createSSOAppWithSubdomain(t *testing.T, c *harness.Client, subdomain string) (int64, string) {
	t.Helper()
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        subdomain,
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{"__host__"},
		"auth_method": "sso",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create SSO app %s should succeed", subdomain)

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	require.NotZero(t, result.Data.ID)

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", result.Data.ID))
	})
	return result.Data.ID, subdomain
}

func TestTempLogin_EnableWhenTOTPAlreadyEnabled(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// 先重置确保干净
	c.Post(t, "/api/totp/reset", map[string]interface{}{"password": "admin123"})

	// 1. 先启用 TOTP 二次验证（模拟用户已有二次验证的场景）
	setupResp := c.Get(t, "/api/totp/setup")
	defer setupResp.Body.Close()
	require.Equal(t, http.StatusOK, setupResp.StatusCode)
	var setup struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(setupResp.Body, &setup))
	secret := setup.Data.Secret

	// 生成验证码启用二次验证
	code := generateTOTPCode(t, secret)
	enableTOTPResp := c.Post(t, "/api/totp/enable", map[string]interface{}{"code": code})
	defer enableTOTPResp.Body.Close()
	require.Equal(t, http.StatusOK, enableTOTPResp.StatusCode, "enable TOTP should succeed")

	t.Cleanup(func() {
		c.Post(t, "/api/totp/disable", map[string]interface{}{"password": "admin123"})
		c.Post(t, "/api/totp/reset", map[string]interface{}{"password": "admin123"})
		c.Put(t, "/api/user-settings", map[string]interface{}{"temp_login_enabled": false})
	})

	// 2. 在二次验证已启用状态下，启用临时登录
	enableTempResp := c.Put(t, "/api/user-settings", map[string]interface{}{
		"temp_login_enabled": true,
		"temp_login_pin":     "123456",
	})
	defer enableTempResp.Body.Close()
	require.Equal(t, http.StatusOK, enableTempResp.StatusCode, "enable temp login should succeed")

	// 3. 显式验证 temp_login_enabled=true（防止"返回 200 但没写入"的回归）
	verifyResp := c.Get(t, "/api/user-settings")
	defer verifyResp.Body.Close()
	var verify struct {
		Data struct {
			TempLoginEnabled bool `json:"temp_login_enabled"`
			TOTPSecretSet    bool `json:"totp_secret_set"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(verifyResp.Body, &verify))
	require.True(t, verify.Data.TempLoginEnabled, "temp_login_enabled must be true")
	require.True(t, verify.Data.TOTPSecretSet, "totp_secret_set must be true")

	// 4. 验证临时登录实际可用（端到端）
	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})
	redirect := fmt.Sprintf("http://%s.%s/", subdomain, proxyDomain)
	token := generateTOTPCode(t, secret)

	resp := callTempLogin(t, c, "admin", "123456", token, redirect)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "temp login should work after enabling")

	// 验证 SSO cookie 下发
	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	found := false
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName {
			found = true
			break
		}
	}
	assert.True(t, found, "SSO cookie should be set")
}

func TestTempLogin_DisableAfterEnable(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	secret := setupTempLoginForTest(t, c, "123456")

	_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:  "http://test-backend:8000",
		ClientIDs:  []string{"__host__"},
		AuthMethod: "sso",
	})

	redirect := fmt.Sprintf("http://%s.%s/", subdomain, proxyDomain)

	// 先关闭临时登录
	disableResp := c.Put(t, "/api/user-settings", map[string]interface{}{
		"temp_login_enabled": false,
	})
	defer disableResp.Body.Close()
	require.Equal(t, http.StatusOK, disableResp.StatusCode)

	// 验证：临时登录已关闭，但 TOTP 密钥保留（重置走独立功能）
	settingsResp := c.Get(t, "/api/user-settings")
	defer settingsResp.Body.Close()
	var settings struct {
		Data struct {
			TOTPSecretSet    bool `json:"totp_secret_set"`
			TempLoginEnabled bool `json:"temp_login_enabled"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(settingsResp.Body, &settings))
	assert.False(t, settings.Data.TempLoginEnabled, "temp login should be disabled")
	assert.True(t, settings.Data.TOTPSecretSet, "totp secret should be retained after disable (reset is separate)")

	// 重新启用临时登录，旧 secret 仍可用
	reEnableResp := c.Put(t, "/api/user-settings", map[string]interface{}{
		"temp_login_enabled": true,
		"temp_login_pin":     "654321",
	})
	defer reEnableResp.Body.Close()
	require.Equal(t, http.StatusOK, reEnableResp.StatusCode)

	// 用旧 secret 生成 token，应能成功换取 SSO cookie
	token := generateTOTPCode(t, secret)
	resp := callTempLogin(t, c, "admin", "654321", token, redirect)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("temp login with retained secret should succeed: status=%d body=%s", resp.StatusCode, string(body))
	}
}
