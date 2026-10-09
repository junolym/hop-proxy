package suites

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// P3/P4/P5/P6 共享 helper
// =============================================================================

// localProxyClient1 返回一个发往 client-1 LocalProxy 的 ProxyClient
func localProxyClient1() *harness.ProxyClient {
	return harness.NewProxyClient("http://localhost:19091", proxyDomain)
}

// localProxyClient2 返回一个发往 client-2 LocalProxy 的 ProxyClient
func localProxyClient2() *harness.ProxyClient {
	return harness.NewProxyClient("http://localhost:19092", proxyDomain)
}

// secondClientID 返回第二个注册的客户端 UUID（跳过 __host__ 虚拟客户端）
func secondClientID(t *testing.T, c *harness.Client) string {
	t.Helper()
	resp := c.Get(t, "/api/clients")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	require.GreaterOrEqual(t, len(result.Data), 2, "at least 2 clients needed")
	// Skip __host__ virtual client (same logic as firstClientID)
	count := 0
	for _, cl := range result.Data {
		if cl.ID == "__host__" {
			continue
		}
		count++
		if count == 2 {
			return cl.ID
		}
	}
	t.Fatal("second real client not found")
	return ""
}

// waitForAppSync 等待 app 配置同步到客户端（最多 timeout）
// 通过发请求到 LocalProxy 并期望非 502/404 来判断
func waitForAppSync(t *testing.T, lpc *harness.ProxyClient, subdomain string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp := lpc.DoGet(t, subdomain, "/")
		resp.Body.Close()
		// 502 = client 还没同步到 app 配置（无法路由）
		// 404 可能是 app 不存在
		if resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusNotFound {
			return // app 已同步，请求被正常处理
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("app not synced to client after %v (subdomain=%s)", timeout, subdomain)
}

// dialLocalProxyWS 通过客户端 LocalProxy 建立 WebSocket 连接
// 使用 hostHeaderTransport 设置正确的 Host header
func dialLocalProxyWS(t *testing.T, localProxyHost string, subdomain, path string) (*websocket.Conn, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	wsURL := fmt.Sprintf("ws://%s%s", localProxyHost, path)

	// Host header 用于 LocalProxy 子域名路由
	hostHeader := fmt.Sprintf("%s.%s", subdomain, proxyDomain)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: &hostHeaderTransport{
				host:    hostHeader,
				wrapped: http.DefaultTransport,
			},
		},
	})
	require.NoError(t, err, "WebSocket dial via LocalProxy failed (Host=%s)", hostHeader)

	return conn, cancel
}

// setupP3App 创建 P3 路径测试 App，关联到 client-1
func setupP3App(t *testing.T) (string, string) {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)
	appID, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, clientID, "http://test-backend:8000")
	return fmt.Sprintf("%d", appID), subdomain
}

// setupP5App 创建 P5 路径测试 App，关联到 client-2（非 client-1）
// 这样从 client-1 LocalProxy 访问时，本地未命中 → forwardToServer
func setupP5App(t *testing.T) (string, string) {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	cid2 := secondClientID(t, c)
	appID, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, cid2, "http://test-backend:8000")
	return fmt.Sprintf("%d", appID), subdomain
}

// =============================================================================
// P3 Client LocalProxy 路径测试
// =============================================================================

// TestE2E_P3_LocalProxy_HTTP_Echo 验证 P3 路径 HTTP echo 响应与直连一致
func TestE2E_P3_LocalProxy_HTTP_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP3App(t)

	// 等待 client-1 的 AppManager 同步 app 配置
	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	direct := harness.NewDirectClient()

	body := strings.NewReader("hello-p3-localproxy")

	directResp := direct.Post(t, "/echo", body, "text/plain")
	body.Reset("hello-p3-localproxy")
	proxyResp := lpc.DoPost(t, subdomain, "/echo", body, "text/plain")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P3_LocalProxy_HTTP_Headers 验证 P3 路径 Header 透传
func TestE2E_P3_LocalProxy_HTTP_Headers(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP3App(t)

	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	proxyResp := lpc.Do(t, harness.ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      "/headers",
		Headers:   map[string]string{"X-Test-Custom": "p3-value"},
	})
	defer proxyResp.Body.Close()

	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	var result struct {
		Headers map[string]string `json:"headers"`
	}
	err := harness.DecodeJSON(proxyResp.Body, &result)
	require.NoError(t, err)

	assert.Equal(t, "p3-value", result.Headers["X-Test-Custom"],
		"custom header should be passed through LocalProxy")
}

// TestE2E_P3_LocalProxy_WS_Echo 验证 P3 路径 WebSocket echo 消息一致性
func TestE2E_P3_LocalProxy_WS_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP3App(t)

	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	messages := []string{"hello-p3", "world-p3", "websocket-p3"}
	for _, msg := range messages {
		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		require.NoError(t, err, "write message %q", msg)

		_, reader, err := conn.Reader(ctx)
		require.NoError(t, err, "read echo for message %q", msg)

		var buf [4096]byte
		n, err := reader.Read(buf[:])
		require.NoError(t, err, "read echo data for message %q", msg)

		assert.Equal(t, msg, string(buf[:n]),
			"echo mismatch: sent %q, got %q", msg, string(buf[:n]))
	}
}

// =============================================================================
// P5 Client-Server Relay 路径测试
// =============================================================================

// TestE2E_P5_Relay_HTTP_Echo 验证 P5 路径 HTTP echo 响应与直连一致
func TestE2E_P5_Relay_HTTP_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP5App(t)

	// 等待 client-1 同步（P5 需要从 client-1 relay，client-1 不需要本地有此 app，
	// 但 client-2 需要通过 tunnel 正常处理请求）
	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	direct := harness.NewDirectClient()

	body := strings.NewReader("hello-p5-relay")

	directResp := direct.Post(t, "/echo", body, "text/plain")
	body.Reset("hello-p5-relay")
	// 从 client-1 LocalProxy 发请求，client-1 本地无此 app → forwardToServer
	proxyResp := lpc1.DoPost(t, subdomain, "/echo", body, "text/plain")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P5_Relay_HTTP_StatusCodes 验证 P5 路径状态码透传
func TestE2E_P5_Relay_HTTP_StatusCodes(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP5App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	direct := harness.NewDirectClient()

	codes := []int{200, 404, 500}
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

// =============================================================================
// P4 Peer Tunnel 路径测试
// =============================================================================

// waitForClientProxyEnabled 等待目标客户端上报 proxy_enabled=true（最多 timeout）
// 客户端连接隧道后会定时上报代理状态，需要等服务端收到并更新 DB
func waitForClientProxyEnabled(t *testing.T, c *harness.Client, clientID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp := c.Get(t, "/api/clients")
		var result struct {
			Data []struct {
				ID           string `json:"id"`
				ProxyEnabled bool   `json:"proxy_enabled"`
			} `json:"data"`
		}
		err := harness.DecodeJSON(resp.Body, &result)
		resp.Body.Close()
		require.NoError(t, err)
		for _, cl := range result.Data {
			if cl.ID == clientID && cl.ProxyEnabled {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("client %s did not report proxy_enabled=true within %v", clientID, timeout)
}

// createPeerProxy 在 client-1 上创建一个指向 client-2 的 peer 类型代理配置
// 返回 proxyID
func createPeerProxy(t *testing.T, c *harness.Client, client1ID, client2ID string) int64 {
	t.Helper()

	// 等待 client-2 上报 proxy_enabled=true
	// 客户端连接隧道后会异步上报代理状态，创建 peer 前必须确保目标客户端已上报
	waitForClientProxyEnabled(t, c, client2ID, 15*time.Second)

	resp := c.Post(t, fmt.Sprintf("/api/clients/%s/proxies", client1ID), map[string]interface{}{
		"name":             "peer-to-client2",
		"proxy_type":       "peer",
		"proxy_address":    "",
		"proxy_password":   "",
		"target_client_id": client2ID,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create peer proxy should succeed")

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	require.NotZero(t, result.Data.ID, "proxy ID should not be zero")
	return result.Data.ID
}

// setupP4App 创建 P4 Peer 路径测试 App：
// 在 client-1 上创建 peer proxy 指向 client-2，
// 然后创建 app 关联到 client-1 并使用该 peer proxy，
// target_url 指向 test-backend
func setupP4App(t *testing.T) (string, string) {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)
	client2ID := secondClientID(t, c)

	// Step 1: 创建 peer proxy 配置
	proxyID := createPeerProxy(t, c, client1ID, client2ID)

	// Step 2: 创建 app，关联 client-1 并使用 peer proxy
	subdomain := harness.DeriveSubdomain(t.Name())
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       t.Name(),
		"subdomain":  subdomain,
		"target_url": "http://test-backend:8000",
		"client_ids": []string{client1ID},
		"client_configs": map[string]interface{}{
			client1ID: map[string]interface{}{
				"target_url": "http://test-backend:8000",
				"proxy_id":   proxyID,
			},
		},
		"auth_method": "none",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create P4 peer app should succeed")

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	require.NotZero(t, result.Data.ID, "app ID should not be zero")

	appID := result.Data.ID

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", appID))
	})

	return fmt.Sprintf("%d", appID), subdomain
}

// TestE2E_P4_Peer_HTTP_Echo 验证 P4 Peer 路径 HTTP echo 响应与直连一致
// 流程: client-1 LocalProxy → Peer → client-2 → test-backend
func TestE2E_P4_Peer_HTTP_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	direct := harness.NewDirectClient()

	body := strings.NewReader("hello-p4-peer")

	directResp := direct.Post(t, "/echo", body, "text/plain")
	body.Reset("hello-p4-peer")
	proxyResp := lpc1.DoPost(t, subdomain, "/echo", body, "text/plain")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}

// TestE2E_P4_Peer_WS_Echo 验证 P4 Peer 路径 WebSocket echo 消息一致性
func TestE2E_P4_Peer_WS_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	messages := []string{"hello-p4", "world-p4", "websocket-p4"}
	for _, msg := range messages {
		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		require.NoError(t, err, "write message %q", msg)

		_, reader, err := conn.Reader(ctx)
		require.NoError(t, err, "read echo for message %q", msg)

		var buf [4096]byte
		n, err := reader.Read(buf[:])
		require.NoError(t, err, "read echo data for message %q", msg)

		assert.Equal(t, msg, string(buf[:n]),
			"echo mismatch: sent %q, got %q", msg, string(buf[:n]))
	}
}

// =============================================================================
// P6 Tunnel+Peer 组合路径测试
// =============================================================================

// TestE2E_P6_TunnelPeer_HTTP_Echo 验证 P6 组合路径 HTTP echo 响应与直连一致
// 流程: 公网 → Server → 隧道 → client-1 → Peer → client-2 → test-backend
func TestE2E_P6_TunnelPeer_HTTP_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	// P6 从公网 (server:18080) 发请求，走隧道路径
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	direct := harness.NewDirectClient()

	body := strings.NewReader("hello-p6-tunnelpeer")

	directResp := direct.Post(t, "/echo", body, "text/plain")
	body.Reset("hello-p6-tunnelpeer")
	proxyResp := proxy.DoPost(t, subdomain, "/echo", body, "text/plain")

	harness.AssertResponseEqual(t, directResp, proxyResp)
}
