package suites

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// P4 Peer Tunnel Deep HTTP + SSE 测试
// =============================================================================

// TestE2E_P4_Peer_HTTP_StatusCodes 验证 P4 Peer 路径各状态码正确透传（含 301 重定向）
// 流程: client-1 LocalProxy → Peer → client-2 → test-backend
func TestE2E_P4_Peer_HTTP_StatusCodes(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	direct := harness.NewDirectClient()

	codes := []int{200, 301, 404, 500}
	for _, code := range codes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			directResp := direct.Get(t, fmt.Sprintf("/status/%d", code))
			proxyResp := lpc1.DoGet(t, subdomain, fmt.Sprintf("/status/%d", code))

			assert.Equal(t, directResp.StatusCode, proxyResp.StatusCode,
				"status code mismatch for /status/%d: direct=%d, proxied=%d",
				code, directResp.StatusCode, proxyResp.StatusCode)
			directResp.Body.Close()
			proxyResp.Body.Close()
		})
	}
}

// TestE2E_P4_Peer_HTTP_Chunked 验证 P4 Peer 路径 Chunked 响应正确处理
func TestE2E_P4_Peer_HTTP_Chunked(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	direct := harness.NewDirectClient()

	directResp := direct.Get(t, "/chunked")
	proxyResp := lpc1.DoGet(t, subdomain, "/chunked")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P4_Peer_HTTP_MultiValueHeaders 验证 P4 Peer 路径多值头透传
func TestE2E_P4_Peer_HTTP_MultiValueHeaders(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	proxyResp := lpc1.DoGet(t, subdomain, "/multi-headers")
	defer proxyResp.Body.Close()

	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	// 验证多值 Set-Cookie 头
	cookies := proxyResp.Header.Values("Set-Cookie")
	assert.GreaterOrEqual(t, len(cookies), 2,
		"should have at least 2 Set-Cookie headers, got %d", len(cookies))

	// 验证多值 X-Custom-Values 头
	harness.AssertHeaderContains(t, proxyResp, "X-Custom-Values", "val1")
	harness.AssertHeaderContains(t, proxyResp, "X-Custom-Values", "val2")
}

// TestE2E_P4_Peer_HTTP_SetCookies 验证 P4 Peer 路径 Set-Cookie 透传
func TestE2E_P4_Peer_HTTP_SetCookies(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	direct := harness.NewDirectClient()

	directResp := direct.Get(t, "/set-cookies")
	proxyResp := lpc1.DoGet(t, subdomain, "/set-cookies")

	// 验证 Set-Cookie 数量一致
	directCookies := directResp.Header.Values("Set-Cookie")
	proxyCookies := proxyResp.Header.Values("Set-Cookie")
	assert.Equal(t, len(directCookies), len(proxyCookies),
		"Set-Cookie count mismatch: direct=%d, proxied=%d", len(directCookies), len(proxyCookies))

	directResp.Body.Close()
	proxyResp.Body.Close()
}

// TestE2E_P4_Peer_HTTP_LargeBody 验证 P4 Peer 路径大 Body 传输完整性
func TestE2E_P4_Peer_HTTP_LargeBody(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	direct := harness.NewDirectClient()

	// 生成 1MB 确定性数据
	data := bytes.Repeat([]byte("HopProxy-P4-LargeBody-Test!"), 1<<20/26+1)
	data = data[:1<<20] // 精确 1MB

	// 直连：POST /sha256 获取哈希
	directResp := direct.Post(t, "/sha256", bytes.NewReader(data), "application/octet-stream")
	require.Equal(t, http.StatusOK, directResp.StatusCode)

	var directResult struct {
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	}
	err := harness.DecodeJSON(directResp.Body, &directResult)
	require.NoError(t, err)
	directResp.Body.Close()

	// 代理：POST /sha256 获取哈希
	proxyResp := lpc1.DoPost(t, subdomain, "/sha256", bytes.NewReader(data), "application/octet-stream")
	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	var proxyResult struct {
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	}
	err = harness.DecodeJSON(proxyResp.Body, &proxyResult)
	require.NoError(t, err)
	proxyResp.Body.Close()

	assert.Equal(t, directResult.Hash, proxyResult.Hash,
		"SHA-256 hash mismatch: direct=%s, proxied=%s", directResult.Hash, proxyResult.Hash)
	assert.Equal(t, directResult.Size, proxyResult.Size,
		"body size mismatch: direct=%d, proxied=%d", directResult.Size, proxyResult.Size)
}

// TestE2E_P4_Peer_SSE_Stream 验证 P4 Peer 路径 SSE 流式事件完整到达
func TestE2E_P4_Peer_SSE_Stream(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	resp := lpc1.DoGet(t, subdomain, "/stream?count=10&interval=50ms")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"Content-Type should be text/event-stream")

	events := parseSSEEvents(t, resp)

	// 应收到 10 个 data 事件 + 1 个 done 事件 = 共 11 个
	require.Len(t, events, 11, "expected 11 SSE events (10 data + 1 done), got %d", len(events))

	// 验证前 10 个事件的 index 字段从 0 递增到 9
	for i := 0; i < 10; i++ {
		assert.Contains(t, events[i], fmt.Sprintf(`"index":%d`, i),
			"SSE event %d should contain index %d, got: %s", i, i, events[i])
	}

	// 最后一个事件是 done
	assert.Contains(t, events[10], `"done":true`, "last event should be done")
}

// =============================================================================
// P6 Tunnel+Peer Deep HTTP + SSE 测试
// =============================================================================

// TestE2E_P6_TunnelPeer_HTTP_StatusCodes 验证 P6 组合路径各状态码正确透传（含 301 重定向）
// 流程: 公网 → Server → 隧道 → client-1 → Peer → client-2 → test-backend
func TestE2E_P6_TunnelPeer_HTTP_StatusCodes(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	// P6 从公网 (server:18080) 发请求，走隧道+Peer 路径
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	direct := harness.NewDirectClient()

	codes := []int{200, 301, 404, 500}
	for _, code := range codes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			directResp := direct.Get(t, fmt.Sprintf("/status/%d", code))
			proxyResp := proxy.DoGet(t, subdomain, fmt.Sprintf("/status/%d", code))

			assert.Equal(t, directResp.StatusCode, proxyResp.StatusCode,
				"status code mismatch for /status/%d: direct=%d, proxied=%d",
				code, directResp.StatusCode, proxyResp.StatusCode)
			directResp.Body.Close()
			proxyResp.Body.Close()
		})
	}
}

// TestE2E_P6_TunnelPeer_HTTP_Chunked 验证 P6 组合路径 Chunked 响应正确处理
func TestE2E_P6_TunnelPeer_HTTP_Chunked(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	direct := harness.NewDirectClient()

	directResp := direct.Get(t, "/chunked")
	proxyResp := proxy.DoGet(t, subdomain, "/chunked")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P6_TunnelPeer_HTTP_MultiValueHeaders 验证 P6 组合路径多值头透传
func TestE2E_P6_TunnelPeer_HTTP_MultiValueHeaders(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	proxyResp := proxy.DoGet(t, subdomain, "/multi-headers")
	defer proxyResp.Body.Close()

	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	// 验证多值 Set-Cookie 头
	cookies := proxyResp.Header.Values("Set-Cookie")
	assert.GreaterOrEqual(t, len(cookies), 2,
		"should have at least 2 Set-Cookie headers, got %d", len(cookies))

	// 验证多值 X-Custom-Values 头
	harness.AssertHeaderContains(t, proxyResp, "X-Custom-Values", "val1")
	harness.AssertHeaderContains(t, proxyResp, "X-Custom-Values", "val2")
}

// TestE2E_P6_TunnelPeer_HTTP_SetCookies 验证 P6 组合路径 Set-Cookie 透传
func TestE2E_P6_TunnelPeer_HTTP_SetCookies(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	direct := harness.NewDirectClient()

	directResp := direct.Get(t, "/set-cookies")
	proxyResp := proxy.DoGet(t, subdomain, "/set-cookies")

	// 验证 Set-Cookie 数量一致
	directCookies := directResp.Header.Values("Set-Cookie")
	proxyCookies := proxyResp.Header.Values("Set-Cookie")
	assert.Equal(t, len(directCookies), len(proxyCookies),
		"Set-Cookie count mismatch: direct=%d, proxied=%d", len(directCookies), len(proxyCookies))

	directResp.Body.Close()
	proxyResp.Body.Close()
}

// TestE2E_P6_TunnelPeer_HTTP_LargeBody 验证 P6 组合路径大 Body 传输完整性
func TestE2E_P6_TunnelPeer_HTTP_LargeBody(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	direct := harness.NewDirectClient()

	// 生成 1MB 确定性数据
	data := bytes.Repeat([]byte("HopProxy-P6-LargeBody-Test!"), 1<<20/26+1)
	data = data[:1<<20] // 精确 1MB

	// 直连：POST /sha256 获取哈希
	directResp := direct.Post(t, "/sha256", bytes.NewReader(data), "application/octet-stream")
	require.Equal(t, http.StatusOK, directResp.StatusCode)

	var directResult struct {
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	}
	err := harness.DecodeJSON(directResp.Body, &directResult)
	require.NoError(t, err)
	directResp.Body.Close()

	// 代理：POST /sha256 获取哈希
	proxyResp := proxy.DoPost(t, subdomain, "/sha256", bytes.NewReader(data), "application/octet-stream")
	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	var proxyResult struct {
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	}
	err = harness.DecodeJSON(proxyResp.Body, &proxyResult)
	require.NoError(t, err)
	proxyResp.Body.Close()

	assert.Equal(t, directResult.Hash, proxyResult.Hash,
		"SHA-256 hash mismatch: direct=%s, proxied=%s", directResult.Hash, proxyResult.Hash)
	assert.Equal(t, directResult.Size, proxyResult.Size,
		"body size mismatch: direct=%d, proxied=%d", directResult.Size, proxyResult.Size)
}

// TestE2E_P6_TunnelPeer_SSE_Stream 验证 P6 组合路径 SSE 流式事件完整到达
func TestE2E_P6_TunnelPeer_SSE_Stream(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	resp := proxy.DoGet(t, subdomain, "/stream?count=10&interval=50ms")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"Content-Type should be text/event-stream")

	events := parseSSEEvents(t, resp)

	// 应收到 10 个 data 事件 + 1 个 done 事件 = 共 11 个
	require.Len(t, events, 11, "expected 11 SSE events (10 data + 1 done), got %d", len(events))

	// 验证前 10 个事件的 index 字段从 0 递增到 9
	for i := 0; i < 10; i++ {
		assert.Contains(t, events[i], fmt.Sprintf(`"index":%d`, i),
			"SSE event %d should contain index %d, got: %s", i, i, events[i])
	}

	// 最后一个事件是 done
	assert.Contains(t, events[10], `"done":true`, "last event should be done")
}
