package suites

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// 深度 WebSocket 测试 — 所有 6 条代理路径
// 覆盖：binary message、concurrent messages、large message (1MB text frame)
// 本文件是 D-02 和 D-03 的唯一来源，不与 e2e_ws_sse_test.go 或
// e2e_p3_p6_test.go 中的已有 echo 测试重复。
// =============================================================================

// =============================================================================
// P1 __host__ 深度 WebSocket 测试
// =============================================================================

// TestE2E_P1_WS_BinaryMessage 验证 __host__ 路径 WebSocket 二进制消息传输
func TestE2E_P1_WS_BinaryMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

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

// TestE2E_P1_WS_ConcurrentMessages 验证 __host__ 路径 WebSocket 快速连续发送消息不丢失不乱序
func TestE2E_P1_WS_ConcurrentMessages(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

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

// TestE2E_P1_WS_LargeMessage 验证 __host__ 路径 WebSocket 1MB 文本帧传输完整性
func TestE2E_P1_WS_LargeMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupHostApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	// 构造恰好 1MB 的文本
	largeMsg := strings.Repeat("LargeWSMessage-P1!", 1<<20/18+1)
	largeMsg = largeMsg[:1<<20]

	err := conn.Write(ctx, websocket.MessageText, []byte(largeMsg))
	require.NoError(t, err, "write 1MB text message")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read 1MB echo")

	received, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	require.NoError(t, err, "read all echo data")

	assert.Equal(t, len(largeMsg), len(received),
		"large message length mismatch: sent %d bytes, got %d bytes", len(largeMsg), len(received))
	assert.Equal(t, largeMsg, string(received), "large message content mismatch")
}

// =============================================================================
// P2 Tunnel 深度 WebSocket 测试 — 大消息（binary 和 concurrent 已在 e2e_ws_sse_test.go）
// =============================================================================

// TestE2E_P2_WS_LargeMessage 验证隧道路径 WebSocket 1MB 文本帧传输完整性
func TestE2E_P2_WS_LargeMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	largeMsg := strings.Repeat("LargeWSMessage-P2!", 1<<20/18+1)
	largeMsg = largeMsg[:1<<20]

	err := conn.Write(ctx, websocket.MessageText, []byte(largeMsg))
	require.NoError(t, err, "write 1MB text message")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read 1MB echo")

	received, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	require.NoError(t, err, "read all echo data")

	assert.Equal(t, len(largeMsg), len(received),
		"large message length mismatch: sent %d bytes, got %d bytes", len(largeMsg), len(received))
	assert.Equal(t, largeMsg, string(received), "large message content mismatch")
}

// =============================================================================
// P3 LocalProxy 深度 WebSocket 测试
// =============================================================================

// TestE2E_P3_WS_BinaryMessage 验证 P3 路径 WebSocket 二进制消息传输
func TestE2E_P3_WS_BinaryMessage(t *testing.T) {
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

	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}

	err := conn.Write(ctx, websocket.MessageBinary, binaryData)
	require.NoError(t, err, "write binary message")

	msgType, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read binary echo")

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

// TestE2E_P3_WS_ConcurrentMessages 验证 P3 路径 WebSocket 快速连续发送消息不丢失不乱序
func TestE2E_P3_WS_ConcurrentMessages(t *testing.T) {
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

	const numMessages = 20

	sentMessages := make([]string, numMessages)
	for i := 0; i < numMessages; i++ {
		msg := fmt.Sprintf("msg-%03d", i)
		sentMessages[i] = msg
		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		require.NoError(t, err, "write message %d", i)
	}

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

// TestE2E_P3_WS_LargeMessage 验证 P3 路径 WebSocket 1MB 文本帧传输完整性
func TestE2E_P3_WS_LargeMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP3App(t)

	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	largeMsg := strings.Repeat("LargeWSMessage-P3!", 1<<20/18+1)
	largeMsg = largeMsg[:1<<20]

	err := conn.Write(ctx, websocket.MessageText, []byte(largeMsg))
	require.NoError(t, err, "write 1MB text message")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read 1MB echo")

	received, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	require.NoError(t, err, "read all echo data")

	assert.Equal(t, len(largeMsg), len(received),
		"large message length mismatch: sent %d bytes, got %d bytes", len(largeMsg), len(received))
	assert.Equal(t, largeMsg, string(received), "large message content mismatch")
}

// =============================================================================
// P4 Peer 深度 WebSocket 测试
// =============================================================================

// TestE2E_P4_WS_BinaryMessage 验证 P4 Peer 路径 WebSocket 二进制消息传输
func TestE2E_P4_WS_BinaryMessage(t *testing.T) {
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

	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}

	err := conn.Write(ctx, websocket.MessageBinary, binaryData)
	require.NoError(t, err, "write binary message")

	msgType, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read binary echo")

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

// TestE2E_P4_WS_ConcurrentMessages 验证 P4 Peer 路径 WebSocket 快速连续发送消息不丢失不乱序
func TestE2E_P4_WS_ConcurrentMessages(t *testing.T) {
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

	const numMessages = 20

	sentMessages := make([]string, numMessages)
	for i := 0; i < numMessages; i++ {
		msg := fmt.Sprintf("msg-%03d", i)
		sentMessages[i] = msg
		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		require.NoError(t, err, "write message %d", i)
	}

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

// TestE2E_P4_WS_LargeMessage 验证 P4 Peer 路径 WebSocket 1MB 文本帧传输完整性
func TestE2E_P4_WS_LargeMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 15*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	largeMsg := strings.Repeat("LargeWSMessage-P4!", 1<<20/18+1)
	largeMsg = largeMsg[:1<<20]

	err := conn.Write(ctx, websocket.MessageText, []byte(largeMsg))
	require.NoError(t, err, "write 1MB text message")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read 1MB echo")

	received, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	require.NoError(t, err, "read all echo data")

	assert.Equal(t, len(largeMsg), len(received),
		"large message length mismatch: sent %d bytes, got %d bytes", len(largeMsg), len(received))
	assert.Equal(t, largeMsg, string(received), "large message content mismatch")
}

// =============================================================================
// P5 Relay 深度 WebSocket 测试
// =============================================================================

// TestE2E_P5_WS_BinaryMessage 验证 P5 Relay 路径 WebSocket 二进制消息传输
func TestE2E_P5_WS_BinaryMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP5App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}

	err := conn.Write(ctx, websocket.MessageBinary, binaryData)
	require.NoError(t, err, "write binary message")

	msgType, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read binary echo")

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

// TestE2E_P5_WS_ConcurrentMessages 验证 P5 Relay 路径 WebSocket 快速连续发送消息不丢失不乱序
func TestE2E_P5_WS_ConcurrentMessages(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP5App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	const numMessages = 20

	sentMessages := make([]string, numMessages)
	for i := 0; i < numMessages; i++ {
		msg := fmt.Sprintf("msg-%03d", i)
		sentMessages[i] = msg
		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		require.NoError(t, err, "write message %d", i)
	}

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

// TestE2E_P5_WS_LargeMessage 验证 P5 Relay 路径 WebSocket 1MB 文本帧传输完整性
func TestE2E_P5_WS_LargeMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP5App(t)

	lpc1 := localProxyClient1()
	waitForAppSync(t, lpc1, subdomain, 10*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	largeMsg := strings.Repeat("LargeWSMessage-P5!", 1<<20/18+1)
	largeMsg = largeMsg[:1<<20]

	err := conn.Write(ctx, websocket.MessageText, []byte(largeMsg))
	require.NoError(t, err, "write 1MB text message")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read 1MB echo")

	received, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	require.NoError(t, err, "read all echo data")

	assert.Equal(t, len(largeMsg), len(received),
		"large message length mismatch: sent %d bytes, got %d bytes", len(largeMsg), len(received))
	assert.Equal(t, largeMsg, string(received), "large message content mismatch")
}

// =============================================================================
// P6 TunnelPeer 深度 WebSocket 测试
// P6 复用 P4 的 peer 配置，但通过 server 入口 (dialProxyWS) 而非 LocalProxy
// =============================================================================

// TestE2E_P6_WS_BinaryMessage 验证 P6 TunnelPeer 路径 WebSocket 二进制消息传输
func TestE2E_P6_WS_BinaryMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t) // P6 复用 P4 的 peer 配置

	conn, cancel := dialProxyWS(t, subdomain, "/ws") // 通过 server 入口
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}

	err := conn.Write(ctx, websocket.MessageBinary, binaryData)
	require.NoError(t, err, "write binary message")

	msgType, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read binary echo")

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

// TestE2E_P6_WS_ConcurrentMessages 验证 P6 TunnelPeer 路径 WebSocket 快速连续发送消息不丢失不乱序
func TestE2E_P6_WS_ConcurrentMessages(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t) // P6 复用 P4 的 peer 配置

	conn, cancel := dialProxyWS(t, subdomain, "/ws") // 通过 server 入口
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	const numMessages = 20

	sentMessages := make([]string, numMessages)
	for i := 0; i < numMessages; i++ {
		msg := fmt.Sprintf("msg-%03d", i)
		sentMessages[i] = msg
		err := conn.Write(ctx, websocket.MessageText, []byte(msg))
		require.NoError(t, err, "write message %d", i)
	}

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

// TestE2E_P6_WS_LargeMessage 验证 P6 TunnelPeer 路径 WebSocket 1MB 文本帧传输完整性
func TestE2E_P6_WS_LargeMessage(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupP4App(t) // P6 复用 P4 的 peer 配置

	conn, cancel := dialProxyWS(t, subdomain, "/ws") // 通过 server 入口
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	largeMsg := strings.Repeat("LargeWSMessage-P6!", 1<<20/18+1)
	largeMsg = largeMsg[:1<<20]

	err := conn.Write(ctx, websocket.MessageText, []byte(largeMsg))
	require.NoError(t, err, "write 1MB text message")

	_, reader, err := conn.Reader(ctx)
	require.NoError(t, err, "read 1MB echo")

	received, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	require.NoError(t, err, "read all echo data")

	assert.Equal(t, len(largeMsg), len(received),
		"large message length mismatch: sent %d bytes, got %d bytes", len(largeMsg), len(received))
	assert.Equal(t, largeMsg, string(received), "large message content mismatch")
}
