package suites

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// Agent 安全代理集成测试
//
// 覆盖所有代理路径的 agent 场景：
//   P1 (server __host__ → agent):         HTTP + WS
//   P2 (client → agent via tunnel):        HTTP + WS
//   P3 (localproxy → agent):               HTTP + WS
//
// 信任链：
//   - test-agent 容器启动时从 /pubkey/{uuid} 拉 caller 公钥
//   - server 从 DB 拿私钥做 Noise_KN 握手 (P1)
//   - client 通过控制流 get_agent_key 拿私钥做 Noise_KN 握手 (P2/P3)
// =============================================================================

// agentKeyUUID 获取测试环境预创建的 agent_key UUID
func agentKeyUUID(t *testing.T) string {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	resp := c.Get(t, "/api/agent-keys")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result struct {
		Data []struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	require.NotEmpty(t, result.Data, "agent key not created (test-agent container may fail to start)")
	return result.Data[0].UUID
}

// setupAgentApp 创建一个 app，target_url 指向 test-agent:9000，关联 agent_key_uuid
// clientID 为 "__host__" 时走 P1，为真实 client 时走 P2/P3
func setupAgentApp(t *testing.T, clientID string) (int64, string) {
	t.Helper()
	c := harness.Login(t, serverURL, adminDomain)
	uuid := agentKeyUUID(t)

	subdomain := harness.DeriveSubdomain(t.Name())

	body := map[string]interface{}{
		"name":           t.Name(),
		"subdomain":      subdomain,
		"target_url":     "http://test-agent:9000",
		"client_ids":     []string{clientID},
		"auth_method":    "none",
		"agent_key_uuid": uuid,
	}
	resp := c.Post(t, "/api/apps", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create agent app should succeed")

	var r struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &r))
	require.NotZero(t, r.Data.ID)

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", r.Data.ID))
	})
	return r.Data.ID, subdomain
}

// -----------------------------------------------------------------------------
// P1: server __host__ → agent
// -----------------------------------------------------------------------------

// TestE2E_Agent_P1_HTTP 验证 Path 1（server 直连 agent）HTTP 请求
func TestE2E_Agent_P1_HTTP(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("server not initialized")
	}
	_, subdomain := setupAgentApp(t, "__host__")

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 发请求 → server → agent (Noise_KN) → test-backend
	// / 返回 JSON 含 query 字段，用于验证请求到达后端
	resp := proxy.DoGet(t, subdomain, "/?msg=hello-agent-p1")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "P1 agent HTTP should succeed")

	body := harness.ReadBody(t, resp)
	t.Logf("P1 response body: %q", string(body))
	assert.Contains(t, string(body), "hello-agent-p1", "response should contain query msg")
}

// TestE2E_Agent_P1_WS 验证 Path 1（server 直连 agent）WebSocket
func TestE2E_Agent_P1_WS(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("server not initialized")
	}
	_, subdomain := setupAgentApp(t, "__host__")

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	defer cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	msg := []byte("agent-p1-ws-test")
	require.NoError(t, conn.Write(ctx, websocket.MessageText, msg))

	_, data, err := conn.Read(ctx)
	require.NoError(t, err, "should read response from agent WS")
	assert.Equal(t, msg, data, "WS echo should match")
}

// -----------------------------------------------------------------------------
// P2: client → agent via tunnel
// -----------------------------------------------------------------------------

// TestE2E_Agent_P2_HTTP 验证 Path 2（client 经隧道到 agent）HTTP 请求
func TestE2E_Agent_P2_HTTP(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("server not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	_, subdomain := setupAgentApp(t, clientID)

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	resp := proxy.DoGet(t, subdomain, "/?msg=hello-agent-p2")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "P2 agent HTTP should succeed")

	body := harness.ReadBody(t, resp)
	assert.Contains(t, string(body), "hello-agent-p2", "response should contain query msg")
}

// TestE2E_Agent_P2_WS 验证 Path 2（client 经隧道到 agent）WebSocket
func TestE2E_Agent_P2_WS(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("server not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	_, subdomain := setupAgentApp(t, clientID)

	conn, cancel := dialProxyWS(t, subdomain, "/ws")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	defer cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	msg := []byte("agent-p2-ws-test")
	require.NoError(t, conn.Write(ctx, websocket.MessageText, msg))

	_, data, err := conn.Read(ctx)
	require.NoError(t, err, "should read response from agent WS via tunnel")
	assert.Equal(t, msg, data, "WS echo should match")
}

// -----------------------------------------------------------------------------
// P3: localproxy → agent
// -----------------------------------------------------------------------------

// TestE2E_Agent_P3_HTTP 验证 Path 3（client 本地代理到 agent）HTTP 请求
func TestE2E_Agent_P3_HTTP(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("server not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	_, subdomain := setupAgentApp(t, clientID)

	// 通过 client-1 的 LocalProxy 访问
	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 30*time.Second)

	resp := lpc.DoGet(t, subdomain, "/?msg=hello-agent-p3")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "P3 agent HTTP should succeed")

	body := harness.ReadBody(t, resp)
	assert.Contains(t, string(body), "hello-agent-p3", "response should contain query msg")
}

// TestE2E_Agent_P3_WS 验证 Path 3（client 本地代理到 agent）WebSocket
func TestE2E_Agent_P3_WS(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("server not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	_, subdomain := setupAgentApp(t, clientID)

	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 30*time.Second)

	conn, cancel := dialLocalProxyWS(t, "localhost:19091", subdomain, "/ws")
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	defer cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	msg := []byte("agent-p3-ws-test")
	require.NoError(t, conn.Write(ctx, websocket.MessageText, msg))

	_, data, err := conn.Read(ctx)
	require.NoError(t, err, "should read response from agent WS via localproxy")
	assert.Equal(t, msg, data, "WS echo should match")
}

// -----------------------------------------------------------------------------
// 安全性测试
// -----------------------------------------------------------------------------

// TestE2E_Agent_UnauthorizedCaller 验证未持私钥的 caller 被 agent 拒绝
// 创建 app target_url 指向 agent 地址但不配 agent_key_uuid，
// server 直连 agent 发明文 HTTP（无 Noise 握手），应失败
func TestE2E_Agent_UnauthorizedCaller(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("server not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)

	subdomain := harness.DeriveSubdomain(t.Name())
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       t.Name(),
		"subdomain":  subdomain,
		"target_url": "http://test-agent:9000", // agent 地址但不配 agent_key
		"client_ids": []string{"__host__"},
		"auth_method": "none",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var r struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &r))
	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", r.Data.ID))
	})

	// server 直连 agent 发明文 HTTP（没 Noise 握手）
	// agent 等待 Noise msg1，收到非 Noise 字节，握手失败/超时
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	resp2 := proxy.DoGet(t, subdomain, "/echo?msg=should-fail")
	defer resp2.Body.Close()
	assert.NotEqual(t, http.StatusOK, resp2.StatusCode, "should fail without agent key")
}
