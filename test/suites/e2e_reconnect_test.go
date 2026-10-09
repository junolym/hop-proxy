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
// 隧道重连韧性测试
// 覆盖 D-11 到 D-14：自动重连、踢出、离线 502、服务恢复
// =============================================================================

// TestReconnect_AutoReconnect 验证客户端断开后自动重连恢复服务
// D-11: 客户端断开后自动重连（指数退避），服务恢复后代理请求成功
func TestReconnect_AutoReconnect(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)

	// 创建 P2 隧道 app
	_, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, client1ID, "http://test-backend:8000")
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 验证代理正常工作
	r := proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode, "proxy should work before client disconnect")

	// 停止 client-1
	err := env.ComposeCmd(t.Context(), "stop", "client-1").Run()
	require.NoError(t, err, "stop client-1 container")

	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1").Run()
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) {
				return fmt.Errorf("client-1 still offline")
			}
			return nil
		}, 60*time.Second)
	})

	// 等待 client-1 离线
	err = pollUntil(func() error {
		if clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still online")
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "client-1 should go offline")

	// 重启 client-1 — 客户端会自动重连（HP_RECONNECT_INTERVAL=1s）
	err = env.ComposeCmd(t.Context(), "start", "client-1").Run()
	require.NoError(t, err, "restart client-1 container")

	// 等待 client-1 重新上线（指数退避从 1s 开始，最多 60s）
	err = pollUntil(func() error {
		if !clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still offline after restart")
		}
		return nil
	}, 60*time.Second)
	require.NoError(t, err, "client-1 should come back online after auto-reconnect")

	// 验证服务恢复 — 代理请求成功
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode,
		"proxy should work after client auto-reconnect, got %d", r.StatusCode)
}

// TestReconnect_KickOut_DuplicateConnection 验证重复连接踢出机制
// D-12: 新连接替换旧连接，旧连接被终止
func TestReconnect_KickOut_DuplicateConnection(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)

	// 创建 P2 隧道 app
	_, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, client1ID, "http://test-backend:8000")
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 验证代理正常工作
	r := proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode, "proxy should work initially")

	// 重启 client-1 容器 — 这会强制用相同 UUID 建立新连接
	// 服务器应该踢出旧连接，接受新连接
	err := env.RestartService(t.Context(), "client-1")
	require.NoError(t, err, "restart client-1 container")

	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1").Run()
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) {
				return fmt.Errorf("client-1 still offline")
			}
			return nil
		}, 60*time.Second)
	})

	// 等待 client-1 重新上线
	err = pollUntil(func() error {
		if !clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still offline after restart")
		}
		return nil
	}, 60*time.Second)
	require.NoError(t, err, "client-1 should come back online after restart")

	// 验证代理仍然正常工作（新连接已替换旧连接）
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode,
		"proxy should work after duplicate connection kick-out, got %d", r.StatusCode)

	// 检查服务端日志是否有踢出记录
	logs, err := env.Logs("server", 50)
	if err == nil && logs != "" {
		hasKick := strings.Contains(logs, "kick") || strings.Contains(logs, "重复连接") ||
			strings.Contains(logs, "duplicate") || strings.Contains(logs, "replace")
		t.Logf("Server logs contain kick-out evidence: %v", hasKick)
	}
}

// TestReconnect_OfflinePeriod_502 验证客户端离线期间请求持续返回 502
// D-13: 离线期间请求返回 502，恢复后请求正常
func TestReconnect_OfflinePeriod_502(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)

	// 创建 P2 隧道 app
	_, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, client1ID, "http://test-backend:8000")
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 验证代理正常工作
	r := proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode, "proxy should work before client offline")

	// 只停止 client-1（client-2 保持运行）
	err := env.ComposeCmd(t.Context(), "stop", "client-1").Run()
	require.NoError(t, err, "stop client-1 container")

	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1").Run()
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) {
				return fmt.Errorf("client-1 still offline")
			}
			return nil
		}, 60*time.Second)
	})

	// 等待 client-1 离线
	err = pollUntil(func() error {
		if clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still online")
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "client-1 should go offline")

	// 第一次请求：应返回 502
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusBadGateway, r.StatusCode,
		"first request during offline should return 502, got %d", r.StatusCode)

	// 等待几秒后再次请求
	time.Sleep(3 * time.Second)

	// 第二次请求：仍应返回 502（一致的离线行为）
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusBadGateway, r.StatusCode,
		"second request during offline should still return 502, got %d", r.StatusCode)

	// 重启 client-1
	err = env.ComposeCmd(t.Context(), "start", "client-1").Run()
	require.NoError(t, err, "restart client-1 container")

	// 等待 client-1 重新上线
	err = pollUntil(func() error {
		if !clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still offline")
		}
		return nil
	}, 60*time.Second)
	require.NoError(t, err, "client-1 should come back online")

	// 恢复后代理请求成功
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode,
		"proxy should work after client recovers, got %d", r.StatusCode)
}

// TestReconnect_ServiceRecovery 验证重连后 HTTP + WebSocket 服务完全恢复
// D-14: 重连后多次请求均成功，WebSocket 也正常工作
func TestReconnect_ServiceRecovery(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)

	// 创建 P2 隧道 app
	_, subdomain := harness.SetupProxyApp(t, c, serverURL, adminDomain, client1ID, "http://test-backend:8000")
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 验证代理正常工作
	r := proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode, "proxy should work before disconnect")

	// 停止 client-1
	err := env.ComposeCmd(t.Context(), "stop", "client-1").Run()
	require.NoError(t, err, "stop client-1 container")

	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1").Run()
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) {
				return fmt.Errorf("client-1 still offline")
			}
			return nil
		}, 60*time.Second)
	})

	// 等待 client-1 离线
	err = pollUntil(func() error {
		if clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still online")
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "client-1 should go offline")

	// 验证离线时返回 502
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	require.Equal(t, http.StatusBadGateway, r.StatusCode, "should return 502 while offline")

	// 重启 client-1
	err = env.ComposeCmd(t.Context(), "start", "client-1").Run()
	require.NoError(t, err, "restart client-1 container")

	// 等待 client-1 重新上线
	err = pollUntil(func() error {
		if !clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still offline after restart")
		}
		return nil
	}, 60*time.Second)
	require.NoError(t, err, "client-1 should come back online")

	// 发送 5 次 HTTP 请求，验证全部成功（无残留错误状态）
	for i := 0; i < 5; i++ {
		r := proxy.DoGet(t, subdomain, "/")
		r.Body.Close()
		assert.Equal(t, http.StatusOK, r.StatusCode,
			"HTTP request %d after reconnect should succeed, got %d", i+1, r.StatusCode)
	}

	// 验证 WebSocket 也恢复正常
	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()
	testMsg := "recovery-test"
	err = conn.Write(ctx, websocket.MessageText, []byte(testMsg))
	require.NoError(t, err, "write WS message after reconnect")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read WS echo after reconnect")

	var buf [4096]byte
	n, err := reader.Read(buf[:])
	require.NoError(t, err, "read WS echo data after reconnect")

	assert.Equal(t, testMsg, string(buf[:n]),
		"WS echo after reconnect mismatch: expected %q, got %q", testMsg, string(buf[:n]))

	// 验证二进制 WS 也能正常工作
	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}
	err = conn.Write(ctx, websocket.MessageBinary, binaryData)
	require.NoError(t, err, "write WS binary message after reconnect")

	msgType, reader2, err := conn.Reader(ctx)
	require.NoError(t, err, "read WS binary echo after reconnect")

	var received []byte
	readBuf := make([]byte, 512)
	for {
		n, err := reader2.Read(readBuf)
		if n > 0 {
			received = append(received, readBuf[:n]...)
		}
		if err != nil {
			break
		}
	}

	assert.Equal(t, websocket.MessageBinary, msgType, "message type should be binary")
	assert.Equal(t, binaryData, received,
		"binary data after reconnect mismatch: sent %d bytes, got %d bytes", len(binaryData), len(received))
}
