package suites

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// WebSocket Close 竞争问题复现测试（TDD RED 阶段）
//
// 复现 Bug 1：客户端主动发 Close frame → nhooyr 自动回应 → defer 再次发 Close
// 导致浏览器报 "Close received after close"
//
// 预期：所有测试 FAIL，证明 Bug 真实存在。
// Fix 之后，所有测试应 PASS。
// =============================================================================

// backendCloseAll 通过直连后端调用 POST /ws/close-all，关闭所有活跃 WS 连接
func backendCloseAll(t *testing.T) int {
	t.Helper()
	resp, err := http.Post(harness.DirectBackendURL+"/ws/close-all", "application/json", nil)
	require.NoError(t, err, "POST /ws/close-all failed")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /ws/close-all should return 200")

	var result struct {
		Status string `json:"status"`
		Closed int    `json:"closed"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "decode /ws/close-all response")
	t.Logf("backendCloseAll: closed %d connections", result.Closed)
	return result.Closed
}

// readEchoMessage 发送一条消息并等待 echo 回复，验证连接正常工作
func readEchoMessage(t *testing.T, conn *websocket.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err, "write echo message")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read echo response")

	data, err := io.ReadAll(io.LimitReader(reader, 4096))
	require.NoError(t, err, "read echo data")
	assert.Equal(t, msg, string(data), "echo message mismatch")
}

// =============================================================================
// TestE2E_P2_WS_ClientClose_NoDoubleFrame
//
// Bug 1 复现：客户端主动发 Close frame，验证服务端不发两次 Close frame。
//
// Bug 触发路径：
//   浏览器/客户端 发 Close frame
//   → nhooyr handleControl 自动回应 Close frame（c.closing 未设 true）
//   → proxy 读 goroutine 退出 → defer 发 TypeWSClose
//   → proxyWebSocket 函数返回
//   → defer ws.Close(StatusNormalClosure) 触发
//   → casClosing() 返回 true（c.closing 仍 false）
//   → closeHandshake 再次发 Close frame → 浏览器报错
//
// FAIL 表现：conn.Close() 超时（>5s）或返回非预期错误。
// =============================================================================
func TestE2E_P2_WS_ClientClose_NoDoubleFrame(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 建立 P2 WS 连接（隧道路径）
	conn, closeCancel := dialProxyWS(t, subdomain, "/ws")
	defer closeCancel()

	// 发一条 echo 消息，验证连接正常
	readEchoMessage(t, conn, "ping-before-close")

	// 测量 conn.Close() 的耗时，bug 存在时会接近 5 秒超时
	closeStart := time.Now()
	closeCtx, closeCtxCancel := context.WithTimeout(ctx, 6*time.Second)
	defer closeCtxCancel()
	_ = closeCtx

	// 客户端主动发 Close frame
	// Bug 存在时：服务端 defer 再次发 Close frame → nhooyr 等待另一次握手 → 超时
	closeErr := conn.Close(websocket.StatusNormalClosure, "")
	closeDur := time.Since(closeStart)

	t.Logf("conn.Close() duration: %v, err: %v", closeDur, closeErr)

	// 验证 Close 握手应快速完成（< 3 秒）
	// Bug 存在时：Close() 超时（接近 5 秒）或返回 CloseError
	assert.Less(t, closeDur, 3*time.Second,
		"conn.Close() took %v — server sent a second Close frame causing the close handshake to hang (Bug 1)", closeDur)

	// 验证 Close 错误：应为 nil 或 StatusNormalClosure（正常完成）
	if closeErr != nil {
		var closeError websocket.CloseError
		if errors.As(closeErr, &closeError) {
			assert.Equal(t, websocket.StatusNormalClosure, closeError.Code,
				"Close() returned unexpected CloseError code — server may have sent a second Close frame: %v", closeErr)
		} else {
			assert.NoError(t, closeErr, "conn.Close() should not return a non-CloseError")
		}
	}
}

// =============================================================================
// TestE2E_P2_WS_BackendClose_NoDoubleFrame
//
// 模拟后端主动断开 WS 连接，验证代理正确传播关闭，
// 且客户端能在合理时间内收到关闭通知（不 hang）。
// =============================================================================
func TestE2E_P2_WS_BackendClose_NoDoubleFrame(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	conn, closeCancel := dialProxyWS(t, subdomain, "/ws")
	defer closeCancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 发一条 echo 消息，确认连接正常
	readEchoMessage(t, conn, "ping-before-backend-close")

	// 后端主动关闭所有 WS 连接
	closed := backendCloseAll(t)
	assert.GreaterOrEqual(t, closed, 1, "should have closed at least one WS connection")

	// 等待代理侧连接关闭（conn.Read 应返回 CloseError 或 EOF）
	closeStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 持续 Read 直到收到关闭信号
	for {
		_, _, err := conn.Reader(ctx)
		if err != nil {
			dur := time.Since(closeStart)
			t.Logf("conn.Reader() returned after %v: %v", dur, err)

			// 关闭传播不应超过 3 秒
			assert.Less(t, dur, 3*time.Second,
				"took %v to propagate backend close to proxy client (Bug: TypeWSClose not delivered)", dur)
			break
		}
	}

	// 验证 ctx 没有超时（意味着连接在 5 秒内正常关闭）
	assert.NoError(t, ctx.Err(),
		"context timed out — proxy did not propagate backend WS close within 5s")
}

// =============================================================================
// TestE2E_P2_WS_RapidConnectDisconnect
//
// 快速建立和断开 WS 连接，检测有无 goroutine 泄露或 panic。
// Bug 存在时：某次 Close() 可能超时导致整体超时。
// =============================================================================
func TestE2E_P2_WS_RapidConnectDisconnect(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	const iterations = 5

	for i := 0; i < iterations; i++ {
		t.Run("", func(t *testing.T) {
			conn, closeCancel := dialProxyWS(t, subdomain, "/ws")
			defer closeCancel()

			// 每次循环发一条消息确认连接正常
			msg := "rapid-test-msg"
			ctx5s, cancel5s := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel5s()

			err := conn.Write(ctx5s, websocket.MessageText, []byte(msg))
			require.NoError(t, err, "iteration %d: write message", i)

			_, reader, err := conn.Reader(ctx5s)
			require.NoError(t, err, "iteration %d: read echo", i)
			io.Copy(io.Discard, reader) //nolint:errcheck

			// 立即关闭 — bug 存在时可能超时
			closeStart := time.Now()
			closeErr := conn.Close(websocket.StatusNormalClosure, "")
			closeDur := time.Since(closeStart)

			t.Logf("iteration %d: Close() took %v, err: %v", i, closeDur, closeErr)

			assert.Less(t, closeDur, 3*time.Second,
				"iteration %d: Close() took %v — possible double Close frame bug", i, closeDur)
		})
	}
}

// =============================================================================
// TestE2E_P1_WS_ClientClose_NoDoubleFrame
//
// P1 路径（__host__，local_ws.go）对比测试。
// 验证 P1 路径是否也存在 double Close frame 问题。
// =============================================================================
func TestE2E_P1_WS_ClientClose_NoDoubleFrame(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	conn, closeCancel := dialProxyWS(t, subdomain, "/ws")
	defer closeCancel()

	// 发一条 echo 消息，验证连接正常
	readEchoMessage(t, conn, "ping-before-close-p1")

	// 测量 conn.Close() 的耗时
	closeStart := time.Now()
	closeErr := conn.Close(websocket.StatusNormalClosure, "")
	closeDur := time.Since(closeStart)

	t.Logf("P1 conn.Close() duration: %v, err: %v", closeDur, closeErr)

	// P1 路径如果也有 double close，Close() 也会超时
	assert.Less(t, closeDur, 3*time.Second,
		"P1 conn.Close() took %v — possible double Close frame in local_ws.go", closeDur)

	if closeErr != nil {
		var closeError websocket.CloseError
		if errors.As(closeErr, &closeError) {
			assert.Equal(t, websocket.StatusNormalClosure, closeError.Code,
				"P1 Close() returned unexpected CloseError: %v", closeErr)
		} else {
			assert.NoError(t, closeErr, "P1 conn.Close() should not return a non-CloseError")
		}
	}
}

// =============================================================================
// TestE2E_P2_WS_BackendClose_DirectConfirm
//
// 直接连接后端（不经过代理）验证 /ws/close-all 端点工作正常。
// 这是辅助测试，确认 test-backend 端点正确实现。
// =============================================================================
func TestE2E_P2_WS_BackendClose_DirectConfirm(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	// 直连后端建立 WS 连接
	conn, closeCancel := dialDirectWS(t, "/ws")
	defer closeCancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 确认连接正常
	ctx := context.Background()
	err := conn.Write(ctx, websocket.MessageText, []byte("hello"))
	require.NoError(t, err, "direct write")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "direct read echo")
	io.Copy(io.Discard, reader) //nolint:errcheck

	// 关闭所有后端连接
	closed := backendCloseAll(t)
	assert.GreaterOrEqual(t, closed, 1, "should have closed at least one connection")

	// 连接应在 3 秒内关闭
	closeStart := time.Now()
	readCtx, readCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer readCancel()

	for {
		_, _, err := conn.Reader(readCtx)
		if err != nil {
			dur := time.Since(closeStart)
			t.Logf("direct conn closed after %v: %v", dur, err)
			assert.Less(t, dur, 3*time.Second, "direct conn should close quickly after /ws/close-all")
			break
		}
	}

	// 验证没有超时
	assert.NoError(t, readCtx.Err(), "direct conn close should not timeout")
}
