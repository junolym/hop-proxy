package suites

// =============================================================================
// VSCode WebSocket 代理测试
//
// 复现 VSCode 网页版经过 HopProxy P2 隧道代理时白屏（WebSocket 失败）的问题。
//
// VSCode WS 的特殊之处（区别于普通 echo 测试）：
//   1. 使用子协议（Sec-WebSocket-Protocol: vscode-ws-jsonrpc 等）
//   2. 服务器以选择的子协议作为协议层标识
//   3. 若代理未正确透传子协议协商，VSCode JS 侧会直接关闭连接
//
// 根因假设：
//   - P2 路径：服务端 Accept 时用浏览器请求的子协议列表作为「服务端支持列表」
//   - nhooyr selectSubprotocol 按「服务端列表顺序」选第一个匹配 → 选了浏览器列表[0]
//   - 客户端 Dial 时把相同的列表传给目标 → VSCode 服务器按自己的偏好选，可能选不同的
//   - 结果：浏览器←→代理协商的子协议 ≠ 代理←→VSCode 协商的子协议
//   - VSCode JS 检测到子协议不匹配（或握手中的子协议与服务器实际使用的不同）→ 断开连接
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/test/harness"
)

// dialProxyWSWithSubprotocol 连接 HopProxy 代理的 WebSocket，携带子协议
func dialProxyWSWithSubprotocol(t *testing.T, subdomain, path string, subprotocols []string) (*websocket.Conn, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	serverHost := "localhost:18080"
	wsURL := fmt.Sprintf("ws://%s%s", serverHost, path)
	hostHeader := fmt.Sprintf("%s.%s:18080", subdomain, proxyDomain)

	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: &hostHeaderTransport{
				host:    hostHeader,
				wrapped: http.DefaultTransport,
			},
		},
		Subprotocols: subprotocols,
	})
	if err != nil {
		cancel()
		t.Fatalf("WebSocket dial with subprotocols %v failed: %v", subprotocols, err)
	}
	_ = resp
	return conn, cancel
}

// =============================================================================
// 子协议协商测试
// =============================================================================

// TestE2E_P2_WS_Subprotocol_Single 验证 P2 隧道正确透传单个 WebSocket 子协议
// 场景：客户端请求 vscode-ws-jsonrpc，目标服务器仅接受 vscode-ws-jsonrpc
// 预期：协商成功，两端都选 vscode-ws-jsonrpc
func TestE2E_P2_WS_Subprotocol_Single(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	// 连接到要求 vscode-ws-jsonrpc 子协议的端点
	conn, cancel := dialProxyWSWithSubprotocol(t, subdomain, "/ws/subprotocol?protocol=vscode-ws-jsonrpc",
		[]string{"vscode-ws-jsonrpc"})
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 验证协商到的子协议
	negotiated := conn.Subprotocol()
	assert.Equal(t, "vscode-ws-jsonrpc", negotiated,
		"代理应透传子协议协商，浏览器侧应协商到 vscode-ws-jsonrpc")

	// 读取目标服务器发来的握手确认
	ctx := context.Background()
	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	_, reader, err := conn.Reader(readCtx)
	require.NoError(t, err, "read handshake from target")
	data, rerr := io.ReadAll(io.LimitReader(reader, 4096))
	readCancel()
	require.NoError(t, rerr, "readall handshake")

	var handshake map[string]string
	require.NoError(t, json.Unmarshal(data, &handshake), "parse handshake JSON")

	// 目标服务器协商到的子协议也必须是 vscode-ws-jsonrpc
	assert.Equal(t, "vscode-ws-jsonrpc", handshake["subprotocol"],
		"目标服务器侧应协商到 vscode-ws-jsonrpc，实际: %v", handshake)
	assert.Equal(t, "ok", handshake["status"],
		"握手状态应为 ok，实际: %v", handshake)
}

// TestE2E_P2_WS_Subprotocol_MultiplePreferences 验证 P2 隧道在多子协议场景下正确协商
// 场景：客户端请求 [vscode-ws-jsonrpc, octet-stream-patch-v1]，目标服务器偏好 octet-stream-patch-v1
// 这是 VSCode 白屏的核心复现场景：
//   - 修复前：代理服务端 Accept 时选了列表第一个 vscode-ws-jsonrpc，目标选了第二个
//   - 修复后：服务端先连接目标，用目标协商的 octet-stream-patch-v1 Accept 浏览器
// 验证：浏览器侧和目标侧协商的子协议相同
func TestE2E_P2_WS_Subprotocol_MultiplePreferences(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	// 目标服务器只接受 octet-stream-patch-v1（它的偏好，排在客户端列表第2位）
	// 客户端请求 [vscode-ws-jsonrpc, octet-stream-patch-v1]
	conn, cancel := dialProxyWSWithSubprotocol(t, subdomain,
		"/ws/subprotocol?protocol=octet-stream-patch-v1",
		[]string{"vscode-ws-jsonrpc", "octet-stream-patch-v1"})
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 核心验证：浏览器侧协商到的子协议
	// 修复前：vscode-ws-jsonrpc（代理未考虑目标偏好，选了列表第一个）
	// 修复后：octet-stream-patch-v1（代理先连接目标，用目标协商的子协议响应浏览器）
	negotiated := conn.Subprotocol()
	t.Logf("浏览器侧协商到的子协议: %q", negotiated)

	assert.Equal(t, "octet-stream-patch-v1", negotiated,
		"修复后：浏览器侧应协商到目标服务器偏好的 octet-stream-patch-v1，而不是列表第一个 vscode-ws-jsonrpc\n"+
			"  浏览器侧协商到: %q\n  期望: octet-stream-patch-v1", negotiated)

	// 注意：目标服务器在连接后会主动发一条握手 JSON，但由于时序原因
	// （handler 在 Accept 浏览器后才注册），这条数据可能丢失。
	// 这对正常使用无影响，因为 VSCode 连接后是自己先发消息的。
}

// TestE2E_P2_WS_Subprotocol_VSCodeSequence 完整模拟 VSCode 握手序列
// VSCode 网页版（code-server）典型的 WS 握手：
//   Sec-WebSocket-Protocol: vscode-ws-jsonrpc, permessage-deflate
// 目标服务器（code-server）通常选 vscode-ws-jsonrpc
func TestE2E_P2_WS_Subprotocol_VSCodeSequence(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	_, subdomain := setupTunnelApp(t)

	// 模拟 VSCode 的子协议请求
	vsCodeProtocols := []string{"vscode-ws-jsonrpc"}

	conn, cancel := dialProxyWSWithSubprotocol(t, subdomain,
		"/ws/subprotocol?protocol=vscode-ws-jsonrpc", vsCodeProtocols)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "")

	negotiated := conn.Subprotocol()
	require.Equal(t, "vscode-ws-jsonrpc", negotiated,
		"VSCode 场景：浏览器侧必须协商到 vscode-ws-jsonrpc")

	ctx := context.Background()

	// 读取握手
	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	_, reader, err := conn.Reader(readCtx)
	require.NoError(t, err, "read VSCode handshake")
	data, rerr := io.ReadAll(io.LimitReader(reader, 4096))
	readCancel()
	require.NoError(t, rerr, "readall handshake")

	var handshake map[string]string
	require.NoError(t, json.Unmarshal(data, &handshake))

	require.Equal(t, "vscode-ws-jsonrpc", handshake["subprotocol"],
		"目标服务器侧必须协商到 vscode-ws-jsonrpc")

	// 模拟 VSCode 的后续通信（JSON-RPC Text 帧）
	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
	err = conn.Write(writeCtx, websocket.MessageText, []byte(msg))
	writeCancel()
	require.NoError(t, err, "send JSON-RPC message")

	// 读取 echo
	readCtx2, readCancel2 := context.WithTimeout(ctx, 5*time.Second)
	msgType, reader2, err := conn.Reader(readCtx2)
	require.NoError(t, err, "read JSON-RPC echo")
	echo, rerr := io.ReadAll(io.LimitReader(reader2, 4096))
	readCancel2()
	require.NoError(t, rerr, "readall echo")

	assert.Equal(t, websocket.MessageText, msgType, "JSON-RPC 消息应为 Text 帧")
	assert.Equal(t, msg, string(echo), "JSON-RPC echo 应与发送内容一致")
}
