package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v4"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

var (
	ErrClientOffline = fmt.Errorf("客户端不在线")
)

// Conn 表示一个客户端的 yamux 隧道连接
type Conn struct {
	clientID string
	session  *yamux.Session
	ctx      context.Context
	cancel   context.CancelFunc
	version  string
	lastRTT  atomic.Int64
	closed   atomic.Bool
	draining atomic.Bool // 客户端通知该连接正在 drain（连接池淘汰），OpenStream 跳过它

	stats     *countingConn // 底层 net.Conn 字节统计
	createdAt time.Time     // 连接创建时间

	// onStream 处理来自客户端的流（Path 5 转发请求等）
	onStream func(ctx context.Context, stream *yamux.Stream, conn *Conn)
}

// countingConn 包装 net.Conn，统计读写字节数（yamux 的所有读写都经过它）
type countingConn struct {
	net.Conn
	sent atomic.Int64 // 发出的字节（服务端→客户端，即客户端下载）
	recv atomic.Int64 // 收到的字节（客户端→服务端，即客户端上传）
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.recv.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.sent.Add(int64(n))
	return n, err
}

// NewConn 创建隧道连接（服务端侧）
func NewConn(clientID string, ws *websocket.Conn, ctx context.Context) (*Conn, error) {
	netConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	stats := &countingConn{Conn: netConn}
	sess, err := yamux.Server(stats, pkgTunnel.YamuxConfig(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建 yamux server 失败: %w", err)
	}
	connCtx, cancel := context.WithCancel(ctx)
	c := &Conn{
		clientID:  clientID,
		session:   sess,
		ctx:       connCtx,
		cancel:    cancel,
		stats:     stats,
		createdAt: time.Now(),
	}
	return c, nil
}

// SetStreamHandler 设置来自客户端的流处理器（Path 5 转发请求）
func (c *Conn) SetStreamHandler(handler func(ctx context.Context, stream *yamux.Stream, conn *Conn)) {
	c.onStream = handler
}

// SetVersion 设置客户端版本
func (c *Conn) SetVersion(ver string) { c.version = ver }

// GetVersion 获取客户端版本
func (c *Conn) GetVersion() string { return c.version }

// ClientID 返回客户端 ID
func (c *Conn) ClientID() string { return c.clientID }

// streamOpenTimeout 服务端主动开流的超时（v4 OpenStream 需要 ctx，
// 防止对端不 ACK 新流时永久阻塞；此值替代 hashicorp 版的 StreamOpenTimeout）
const streamOpenTimeout = 30 * time.Second

// OpenStream 服务端主动开流（发送代理请求给客户端）
func (c *Conn) OpenStream() (*yamux.Stream, error) {
	ctx, cancel := context.WithTimeout(c.ctx, streamOpenTimeout)
	defer cancel()
	return c.session.OpenStream(ctx)
}

// Done 连接关闭时触发
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }

// IsClosed 连接是否已关闭
func (c *Conn) IsClosed() bool { return c.closed.Load() || c.session.IsClosed() }

// Close 关闭连接
func (c *Conn) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	slog.Debug("关闭隧道连接", "type", "tunnel", "client_id", c.clientID)
	c.session.Close()
	c.cancel()
}

// MeasureLatency 用 yamux 内置 Ping 测量 RTT
func (c *Conn) MeasureLatency() int64 {
	if c.session.IsClosed() {
		return -1
	}
	rtt, err := c.session.Ping()
	if err != nil {
		return -1
	}
	c.lastRTT.Store(rtt.Milliseconds())
	return rtt.Milliseconds()
}

// GetLatency 返回最近一次测量的 RTT
func (c *Conn) GetLatency() int64 {
	return c.lastRTT.Load()
}

// NumStreams 返回该连接上当前活跃的 yamux stream 数
func (c *Conn) NumStreams() int {
	if c.session.IsClosed() {
		return 0
	}
	return c.session.NumStreams()
}

// IsDraining 连接是否正在 drain（客户端连接池淘汰时通知，OpenStream 应跳过）
func (c *Conn) IsDraining() bool { return c.draining.Load() }

// SetDraining 标记连接为 draining 状态
func (c *Conn) SetDraining(v bool) { c.draining.Store(v) }

// RemoteAddr 返回客户端地址
func (c *Conn) RemoteAddr() string {
	if c.stats == nil {
		return ""
	}
	return c.stats.RemoteAddr().String()
}

// CreatedAt 返回连接创建时间
func (c *Conn) CreatedAt() time.Time { return c.createdAt }

// BytesSent 返回发出的字节数（服务端→客户端，即客户端下载）
func (c *Conn) BytesSent() int64 {
	if c.stats == nil {
		return 0
	}
	return c.stats.sent.Load()
}

// BytesRecv 返回收到的字节数（客户端→服务端，即客户端上传）
func (c *Conn) BytesRecv() int64 {
	if c.stats == nil {
		return 0
	}
	return c.stats.recv.Load()
}

// Run 启动流接受循环
func (c *Conn) Run() {
	defer c.Close()
	for {
		stream, err := c.session.AcceptStream()
		if err != nil {
			if !c.closed.Load() {
				slog.Debug("隧道流接受结束", "type", "tunnel", "client_id", c.clientID, "error", err)
			}
			return
		}
		go c.handleStream(stream)
	}
}

// handleStream 处理来自客户端的流
func (c *Conn) handleStream(stream *yamux.Stream) {
	defer stream.Close()
	// 不在这里读 StreamType——由 onStream 回调（Handler.handleStream）读取并分发
	if c.onStream != nil {
		c.onStream(c.ctx, stream, c)
	}
}

// SendKick 发送 kick 控制流
func (c *Conn) SendKick(reason string) {
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	stream, err := c.session.OpenStream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()
	pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
		Action: pkgTunnel.ActionKick,
		Reason: reason,
	})
}
