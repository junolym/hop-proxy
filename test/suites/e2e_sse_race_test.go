package suites

// =============================================================================
// SSE 偶现截断复现测试
//
// 根因分析：
//   在代理服务端 proxyHTTP 的消费循环里，同时监听 streamCh 和 r.Context().Done()：
//
//   for {
//       select {
//       case streamMsg := <-streamCh:
//           // 处理数据
//       case <-r.Context().Done():
//           return nil  // ← 客户端断开时退出
//       }
//   }
//
//   当 SSE 流的最后几条数据和 r.Context().Done() 几乎同时 ready 时，
//   Go 的 select 随机选择，可能选择 r.Context().Done() 而不是处理剩余数据，
//   导致最后几条 SSE 事件丢失。
//
// 复现方法：
//   1. 后端一次性写入所有事件数据（无 Flush 间隔），然后立即关闭响应
//   2. 从代理视角：所有数据到达的时间窗口很窄（几乎同时），
//      并且后端关闭后，代理的 HTTP 客户端 ctx 也会很快取消
//   3. 高并发 + 大量重复可提高竞争触发概率
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

// TestE2E_P2_SSE_RaceCondition_Repeat 用 /stream/race 端点高频复现 SSE 截断竞争
// /stream/race 一次性发完所有事件后立即关闭（不 Flush），最大化竞争窗口
func TestE2E_P2_SSE_RaceCondition_Repeat(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	const iterations = 100
	const eventCount = 10
	failCount := 0
	failDetails := []string{}

	for i := 0; i < iterations; i++ {
		resp := proxy.DoGet(t, subdomain, fmt.Sprintf("/stream/race?count=%d", eventCount))
		require.Equal(t, http.StatusOK, resp.StatusCode, "iteration %d", i)

		events := parseSSEEvents(t, resp)
		expected := eventCount + 1 // N data + 1 done
		if len(events) != expected {
			failCount++
			failDetails = append(failDetails,
				fmt.Sprintf("#%d: expected %d events, got %d", i, expected, len(events)))
		}
	}

	for _, d := range failDetails {
		t.Logf("SSE 截断: %s", d)
	}
	assert.Equal(t, 0, failCount,
		"/stream/race SSE: %d/%d 次截断（expected 0，若 > 0 则复现了 select 竞争 bug）",
		failCount, iterations)
}

// TestE2E_P2_SSE_RaceCondition_Concurrent 并发 10 路同时请求 /stream/race
// 增加系统负载，提高竞争触发概率
func TestE2E_P2_SSE_RaceCondition_Concurrent(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	const concurrency = 10
	const rounds = 30
	const eventCount = 10

	type result struct {
		idx   int
		fails int
	}
	results := make([]result, concurrency)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			proxy := harness.NewProxyClient(serverURL, proxyDomain)
			fails := 0
			for j := 0; j < rounds; j++ {
				resp := proxy.DoGet(t, subdomain, fmt.Sprintf("/stream/race?count=%d", eventCount))
				events := parseSSEEvents(t, resp)
				if len(events) != eventCount+1 {
					fails++
				}
			}
			results[idx] = result{idx: idx, fails: fails}
		}(i)
	}
	wg.Wait()

	totalFails := 0
	for _, r := range results {
		if r.fails > 0 {
			t.Logf("goroutine #%d: %d/%d 次截断", r.idx, r.fails, rounds)
			totalFails += r.fails
		}
	}
	assert.Equal(t, 0, totalFails,
		"/stream/race 并发 SSE: %d/%d 次截断（expected 0）",
		totalFails, concurrency*rounds)
}
