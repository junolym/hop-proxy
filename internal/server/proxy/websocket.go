package proxy

import (
	"bufio"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/pkg/httputil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// proxyWebSocket 代理 WebSocket 连接（隧道模式，Path 2/6）。
// 流协议：[1B StreamWS][原生 HTTP/1.1 升级请求][原生 HTTP/1.1 101 响应][WS 帧双向桥接]。
// 握手用原生 HTTP 升级（Upgrade: websocket），握手后帧走 WriteWSFrame/ReadWSFrame 分帧。
// rc 提供目标客户端（Selection）、目标地址与代理策略（Hop）、出站头配置与捕获。
func (p *Proxy) proxyWebSocket(rc *ReqContext) {
	ctx := rc.R.Context()
	r := rc.R

	conn, ok := p.hub.GetConn(rc.Selection.ClientID)
	if !ok {
		slog.Warn("WS 代理: 客户端不在线", append(rc.LogAttrs(), "client_id", rc.Selection.ClientID)...)
		http.Error(rc.W, "客户端不在线", http.StatusBadGateway)
		return
	}

	stream, err := conn.OpenStream()
	if err != nil {
		slog.Error("WS 代理: 打开流失败", append(rc.LogAttrs(), "client_id", rc.Selection.ClientID, "error", err)...)
		http.Error(rc.W, "隧道不可用", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// 构建升级请求：克隆入站请求（保留 Upgrade/Connection 头以标识 WS），
	// 统一解析出站请求头（#46），再经 envelope 传递上下文
	outreq := r.Clone(ctx)
	outreq.Header, outreq.Host = httputil.ResolveOutboundHeader(r.Header, rc.CustomHeaders, rc.App.HeaderMode, rc.Hop.TargetURL, r.Host, httputil.RequestProto(r), httputil.RemoteIP(r))
	if err := rc.Hop.ApplyTo(outreq.Header); err != nil {
		http.Error(rc.W, "请求目标服务失败", http.StatusBadGateway)
		return
	}

	if err := pkgTunnel.WriteStreamType(stream, pkgTunnel.StreamWS); err != nil {
		http.Error(rc.W, "隧道写入失败", http.StatusBadGateway)
		return
	}
	if err := outreq.Write(stream); err != nil {
		slog.Error("WS 代理: 写入升级请求失败", append(rc.LogAttrs(), "error", err)...)
		http.Error(rc.W, "隧道写入失败", http.StatusBadGateway)
		return
	}
	// 捕获发送给执行端的请求头（剥离 X-Hop-* 带外头；执行端还会过滤 WS 握手专用头后才发给后端）
	if rc.HdrCapture != nil {
		sentHeader := outreq.Header.Clone()
		httputil.StripHopProxyHeaders(sentHeader)
		rc.HdrCapture.ReqHeaders = sentHeader
	}

	// 读取执行端返回的 101 响应（仅头，不含升级后的帧）
	br := bufio.NewReader(stream)
	resp, err := pkgTunnel.ReadResponseHeader(br)
	if err != nil {
		slog.Error("WS 代理: 读取 dial 结果失败", append(rc.LogAttrs(), "error", err)...)
		http.Error(rc.W, "代理目标连接失败", http.StatusBadGateway)
		return
	}
	// 捕获最末端 resp headers（后端返回的 WS 握手响应头）
	if rc.HdrCapture != nil {
		rc.HdrCapture.RespHeaders = resp.Header.Clone()
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		slog.Warn("目标服务未接受 WebSocket 连接", append(rc.LogAttrs(), "status", resp.StatusCode)...)
		http.Error(rc.W, "目标服务未接受 WebSocket 连接", http.StatusBadGateway)
		return
	}
	subprotocol := resp.Header.Get("Sec-WebSocket-Protocol")
	slog.Debug("WS 代理: dial 成功", append(rc.LogAttrs(), "subprotocol", subprotocol, "client_id", rc.Selection.ClientID, "target", rc.Hop.TargetURL)...)

	// 用目标协商的子协议 Accept 浏览器
	var acceptSubprotocols []string
	if subprotocol != "" {
		acceptSubprotocols = []string{subprotocol}
	} else {
		acceptSubprotocols = httputil.ParseSubprotocolsFromHTTP(r.Header)
	}
	ws, err := websocket.Accept(rc.W, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       acceptSubprotocols,
	})
	if err != nil {
		slog.Error("外部 WebSocket 升级失败", append(rc.LogAttrs(), "error", err)...)
		return
	}
	// WS 消息上限按代理限制配置生效值（#47；0=不限）
	if rc.Hop.MaxBodySize > 0 {
		ws.SetReadLimit(rc.Hop.MaxBodySize)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	// 双向桥接：浏览器 WS ↔ yamux stream（bufio 包装以保留 ReadResponseHeader 缓冲的字节）
	bs := &pkgTunnel.BufioStream{BR: br, WC: stream}
	done := make(chan struct{})
	go func() {
		pkgTunnel.BridgeWS(ctx, bs, ws)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		stream.Close()
	case <-conn.Done():
		stream.Close()
	}
}

// isWebSocketUpgrade 检查请求是否为 WebSocket 升级（供 forward 路径判断）
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
