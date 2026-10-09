package tunnel

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-yamux/v4"
)

// ConnGroup 同一 clientID 的多条隧道连接（连接池服务端侧）。
// 替代原 map[string]*Conn 中的单 *Conn，允许多条 WS+yamux 连接并存。
type ConnGroup struct {
	clientID string
	mu       sync.RWMutex
	conns    []*Conn
	rr       atomic.Int64 // Round Robin 计数
	version  string
	doneCh   chan struct{}
	doneOnce sync.Once
}

// newConnGroup 创建连接组
func newConnGroup(clientID string) *ConnGroup {
	return &ConnGroup{
		clientID: clientID,
		doneCh:   make(chan struct{}),
	}
}

// add 添加连接到组
func (g *ConnGroup) add(conn *Conn) {
	g.mu.Lock()
	g.conns = append(g.conns, conn)
	g.mu.Unlock()
}

// remove 从组中移除连接；组空时关闭 doneCh
func (g *ConnGroup) remove(conn *Conn) {
	g.mu.Lock()
	for i, c := range g.conns {
		if c == conn {
			g.conns = append(g.conns[:i], g.conns[i+1:]...)
			break
		}
	}
	empty := len(g.conns) == 0
	g.mu.Unlock()
	if empty {
		g.doneOnce.Do(func() { close(g.doneCh) })
	}
}

// count 返回当前连接数（读锁）
func (g *ConnGroup) count() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.conns)
}

// Count 返回当前 WS 连接数（公开方法，b 值）
func (g *ConnGroup) Count() int {
	return g.count()
}

// NumStreams 返回所有连接的活跃 yamux stream 总数（a 值）
func (g *ConnGroup) NumStreams() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	total := 0
	for _, c := range g.conns {
		total += c.NumStreams()
	}
	return total
}

// ConnInfo 单条隧道连接的详情（用于管理面展示）
type ConnInfo struct {
	RemoteAddr string    `json:"remote_addr"` // 客户端地址
	CreatedAt  time.Time `json:"created_at"`  // 连接创建时间
	NumStreams int       `json:"num_streams"` // 活跃 stream 数
	LatencyMs  int64     `json:"latency_ms"`  // 最近一次测量的 RTT（毫秒）
	Draining   bool      `json:"draining"`    // 是否正在 drain
	BytesSent  int64     `json:"bytes_sent"`  // 发出字节（服务端→客户端）
	BytesRecv  int64     `json:"bytes_recv"`  // 收到字节（客户端→服务端）
}

// Details 返回组内所有连接的详情
func (g *ConnGroup) Details() []ConnInfo {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]ConnInfo, 0, len(g.conns))
	for _, c := range g.conns {
		out = append(out, ConnInfo{
			RemoteAddr: c.RemoteAddr(),
			CreatedAt:  c.CreatedAt(),
			NumStreams: c.NumStreams(),
			LatencyMs:  c.GetLatency(),
			Draining:   c.IsDraining(),
			BytesSent:  c.BytesSent(),
			BytesRecv:  c.BytesRecv(),
		})
	}
	return out
}

// OpenStream 在组内非关闭的连接上以 Round Robin 方式开流
func (g *ConnGroup) OpenStream() (*yamux.Stream, error) {
	g.mu.RLock()
	live := make([]*Conn, 0, len(g.conns))
	for _, c := range g.conns {
		if !c.IsClosed() && !c.IsDraining() {
			live = append(live, c)
		}
	}
	g.mu.RUnlock()
	if len(live) == 0 {
		return nil, ErrClientOffline
	}
	idx := g.rr.Add(1) % int64(len(live))
	return live[idx].OpenStream()
}

// Done 组内所有连接关闭时触发
func (g *ConnGroup) Done() <-chan struct{} { return g.doneCh }

// GetLatency 返回组内最小存储 RTT（快速路径，不主动 ping）
func (g *ConnGroup) GetLatency() int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var min int64 = -1
	for _, c := range g.conns {
		rtt := c.GetLatency()
		if rtt < 0 {
			continue
		}
		if min < 0 || rtt < min {
			min = rtt
		}
	}
	return min
}

// MeasureLatency 主动 ping 组内连接，返回最小 RTT
func (g *ConnGroup) MeasureLatency() int64 {
	g.mu.RLock()
	conns := make([]*Conn, len(g.conns))
	copy(conns, g.conns)
	g.mu.RUnlock()
	if len(conns) == 0 {
		return -1
	}
	var min int64 = -1
	for _, c := range conns {
		rtt := c.MeasureLatency()
		if rtt < 0 {
			continue
		}
		if min < 0 || rtt < min {
			min = rtt
		}
	}
	return min
}

// GetVersion 返回客户端版本
func (g *ConnGroup) GetVersion() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.version
}

// SetVersion 设置客户端版本
func (g *ConnGroup) SetVersion(ver string) {
	g.mu.Lock()
	g.version = ver
	g.mu.Unlock()
}

// SendKick 给组内所有连接发送 kick 控制流
func (g *ConnGroup) SendKick(reason string) {
	g.mu.RLock()
	conns := make([]*Conn, len(g.conns))
	copy(conns, g.conns)
	g.mu.RUnlock()
	for _, c := range conns {
		c.SendKick(reason)
	}
}

// Close 关闭组内所有连接
func (g *ConnGroup) Close() {
	g.mu.RLock()
	conns := make([]*Conn, len(g.conns))
	copy(conns, g.conns)
	g.mu.RUnlock()
	for _, c := range conns {
		c.Close()
	}
}

// IsClosed 组内是否无连接
func (g *ConnGroup) IsClosed() bool {
	return g.count() == 0
}

// ClientID 返回客户端 ID
func (g *ConnGroup) ClientID() string { return g.clientID }
