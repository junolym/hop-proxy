package suites

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// 共享 helper
// =============================================================================

// setupHostApp 创建 __host__ 模式的测试 App，targetURL 指向 Docker 内部后端
func setupHostApp(t *testing.T) (string, string) {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	appID, subdomain := harness.SetupHostApp(t, c, serverURL, adminDomain, "http://test-backend:8000")
	return fmt.Sprintf("%d", appID), subdomain
}

// setupTunnelApp 创建隧道模式的测试 App，targetURL 指向 Docker 内部后端
func setupTunnelApp(t *testing.T) (string, string) {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)
	appID, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, clientID, "http://test-backend:8000")
	return fmt.Sprintf("%d", appID), subdomain
}

// =============================================================================
// P1 Server-Local 路径测试（__host__ 模式）
// =============================================================================

// TestE2E_P1_HTTP_Echo 验证 __host__ 路径 POST /echo 的请求/响应一致性
func TestE2E_P1_HTTP_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	body := strings.NewReader("hello-p1-proxy")

	directResp := direct.Post(t, "/echo", body, "text/plain")
	body.Reset("hello-p1-proxy")
	proxyResp := proxy.DoPost(t, subdomain, "/echo", body, "text/plain")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P1_HTTP_HeadersPassthrough 验证 __host__ 路径透传自定义 Header
func TestE2E_P1_HTTP_HeadersPassthrough(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	proxyResp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/headers",
		Headers:   map[string]string{"X-Test-Custom": "p1-value"},
	})
	defer proxyResp.Body.Close()

	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	// 解析后端返回的 headers JSON
	var result struct {
		Headers map[string]string `json:"headers"`
	}
	err := harness.DecodeJSON(proxyResp.Body, &result)
	require.NoError(t, err)

	assert.Equal(t, "p1-value", result.Headers["X-Test-Custom"],
		"custom header should be passed through proxy")
}

// TestE2E_P1_HTTP_StatusCodes 验证 __host__ 路径各状态码正确透传
func TestE2E_P1_HTTP_StatusCodes(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

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

// TestE2E_P1_HTTP_Chunked 验证 __host__ 路径 Chunked 响应正确处理
func TestE2E_P1_HTTP_Chunked(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	directResp := direct.Get(t, "/chunked")
	proxyResp := proxy.DoGet(t, subdomain, "/chunked")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P1_HTTP_MultiValueHeaders 验证 __host__ 路径多值头透传
func TestE2E_P1_HTTP_MultiValueHeaders(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

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

// TestE2E_P1_HTTP_SetCookiesPassthrough 验证 __host__ 路径 Set-Cookie 透传
func TestE2E_P1_HTTP_SetCookiesPassthrough(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

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

// =============================================================================
// P2 Tunnel 路径测试（经典隧道模式）
// =============================================================================

// TestE2E_P2_HTTP_Echo 验证隧道路径 POST /echo 的请求/响应一致性
func TestE2E_P2_HTTP_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	body := strings.NewReader("hello-p2-tunnel")

	directResp := direct.Post(t, "/echo", body, "text/plain")
	body.Reset("hello-p2-tunnel")
	proxyResp := proxy.DoPost(t, subdomain, "/echo", body, "text/plain")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P2_HTTP_HeadersPassthrough 验证隧道路径透传自定义 Header
func TestE2E_P2_HTTP_HeadersPassthrough(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	proxyResp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/headers",
		Headers:   map[string]string{"X-Test-Custom": "p2-value"},
	})
	defer proxyResp.Body.Close()

	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	var result struct {
		Headers map[string]string `json:"headers"`
	}
	err := harness.DecodeJSON(proxyResp.Body, &result)
	require.NoError(t, err)

	assert.Equal(t, "p2-value", result.Headers["X-Test-Custom"],
		"custom header should be passed through tunnel proxy")
}

// TestE2E_P2_HTTP_StatusCodes 验证隧道路径各状态码正确透传
func TestE2E_P2_HTTP_StatusCodes(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	codes := []int{200, 404, 500}
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

// TestE2E_P2_HTTP_Chunked 验证隧道路径 Chunked 响应正确处理
func TestE2E_P2_HTTP_Chunked(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	directResp := direct.Get(t, "/chunked")
	proxyResp := proxy.DoGet(t, subdomain, "/chunked")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P2_HTTP_MultiValueHeaders 验证隧道路径多值头透传
func TestE2E_P2_HTTP_MultiValueHeaders(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

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

// TestE2E_P2_HTTP_LargeBody 验证隧道路径大 Body 传输完整性
func TestE2E_P2_HTTP_LargeBody(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 生成 1MB 确定性数据
	data := bytes.Repeat([]byte("HopProxy-E2E-LargeBody-Test!"), 1<<20/26+1)
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

// =============================================================================
// SHA-256 辅助函数
// =============================================================================

// sha256Hash 计算数据的 SHA-256 哈希（十六进制字符串）
func sha256Hash(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}
