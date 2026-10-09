package localproxy

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/pkg/httputil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// peerAuthTimeout peer 认证握手超时，防止未认证连接永久占用 handler goroutine
const peerAuthTimeout = 10 * time.Second

// handleWSPeer 处理 /ws-peer WebSocket 端点（客户端 B 侧）
// 接受 peer 连接，验证认证，创建 yamux session，接受代理流
func (p *LocalProxy) handleWSPeer(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Error("WebSocket peer 升级失败", "type", "proxy", "error", err)
		return
	}
	defer ws.CloseNow()

	authCtx, cancel := context.WithTimeout(r.Context(), peerAuthTimeout)
	defer cancel()

	ws.SetReadLimit(50 * 1024 * 1024)

	// 1. 读取 peer 认证（gob 编码的原始 WS 消息，在 yamux 之前）
	_, data, err := ws.Read(authCtx)
	if err != nil {
		slog.Warn("等待 peer_auth 失败", "type", "proxy", "remote_addr", r.RemoteAddr, "error", err)
		ws.Close(websocket.StatusPolicyViolation, "认证失败")
		return
	}

	var auth peerAuth
	if err := pkgTunnel.ReadGobFromBytes(data, &auth); err != nil {
		slog.Warn("解码 peer_auth 失败", "type", "proxy", "remote_addr", r.RemoteAddr, "error", err)
		ws.Close(websocket.StatusPolicyViolation, "认证失败")
		return
	}

	ok, reason := p.verifyPeerAuth(&auth, r.RemoteAddr)
	if !ok {
		// 回传自身当前密钥指纹（不回传 UUID——UUID 是隧道注册凭证，不能暴露给
		// 未认证方）：供发起方链路上的服务端比对，定位代理配置指向了错误客户端 (#67)
		resp := peerAuthResp{Success: false, Reason: reason}
		if p.peerSecret != "" {
			resp.SecretFP = httputil.PeerSecretFingerprint(p.peerSecret)
		}
		respData, _ := pkgTunnel.WriteGobToBytes(&resp)
		ws.Write(authCtx, websocket.MessageBinary, respData)
		ws.Close(websocket.StatusPolicyViolation, "认证失败")
		return
	}

	slog.Info("peer 隧道认证成功", "type", "proxy", "peer_client", auth.ClientID, "peer_version", peerVersionForLog(auth.Version))

	// 2. 发送认证成功响应
	respData, _ := pkgTunnel.WriteGobToBytes(&peerAuthResp{Success: true})
	if err := ws.Write(authCtx, websocket.MessageBinary, respData); err != nil {
		slog.Error("发送 peer_auth_resp 失败", "type", "proxy", "error", err)
		return
	}

	// 3. 创建 yamux server session
	netConn := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
	sess, err := yamux.Server(netConn, peerYamuxConfig(), nil)
	if err != nil {
		slog.Error("创建 yamux server 失败", "type", "proxy", "error", err)
		return
	}
	defer sess.Close()

	// 4. 接受流循环
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			slog.Info("peer 隧道流接受结束", "type", "proxy", "peer_client", auth.ClientID, "error", err)
			return
		}
		go p.handlePeerStream(r.Context(), stream)
	}
}

// handlePeerStream 处理来自 peer 的流（原生 HTTP/1.1，由各 handler 自行读取）
func (p *LocalProxy) handlePeerStream(ctx context.Context, stream *yamux.Stream) {
	defer stream.Close()

	streamType, err := pkgTunnel.ReadStreamTypeWithTimeout(stream, pkgTunnel.StreamTypeReadTimeout)
	if err != nil {
		return
	}

	switch streamType {
	case pkgTunnel.StreamHTTP:
		p.handlePeerHTTPStream(ctx, stream)
	case pkgTunnel.StreamWS:
		p.handlePeerWSStream(ctx, stream)
	}
}

// peerVersionForLog 拨号方版本日志展示：旧版本进程的 peerAuth 不携带版本
// （gob 缺省零值），显式标注以便从认证日志直接识别仍在拨号的孤儿/过期进程 (#67)
func peerVersionForLog(v string) string {
	if v == "" {
		return "未知（旧版本进程）"
	}
	return v
}

// verifyPeerAuth 验证 peer 认证，返回 (是否通过, 失败原因)。
// 失败原因会回传给发起方，便于对端诊断（如密钥不匹配 vs 签名不匹配）。
// 认证日志携带拨号方版本（peer_version）：密钥不匹配持续出现且版本为旧版/
// 未知时，说明存在未升级的孤儿客户端进程仍在拨号 (#67)。
func (p *LocalProxy) verifyPeerAuth(auth *peerAuth, remoteAddr string) (bool, string) {
	ver := peerVersionForLog(auth.Version)
	if p.peerSecret == "" {
		slog.Warn("peer_auth 本地密钥未就绪", "type", "proxy", "peer_client", auth.ClientID, "remote_addr", remoteAddr, "peer_version", ver)
		return false, "本地密钥未就绪"
	}

	if auth.ClientID == "" || auth.Secret == "" || auth.Timestamp == "" || auth.Signature == "" {
		slog.Warn("peer_auth 消息缺少字段", "type", "proxy", "peer_client", auth.ClientID, "remote_addr", remoteAddr, "peer_version", ver)
		return false, "认证消息缺少字段"
	}

	if auth.Secret != p.peerSecret {
		slog.Warn("peer_auth 密钥不匹配", "type", "proxy", "peer_client", auth.ClientID, "remote_addr", remoteAddr, "peer_version", ver)
		return false, "密钥不匹配"
	}

	ts, err := strconv.ParseInt(auth.Timestamp, 10, 64)
	if err != nil {
		slog.Warn("peer_auth 时间戳格式无效", "type", "proxy", "peer_client", auth.ClientID, "remote_addr", remoteAddr, "peer_version", ver, "error", err)
		return false, "时间戳格式无效"
	}
	if time.Now().Unix()-ts > 300 || ts-time.Now().Unix() > 60 {
		slog.Warn("peer_auth 时间戳过期", "type", "proxy", "peer_client", auth.ClientID, "remote_addr", remoteAddr, "peer_version", ver, "timestamp", auth.Timestamp)
		return false, "时间戳过期"
	}

	expected := httputil.HMACSHA256(p.peerSecret, auth.ClientID+":"+auth.Timestamp)
	if auth.Signature != expected {
		slog.Warn("peer_auth 签名不匹配", "type", "proxy", "peer_client", auth.ClientID, "remote_addr", remoteAddr, "peer_version", ver)
		return false, "签名不匹配"
	}

	return true, ""
}

// handlePeerHTTPStream 处理来自 peer 隧道的 HTTP 请求（客户端 B 侧）。
// 使用 stdlib http.Server 解析请求并直连反向代理到目标。
func (p *LocalProxy) handlePeerHTTPStream(ctx context.Context, stream *yamux.Stream) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读即 pop：取上下文并剥离，后续全部在 hop 中维护
		hop, err := pkgTunnel.PopHopContext(r.Header)
		if err != nil {
			slog.Warn("peer 隧道读取上下文失败", "type", "proxy", "error", err)
			http.Error(w, "缺少代理上下文", http.StatusBadRequest)
			return
		}
		if hop.TargetURL == "" {
			http.Error(w, "缺少目标地址", http.StatusBadRequest)
			return
		}
		slog.Info("peer 隧道处理 HTTP 请求", append(hop.LogAttrs(), "method", r.Method, "url", r.URL.Path)...)
		pkgTunnel.ServeHTTPDirectWithTransport(w, r, hop, p.agentManager)
	})
	pkgTunnel.ServeHTTPStream(stream, handler)
}

// handlePeerWSStream 处理来自 peer 隧道的 WS 升级请求（客户端 B 侧，原生 HTTP/1.1）
func (p *LocalProxy) handlePeerWSStream(ctx context.Context, stream *yamux.Stream) {
	req, hopPtr, err := pkgTunnel.ReadProxyRequest(stream)
	if err != nil {
		slog.Warn("peer 隧道读取 WS 升级请求失败", "type", "proxy", "error", err)
		return
	}
	hop := *hopPtr
	slog.Info("peer 隧道处理 WS 打开", append(hop.LogAttrs(), "target", req.URL.String())...)
	pkgTunnel.ServeDirectWSWithTransport(ctx, stream, req, hop, p.agentManager)
}
