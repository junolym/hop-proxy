package suites

// P3 本地代理认证规则回归测试 (#68)：
// 路由规则认证覆盖（auth_method=none）与 SSO 路径豁免（exempt_paths）
// 必须在客户端本地代理（Path 3）上与服务端行为一致。

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/robin/hop-proxy/test/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_P3_LocalProxy_AuthRules 验证本地代理上的认证规则生效
func TestE2E_P3_LocalProxy_AuthRules(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	t.Run("route_auth_override_none", func(t *testing.T) {
		c := harness.Login(t, serverURL, adminDomain)
		clientID := firstClientID(t, c)

		// 应用整体 auth=sso，通过路由规则对 /echo 豁免认证
		appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
			TargetURL: "http://test-backend:8000",
			ClientIDs: []string{clientID},
			AuthMethod: "sso",
		})

		lpc := localProxyClient1()
		// 等待 app 同步到 client-1（302 = SSO 重定向，视为已同步）
		waitForAppSync(t, lpc, subdomain, 10*time.Second)

		// 创建路由规则前：/echo → 302 跳 SSO
		respBefore := lpc.DoGet(t, subdomain, "/echo")
		respBefore.Body.Close()
		require.True(t, respBefore.StatusCode == http.StatusFound || respBefore.StatusCode == http.StatusUnauthorized,
			"路由规则创建前 /echo 应要求认证，got %d", respBefore.StatusCode)

		// 创建路由规则：GET /echo → auth_method=none
		// (#68：创建后应经 apps_changed 通知客户端即时刷新，而非等每小时轮询)
		routeResp := c.Post(t, fmt.Sprintf("/api/apps/%d/routes", appID), map[string]interface{}{
			"method":       "GET",
			"path_pattern": "/echo",
			"auth_method":  "none",
			"priority":     0,
			"enabled":      true,
		})
		routeResp.Body.Close()
		require.Equal(t, http.StatusOK, routeResp.StatusCode, "创建路由规则失败")

		// 轮询等待路由规则同步生效
		var got int
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			resp := lpc.DoGet(t, subdomain, "/echo")
			resp.Body.Close()
			got = resp.StatusCode
			if got == http.StatusOK {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		assert.Equal(t, http.StatusOK, got,
			"路由规则 auth_method=none 应在本地代理豁免认证（10s 内同步生效）")

		// 未覆盖路径 / 仍需认证 → 302
		respRoot := lpc.DoGet(t, subdomain, "/")
		respRoot.Body.Close()
		assert.True(t, respRoot.StatusCode == http.StatusFound || respRoot.StatusCode == http.StatusUnauthorized,
			"未覆盖路径 / 仍应要求认证，got %d", respRoot.StatusCode)
	})

	t.Run("exempt_paths", func(t *testing.T) {
		c := harness.Login(t, serverURL, adminDomain)
		clientID := firstClientID(t, c)

		// auth=sso + 路径豁免：精确 /health、前缀 /status/
		_, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
			TargetURL:   "http://test-backend:8000",
			ClientIDs:   []string{clientID},
			AuthMethod:  "sso",
			ExemptPaths: []string{"/health", "/status/"},
		})

		lpc := localProxyClient1()
		waitForAppSync(t, lpc, subdomain, 10*time.Second)

		// 精确匹配：/health → 200 免认证
		respHealth := lpc.DoGet(t, subdomain, "/health")
		respHealth.Body.Close()
		assert.Equal(t, http.StatusOK, respHealth.StatusCode,
			"豁免路径 /health 应免认证直接代理，got %d", respHealth.StatusCode)

		// 前缀匹配：/status/204 → 204 免认证（后端按状态码回显）
		respStatus := lpc.DoGet(t, subdomain, "/status/204")
		respStatus.Body.Close()
		assert.Equal(t, http.StatusNoContent, respStatus.StatusCode,
			"豁免前缀 /status/ 下 /status/204 应免认证，got %d", respStatus.StatusCode)

		// 非豁免路径 / → 仍需认证
		respRoot := lpc.DoGet(t, subdomain, "/")
		respRoot.Body.Close()
		assert.True(t, respRoot.StatusCode == http.StatusFound || respRoot.StatusCode == http.StatusUnauthorized,
			"非豁免路径 / 应要求认证，got %d", respRoot.StatusCode)
	})
}
