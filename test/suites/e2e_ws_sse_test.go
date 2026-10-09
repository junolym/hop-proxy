package suites

import (
	"bufio"
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
// WebSocket 辅助函数
// =============================================================================

// dialProxyWS 通过 HopProxy 代理建立 WebSocket 连接
// subdomain 用于构造 Host header: {subdomain}.{proxyDomain}:{serverPort}
func dialProxyWS(t *testing.T, subdomain, path string) (*websocket.Conn, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	// 解析 serverURL 提取端口
	serverHost := "localhost:18080"
	wsURL := fmt.Sprintf("ws://%s%s", serverHost, path)

	// Host header 用于 HopProxy 子域名路由
	// 注意：Go 的 net/http 会忽略 Header 中的 "Host" 键，必须通过
	// HTTPClient 的 Transport 设置 req.Host 才能生效
	hostHeader := fmt.Sprintf("%s.%s:18080", subdomain, proxyDomain)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: &hostHeaderTransport{
				host:    hostHeader,
				wrapped: http.DefaultTransport,
			},
		},
	})
	require.NoError(t, err, "WebSocket dial via proxy failed (Host=%s)", hostHeader)

	return conn, cancel
}

// hostHeaderTransport 是一个 http.RoundTripper，在请求发送前设置 req.Host。
// 这是必要的，因为 Go 的 net/http 包会忽略 Header 中的 "Host" 键，
// 必须通过 req.Host 字段才能正确设置 Host header。
type hostHeaderTransport struct {
	host    string
	wrapped http.RoundTripper
}

func (t *hostHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Host = t.host
	return t.wrapped.RoundTrip(req)
}

// dialDirectWS 直接连到 test-backend 的 WebSocket
func dialDirectWS(t *testing.T, path string) (*websocket.Conn, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	wsURL := fmt.Sprintf("ws://localhost:18081%s", path)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err, "WebSocket dial direct failed")

	return conn, cancel
}

// =============================================================================
// SSE 辅助函数
// =============================================================================

// parseSSEEvents 从 HTTP 响应中解析 SSE 事件
// 返回所有 "data: ..." 行的内容
func parseSSEEvents(t *testing.T, resp *http.Response) []string {
	t.Helper()
	defer resp.Body.Close()

	var events []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 修复根因 A：1MB token 上限，避免大事件被 ErrTooLong 截断
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			events = append(events, strings.TrimPrefix(line, "data: "))
		}
	}
	require.NoError(t, scanner.Err(), "scanning SSE response")
	return events
}

// =============================================================================
// P1 Server-Local WebSocket 测试（__host__ 模式）
// =============================================================================

// TestE2E_P1_WS_Echo 验证 __host__ 路径 WebSocket echo 消息一致性
func TestE2E_P1_WS_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	messages := []string{"hello-p1", "world-p1", "websocket-p1"}
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
// P2 Tunnel WebSocket 测试（经典隧道模式）
// =============================================================================

// TestE2E_P2_WS_Echo 验证隧道路径 WebSocket echo 消息一致性
func TestE2E_P2_WS_Echo(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	messages := []string{"msg-1", "msg-2", "msg-3", "msg-4", "msg-5"}
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

// TestE2E_P2_WS_BinaryMessage 验证隧道路径 WebSocket 二进制消息传输
func TestE2E_P2_WS_BinaryMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	// 构造 256 字节二进制数据（0x00-0xFF 遍历）
	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}

	err := conn.Write(ctx, websocket.MessageBinary, binaryData)
	require.NoError(t, err, "write binary message")

	msgType, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read binary echo")

	// 读取全部返回数据
	var received []byte
	buf := make([]byte, 512)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			received = append(received, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	assert.Equal(t, websocket.MessageBinary, msgType, "message type should be binary")
	assert.Equal(t, binaryData, received,
		"binary data mismatch: sent %d bytes, got %d bytes", len(binaryData), len(received))
}

// TestE2E_P2_WS_ConcurrentMessages 验证隧道路径 WebSocket 快速连续发送消息不丢失不乱序
func TestE2E_P2_WS_ConcurrentMessages(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	const numMessages = 20

	// 快速连续发送 20 条消息
	sentMessages := make([]string, numMessages)
	for i := 0; i < numMessages; i++ {
		msg := fmt.Sprintf("msg-%03d", i)
		sentMessages[i] = msg
		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		require.NoError(t, err, "write message %d", i)
	}

	// 逐条读取 echo 回复，验证顺序和内容完整
	for i, expected := range sentMessages {
		_, reader, err := conn.Reader(ctx)
		require.NoError(t, err, "read echo for message %d", i)

		var buf [4096]byte
		n, err := reader.Read(buf[:])
		require.NoError(t, err, "read echo data for message %d", i)

		assert.Equal(t, expected, string(buf[:n]),
			"concurrent echo mismatch at index %d: expected %q, got %q", i, expected, string(buf[:n]))
	}
}

// =============================================================================
// P1 Server-Local SSE 测试（__host__ 模式）
// =============================================================================

// TestE2E_P1_SSE_Stream 验证 __host__ 路径 SSE 流式事件完整到达
func TestE2E_P1_SSE_Stream(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

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

// =============================================================================
// P2 Tunnel SSE 测试（经典隧道模式）
// =============================================================================

// TestE2E_P2_SSE_Stream 验证隧道路径 SSE 流式事件完整到达
func TestE2E_P2_SSE_Stream(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

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
