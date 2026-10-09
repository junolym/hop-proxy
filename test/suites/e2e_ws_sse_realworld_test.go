package suites

// =============================================================================
// 真实场景复现测试 — WS 二进制流 & SSE 极短流
//
// 针对实际应用（VSCode 网页版）报告的问题：
//   1. WebSocket done channel double-close：高频二进制帧传输时连接断开
//   2. SSE 偶现截断：10 次刷新约 1-2 次数据不完整
//
// 与现有测试的区别：
//   - WS：不等 echo，连续发帧（全双工），模拟 Language Server Protocol 通信模式
//   - SSE：interval=0ms 极短流，repeat 50 次，触发 r.Context().Done() 与 StreamEnd 竞争
// =============================================================================

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// P2 Tunnel WebSocket 真实场景测试
// =============================================================================

// TestE2E_P2_WS_BinaryStream_NoWait 验证隧道路径 WS 全双工二进制流：
// 不等 echo，连续发送 N 帧，再批量接收所有 echo，验证顺序和内容完整
// 复现场景：VSCode Language Server 连接后立即发送多帧协商消息
func TestE2E_P2_WS_BinaryStream_NoWait(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()
	const numFrames = 50
	const frameSize = 512

	// 构造 50 帧，每帧前4字节为序号，其余为递增模式
	frames := make([][]byte, numFrames)
	for i := range frames {
		b := make([]byte, frameSize)
		b[0] = byte(i >> 24)
		b[1] = byte(i >> 16)
		b[2] = byte(i >> 8)
		b[3] = byte(i)
		for j := 4; j < frameSize; j++ {
			b[j] = byte((i + j) & 0xFF)
		}
		frames[i] = b
	}

	// 先批量发送所有帧（不等 echo）
	for i, frame := range frames {
		err := conn.Write(ctx, websocket.MessageBinary, frame)
		require.NoError(t, err, "write binary frame %d", i)
	}

	// 再批量接收所有 echo，验证顺序和内容
	for i := 0; i < numFrames; i++ {
		readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
		msgType, reader, err := conn.Reader(readCtx)
		if err != nil {
			readCancel()
			require.NoError(t, err, "read echo frame %d", i)
		}
		assert.Equal(t, websocket.MessageBinary, msgType,
			"frame %d: expected binary, got text", i)

		// 注意：readCancel 必须在读完数据后调用，因为 reader 依赖 readCtx
		received, err := io.ReadAll(io.LimitReader(reader, int64(frameSize)+1))
		readCancel()
		require.NoError(t, err, "readall echo frame %d", i)
		assert.Equal(t, frames[i], received,
			"frame %d content mismatch: sent %d bytes, got %d bytes", i, len(frames[i]), len(received))
	}
}

// TestE2E_P2_WS_BinaryStream_HighFreq 验证隧道路径 WS 高频交替发/收：
// 每帧发送后立即等 echo，但帧间隔为 0（不加 sleep），模拟高吞吐会话
// 复现场景：VSCode Remote SSH 代理连接中的高频控制帧
func TestE2E_P2_WS_BinaryStream_HighFreq(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()
	const numFrames = 200
	const frameSize = 128

	for i := 0; i < numFrames; i++ {
		frame := make([]byte, frameSize)
		frame[0] = byte(i >> 8)
		frame[1] = byte(i)
		for j := 2; j < frameSize; j++ {
			frame[j] = byte((i*7 + j) & 0xFF)
		}

		// 发送
		writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
		err := conn.Write(writeCtx, websocket.MessageBinary, frame)
		writeCancel()
		require.NoError(t, err, "write frame %d", i)

		// 立即读取 echo
		readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
		msgType, reader, err := conn.Reader(readCtx)
		if err != nil {
			readCancel()
			require.NoError(t, err, "read echo frame %d", i)
		}
		assert.Equal(t, websocket.MessageBinary, msgType, "frame %d: type should be binary", i)

		// 注意：readCancel 必须在读完数据后调用
		received, err := io.ReadAll(io.LimitReader(reader, int64(frameSize)+1))
		readCancel()
		require.NoError(t, err, "readall echo frame %d", i)
		assert.Equal(t, frame, received,
			"frame %d content mismatch", i)
	}
}

// TestE2E_P2_WS_BinaryStream_ServerPush 验证隧道路径服务端主动推送二进制帧：
// 服务端先推 N 帧，客户端读取后验证顺序和内容，再发送 N 帧 echo
// 复现场景：服务端推送场景（如通知、状态更新）中的二进制数据完整性
func TestE2E_P2_WS_BinaryStream_ServerPush(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	const numFrames = 50
	const frameSize = 256

	// 连接到 /ws/push 端点（服务端先推送）
	conn, cancel := dialProxyWS(t, subdomain, fmt.Sprintf("/ws/push?count=%d&size=%d&type=binary", numFrames, frameSize))
	defer cancel()
	conn.SetReadLimit(50 * 1024 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := context.Background()

	// 接收服务端推送的 N 帧，验证顺序和内容
	receivedFrames := make([][]byte, 0, numFrames)
	for i := 0; i < numFrames; i++ {
		readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
		msgType, reader, err := conn.Reader(readCtx)
		if err != nil {
			readCancel()
			require.NoError(t, err, "read server-push frame %d", i)
		}
		assert.Equal(t, websocket.MessageBinary, msgType,
			"server-push frame %d: expected binary", i)

		// 注意：readCancel 必须在读完数据后调用
		data, err := io.ReadAll(io.LimitReader(reader, int64(frameSize)+1))
		readCancel()
		require.NoError(t, err, "readall server-push frame %d", i)
		require.Len(t, data, frameSize,
			"server-push frame %d: expected %d bytes, got %d", i, frameSize, len(data))

		// 验证序号（前4字节）
		seqNum := int(data[0])<<24 | int(data[1])<<16 | int(data[2])<<8 | int(data[3])
		assert.Equal(t, i, seqNum,
			"server-push frame %d: sequence number mismatch (expected %d, got %d)", i, i, seqNum)

		receivedFrames = append(receivedFrames, data)
	}

	// 发送回 echo（服务端 /ws/push 需要接收这些 echo 才能关闭连接）
	for i, frame := range receivedFrames {
		writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
		err := conn.Write(writeCtx, websocket.MessageBinary, frame)
		writeCancel()
		require.NoError(t, err, "echo frame %d back", i)
	}
}

// TestE2E_P2_WS_BinaryStream_ConcurrentConns 验证多路并发 WS 连接同时传输二进制帧
// 复现场景：多个 VSCode 标签页或窗口同时使用代理
func TestE2E_P2_WS_BinaryStream_ConcurrentConns(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	const numConns = 5
	const framesPerConn = 30
	const frameSize = 256

	type result struct {
		idx    int
		failed int
		err    string
	}
	results := make([]result, numConns)
	var wg sync.WaitGroup

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			conn, cancel := dialProxyWS(t, subdomain, "/ws")
			defer cancel()
			conn.SetReadLimit(50 * 1024 * 1024)
			defer conn.Close(websocket.StatusNormalClosure, "")

			ctx := context.Background()
			failCount := 0

			for j := 0; j < framesPerConn; j++ {
				frame := make([]byte, frameSize)
				frame[0] = byte(idx)
				frame[1] = byte(j >> 8)
				frame[2] = byte(j)
				for k := 3; k < frameSize; k++ {
					frame[k] = byte((idx*31 + j*7 + k) & 0xFF)
				}

				writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Write(writeCtx, websocket.MessageBinary, frame)
				writeCancel()
				if err != nil {
					results[idx] = result{idx: idx, err: fmt.Sprintf("conn %d frame %d write: %v", idx, j, err)}
					return
				}

			readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
			msgType, reader, err := conn.Reader(readCtx)
			if err != nil {
				readCancel()
				results[idx] = result{idx: idx, err: fmt.Sprintf("conn %d frame %d read: %v", idx, j, err)}
				return
			}

			// 注意：readCancel 必须在读完数据后调用
			received, err := io.ReadAll(io.LimitReader(reader, int64(frameSize)+1))
			readCancel()
			if err != nil || msgType != websocket.MessageBinary || len(received) != frameSize {
				failCount++
			}
			}

			results[idx] = result{idx: idx, failed: failCount}
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != "" {
			t.Errorf("并发 WS 连接 #%d: %s", r.idx, r.err)
			continue
		}
		assert.Equal(t, 0, r.failed,
			"并发 WS 连接 #%d: %d/%d 帧传输异常", r.idx, r.failed, framesPerConn)
	}
}

// =============================================================================
// P2 Tunnel SSE 真实场景测试
// =============================================================================

// TestE2E_P2_SSE_ZeroInterval_Repeat 验证极短 SSE 流（interval=0ms）连续请求不截断
// 复现场景：用户快速刷新页面，SSE 响应几乎瞬间结束
// interval=0ms 使 backend 发完所有事件后立即关闭，r.Context().Done() 与 StreamEnd 竞争窗口最大
func TestE2E_P2_SSE_ZeroInterval_Repeat(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	const iterations = 50
	const eventCount = 5
	failCount := 0
	failDetails := []string{}

	for i := 0; i < iterations; i++ {
		resp := proxy.DoGet(t, subdomain, fmt.Sprintf("/stream?count=%d&interval=0ms", eventCount))
		require.Equal(t, 200, resp.StatusCode,
			"iteration %d: HTTP status should be 200", i)

		events := parseSSEEvents(t, resp)
		expected := eventCount + 1 // N data + 1 done
		if len(events) != expected {
			failCount++
			failDetails = append(failDetails,
				fmt.Sprintf("#%d: expected %d events, got %d", i, expected, len(events)))
		}
	}

	if failCount > 0 {
		for _, d := range failDetails {
			t.Logf("SSE 截断: %s", d)
		}
	}
	assert.Equal(t, 0, failCount,
		"interval=0ms SSE: %d/%d 次截断（expected 0）", failCount, iterations)
}

// TestE2E_P2_SSE_ZeroInterval_Concurrent 验证极短 SSE 流并发请求不截断
// 10 路并发同时请求 interval=0ms 的 SSE 流
func TestE2E_P2_SSE_ZeroInterval_Concurrent(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	const concurrency = 10
	const eventCount = 5

	type result struct {
		idx    int
		got    int
		want   int
	}

	results := make([]result, concurrency)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 每个 goroutine 需要独立的代理客户端（非并发安全）
			proxy := harness.NewProxyClient(serverURL, proxyDomain)
			resp := proxy.DoGet(t, subdomain, fmt.Sprintf("/stream?count=%d&interval=0ms", eventCount))
			events := parseSSEEvents(t, resp)
			results[idx] = result{idx: idx, got: len(events), want: eventCount + 1}
		}(i)
	}
	wg.Wait()

	failCount := 0
	for _, r := range results {
		if r.got != r.want {
			failCount++
			t.Logf("并发 SSE 截断 #%d: expected %d events, got %d", r.idx, r.want, r.got)
		}
	}
	assert.Equal(t, 0, failCount,
		"interval=0ms 并发 SSE: %d/%d 路截断（expected 0）", failCount, concurrency)
}

// TestE2E_P2_SSE_ZeroInterval_LargePayload 验证极短流 + 大 payload 不截断
// 5 个事件，每事件 10KB payload，interval=0ms
// 大 payload 增加了每帧的传输时间，让 StreamEnd 竞争窗口更明显
func TestE2E_P2_SSE_ZeroInterval_LargePayload(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	const iterations = 20
	const eventCount = 5
	const payloadSize = 10000 // 10KB per event
	failCount := 0

	for i := 0; i < iterations; i++ {
		resp := proxy.DoGet(t, subdomain,
			fmt.Sprintf("/stream?count=%d&interval=0ms&payload_size=%d", eventCount, payloadSize))
		require.Equal(t, 200, resp.StatusCode, "iteration %d: HTTP status", i)

		events := parseSSEEvents(t, resp)
		expected := eventCount + 1
		if len(events) != expected {
			failCount++
			t.Logf("大 payload SSE 截断 #%d: expected %d events, got %d", i, expected, len(events))
		} else {
			// 验证每个 data 事件包含 padding
			for j := 0; j < eventCount; j++ {
				assert.Contains(t, events[j], `"padding":"`,
					"iteration %d event %d: missing padding", i, j)
			}
		}
	}

	assert.Equal(t, 0, failCount,
		"大 payload SSE: %d/%d 次截断（expected 0）", failCount, iterations)
}
