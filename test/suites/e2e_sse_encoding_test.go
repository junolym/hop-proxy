package suites

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// TestE2E_P1_SSE_AcceptEncodingStripped 验证 P1 __host__ 路径对 SSE 请求剔除
// Accept-Encoding：浏览器发送 Accept: text/event-stream + Accept-Encoding: gzip 时，
// 代理应在转发前剔除 Accept-Encoding，防止目标对 SSE 响应做 gzip 压缩缓冲。
func TestE2E_P1_SSE_AcceptEncodingStripped(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/stream/echo-headers",
		Headers: map[string]string{
			"Accept":          "text/event-stream",
			"Accept-Encoding": "gzip, deflate, br",
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// 目标后端在收到 Accept-Encoding: gzip 时会对 SSE 做 gzip 压缩。
	// 代理剔除后后端不应压缩——响应不得带 Content-Encoding。
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"SSE 响应不应被压缩：代理应剔除 Accept-Encoding 防止目标压缩 SSE")
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	events := parseSSEEvents(t, resp)
	require.Len(t, events, 1, "应收到 1 个 SSE 事件")
	var ev struct {
		AcceptEncoding string `json:"accept_encoding"`
	}
	require.NoError(t, json.Unmarshal([]byte(events[0]), &ev))
	assert.Empty(t, ev.AcceptEncoding,
		"后端收到的 Accept-Encoding 应为空（代理已剔除）；实际: %q", ev.AcceptEncoding)
}

// TestE2E_P2_SSE_AcceptEncodingStripped 验证 P2 隧道路径对 SSE 请求剔除
// Accept-Encoding（与 P1 相同逻辑，覆盖隧道执行端 ServeHTTPDirect）。
func TestE2E_P2_SSE_AcceptEncodingStripped(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/stream/echo-headers",
		Headers: map[string]string{
			"Accept":          "text/event-stream",
			"Accept-Encoding": "gzip, deflate, br",
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"SSE 响应不应被压缩：代理应剔除 Accept-Encoding 防止目标压缩 SSE")
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	events := parseSSEEvents(t, resp)
	require.Len(t, events, 1, "应收到 1 个 SSE 事件")
	var ev struct {
		AcceptEncoding string `json:"accept_encoding"`
	}
	require.NoError(t, json.Unmarshal([]byte(events[0]), &ev))
	assert.Empty(t, ev.AcceptEncoding,
		"后端收到的 Accept-Encoding 应为空（代理已剔除）；实际: %q", ev.AcceptEncoding)
}

// TestE2E_P1_NonSSE_AcceptEncodingPreserved 验证非 SSE 请求保留 Accept-Encoding
// （防过度剔除破坏端到端压缩协商）。
func TestE2E_P1_NonSSE_AcceptEncodingPreserved(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	resp := proxy.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/headers",
		Headers: map[string]string{
			"Accept":          "application/json",
			"Accept-Encoding": "gzip",
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Headers map[string]string `json:"headers"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Contains(t, result.Headers["Accept-Encoding"], "gzip",
		"非 SSE 请求应保留 Accept-Encoding；实际 headers: %v", result.Headers)
}
