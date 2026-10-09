// Package tunnel 管理客户端到服务端的 yamux 隧道连接。
package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-yamux/v4"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

const (
	defaultMaxBackoff     = 60 * time.Second
	resetBackoffThreshold = 30 * time.Second

	// ctrlRequestTimeout 控制流请求超时
	ctrlRequestTimeout = 30 * time.Second
)

// HTTPHandler 处理来自服务端的 HTTP 代理请求流（原生 HTTP/1.1，由 handler 自行读取）
type HTTPHandler func(ctx context.Context, stream *yamux.Stream)

// WSHandler 处理来自服务端的 WS 代理请求流（原生 HTTP 升级，由 handler 自行读取）
type WSHandler func(ctx context.Context, stream *yamux.Stream)

// poolConfig 连接池配置
type poolConfig struct {
	size       int           // 目标连接数（默认 4）
	minTTL     time.Duration // 淘汰下限（默认 5min）
	maxTTL     time.Duration // 淘汰上限（默认 10min）
	drainGrace time.Duration // drain 宽限期（默认 0=永久等待，旧连接保留至所有 stream 结束）
}

// Client 客户端 yamux 隧道连接（内部使用连接池，对外 API 不变）
type Client struct {
	url               string
	onHTTP            HTTPHandler
	onWS              WSHandler
	onAppsChanged     func() // 应用列表变更通知
	initialBackoff    time.Duration
	maxBackoff        time.Duration
	poolCfg           poolConfig
	pool              *connPool
	kicked            atomic.Bool // 收到 kick 通知，用于让 Run 退出而非重连
	onConnEstablished func()      // 新连接建立后回调（用于重新上报版本等元信息）
}

// NewClient 创建客户端隧道
func NewClient(url string, reconnectInterval time.Duration) *Client {
	if reconnectInterval <= 0 {
		reconnectInterval = 10 * time.Second
	}
	return &Client{
		url:            url,
		initialBackoff: reconnectInterval,
		maxBackoff:     defaultMaxBackoff,
		poolCfg: poolConfig{
			size:       4,
			minTTL:     5 * time.Minute,
			maxTTL:     10 * time.Minute,
			drainGrace: 0,
		},
	}
}

// SetPoolConfig 设置连接池参数（在 Run 之前调用）
func (c *Client) SetPoolConfig(size int, minTTL, maxTTL, drainGrace time.Duration) {
	if size < 1 {
		size = 1
	}
	c.poolCfg = poolConfig{
		size:       size,
		minTTL:     minTTL,
		maxTTL:     maxTTL,
		drainGrace: drainGrace,
	}
}

// SetHandlers 设置流处理器
func (c *Client) SetHandlers(onHTTP HTTPHandler, onWS WSHandler, onAppsChanged func()) {
	c.onHTTP = onHTTP
	c.onWS = onWS
	c.onAppsChanged = onAppsChanged
}

// SetConnEstablishedHandler 设置新连接建立后的回调。
// 每条新的 yamux 连接成功 dial 并加入池后都会触发一次，
// 用于让上层重新上报版本等元信息——避免 pool_size=1 时连接池 TTL
// 轮换造成服务端 ConnGroup 被删除重建后版本字段丢失。
// 必须在 Run 之前调用。
func (c *Client) SetConnEstablishedHandler(cb func()) {
	c.onConnEstablished = cb
}

func (c *Client) IsConnected() bool {
	if c.pool == nil {
		return false
	}
	return c.pool.IsConnected()
}

// OpenStream 客户端主动开流（用于 Path 5 转发请求到服务端、reportProxyStatus 等）
func (c *Client) OpenStream() (*yamux.Stream, error) {
	if c.pool == nil {
		return nil, fmt.Errorf("隧道未连接")
	}
	return c.pool.OpenStream()
}

// Run 启动连接池（N 个 manageConn goroutine），阻塞直到 ctx 取消或被踢出。
//
// 生命周期由 ctx 控制：ctx 取消（SIGINT/SIGTERM）或 kicked 时 pool.Close()
// 关闭所有连接，manageConn goroutine 随之退出。TTL 替换启动的新 manageConn
// 不计入任何 WaitGroup——靠 pool.Close() 间接回收，而非显式等待。
func (c *Client) Run(ctx context.Context) {
	c.pool = newConnPool(ctx, c.url, c.poolCfg.size, c.poolCfg.minTTL, c.poolCfg.maxTTL,
		c.poolCfg.drainGrace, c.initialBackoff, c.maxBackoff, &c.kicked)
	c.pool.streamHandler = c.handleStream
	c.pool.onConnEstablished = c.onConnEstablished

	for i := 0; i < c.poolCfg.size; i++ {
		go c.pool.manageConn(ctx)
	}

	<-ctx.Done()
	c.pool.Close()

	if c.kicked.Load() {
		slog.Warn("被服务端踢出，退出进程", "type", "tunnel")
	}
}

// handleStream 处理来自服务端的流
func (c *Client) handleStream(ctx context.Context, stream *yamux.Stream) {
	defer stream.Close()

	streamType, err := pkgTunnel.ReadStreamTypeWithTimeout(stream, pkgTunnel.StreamTypeReadTimeout)
	if err != nil {
		return
	}

	switch streamType {
	case pkgTunnel.StreamHTTP:
		// 原生 HTTP/1.1 请求由 onHTTP 自行读取
		if c.onHTTP != nil {
			c.onHTTP(ctx, stream)
		}

	case pkgTunnel.StreamWS:
		// 原生 HTTP/1.1 升级请求由 onWS 自行读取
		if c.onWS != nil {
			c.onWS(ctx, stream)
		}

	case pkgTunnel.StreamCtrl:
		msg, err := pkgTunnel.ReadCtrlMessage(stream)
		if err != nil {
			return
		}
		c.handleCtrl(ctx, msg)
	}
}

// handleCtrl 处理控制消息
func (c *Client) handleCtrl(ctx context.Context, msg *pkgTunnel.CtrlMessage) {
	switch msg.Action {
	case pkgTunnel.ActionKick:
		slog.Warn("收到踢出通知", "type", "tunnel", "reason", msg.Reason)
		// 设置 kicked 标志并关闭整个连接池，
		// 所有 manageConn 退出，Run 返回
		c.kicked.Store(true)
		if c.pool != nil {
			c.pool.Close()
		}
	case pkgTunnel.ActionAppsChanged:
		slog.Debug("收到应用变更通知", "type", "tunnel")
		if c.onAppsChanged != nil {
			c.onAppsChanged()
		}
	default:
		slog.Warn("未知控制消息", "type", "tunnel", "action", msg.Action)
	}
}

// GetAgentKey 通过控制流向 server 请求 agent 私钥（实现 agenttransport.TunnelClient 接口）
func (c *Client) GetAgentKey(uuid string) (string, error) {
	stream, err := c.OpenStream()
	if err != nil {
		return "", fmt.Errorf("开流失败: %w", err)
	}
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(ctrlRequestTimeout))
	defer stream.SetReadDeadline(time.Time{})

	if err := pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
		Action:       pkgTunnel.ActionGetAgentKey,
		AgentKeyUUID: uuid,
	}); err != nil {
		return "", fmt.Errorf("发送 get_agent_key 失败: %w", err)
	}

	resp, err := pkgTunnel.ReadCtrlMessage(stream)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}
	if !resp.Success {
		return "", fmt.Errorf("server 拒绝下发私钥（权限不足或密钥不存在）")
	}
	if len(resp.Body) == 0 {
		return "", fmt.Errorf("响应私钥为空")
	}
	return string(resp.Body), nil
}
