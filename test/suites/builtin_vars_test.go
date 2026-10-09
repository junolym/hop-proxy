package suites

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// TestBuiltinVars 验证应用配置内置变量展开（issue #50）：
//   - 自定义 Header 值支持 ${host}/${subdomain}/${proto}/${remote_ip}/${method}/${path}/${query}/
//     ${user_id}/${app_id}/${app_name} 等内置变量
//   - 目标地址（app 级 + 路由级）与跳转目标同样支持内置变量
//   - 未知的变量名原样输出（存量配置兼容）
//   - 与模糊子域名捕获组（$1/${1}）可混用
func TestBuiltinVars(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// 全部用 -bvars 后缀以避免与其他测试冲突
	subdomain := "app-bvars"
	proxyHost := subdomain + "." + proxyDomain

	customHeaders := map[string]string{
		"X-Test-Host":      "${host}",
		"X-Test-Subdomain": "${subdomain}",
		"X-Test-Proto":     "${proto}",
		"X-Test-Method":    "${method}",
		"X-Test-Path":      "${path}",
		"X-Test-Query":     "${query}",
		"X-Test-Remote-Ip": "${remote_ip}",
		"X-Test-User-Id":   "${user_id}",
		"X-Test-App-Id":    "${app_id}",
		"X-Test-App-Name":  "${app_name}",
		"X-Test-Unknown":   "keep-${no_such_var}-literal",
		"X-Test-Mixed":     "sub=${subdomain}-path=${path}",
	}

	t.Run("custom_headers_builtin_vars", func(t *testing.T) {
		appID := createBuiltinVarsApp(t, c, subdomain, "http://test-backend:8000", clientID, customHeaders)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// GET /headers?a=1&b=2 → 后端收到的自定义 Header 均为展开后的值
		resp := rawGet(t, serverURL, proxyHost, "/headers?a=1&b=2", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))

		assert.Equal(t, proxyHost, hdrs.Headers["X-Test-Host"], "${host} 应展开为完整请求域名")
		assert.Equal(t, subdomain, hdrs.Headers["X-Test-Subdomain"], "${subdomain} 应展开为命中的子域名")
		assert.Equal(t, "http", hdrs.Headers["X-Test-Proto"], "${proto} 直连无 TLS/XFP 时应为 http")
		assert.Equal(t, "GET", hdrs.Headers["X-Test-Method"], "${method} 应展开为请求方法")
		assert.Equal(t, "/headers", hdrs.Headers["X-Test-Path"], "${path} 应展开为请求路径")
		assert.Equal(t, "a=1&b=2", hdrs.Headers["X-Test-Query"], "${query} 应展开为原始 query（不含 ?）")
		// auth_method=none → 匿名访问，user_id=0
		assert.Equal(t, "0", hdrs.Headers["X-Test-User-Id"], "匿名访问 ${user_id} 应为 0")
		assert.Equal(t, itoa(appID), hdrs.Headers["X-Test-App-Id"], "${app_id} 应展开为应用 ID")
		assert.Equal(t, builtinVarsAppName(subdomain), hdrs.Headers["X-Test-App-Name"], "${app_name} 应展开为应用名称")
		// 未知变量名原样输出（存量配置兼容）
		assert.Equal(t, "keep-${no_such_var}-literal", hdrs.Headers["X-Test-Unknown"],
			"未知变量名应原样输出")
		assert.Equal(t, "sub="+subdomain+"-path=/headers", hdrs.Headers["X-Test-Mixed"],
			"多个内置变量应可在同一值内混用")
		// ${remote_ip}：测试直连 server（无 XFF），取 RemoteAddr host，只断言非空
		assert.NotEmpty(t, hdrs.Headers["X-Test-Remote-Ip"], "${remote_ip} 应展开为客户端 IP")
	})

	t.Run("custom_headers_remote_ip_prefers_xff", func(t *testing.T) {
		appID := createBuiltinVarsApp(t, c, subdomain, "http://test-backend:8000", clientID,
			map[string]string{"X-Test-Remote-Ip": "${remote_ip}"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 伪造 X-Forwarded-For（模拟经 nginx 转发）：${remote_ip} 应信任 XFF 链取最原始 IP
		req, err := http.NewRequest("GET", serverURL+"/headers", nil)
		require.NoError(t, err)
		req.Host = proxyHost
		req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))
		assert.Equal(t, "203.0.113.7", hdrs.Headers["X-Test-Remote-Ip"],
			"${remote_ip} 应取 X-Forwarded-For 链的首个 IP（最原始客户端 IP）")
	})

	t.Run("target_url_builtin_var", func(t *testing.T) {
		// target_url 含 ${subdomain} → 请求 / 时后端收到 /app-bvars/
		appID := createBuiltinVarsApp(t, c, subdomain, "http://test-backend:8000/${subdomain}", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		resp := rawGet(t, serverURL, proxyHost, "/", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var info struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
		assert.Equal(t, "/app-bvars/", info.Path, "target_url 中的 ${subdomain} 应展开")
	})

	t.Run("route_target_url_builtin_var", func(t *testing.T) {
		appID := createBuiltinVarsApp(t, c, subdomain, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 路由规则：GET /api → target_url=http://test-backend:8000/r-${subdomain}
		resp := c.Post(t, "/api/apps/"+itoa(appID)+"/routes", map[string]interface{}{
			"method":       "GET",
			"path_pattern": "/api",
			"target_url":   "http://test-backend:8000/r-${subdomain}",
			"priority":     0,
			"enabled":      true,
		})
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "创建路由规则失败")

		r := rawGet(t, serverURL, proxyHost, "/api", "")
		defer r.Body.Close()
		require.Equal(t, http.StatusOK, r.StatusCode)
		var info struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&info))
		assert.Equal(t, "/r-app-bvars/api", info.Path, "route.target_url 中的 ${subdomain} 应展开")
	})

	t.Run("redirect_target_builtin_var", func(t *testing.T) {
		appID := createBuiltinVarsApp(t, c, subdomain, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 跳转规则：/old → /moved-${path}
		resp := c.Post(t, "/api/apps/"+itoa(appID)+"/redirects", map[string]interface{}{
			"match_type":      "exact",
			"match_path":      "/old",
			"redirect_target": "/moved-${path}",
			"status_code":     302,
			"priority":        0,
		})
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "创建跳转规则失败")

		r := rawGet(t, serverURL, proxyHost, "/old", "")
		defer r.Body.Close()
		assert.Equal(t, http.StatusFound, r.StatusCode, "应 302 跳转")
		assert.Equal(t, "/moved-/old", r.Header.Get("Location"),
			"redirect_target 中的 ${path} 应展开为请求路径")
	})

	t.Run("builtin_vars_with_fuzzy_captures", func(t *testing.T) {
		// 模糊模式 *-bvars-fuzzy + 捕获组与内置变量混用
		pattern := "*-bvars-fuzzy"
		appID := createBuiltinVarsApp(t, c, pattern, "http://test-backend:8000", clientID,
			map[string]string{"X-Test-Mix": "${1}-${subdomain}-${path}"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		resp := rawGet(t, serverURL, "myapp-bvars-fuzzy."+proxyDomain, "/headers", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))
		assert.Equal(t, "myapp-myapp-bvars-fuzzy-/headers", hdrs.Headers["X-Test-Mix"],
			"捕获组 $1（myapp）、内置 ${subdomain}（myapp-bvars-fuzzy）与 ${path} 应可混用展开")
	})
}

// builtinVarsAppName 生成内置变量测试应用的应用名
func builtinVarsAppName(subdomain string) string {
	return "builtin-vars-" + subdomain
}

// createBuiltinVarsApp 创建一个用于内置变量测试的临时应用（先清理同 subdomain 残留）
func createBuiltinVarsApp(t *testing.T, c *harness.Client, subdomain, targetURL, clientID string, customHeaders map[string]string) int64 {
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

	body := map[string]interface{}{
		"name":           builtinVarsAppName(subdomain),
		"subdomain":      subdomain,
		"target_url":     targetURL,
		"client_ids":     []string{clientID},
		"auth_method":    "none",
		"custom_headers": customHeaders,
	}
	resp2 := c.Post(t, "/api/apps", body)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		var errRes struct {
			Error string `json:"error"`
		}
		harness.DecodeJSON(resp2.Body, &errRes)
		t.Fatalf("create builtin-vars app %q failed (status %d): %s", subdomain, resp2.StatusCode, errRes.Error)
	}

	var result singleAppResponse
	require.NoError(t, harness.DecodeJSON(resp2.Body, &result))
	return result.Data.ID
}
