package localproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/pkg/httputil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

var (
	ErrPeerTunnelClosed = errors.New("peer 隧道已关闭")
	ErrPeerAuthFailed   = errors.New("peer 认证失败")
)

// PeerAuthError peer 认证失败（密钥不匹配等永久性失败）。
// 携带对端自报的密钥指纹，供链路上的服务端比对下发的密钥、定位代理配置
// 指向了错误的客户端记录 (#67)
type PeerAuthError struct {
	Reason   string // 对端返回的失败原因
	SecretFP string // 对端自报的当前密钥指纹（对端为新版本时提供）
}

func (e *PeerAuthError) Error() string {
	if e.SecretFP != "" {
		return fmt.Sprintf("peer 认证失败: %s（对端密钥指纹 %s）", e.Reason, e.SecretFP)
	}
	return "peer 认证失败: " + e.Reason
}

// Unwrap 保持 errors.Is(err, ErrPeerAuthFailed) 语义（PeerManager 停止重连依赖）
func (e *PeerAuthError) Unwrap() error { return ErrPeerAuthFailed }

// PeerSecretFP 供 pkg/tunnel.ServePeerForward 经 errors.As 提取指纹并透传给服务端
func (e *PeerAuthError) PeerSecretFP() string { return e.SecretFP }

const peerPingInterval = 30 * time.Second

// peerYamuxConfig Peer 隧道 yamux 配置（libp2p/go-yamux/v4）。
// 必须设置 LogOutput（yamux.VerifyConfig 要求 Logger/LogOutput 至少一个）；
// ConnectionWriteTimeout 必须为正，否则零值会让 yamux 的 time.NewTimer 立即触发。
// InitialStreamWindowSize/PingBacklog 必须满足 v4 VerifyConfig（>=256KB、>0）。
// v4 无 StreamOpenTimeout/StreamCloseTimeout：开流由 OpenStream(ctx) 控制，
// Close 立即完成无延迟强制定时器。
func peerYamuxConfig() *yamux.Config {
	return &yamux.Config{
		AcceptBacklog:           64,
		PingBacklog:             8,
		EnableKeepAlive:         true,
		KeepAliveInterval:       peerPingInterval,
		MeasureRTTInterval:      30 * time.Second,
		ConnectionWriteTimeout:  120 * time.Second,
		MaxIncomingStreams:      256,
		InitialStreamWindowSize: 256 * 1024,
		MaxStreamWindowSize:     256 * 1024,
		MaxMessageSize:          256 * 1024,
		LogOutput:               io.Discard,
	}
}

// peerAuth peer 认证消息（gob 编码，在 yamux 之前作为原始 WS 消息发送）
type peerAuth struct {
	ClientID  string
	Secret    string
	Timestamp string
	Signature string
	// Version 拨号方版本号（#67）：旧版本进程不携带（空值），接收方认证日志
	// 直接暴露，用于识别仍在拨号的孤儿/过期客户端进程
	Version string
}

type peerAuthResp struct {
	Success bool
	Reason  string
	// SecretFP 对端自报的当前密钥指纹（仅认证失败时回传，#67）：
	// 供发起方链路上的服务端比对定位"代理配置目标客户端与地址实际客户端不一致"
	SecretFP string
}

// PeerTunnel 管理到另一个客户端的 yamux 隧道连接
type PeerTunnel struct {
	targetClientID string
	proxyAddr      string
	peerSecret     string
	localClientID  string
	localVersion   string

	session *yamux.Session
	ctx     context.Context
	cancel  context.CancelFunc

	mu           sync.Mutex
	ready        bool
	closed       bool
	onClose      func()
	reconnecting atomic.Bool

	// onStream 处理来自 peer 的流（peer B → peer A 方向的请求）
	onStream func(stream *yamux.Stream)
}

// NewPeerTunnel 创建 Peer 隧道
func NewPeerTunnel(targetClientID, proxyAddr, peerSecret, localClientID, localVersion string, onClose func()) *PeerTunnel {
	ctx, cancel := context.WithCancel(context.Background())
	return &PeerTunnel{
		targetClientID: targetClientID,
		proxyAddr:      proxyAddr,
		peerSecret:     peerSecret,
		localClientID:  localClientID,
		localVersion:   localVersion,
		ctx:            ctx,
		cancel:         cancel,
		onClose:        onClose,
	}
}

// SetStreamHandler 设置来自 peer 的流处理器
func (t *PeerTunnel) SetStreamHandler(handler func(stream *yamux.Stream)) {
	t.onStream = handler
}

// Connect 建立 WebSocket 连接，完成 peer 认证，创建 yamux session
func (t *PeerTunnel) Connect() error {
	wsURL := fmt.Sprintf("ws://%s/ws-peer", t.proxyAddr)
	slog.Info("正在连接 peer 隧道", "type", "proxy", "target", t.targetClientID, "url", wsURL)

	// 认证阶段使用带 deadline 的 context，防止对端不响应导致永久阻塞
	authCtx, authCancel := context.WithTimeout(t.ctx, peerAuthTimeout)
	defer authCancel()

	ws, _, err := websocket.Dial(authCtx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("连接 peer WebSocket 失败: %w", err)
	}
	ws.SetReadLimit(50 * 1024 * 1024)

	// 1. 发送 peer 认证（gob 编码的原始 WS 消息，在 yamux 之前）
	ts, sig := httputil.ComputePeerSignature(t.peerSecret, t.localClientID)
	authMsg := peerAuth{
		ClientID:  t.localClientID,
		Secret:    t.peerSecret,
		Timestamp: ts,
		Signature: sig,
		Version:   t.localVersion,
	}
	authData, err := pkgTunnel.WriteGobToBytes(&authMsg)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "编码认证失败")
		return fmt.Errorf("编码 peer_auth 失败: %w", err)
	}
	if err := ws.Write(authCtx, websocket.MessageBinary, authData); err != nil {
		ws.Close(websocket.StatusInternalError, "发送认证失败")
		return fmt.Errorf("发送 peer_auth 失败: %w", err)
	}

	// 2. 等待认证响应（带 deadline）
	_, respData, err := ws.Read(authCtx)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "等待认证响应失败")
		return fmt.Errorf("等待 peer_auth_resp 失败: %w", err)
	}
	var resp peerAuthResp
	if err := pkgTunnel.ReadGobFromBytes(respData, &resp); err != nil {
		ws.Close(websocket.StatusInternalError, "解码认证响应失败")
		return fmt.Errorf("解码 peer_auth_resp 失败: %w", err)
	}
	if !resp.Success {
		ws.Close(websocket.StatusPolicyViolation, "认证失败")
		// 类型化错误携带对端密钥指纹，供服务端比对定位配置错位 (#67)
		return &PeerAuthError{Reason: resp.Reason, SecretFP: resp.SecretFP}
	}

	slog.Info("peer 隧道认证成功", "type", "proxy", "target", t.targetClientID)

	// 3. 创建 yamux session（认证后的 WS 连接转为 yamux 多路复用）
	// 后续流的生命周期由 yamux keepalive 和 OpenStream(ctx) 管理，使用 t.ctx
	netConn := websocket.NetConn(t.ctx, ws, websocket.MessageBinary)
	sess, err := yamux.Client(netConn, peerYamuxConfig(), nil)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "yamux 创建失败")
		return fmt.Errorf("创建 yamux client 失败: %w", err)
	}
	t.session = sess

	t.mu.Lock()
	t.ready = true
	t.mu.Unlock()

	// 4. 启动流接受循环
	go t.acceptLoop()

	return nil
}

// acceptLoop 接受来自 peer 的流
func (t *PeerTunnel) acceptLoop() {
	for {
		stream, err := t.session.AcceptStream()
		if err != nil {
			t.Close()
			return
		}
		if t.onStream != nil {
			go t.onStream(stream)
		} else {
			stream.Close()
		}
	}
}

// OpenStream 打开一条 yamux 流（用于发送代理请求到 peer）。
// 使用带超时的 ctx 调用 v4 OpenStream(ctx)，防止对端不响应时永久阻塞。
func (t *PeerTunnel) OpenStream() (*yamux.Stream, error) {
	t.mu.Lock()
	if !t.ready || t.closed {
		t.mu.Unlock()
		return nil, ErrPeerTunnelClosed
	}
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.ctx, streamOpenTimeout)
	defer cancel()
	return t.session.OpenStream(ctx)
}

// streamOpenTimeout peer 流打开超时
const streamOpenTimeout = 30 * time.Second

func (t *PeerTunnel) IsReady() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ready && !t.closed
}

func (t *PeerTunnel) IsReconnecting() bool   { return t.reconnecting.Load() }
func (t *PeerTunnel) SetReconnecting(v bool) { t.reconnecting.Store(v) }

// Close 关闭隧道
func (t *PeerTunnel) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.ready = false
	t.mu.Unlock()

	t.cancel()
	if t.session != nil {
		t.session.Close()
	}

	slog.Info("peer 隧道已关闭", "type", "proxy", "target", t.targetClientID)
	if t.onClose != nil {
		go t.onClose()
	}
}
