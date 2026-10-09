package suites

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// 性能测量辅助函数
// =============================================================================

// measureGetRequest 执行 GET 请求并返回 body 字节数和耗时
func measureGetRequest(t *testing.T, client *harness.DirectClient, path string) (bytes int64, dur time.Duration) {
	t.Helper()
	start := time.Now()
	resp := client.Get(t, path)
	body := harness.ReadBody(t, resp)
	elapsed := time.Since(start)
	return int64(len(body)), elapsed
}

// measureProxyGetRequest 通过代理执行 GET 请求并返回 body 字节数和耗时
func measureProxyGetRequest(t *testing.T, client *harness.ProxyClient, subdomain, path string) (bytes int64, dur time.Duration) {
	t.Helper()
	start := time.Now()
	resp := client.DoGet(t, subdomain, path)
	body := harness.ReadBody(t, resp)
	elapsed := time.Since(start)
	return int64(len(body)), elapsed
}

// =============================================================================
// 大文件传输测试
// =============================================================================

// TestE2E_P1_LargeFile_10MB 验证 __host__ 路径 10MB 文件传输完整性和性能
func TestE2E_P1_LargeFile_10MB(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 直连基线
	directResp := direct.Get(t, "/random/10MB")
	proxyResp := proxy.DoGet(t, subdomain, "/random/10MB")

	// 验证 SHA-256 哈希一致
	harness.AssertBodyHashEqual(t, directResp, proxyResp)
}

// TestE2E_P2_LargeFile_10MB 验证隧道路径 10MB 文件传输完整性和性能
func TestE2E_P2_LargeFile_10MB(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 性能测量：直连基线
	baseBytes, baseDur := measureGetRequest(t, direct, "/random/10MB")

	// 性能测量：代理请求
	proxyBytes, proxyDur := measureProxyGetRequest(t, proxy, subdomain, "/random/10MB")

	// 验证传输字节数一致
	assert.Equal(t, baseBytes, proxyBytes,
		"transferred bytes mismatch: direct=%d, proxy=%d", baseBytes, proxyBytes)

	// 验证 SHA-256 哈希一致（重新请求以获取独立响应）
	directResp := direct.Get(t, "/random/10MB")
	proxyResp := proxy.DoGet(t, subdomain, "/random/10MB")
	harness.AssertBodyHashEqual(t, directResp, proxyResp)

	// 性能断言：代理吞吐量不低于直连 25%（隧道代理有 WebSocket 编解码和 Docker 网络开销）
	harness.AssertPerformance(t, "P2-10MB", baseBytes, proxyBytes, baseDur, proxyDur, 0.25)
}

// TestE2E_P2_LargeFile_50MB 验证隧道路径 50MB 文件传输完整性（压力测试）
func TestE2E_P2_LargeFile_50MB(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	directResp := direct.Get(t, "/random/50MB")
	proxyResp := proxy.DoGet(t, subdomain, "/random/50MB")

	// 仅验证哈希一致性（50MB 在 Docker 环境可能较慢，性能比仅供参考）
	harness.AssertBodyHashEqual(t, directResp, proxyResp)
}

// =============================================================================
// Chunked Transfer Encoding 测试
// =============================================================================

// TestE2E_P1_Chunked_LargeResponse 验证 __host__ 路径 chunked 响应 body 完全一致
func TestE2E_P1_Chunked_LargeResponse(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	directResp := direct.Get(t, "/chunked")
	proxyResp := proxy.DoGet(t, subdomain, "/chunked")

	// 验证 body 完全一致
	directBody := harness.ReadBody(t, directResp)
	proxyBody := harness.ReadBody(t, proxyResp)
	assert.Equal(t, directBody, proxyBody,
		"chunked response body mismatch (direct %d bytes, proxy %d bytes)", len(directBody), len(proxyBody))
}

// TestE2E_P2_Chunked_LargeResponse 验证隧道路径 chunked 响应 body 完全一致
func TestE2E_P2_Chunked_LargeResponse(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	direct := harness.NewDirectClient()
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	directResp := direct.Get(t, "/chunked")
	proxyResp := proxy.DoGet(t, subdomain, "/chunked")

	// 验证 body 完全一致
	directBody := harness.ReadBody(t, directResp)
	proxyBody := harness.ReadBody(t, proxyResp)
	assert.Equal(t, directBody, proxyBody,
		"chunked response body mismatch (direct %d bytes, proxy %d bytes)", len(directBody), len(proxyBody))
}

// =============================================================================
// 客户端离线 502 测试
// =============================================================================

// TestE2E_P2_ClientOffline_502 验证客户端离线后代理请求返回 502 Bad Gateway
func TestE2E_P2_ClientOffline_502(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)
	_, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, clientID, "http://test-backend:8000")

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 先验证代理正常工作
	normalResp := proxy.DoGet(t, subdomain, "/")
	normalResp.Body.Close()
	require.Equal(t, http.StatusOK, normalResp.StatusCode, "proxy should work before client offline")

	// 停止客户端容器
	err := env.ComposeCmd(t.Context(), "stop", "client-1", "client-2").Run()
	require.NoError(t, err, "stop client containers")

	t.Cleanup(func() {
		// 确保 client 容器重启；用 context.Background() 因 t.Context() 已取消
		env.ComposeCmd(context.Background(), "start", "client-1", "client-2").Run()
		// 等待客户端重连并上报在线状态（心跳间隔 30s，需足够长）
		time.Sleep(10 * time.Second)
	})

	// 等待客户端离线（最多 45s，服务器心跳间隔为 30s）
	err = pollUntil(func() error {
		if clientOnline(t, c, clientID) {
			return fmt.Errorf("client still online")
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "client should go offline after container stop")

	// 验证代理返回 502
	offlineResp := proxy.DoGet(t, subdomain, "/")
	defer offlineResp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, offlineResp.StatusCode,
		"should return 502 Bad Gateway when client offline, got %d", offlineResp.StatusCode)
}

