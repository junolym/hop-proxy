package suites

// 路由规则"来源客户端"语义回归测试 (#70)：
// 指定了来源客户端的规则仅匹配从该客户端本地代理进入的请求；
// 公网（Host → 隧道，Path 2）请求无来源客户端，不命中任何指定来源客户端的规则。

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/robin/hop-proxy/test/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_RouteSourceClient 验证来源客户端条件在各代理路径下的匹配语义
func TestE2E_RouteSourceClient(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	// createSourceRoute 创建"来源客户端=srcClientID → auth=none"的路由规则（方法/路径通配）
	createSourceRoute := func(t *testing.T, c *harness.Client, appID int64, srcClientID string) {
		t.Helper()
		resp := c.Post(t, fmt.Sprintf("/api/apps/%d/routes", appID), map[string]interface{}{
			"client_id":   srcClientID,
			"auth_method": "none",
			"priority":    0,
			"enabled":     true,
		})
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "创建路由规则失败")
	}

	// requireAuth 断言响应为"仍需认证"（302 SSO 重定向或 401）
	requireAuth := func(t *testing.T, subdomain string, resp *http.Response) {
		t.Helper()
		assert.True(t, resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusUnauthorized,
			"%s 不应命中指定来源客户端的规则，仍需认证，got %d", subdomain, resp.StatusCode)
	}

	t.Run("public_tunnel_still_requires_auth", func(t *testing.T) {
		// issue #70 报告场景：来源客户端=client-1 → auth=none，
		// 公网（Path 2 隧道）请求必须仍要求认证
		c := harness.Login(t, serverURL, adminDomain)
		clientID := firstClientID(t, c)

		appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
			TargetURL:  "http://test-backend:8000",
			ClientIDs:  []string{clientID},
			AuthMethod: "sso",
		})
		createSourceRoute(t, c, appID, clientID)

		pc := harness.NewProxyClient(serverURL, proxyDomain)
		resp := pc.DoGet(t, subdomain, "/echo")
		defer resp.Body.Close()
		requireAuth(t, "公网（隧道）请求", resp)
	})

	t.Run("source_client_local_proxy_exempt", func(t *testing.T) {
		// 正向场景：从 client-1 本地代理进入（Path 3）→ 命中规则，免认证
		c := harness.Login(t, serverURL, adminDomain)
		clientID := firstClientID(t, c)

		appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
			TargetURL:  "http://test-backend:8000",
			ClientIDs:  []string{clientID},
			AuthMethod: "sso",
		})

		lpc := localProxyClient1()
		// 等待 app 同步到 client-1（302 = SSO 重定向，视为已同步）
		waitForAppSync(t, lpc, subdomain, 10*time.Second)

		// 规则创建前：/echo → 302
		respBefore := lpc.DoGet(t, subdomain, "/echo")
		respBefore.Body.Close()
		require.True(t, respBefore.StatusCode == http.StatusFound || respBefore.StatusCode == http.StatusUnauthorized,
			"路由规则创建前 /echo 应要求认证，got %d", respBefore.StatusCode)

		// 创建规则：来源客户端=client-1 → auth=none（apps_changed 通知即时同步，#68）
		createSourceRoute(t, c, appID, clientID)

		// 轮询等待规则同步生效
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
			"来源客户端本地代理请求应命中规则免认证（10s 内同步生效）")
	})

	t.Run("other_client_path5_not_exempt", func(t *testing.T) {
		// 反向场景：应用挂在 client-1、规则限定来源=client-1，
		// 从 client-2 本地代理进入（Path 5 中转）→ 来源不匹配，仍需认证
		c := harness.Login(t, serverURL, adminDomain)
		clientID := firstClientID(t, c)

		appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
			TargetURL:  "http://test-backend:8000",
			ClientIDs:  []string{clientID},
			AuthMethod: "sso",
		})
		createSourceRoute(t, c, appID, clientID)

		lpc2 := localProxyClient2()
		waitForAppSync(t, lpc2, subdomain, 10*time.Second)

		resp := lpc2.DoGet(t, subdomain, "/echo")
		defer resp.Body.Close()
		requireAuth(t, "非来源客户端（Path 5）请求", resp)
	})

	t.Run("path5_source_client_exempt", func(t *testing.T) {
		// Path 5 正向场景：应用挂在 client-2、规则限定来源=client-1，
		// 从 client-1 本地代理进入（Path 5 中转）→ 来源匹配，免认证
		c := harness.Login(t, serverURL, adminDomain)
		client1ID := firstClientID(t, c)
		client2ID := secondClientID(t, c)

		appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
			TargetURL:  "http://test-backend:8000",
			ClientIDs:  []string{client2ID},
			AuthMethod: "sso",
		})
		createSourceRoute(t, c, appID, client1ID)

		lpc1 := localProxyClient1()
		// 轮询等待生效（服务端实时读取规则，无需客户端同步，留缓冲防抖动）
		var got int
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			resp := lpc1.DoGet(t, subdomain, "/echo")
			resp.Body.Close()
			got = resp.StatusCode
			if got == http.StatusOK {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		assert.Equal(t, http.StatusOK, got,
			"Path 5 来源客户端请求应命中规则免认证，got %d", got)
	})
}
