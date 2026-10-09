package suites

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// chunked 请求体归一化测试
//
// 背景：部分目标服务（如 Proxmox VE 的 termproxy）不支持 chunked 请求体，
// 收到 Transfer-Encoding: chunked 会返回 501 "chunked transfer encoding not supported"。
// HopProxy 应将小请求体归一化为 Content-Length 定长传输，大请求体保持流式（chunked）。
//
// 后端 /reqinfo 返回后端实际收到的 framing 信息：
//   - content_length    int64    （chunked 时为 -1）
//   - transfer_encoding []string （chunked 时为 ["chunked"]）
//   - body_size         int
// =============================================================================

// reqInfoResponse 镜像后端 /reqinfo 的 JSON 响应
type reqInfoResponse struct {
	ContentLength    int64    `json:"content_length"`
	TransferEncoding []string `json:"transfer_encoding"`
	BodySize         int      `json:"body_size"`
}

// proxyChunkedPost 发送强制 chunked 的 POST 请求经代理到后端。
// 通过 ContentLength=-1 + 非 NoBody 的 body 强制 Go 客户端以 chunked 发送。
func proxyChunkedPost(t *testing.T, subdomain, path string, body []byte) *http.Response {
	t.Helper()

	u, err := url.Parse(serverURL)
	require.NoError(t, err, "parse serverURL")
	u.Path = path

	httpReq, err := http.NewRequest(http.MethodPost, u.String(), nil)
	require.NoError(t, err, "NewRequest POST %s", u.String())
	httpReq.Host = fmt.Sprintf("%s.%s:%s", subdomain, proxyDomain, u.Port())
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.ContentLength = -1
	httpReq.Body = io.NopCloser(bytes.NewReader(body))

	resp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err, "proxy chunked POST %s (Host=%s)", path, httpReq.Host)
	return resp
}

// assertBackendReceivesFraming 断言后端实际收到的传输 framing 符合预期。
func assertBackendReceivesFraming(t *testing.T, resp *http.Response, wantContentLength int64, wantChunked bool, wantBodySize int) {
	t.Helper()
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "proxy request to /reqinfo should succeed")

	var got reqInfoResponse
	err := harness.DecodeJSON(resp.Body, &got)
	require.NoError(t, err, "decode /reqinfo response")

	if wantChunked {
		assert.Equal(t, int64(-1), got.ContentLength,
			"large body should still stream as chunked (Content-Length=%d)", got.ContentLength)
		assert.Contains(t, got.TransferEncoding, "chunked",
			"large body should still carry Transfer-Encoding: chunked, got %v", got.TransferEncoding)
	} else {
		assert.Equal(t, wantContentLength, got.ContentLength,
			"backend should receive Content-Length=%d, got %d (chunked should be normalized)", wantContentLength, got.ContentLength)
		assert.Empty(t, got.TransferEncoding,
			"backend should NOT receive Transfer-Encoding: chunked, got %v", got.TransferEncoding)
	}
	assert.Equal(t, wantBodySize, got.BodySize,
		"body size mismatch: want=%d, got=%d", wantBodySize, got.BodySize)
}

// TestE2E_ChunkedRequest_EmptyBody 验证空 body 的 chunked POST 被归一化为 Content-Length: 0
// （对应 Proxmox termproxy 场景：浏览器空 body POST 经 nginx HTTP/2→HTTP/1.1 转 chunked）。
func TestE2E_ChunkedRequest_EmptyBody(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	resp := proxyChunkedPost(t, subdomain, "/reqinfo", nil)
	assertBackendReceivesFraming(t, resp, 0, false, 0)
}

// TestE2E_ChunkedRequest_SmallBody 验证小 body 的 chunked POST 被归一化为 Content-Length: N
func TestE2E_ChunkedRequest_SmallBody(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	body := []byte("hello-chunked")
	resp := proxyChunkedPost(t, subdomain, "/reqinfo", body)
	assertBackendReceivesFraming(t, resp, int64(len(body)), false, len(body))
}

// TestE2E_ChunkedRequest_LargeBody_Streams 验证超过归一化阈值的 chunked body 保持流式传输
func TestE2E_ChunkedRequest_LargeBody_Streams(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	// 2MB > 1MB 归一化阈值，应保持 chunked 流式
	body := bytes.Repeat([]byte("x"), 2*1024*1024)
	resp := proxyChunkedPost(t, subdomain, "/reqinfo", body)
	assertBackendReceivesFraming(t, resp, -1, true, len(body))
}

// TestE2E_P1_ChunkedRequest_EmptyBody 验证 __host__ 路径（本地反向代理）同样归一化 chunked 空 body
func TestE2E_P1_ChunkedRequest_EmptyBody(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	resp := proxyChunkedPost(t, subdomain, "/reqinfo", nil)
	assertBackendReceivesFraming(t, resp, 0, false, 0)
}

// TestE2E_ChunkedRequest_BodyIntegrity 验证归一化后请求体内容不被破坏（echo 一致性）
func TestE2E_ChunkedRequest_BodyIntegrity(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	body := strings.Repeat("chunked-body-", 100) // 1.3KB，小于归一化阈值
	resp := proxyChunkedPost(t, subdomain, "/echo", []byte(body))
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	echoed, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, body, string(echoed), "echoed body should match original after normalization")
}
