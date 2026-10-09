package localproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"
)

// PeerManager 管理到多个客户端的 Peer 隧道
type PeerManager struct {
	mu      sync.RWMutex
	tunnels map[string]*PeerTunnel // targetClientID -> tunnel

	// 重连参数缓存
	proxyAddrs    map[string]string // targetClientID -> proxyAddr
	peerSecrets   map[string]string // targetClientID -> peerSecret
	localClientID string
	localVersion  string // 本客户端版本号，随 peer 认证上报供对端日志识别 (#67)

	// peer 认证失败回调（密钥不匹配时触发，由 LocalProxy 接线用于刷新
	// 应用缓存，见 SetAuthFailureHandler）。独立小锁保护：notifyAuthFailure
	// 可能在持有 m.mu 的路径上调用，不能复用 m.mu（不可重入）
	handlerMu     sync.Mutex
	onAuthFailure func()

	ctx    context.Context
	cancel context.CancelFunc
}

// NewPeerManager 创建 Peer 隧道管理器。localVersion 随 peer 认证消息上报，
// 供对端在认证日志中识别拨号方版本（空值 = 旧版本进程，#67）
func NewPeerManager(localClientID, localVersion string) *PeerManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &PeerManager{
		tunnels:       make(map[string]*PeerTunnel),
		proxyAddrs:    make(map[string]string),
		peerSecrets:   make(map[string]string),
		localClientID: localClientID,
		localVersion:  localVersion,
		ctx:           ctx,
		cancel:        cancel,
	}
}

// SetAuthFailureHandler 设置 peer 认证失败回调：密钥不匹配说明本地持有的
// 对端密钥已过期（对端重启后重新生成），由上层刷新应用缓存自愈 (#67)
func (m *PeerManager) SetAuthFailureHandler(h func()) {
	m.handlerMu.Lock()
	m.onAuthFailure = h
	m.handlerMu.Unlock()
}

// notifyAuthFailure 触发认证失败回调（防御性判空）。回调在独立 goroutine
// 执行，允许在持有 m.mu 的路径上安全调用
func (m *PeerManager) notifyAuthFailure() {
	m.handlerMu.Lock()
	h := m.onAuthFailure
	m.handlerMu.Unlock()
	if h != nil {
		go h()
	}
}

// GetTunnel 获取到指定客户端的隧道，如果不存在则创建
func (m *PeerManager) GetTunnel(targetClientID, proxyAddr, peerSecret, localClientID string) (*PeerTunnel, error) {
	m.mu.RLock()
	t, ok := m.tunnels[targetClientID]
	m.mu.RUnlock()

	if ok && t.IsReady() {
		return t, nil
	}

	// 需要创建新隧道：先在锁外调用 Connect()（可能阻塞），避免持锁期间阻塞其他请求
	m.mu.Lock()
	// 双重检查：可能已有其他 goroutine 完成连接
	if t, ok := m.tunnels[targetClientID]; ok && t.IsReady() {
		m.mu.Unlock()
		return t, nil
	}

	// 关闭旧隧道（如果有）
	if t, ok := m.tunnels[targetClientID]; ok {
		t.Close()
		delete(m.tunnels, targetClientID)
	}

	// 缓存重连参数
	m.proxyAddrs[targetClientID] = proxyAddr
	m.peerSecrets[targetClientID] = peerSecret
	m.localClientID = localClientID

	tunnel := m.newTunnel(targetClientID, proxyAddr, peerSecret, localClientID)
	m.mu.Unlock()

	// 在锁外调用 Connect()，避免持写锁期间阻塞（最长 30 秒）
	if err := tunnel.Connect(); err != nil {
		// 密钥不匹配 = 传入密钥已过期，重试不可能成功：
		// 通知上层刷新应用缓存，后续请求携带新密钥重建 (#67)
		if errors.Is(err, ErrPeerAuthFailed) {
			m.notifyAuthFailure()
		}
		return nil, fmt.Errorf("连接 peer 隧道失败: %w", err)
	}

	m.mu.Lock()
	// 再次检查：如果期间有其他 goroutine 已写入可用隧道，关闭刚建立的连接
	if existing, ok := m.tunnels[targetClientID]; ok && existing.IsReady() {
		m.mu.Unlock()
		tunnel.Close()
		return existing, nil
	}
	m.tunnels[targetClientID] = tunnel
	m.mu.Unlock()

	return tunnel, nil
}

// newTunnel 创建带 onClose 回调的隧道
func (m *PeerManager) newTunnel(targetClientID, proxyAddr, peerSecret, localClientID string) *PeerTunnel {
	return NewPeerTunnel(targetClientID, proxyAddr, peerSecret, localClientID, m.localVersion, func() {
		go m.backgroundReconnect(targetClientID)
	})
}

// backgroundReconnect 后台无限重连（类似主隧道模式）。
// 例外：认证失败（密钥不匹配）属永久性失败——对端重启后密钥已重新生成，
// 用同一密钥重试永远不会成功，此时停止重连并通知上层刷新缓存，
// 待下次请求携带新密钥经 GetTunnel 重建 (#67)
func (m *PeerManager) backgroundReconnect(targetClientID string) {
	backoff := 5 * time.Second
	maxBackoff := 60 * time.Second

	for {
		// 检查是否已有其他重连在进行
		m.mu.RLock()
		if t, ok := m.tunnels[targetClientID]; ok {
			if t.IsReady() {
				// 已有可用隧道，无需重连
				m.mu.RUnlock()
				return
			}
			if t.IsReconnecting() {
				// 已有重连在进行
				m.mu.RUnlock()
				return
			}
			t.SetReconnecting(true)
		}
		m.mu.RUnlock()

		select {
		case <-m.ctx.Done():
			return
		case <-time.After(backoff):
		}

		// 重新检查：backoff 睡眠期间 GetTunnel 可能已建立可用隧道
		// 若已有可用隧道则无需重连，避免覆盖 + 多余重连抖动
		m.mu.RLock()
		if t, ok := m.tunnels[targetClientID]; ok && t.IsReady() {
			m.mu.RUnlock()
			return
		}
		m.mu.RUnlock()

		// 参数缓存读取必须持锁：GetTunnel/ReconnectTunnel 会并发写入这些 map
		m.mu.RLock()
		proxyAddr := m.proxyAddrs[targetClientID]
		peerSecret := m.peerSecrets[targetClientID]
		localClientID := m.localClientID
		m.mu.RUnlock()

		if proxyAddr == "" {
			slog.Warn("peer 隧道重连参数缺失，放弃重连", "type", "proxy", "target", targetClientID)
			return
		}

		slog.Info("尝试 peer 隧道后台重连", "type", "proxy", "target", targetClientID, "backoff", backoff)

		m.mu.Lock()
		// 关闭旧隧道
		if t, ok := m.tunnels[targetClientID]; ok {
			t.Close()
			delete(m.tunnels, targetClientID)
		}

		tunnel := m.newTunnel(targetClientID, proxyAddr, peerSecret, localClientID)
		if err := tunnel.Connect(); err != nil {
			if errors.Is(err, ErrPeerAuthFailed) {
				// 缓存密钥已过期（对端重启重新生成），同一密钥重试永远
				// 无法成功：停止后台重连，通知上层刷新应用缓存 (#67)
				slog.Warn("peer 隧道认证失败，停止后台重连", "type", "proxy", "target", targetClientID, "error", err)
				m.notifyAuthFailure()
				m.mu.Unlock()
				return
			}
			slog.Warn("peer 隧道后台重连失败", "type", "proxy", "target", targetClientID, "error", err)
			m.mu.Unlock()
			backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
			continue
		}

		m.tunnels[targetClientID] = tunnel
		m.mu.Unlock()

		slog.Info("peer 隧道后台重连成功", "type", "proxy", "target", targetClientID)
		return
	}
}

// RemoveTunnel 关闭并移除指定隧道
func (m *PeerManager) RemoveTunnel(targetClientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if t, ok := m.tunnels[targetClientID]; ok {
		t.Close()
		delete(m.tunnels, targetClientID)
	}
	delete(m.proxyAddrs, targetClientID)
	delete(m.peerSecrets, targetClientID)
}

// Cleanup 保留指定的客户端 ID 集合，关闭其余隧道
func (m *PeerManager) Cleanup(keepClientIDs map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, t := range m.tunnels {
		if _, ok := keepClientIDs[id]; !ok {
			slog.Info("清理不再需要的 peer 隧道", "type", "proxy", "target", id)
			t.Close()
			delete(m.tunnels, id)
			delete(m.proxyAddrs, id)
			delete(m.peerSecrets, id)
		}
	}
}

// CloseAll 关闭所有隧道
func (m *PeerManager) CloseAll() {
	m.cancel() // 停止所有后台重连

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, t := range m.tunnels {
		t.Close()
		delete(m.tunnels, id)
	}
}

// ReconnectTunnel 尝试重连指定隧道（带指数退避，无重试上限）。
// 例外：认证失败（密钥不匹配）属永久性失败，立即返回错误并通知上层
// 刷新缓存，避免无限重试挂死请求方、刷爆对端日志 (#67)
func (m *PeerManager) ReconnectTunnel(targetClientID, proxyAddr, peerSecret, localClientID string) (*PeerTunnel, error) {
	backoff := 5 * time.Second
	maxBackoff := 60 * time.Second

	// 缓存重连参数
	m.mu.Lock()
	m.proxyAddrs[targetClientID] = proxyAddr
	m.peerSecrets[targetClientID] = peerSecret
	m.localClientID = localClientID
	m.mu.Unlock()

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			slog.Info("等待 peer 隧道重连", "type", "proxy", "target", targetClientID, "backoff", backoff, "attempt", attempt)

			select {
			case <-m.ctx.Done():
				return nil, fmt.Errorf("peer 隧道重连被取消: %s", targetClientID)
			case <-time.After(backoff):
			}

			backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
		}

		m.mu.Lock()
		// 关闭旧隧道
		if t, ok := m.tunnels[targetClientID]; ok {
			t.Close()
			delete(m.tunnels, targetClientID)
		}

		tunnel := m.newTunnel(targetClientID, proxyAddr, peerSecret, localClientID)
		if err := tunnel.Connect(); err != nil {
			if errors.Is(err, ErrPeerAuthFailed) {
				// 密钥不匹配 = 永久性失败，同一密钥重试不可能成功 (#67)
				m.notifyAuthFailure()
				m.mu.Unlock()
				return nil, err
			}
			slog.Warn("peer 隧道重连失败", "type", "proxy", "target", targetClientID, "error", err)
			m.mu.Unlock()
			continue
		}

		m.tunnels[targetClientID] = tunnel
		m.mu.Unlock()
		return tunnel, nil
	}
}
