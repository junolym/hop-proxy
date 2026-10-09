// Package agenttransport 提供 client 侧的 agent 安全代理 Transport 管理。
//
// client 通过控制流向 server 请求 agent 私钥（带内存缓存），
// 然后用私钥做 Noise_KN 握手连接 agent。Transport 按 agentKeyUUID 缓存复用。
package agenttransport

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	noise "github.com/flynn/noise"
	"github.com/robin/hop-proxy/pkg/agentcrypto"
)

// TunnelClient 控制流客户端接口（解耦 *tunnel.Client）
type TunnelClient interface {
	// GetAgentKey 通过控制流向 server 请求 agent 私钥，返回 hex 私钥
	GetAgentKey(uuid string) (string, error)
}

// Manager 管理 client 侧的 agent Transport 缓存
type Manager struct {
	tunnel TunnelClient

	mu         sync.RWMutex
	transports map[string]*http.Transport // agentKeyUUID → Transport
	privKeys   map[string]noise.DHKey     // agentKeyUUID → 静态密钥对（内存缓存）
}

// NewManager 创建 agent Transport 管理器
func NewManager(tunnel TunnelClient) *Manager {
	return &Manager{
		tunnel:     tunnel,
		transports: make(map[string]*http.Transport),
		privKeys:   make(map[string]noise.DHKey),
	}
}

// GetTransport 获取指定 agent_key_uuid 的 Transport（缓存复用）
// 返回 http.RoundTripper 以满足 pkg/tunnel.AgentTransportProvider 接口
func (m *Manager) GetTransport(agentKeyUUID string) (http.RoundTripper, error) {
	if agentKeyUUID == "" {
		return nil, fmt.Errorf("agent_key_uuid 为空")
	}

	m.mu.RLock()
	if t, ok := m.transports[agentKeyUUID]; ok {
		m.mu.RUnlock()
		return t, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	// double-check
	if t, ok := m.transports[agentKeyUUID]; ok {
		return t, nil
	}

	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// 1. 获取私钥（内存缓存 → 控制流请求）
			staticKey, err := m.getStaticKey(agentKeyUUID)
			if err != nil {
				return nil, fmt.Errorf("获取 agent 私钥失败: %w", err)
			}
			// 2. TCP 连接 agent
			d := net.Dialer{}
			rawConn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, fmt.Errorf("连接 agent 失败: %w", err)
			}
			// 3. Noise_KN 握手
			noiseConn, err := agentcrypto.DialAndHandshake(rawConn, staticKey)
			if err != nil {
				rawConn.Close()
				return nil, fmt.Errorf("Noise 握手失败: %w", err)
			}
			return noiseConn, nil
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		IdleConnTimeout:   30 * time.Second,
		ForceAttemptHTTP2: false,
	}
	m.transports[agentKeyUUID] = transport
	return transport, nil
}

// getStaticKey 获取 caller 静态密钥对（内存缓存 → 控制流请求）
func (m *Manager) getStaticKey(uuid string) (noise.DHKey, error) {
	m.mu.RLock()
	if k, ok := m.privKeys[uuid]; ok {
		m.mu.RUnlock()
		return k, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	// double-check
	if k, ok := m.privKeys[uuid]; ok {
		return k, nil
	}

	// 通过控制流向 server 请求私钥
	privHex, err := m.tunnel.GetAgentKey(uuid)
	if err != nil {
		return noise.DHKey{}, err
	}
	staticKey, err := agentcrypto.LoadPrivateKey(privHex)
	if err != nil {
		return noise.DHKey{}, fmt.Errorf("加载私钥失败: %w", err)
	}
	m.privKeys[uuid] = staticKey
	slog.Debug("已获取 agent 私钥", "type", "tunnel", "uuid", uuid)
	return staticKey, nil
}

// Close 关闭所有 Transport，释放资源
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.transports {
		t.CloseIdleConnections()
	}
	m.transports = make(map[string]*http.Transport)
	m.privKeys = make(map[string]noise.DHKey)
}
