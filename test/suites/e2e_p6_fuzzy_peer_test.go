package suites

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// #58 复现：Path 6（公网 → Server → 隧道 → Client A → Peer → Client B）
// 模糊子域名 + ${1} 目标地址 + peer 代理组合。
// 根因：cmd/client 的 peerOpenStream 一直是 nil 占位（"P7 阶段实现"），
// 客户端 A 的 peer 分支永不触发，静默退化为本机直连目标地址。
// 用户场景中 nas 的 HP_LISTEN 恰好是 8080，退化直连 http://localhost:8080
// 打到 nas 自己的本地代理 → 400「无法解析子域名」（22B，与日志吻合）。
// =============================================================================

// createPeerProxyWithAddress 创建带连接地址的 peer 代理（复刻用户配置：填写地址）
func createPeerProxyWithAddress(t *testing.T, c *harness.Client, client1ID, client2ID, address string) int64 {
	t.Helper()

	// 等待 client-2 上报 proxy_enabled=true
	waitForClientProxyEnabled(t, c, client2ID, 15*time.Second)

	resp := c.Post(t, fmt.Sprintf("/api/clients/%s/proxies", client1ID), map[string]interface{}{
		"name":             "peer-to-client2-addr",
		"proxy_type":       "peer",
		"proxy_address":    address,
		"proxy_password":   "",
		"target_client_id": client2ID,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create peer proxy with address should succeed")

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	require.NotZero(t, result.Data.ID)
	return result.Data.ID
}

// serverLogCount 查询服务端日志库，统计指定 message + subdomain 的日志条数。
// 客户端日志经 logsink（1 秒批量）上报到服务端 logstore，用日志 API 查询。
func serverLogCount(t *testing.T, c *harness.Client, message, subdomain string) int {
	t.Helper()
	path := "/api/admin/logs?message=" + url.QueryEscape(message) +
		"&subdomain=" + url.QueryEscape(subdomain) + "&limit=200"
	resp := c.Get(t, path)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Logf("查询服务端日志失败: status %d", resp.StatusCode)
		return -1
	}
	var result struct {
		Data struct {
			Logs []struct {
				Source string `json:"source"`
			} `json:"logs"`
		} `json:"data"`
	}
	if err := harness.DecodeJSON(resp.Body, &result); err != nil {
		t.Logf("解析服务端日志响应失败: %v", err)
		return -1
	}
	return len(result.Data.Logs)
}

// waitForPeerHandled 轮询等待 client-2 的 peer 执行端日志（"peer 隧道处理 HTTP 请求"）
// 超过 baseline。返回 false 表示超时（请求未经过 peer 执行端——即退化为本机直连）。
// 该日志只会由 client-2 侧的 handlePeerHTTPStream 打出，是 Path 6 真正走通的判据。
func waitForPeerHandled(t *testing.T, c *harness.Client, subdomain string, baseline int) bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if serverLogCount(t, c, "peer 隧道处理 HTTP 请求", subdomain) > baseline {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// setupP6FuzzyApp 创建 Path 6 复现应用：模糊子域名 p6fz-*，目标地址带 ${1}，
// 关联 client-1 并配置 peer 代理指向 client-2（填写连接地址，复刻用户配置）。
// 返回已登录的 admin client（供日志断言使用）。
func setupP6FuzzyApp(t *testing.T) *harness.Client {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	cid1 := firstClientID(t, c)
	cid2 := secondClientID(t, c)

	// client-2 的本地代理在 docker 网络内为 client-2:9090
	proxyID := createPeerProxyWithAddress(t, c, cid1, cid2, "client-2:9090")

	// 清理同 pattern 残留
	resp := c.Get(t, "/api/apps")
	var list listAppsResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &list))
	resp.Body.Close()
	for _, app := range list.Data {
		if app.Subdomain == "p6fz-*" {
			c.Delete(t, fmt.Sprintf("/api/apps/%d", app.ID))
		}
	}

	resp2 := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       t.Name(),
		"subdomain":  "p6fz-*",
		"target_url": "http://test-backend:8000/${1}",
		"client_ids": []string{cid1},
		"client_configs": map[string]interface{}{
			cid1: map[string]interface{}{
				"target_url": "http://test-backend:8000/${1}",
				"proxy_id":   proxyID,
			},
		},
		"auth_method": "none",
	})
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode, "create P6 fuzzy peer app should succeed")

	var result singleAppResponse
	require.NoError(t, harness.DecodeJSON(resp2.Body, &result))
	require.NotZero(t, result.Data.ID)
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", result.Data.ID)) })
	return c
}

// TestP6FuzzyPeer_BasicGet 基础 GET：模糊捕获组展开 + peer 链路连通。
// 双重断言：响应正确 且 请求确实经过 client-2 的 peer 执行端（防止本机直连退化假绿）。
func TestP6FuzzyPeer_BasicGet(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := setupP6FuzzyApp(t)
	time.Sleep(2 * time.Second)

	peerHandledBefore := serverLogCount(t, c, "peer 隧道处理 HTTP 请求", "p6fz-echo")

	// p6fz-echo → 捕获 ["echo"] → 目标 http://test-backend:8000/echo + /favicon.ico
	resp := rawGet(t, serverURL, "p6fz-echo."+proxyDomain, "/favicon.ico", "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"Path 6 模糊子域名 GET 应成功（用户日志中该请求返回 400）")

	var info struct {
		Path string `json:"path"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &info))
	assert.Equal(t, "/echo/favicon.ico", info.Path,
		"${1} 应展开为捕获组 echo")

	// 验证请求确实经过 client-2 的 peer 执行端（而非 client-1 本机直连退化）
	require.True(t, waitForPeerHandled(t, c, "p6fz-echo", peerHandledBefore),
		"请求应经 client-2 的 peer 执行端处理（Path 6），而非退化为本机直连（#58 根因）")
}

// TestP6FuzzyPeer_BrowserHeaders 复刻用户日志中的浏览器请求头
// （favicon 请求带 Priority / Sec-Ch-Ua / Sec-Fetch-* 等头）
func TestP6FuzzyPeer_BrowserHeaders(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	setupP6FuzzyApp(t)
	time.Sleep(2 * time.Second)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: "p6fz-echo",
		Path:      "/favicon.ico",
		Headers: map[string]string{
			"Accept":             "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8",
			"Accept-Encoding":    "gzip, deflate, br, zstd",
			"Accept-Language":    "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7",
			"Dnt":                "1",
			"Priority":           "u=1, i",
			"Referer":            "https://p6fz-echo/login",
			"Sec-Ch-Ua":          `"Not;A=Brand";v="8", "Chromium";v="150", "Google Chrome";v="150"`,
			"Sec-Ch-Ua-Mobile":   "?0",
			"Sec-Ch-Ua-Platform": `"macOS"`,
			"Sec-Fetch-Dest":     "image",
			"Sec-Fetch-Mode":     "no-cors",
			"Sec-Fetch-Site":     "same-origin",
			"User-Agent":         "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36",
		},
	})
	defer resp.Body.Close()
	require.NotEqual(t, http.StatusBadRequest, resp.StatusCode,
		"带浏览器头的 Path 6 GET 不应返回 400（用户日志现象）")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestP6FuzzyPeer_PostBody 复刻登录 POST（带表单 body 的请求经 Path 6）
func TestP6FuzzyPeer_PostBody(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	setupP6FuzzyApp(t)
	time.Sleep(2 * time.Second)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodPost,
		Subdomain: "p6fz-echo",
		Path:      "/login",
		Headers:   map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		Body:      strings.NewReader("username=admin&password=secret"),
	})
	defer resp.Body.Close()
	require.NotEqual(t, http.StatusBadRequest, resp.StatusCode,
		"带 body 的 POST 经 Path 6 不应返回 400")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
