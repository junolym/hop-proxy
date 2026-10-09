package localproxy

import (
	"bufio"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxycfg"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// proxyLocalWebSocketViaPeer 通过 Peer 隧道代理 WebSocket 连接（客户端 A 侧，Path 4）
// 流协议：[1B StreamWS][原生 HTTP 升级请求][原生 101 响应][WS 帧双向桥接]
func (p *LocalProxy) proxyLocalWebSocketViaPeer(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) {
	peerTunnel, err := p.peerManager.GetTunnel(app.ProxyAddress, app.ProxyAddress, app.ProxySecret, p.clientID)
	if err != nil {
		slog.Error("获取 peer 隧道失败", append(hop.LogAttrs(), "error", err, "address", app.ProxyAddress)...)
		http.Error(w, "连接 peer 客户端失败", http.StatusBadGateway)
		return
	}

	stream, err := peerTunnel.OpenStream()
	if err != nil {
		http.Error(w, "peer 隧道不可用", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// 构建升级请求：克隆入站请求，统一解析出站请求头（#46，补齐此前缺失的
	// 自定义 headers 合并与 Host 解析），再经 envelope 传递上下文
	outreq := r.Clone(r.Context())
	outreq.Header, outreq.Host = httputil.ResolveOutboundHeader(r.Header, app.CustomHeaders, app.HeaderMode, app.TargetURL, r.Host, httputil.RequestProto(r), httputil.RemoteIP(r))
	hop.TargetURL = app.TargetURL
	hop.AgentKeyUUID = app.AgentKeyUUID
	hop.ProxyType = app.ProxyType
	hop.ProxyAddress = app.ProxyAddress
	hop.PeerSecret = app.ProxySecret
	hop.SetLimits(appCfg(app))
	if err := hop.ApplyTo(outreq.Header); err != nil {
		http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		return
	}

	if err := pkgTunnel.WriteStreamType(stream, pkgTunnel.StreamWS); err != nil {
		http.Error(w, "peer 隧道写入失败", http.StatusBadGateway)
		return
	}
	if err := outreq.Write(stream); err != nil {
		http.Error(w, "peer 隧道写入失败", http.StatusBadGateway)
		return
	}

	br := bufio.NewReader(stream)
	resp, err := pkgTunnel.ReadResponseHeader(br)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		http.Error(w, "目标连接失败", http.StatusBadGateway)
		return
	}

	var acceptSubs []string
	if sp := resp.Header.Get("Sec-WebSocket-Protocol"); sp != "" {
		acceptSubs = []string{sp}
	} else {
		acceptSubs = httputil.ParseSubprotocolsFromHTTP(r.Header)
	}
	userWS, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       acceptSubs,
	})
	if err != nil {
		return
	}
	userWS.SetReadLimit(proxycfg.SizeOf(appCfg(app), "client_max_body_size"))
	defer userWS.CloseNow()

	bs := &pkgTunnel.BufioStream{BR: br, WC: stream}
	pkgTunnel.BridgeWS(r.Context(), bs, userWS)
}

// forwardToServer 转发 HTTP 请求到服务端（Path 5 relay）。
// 使用 http.Transport（DialContext 开 yamux 流）+ 原生 HTTP/1.1 over yamux，
// 请求/响应 body 流式。
func (p *LocalProxy) forwardToServer(w http.ResponseWriter, r *http.Request, hop pkgTunnel.HopContext) {
	if !p.tunnelOnline() {
		http.Error(w, "隧道未连接", http.StatusBadGateway)
		return
	}

	// 应用不在本客户端（无应用配置），body 上限用全局默认值（#47）
	r.Body = http.MaxBytesReader(w, r.Body, proxycfg.SizeOf(nil, "client_max_body_size"))

	outreq := r.Clone(r.Context())
	// 统一解析出站请求头（#46）：目标地址由 server 侧确定，此处 targetURL 传空，
	// 只完成 X-Hop-* 剥离与 X-Forwarded-* 补充（XFF 追加真实客户端 IP、
	// XFP 缺省设置）；Host/Origin 由 server 侧 ResolveOutboundHeader 二次解析。
	// headerMode 此处未知（本地无该应用配置），传空走默认 auto_xff；
	// 应用若配置了 auto_origin/none，server 侧会剥离本跳补充的 X-Forwarded-*。
	// 随后剔除 hop-by-hop 头（http.Transport 要求）。
	outreq.Header, _ = httputil.ResolveOutboundHeader(r.Header, nil, "", "", r.Host, httputil.RequestProto(r), httputil.RemoteIP(r))
	httputil.RemoveHopHeaders(outreq.Header)
	// 转发上下文（仅 RequestID；路由/目标字段由服务端 relay 重新解析填充，#62）
	if err := hop.ApplyTo(outreq.Header); err != nil {
		http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		return
	}
	// http.Transport 要求绝对 URL（scheme+host），隧道请求行只用 path+query
	pkgTunnel.AbsoluteURL(outreq)

	transport := pkgTunnel.NewHTTPTransport(p.client.OpenStream)

	resp, err := transport.RoundTrip(outreq)
	if err != nil {
		slog.Error("转发到服务端失败", append(hop.LogAttrs(), "error", err)...)
		http.Error(w, "隧道不可用", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if err := httputil.StreamResponse(w, r, resp); err != nil {
		slog.Warn("写回浏览器失败", append(hop.LogAttrs(), "error", err)...)
	}
}

// forwardWSToServer 转发 WebSocket 升级请求到服务端（Path 5 relay）
func (p *LocalProxy) forwardWSToServer(w http.ResponseWriter, r *http.Request, hop pkgTunnel.HopContext) {
	if !p.tunnelOnline() {
		http.Error(w, "隧道未连接", http.StatusBadGateway)
		return
	}

	stream, err := p.client.OpenStream()
	if err != nil {
		http.Error(w, "隧道不可用", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	outreq := r.Clone(r.Context())
	// 统一解析出站请求头（#46）：目标地址由 server 侧确定，此处 targetURL 传空，
	// 只完成 X-Hop-* 剥离与 X-Forwarded-* 补充（XFF 追加真实客户端 IP、
	// XFP 缺省设置）；Host/Origin 由 server 侧 ResolveOutboundHeader 二次解析。
	// headerMode 此处未知（本地无该应用配置），传空走默认 auto_xff；
	// 应用若配置了 auto_origin/none，server 侧会剥离本跳补充的 X-Forwarded-*。
	// 保留 Upgrade/Connection 头（原生 WS 升级请求需要）。
	outreq.Header, _ = httputil.ResolveOutboundHeader(r.Header, nil, "", "", r.Host, httputil.RequestProto(r), httputil.RemoteIP(r))
	// 转发上下文（仅 RequestID；路由/目标字段由服务端 relay 重新解析填充，#62）
	if err := hop.ApplyTo(outreq.Header); err != nil {
		http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		return
	}
	if err := pkgTunnel.WriteStreamType(stream, pkgTunnel.StreamWS); err != nil {
		http.Error(w, "隧道写入失败", http.StatusBadGateway)
		return
	}
	if err := outreq.Write(stream); err != nil {
		http.Error(w, "隧道写入失败", http.StatusBadGateway)
		return
	}

	br := bufio.NewReader(stream)
	resp, err := pkgTunnel.ReadResponseHeader(br)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		http.Error(w, "目标连接失败", http.StatusBadGateway)
		return
	}

	var acceptSubs []string
	if sp := resp.Header.Get("Sec-WebSocket-Protocol"); sp != "" {
		acceptSubs = []string{sp}
	} else {
		acceptSubs = httputil.ParseSubprotocolsFromHTTP(r.Header)
	}
	userWS, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       acceptSubs,
	})
	if err != nil {
		return
	}
	userWS.SetReadLimit(50 * 1024 * 1024)
	defer userWS.CloseNow()

	bs := &pkgTunnel.BufioStream{BR: br, WC: stream}
	pkgTunnel.BridgeWS(r.Context(), bs, userWS)
}
