// Package tunnel 管理服务端与客户端之间的 yamux 隧道连接。
package tunnel

import (
	"log/slog"
	"sync"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// Hub 管理所有在线的客户端隧道连接（每个 clientID 可有多条连接组成 ConnGroup）
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*ConnGroup
}

func NewHub() *Hub {
	return &Hub{conns: make(map[string]*ConnGroup)}
}

// Register 注册客户端连接（加入 ConnGroup，不踢旧连接）。
// 连接池模式下同一 clientID 可有多条 WS 连接并存。
func (h *Hub) Register(clientID string, conn *Conn) {
	h.mu.Lock()
	g, ok := h.conns[clientID]
	if !ok {
		g = newConnGroup(clientID)
		h.conns[clientID] = g
	}
	g.add(conn)
	count := g.count()
	h.mu.Unlock()
	slog.Debug("客户端连接已注册", "type", "tunnel", "client_id", clientID, "conns", count)
}

// Unregister 注销客户端连接（仅从 ConnGroup 移除指定 conn；组空时删除 map 项）
func (h *Hub) Unregister(clientID string, conn *Conn) {
	h.mu.Lock()
	if g, ok := h.conns[clientID]; ok {
		g.remove(conn)
		if g.count() == 0 {
			delete(h.conns, clientID)
			slog.Info("客户端已断开（连接组已空）", "type", "tunnel", "client_id", clientID)
		}
	}
	h.mu.Unlock()
}

func (h *Hub) IsOnline(clientID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[clientID]
	return ok
}

// Kick 踢掉客户端整个连接组。锁内只取 group，SendKick/Close 在锁外执行。
func (h *Hub) Kick(clientID, reason string) {
	h.mu.Lock()
	g, ok := h.conns[clientID]
	if ok {
		delete(h.conns, clientID)
	}
	h.mu.Unlock()
	if ok {
		g.SendKick(reason)
		g.Close()
	}
}

// GetLatency 主动测量 RTT（API 路径，会 ping）
func (h *Hub) GetLatency(clientID string) int64 {
	h.mu.RLock()
	g, ok := h.conns[clientID]
	h.mu.RUnlock()
	if !ok {
		return -1
	}
	return g.MeasureLatency()
}

// GetConnHealth 返回在线状态和存储的 RTT（快速路径，不 ping）
func (h *Hub) GetConnHealth(clientID string) (online bool, rtt int64) {
	h.mu.RLock()
	g, ok := h.conns[clientID]
	h.mu.RUnlock()
	if !ok {
		return false, -1
	}
	return true, g.GetLatency()
}

// GetConn 返回连接组（替代原 *Conn）
func (h *Hub) GetConn(clientID string) (*ConnGroup, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	g, ok := h.conns[clientID]
	return g, ok
}

// GetConnStats 返回 (活跃 stream 数 a, 当前 WS 连接数 b)
func (h *Hub) GetConnStats(clientID string) (streams, conns int) {
	h.mu.RLock()
	g, ok := h.conns[clientID]
	h.mu.RUnlock()
	if !ok {
		return 0, 0
	}
	return g.NumStreams(), g.Count()
}

// GetConnDetails 返回该客户端所有连接的详情（用于连接详情页）
func (h *Hub) GetConnDetails(clientID string) []ConnInfo {
	h.mu.RLock()
	g, ok := h.conns[clientID]
	h.mu.RUnlock()
	if !ok {
		return nil
	}
	return g.Details()
}

func (h *Hub) GetClientVersion(clientID string) string {
	h.mu.RLock()
	g, ok := h.conns[clientID]
	h.mu.RUnlock()
	if !ok {
		return ""
	}
	return g.GetVersion()
}

// NotifyAppsChanged 通过控制流通知客户端应用变更
func (h *Hub) NotifyAppsChanged(clientIDs []string) {
	for _, cid := range clientIDs {
		h.mu.RLock()
		g, ok := h.conns[cid]
		h.mu.RUnlock()
		if !ok {
			continue
		}
		go func(c *ConnGroup) {
			stream, err := c.OpenStream()
			if err != nil {
				return
			}
			defer stream.Close()
			pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
				Action: pkgTunnel.ActionAppsChanged,
			})
		}(g)
	}
}

// NotifyAppsChangedAll 通知所有在线客户端应用变更 (#57)。
// 全局子域名注册表随任何应用的创建/删除/子域名或启停变更而变化，
// 会影响所有客户端本地代理的解析，故需广播（无本地代理的客户端
// 收到通知后仅记日志，onAppsChanged 为 nil 时无副作用）。
func (h *Hub) NotifyAppsChangedAll() {
	h.mu.RLock()
	groups := make([]*ConnGroup, 0, len(h.conns))
	for _, g := range h.conns {
		groups = append(groups, g)
	}
	h.mu.RUnlock()

	for _, g := range groups {
		go func(c *ConnGroup) {
			stream, err := c.OpenStream()
			if err != nil {
				return
			}
			defer stream.Close()
			pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
				Action: pkgTunnel.ActionAppsChanged,
			})
		}(g)
	}
}
