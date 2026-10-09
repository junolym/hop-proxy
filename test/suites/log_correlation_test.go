package suites

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// #78：全链路日志关联验收
//   - 管理 API 请求日志带 request_id（loggingMiddleware 注入，LogMeta 通道）
//   - 一次代理请求的 request_id 同时关联服务端与客户端两侧日志（#62）
// =============================================================================

// logRecord 日志查询结果条目（/api/admin/logs，字段与 logstore.LogRecord 一致）
type logRecord struct {
	Source    string `json:"source"`
	Type      string `json:"type"`
	Subdomain string `json:"subdomain"`
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
}

// queryLogs 调用管理 API 查询日志，失败返回 nil
func queryLogs(t *testing.T, c *harness.Client, filters map[string]string) []logRecord {
	t.Helper()
	q := url.Values{}
	for k, v := range filters {
		q.Set(k, v)
	}
	resp := c.Get(t, "/api/admin/logs?"+q.Encode())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Logf("查询日志失败: status %d", resp.StatusCode)
		return nil
	}
	var result struct {
		Data struct {
			Logs []logRecord `json:"logs"`
		} `json:"data"`
	}
	if err := harness.DecodeJSON(resp.Body, &result); err != nil {
		t.Logf("解析日志响应失败: %v", err)
		return nil
	}
	return result.Data.Logs
}

// TestE2E_APILog_RequestID 管理 API 请求日志必须带 request_id（非 "-"）
// 由 loggingMiddleware 生成并经 LogMeta 注入，handler 内 slog.*Context 自动携带
func TestE2E_APILog_RequestID(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)

	// 触发一次 API 请求（列表接口）
	resp := c.Get(t, "/api/apps")
	resp.Body.Close()

	// 日志落盘存在延迟，轮询等待出现带 request_id 的 api 日志
	var got string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && got == "" {
		for _, rec := range queryLogs(t, c, map[string]string{"type": "api", "limit": "50"}) {
			if rec.RequestID != "" && rec.RequestID != "-" {
				got = rec.RequestID
				break
			}
		}
		if got == "" {
			time.Sleep(300 * time.Millisecond)
		}
	}
	require.NotEmpty(t, got, "管理 API 日志应带 request_id（loggingMiddleware 注入，#78）")
	assert.NotEqual(t, "-", got)
}

// TestE2E_LogCorrelation_P2 一次 Path 2 请求的 request_id 应同时关联服务端与
// 客户端两侧日志（#62：入口生成 + 上下文透传；#78 阶段 3 验收）
func TestE2E_LogCorrelation_P2(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	_, subdomain := setupTunnelApp(t)

	// 客户端应用列表同步需要时间（与其它 e2e 保持一致）
	time.Sleep(2 * time.Second)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	resp := proxy.DoPost(t, subdomain, "/echo", strings.NewReader("log-corr"), "text/plain")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "P2 请求应成功")

	// 1) 从服务端"代理完成"日志取本次请求的 request_id
	var requestID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && requestID == "" {
		for _, rec := range queryLogs(t, c, map[string]string{"message": "代理完成", "subdomain": subdomain, "limit": "20"}) {
			if rec.RequestID != "" && rec.RequestID != "-" {
				requestID = rec.RequestID
				break
			}
		}
		if requestID == "" {
			time.Sleep(300 * time.Millisecond)
		}
	}
	require.NotEmpty(t, requestID, "服务端代理完成日志应带 request_id")

	// 2) 按 request_id 查询：应同时命中服务端（source=host）与客户端（logsink 上报）
	sources := map[string]bool{}
	deadline = time.Now().Add(15 * time.Second) // 客户端日志经 logsink 1 秒批量上报
	for time.Now().Before(deadline) {
		sources = map[string]bool{}
		for _, rec := range queryLogs(t, c, map[string]string{"request_id": requestID, "limit": "200"}) {
			sources[rec.Source] = true
		}
		hasHost, hasClient := false, false
		for s := range sources {
			if s == "host" {
				hasHost = true
			} else {
				hasClient = true
			}
		}
		if hasHost && hasClient {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	assert.True(t, sources["host"], "request_id 应命中服务端日志（source=host）")
	clientHit := false
	for s := range sources {
		if s != "host" {
			clientHit = true
		}
	}
	assert.True(t, clientHit, "request_id 应命中客户端日志（logsink 上报；命中来源: %v）", sources)
}
