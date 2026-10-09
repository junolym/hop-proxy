package suites

// =============================================================================
// SSE 截断复现测试 — 高频 / 大 payload / 重复 / 并发
//
// 设计目标：在代理 bug 修复之前，这些测试应当 FAIL。
// 每个测试用例对应 RESEARCH.md 中识别的一个或多个根因。
// =============================================================================

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// TestE2E_P2_SSE_HighFreq 验证高频 SSE（100 事件，间隔 1ms）经隧道完整到达
// 复现根因 B/C：高速写入可能使 streamCh(64) 满溢，触发 streamHandlerTimeout 丢弃消息
func TestE2E_P2_SSE_HighFreq(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 100 个事件，间隔 1ms——以最快速度向 streamCh 写入，测试背压边界
	resp := proxy.DoGet(t, subdomain, "/stream?count=100&interval=1ms")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"Content-Type should be text/event-stream")

	// 应收到 100 个 data 事件 + 1 个 done 事件 = 共 101 个
	events := parseSSEEvents(t, resp)
	require.Len(t, events, 101, "高频 SSE 截断：expected 101 events (100 data + 1 done), got %d", len(events))

	// 验证前 100 个事件 index 从 0 到 99 连续完整
	for i := 0; i < 100; i++ {
		assert.Contains(t, events[i], fmt.Sprintf(`"index":%d`, i),
			"高频 SSE event %d should contain index %d, got: %s", i, i, events[i])
	}
	assert.Contains(t, events[100], `"done":true`, "最后一个事件应为 done")
}

// TestE2E_P2_SSE_LargePayload 验证大 payload SSE（每事件 50KB）经隧道完整到达
// 复现根因 A：bufio.Scanner 默认 64KB token 上限会截断大事件（修复 parseSSEEvents 后此测试应 PASS）
// 但如果代理本身有截断，同样会暴露出来
func TestE2E_P2_SSE_LargePayload(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 5 个事件，每事件 padding=50000 字节（约 50KB + JSON overhead，接近但未超过 64KB Scanner 上限）
	// payload_size 参数由 Task 1 扩展的 test-backend 支持
	resp := proxy.DoGet(t, subdomain, "/stream?count=5&interval=10ms&payload_size=50000")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"Content-Type should be text/event-stream")

	// 应收到 5 个 data 事件 + 1 个 done 事件 = 共 6 个
	events := parseSSEEvents(t, resp)
	require.Len(t, events, 6, "大 payload SSE 截断：expected 6 events (5 data + 1 done), got %d", len(events))

	// 验证每个事件包含正确的 index 和大量 padding
	for i := 0; i < 5; i++ {
		assert.Contains(t, events[i], fmt.Sprintf(`"index":%d`, i),
			"大 payload SSE event %d should contain index %d", i, i)
		// padding 字段应存在（50000 个 'x'）
		assert.Contains(t, events[i], `"padding":"`,
			"大 payload SSE event %d should contain padding field", i)
	}
	assert.Contains(t, events[5], `"done":true`, "最后一个事件应为 done")
}

// TestE2E_P2_SSE_Repeat 验证同一 app 连续 20 次 SSE 请求 0 截断
// 复现偶发根因：如果有约 20% 失败率，期望至少 1-2 次失败
func TestE2E_P2_SSE_Repeat(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	const iterations = 20
	failCount := 0

	for i := 0; i < iterations; i++ {
		resp := proxy.DoGet(t, subdomain, "/stream?count=10&interval=10ms")
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"重复请求 #%d: HTTP 状态码应为 200", i)

		events := parseSSEEvents(t, resp)
		if len(events) != 11 {
			failCount++
			t.Logf("重复请求 #%d: 截断，expected 11 events, got %d", i, len(events))
		}
	}

	assert.Equal(t, 0, failCount,
		"重复 %d 次 SSE 请求中，%d 次发生截断（expected 0 failures）", iterations, failCount)
}

// TestE2E_P2_SSE_Concurrent 验证 5 路并发 SSE 请求全部完整到达
// 复现根因 B/C：多路流并发争用 streamDispatchCh 和各自 streamCh 资源
func TestE2E_P2_SSE_Concurrent(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	const concurrency = 5
	type result struct {
		idx    int
		events []string
		err    string
	}

	results := make([]result, concurrency)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 每路 50 个事件，间隔 1ms，高并发测试多路流争用
			resp := proxy.DoGet(t, subdomain, "/stream?count=50&interval=1ms")
			if resp.StatusCode != http.StatusOK {
				results[idx] = result{idx: idx, err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
				return
			}
			events := parseSSEEvents(t, resp)
			results[idx] = result{idx: idx, events: events}
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != "" {
			t.Errorf("并发请求 #%d: 请求失败: %s", r.idx, r.err)
			continue
		}
		// 应收到 50 个 data 事件 + 1 个 done 事件 = 共 51 个
		assert.Len(t, r.events, 51,
			"并发 SSE 截断 #%d: expected 51 events (50 data + 1 done), got %d", r.idx, len(r.events))
	}
}
