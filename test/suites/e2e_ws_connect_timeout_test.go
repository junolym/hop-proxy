package suites

import (
	"testing"
	"time"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// WS 连接超时不外泄桥接循环（回归测试）
//
// 根因（#47 引入的回归）：4 处 WS 拨号点用 WithTimeout 覆盖了 ctx 本身，
// 注释称「仅作用于握手阶段」，但同一 ctx 既传给 websocket.Dial 又流入拨号后
// 的桥接循环。coder/websocket 的 ws.Read(ctx) 在 ctx 到期时立即失败 →
// 桥接退出 → 连接关闭。默认 proxy_connect_timeout=30s，表现为 WS 连接
// 固定 30 秒断线。
//
// 修复：WithTimeout 只包裹拨号（独立 dialCtx），桥接阶段用原始 ctx。
//
// 测试策略：应用级覆盖 proxy_connect_timeout: 2s（避免真等 30s），
// 建连后 sleep 超过 2s 再 echo——修复前会在 ~2s 处收到连接关闭而 FAIL，
// 修复后连接仍存活可正常收发。
//
// Path 3 / Path 5 与 P1/P2 同构修复，由源码 grep 断言 + 全量回归覆盖
// （测试环境无对应端到端路径设施，不为它们新建复杂拓扑）。
// =============================================================================

// TestE2E_P2_WS_ConnectTimeoutNotAppliedToBridge 覆盖 Path 2 执行端
// （pkg/tunnel/executor.go ServeDirectWSWithTransport）
func TestE2E_P2_WS_ConnectTimeoutNotAppliedToBridge(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	// setupTunnelApp 模式，但用带 proxy_config 的 helper 覆盖连接超时为 2s
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)
	_, subdomain := harness.SetupProxyAppWithConfig(t, c, serverURL, adminDomain, clientID,
		"http://test-backend:8000", "proxy_connect_timeout: 2s")

	conn, closeCancel := dialProxyWS(t, subdomain, "/ws")
	defer closeCancel()
	defer conn.CloseNow()

	// 确认连通
	readEchoMessage(t, conn, "ping-before-timeout")

	// 跨越 2s 超时窗口：修复前 ctx 到期 ws.Read 失败 → 连接被关
	time.Sleep(3 * time.Second)

	// 修复前此处会在 ~2s 处收到连接关闭错误而 FAIL；修复后正常 echo
	readEchoMessage(t, conn, "ping-after-timeout")
}

// TestE2E_P1_WS_ConnectTimeoutNotAppliedToBridge 覆盖 Path 1
// （internal/server/proxy/local_ws.go proxyLocalWebSocket）
func TestE2E_P1_WS_ConnectTimeoutNotAppliedToBridge(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	// __host__ 模式（P1），同样覆盖 proxy_connect_timeout: 2s
	c := harness.Login(t, serverURL, adminDomain)
	_, subdomain := harness.SetupProxyAppWithConfig(t, c, serverURL, adminDomain, "__host__",
		"http://test-backend:8000", "proxy_connect_timeout: 2s")

	conn, closeCancel := dialProxyWS(t, subdomain, "/ws")
	defer closeCancel()
	defer conn.CloseNow()

	// 确认连通
	readEchoMessage(t, conn, "p1-ping-before-timeout")

	// 跨越 2s 超时窗口
	time.Sleep(3 * time.Second)

	readEchoMessage(t, conn, "p1-ping-after-timeout")
}
