// Package tunnel 提供 yamux-based 隧道协议核心组件。
// 用 websocket.NetConn 将 WebSocket 转为 net.Conn，在其上运行 yamux 多路复用。
// 每个 HTTP/WS 请求 = 一条 yamux 流，无需自定义消息类型、RequestID 匹配或 dispatch loop。
package tunnel

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v4"
)

// yamuxConfig 返回 yamux 配置（libp2p/go-yamux/v4）。
// ConnectionWriteTimeout 必须 > 0（yamux 用 time.NewTimer 创建超时，0 会立即触发）。
// 设为 120s 给足写入时间；websocket.NetConn 的 writeTimer 在 SetWriteDeadline
// 时被 reset 到同样时长，正常写入毫秒级完成不会触发。
//
// MaxStreamWindowSize 设为 4MB：单流滑动窗口决定单流吞吐上限（窗口/RTT）。
// 公网隧道 RTT 常达 50–100ms，原 256KB 把单流卡在 ~2.5–5 MB/s；
// 提到 4MB 后单流可达 ~20–40 MB/s，并减少 window-update 帧对底层 WS 写锁的竞争。
// yamux 允许最大 64MB，4MB 是吞吐与内存的平衡点。
//
// MaxIncomingStreams 限制对端可并发打开的流数，超限即 RST，
// 防止"开流但不发数据"的流/goroutine 无限堆积。
//
// InitialStreamWindowSize 必须 >= 256KB（v4 VerifyConfig 要求），窗口从该值
// 起步随读取增长到 MaxStreamWindowSize；PingBacklog 必须 > 0。
//
// v4 没有 hashicorp 版的 StreamOpenTimeout/StreamCloseTimeout：
//   - 开流等待由 OpenStream(ctx) 的 ctx 控制（调用方传带超时的 ctx）；
//   - Close 立即完成、无延迟 force-close 定时器，长流式响应不会被定时截断。
func yamuxConfig() *yamux.Config {
	return &yamux.Config{
		AcceptBacklog:           512,
		PingBacklog:             32,
		EnableKeepAlive:         true,
		KeepAliveInterval:       30 * time.Second,
		MeasureRTTInterval:      30 * time.Second,
		ConnectionWriteTimeout:  120 * time.Second,
		MaxIncomingStreams:      1024,
		InitialStreamWindowSize: 256 * 1024,
		MaxStreamWindowSize:     4 * 1024 * 1024,
		MaxMessageSize:          256 * 1024,
		LogOutput:               io.Discard,
	}
}

// YamuxConfig 返回 yamux 配置（供需要自行创建 session 的调用方使用，如包装 net.Conn 统计字节）。
func YamuxConfig() *yamux.Config {
	return yamuxConfig()
}

// NewServerSession 服务端侧：WebSocket 升级后创建 yamux 服务端。
// websocket.NetConn 将消息导向的 WS 转为流导向的 net.Conn，yamux 在其上运行。
func NewServerSession(ctx context.Context, ws *websocket.Conn) (*yamux.Session, error) {
	netConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	sess, err := yamux.Server(netConn, yamuxConfig(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建 yamux server 失败: %w", err)
	}
	return sess, nil
}

// NewClientSession 客户端侧：拨号 WebSocket 后创建 yamux 客户端。
// 返回 session 和底层 ws（ws 由 NetConn 管理，调用方不应直接操作）。
func NewClientSession(ctx context.Context, wsURL string) (*yamux.Session, error) {
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("拨号隧道 WebSocket 失败: %w", err)
	}
	// NetConn 内部会设 SetReadLimit(-1)，但显式设置以防意外
	ws.SetReadLimit(50 * 1024 * 1024)
	netConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	sess, err := yamux.Client(netConn, yamuxConfig(), nil)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "yamux 创建失败")
		return nil, fmt.Errorf("创建 yamux client 失败: %w", err)
	}
	return sess, nil
}

// NewClientSessionFromWS 从已有 WebSocket 连接创建 yamux 客户端（用于 peer 隧道）。
func NewClientSessionFromWS(ctx context.Context, ws *websocket.Conn) (*yamux.Session, error) {
	netConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	sess, err := yamux.Client(netConn, yamuxConfig(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建 yamux client 失败: %w", err)
	}
	return sess, nil
}

// NewServerSessionFromWS 从已有 WebSocket 连接创建 yamux 服务端（用于 peer 隧道服务端侧）。
func NewServerSessionFromWS(ctx context.Context, ws *websocket.Conn) (*yamux.Session, error) {
	netConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	sess, err := yamux.Server(netConn, yamuxConfig(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建 yamux server 失败: %w", err)
	}
	return sess, nil
}
