package proxy

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	stdhttputil "net/http/httputil"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxydial"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// HandleForwardedRequest 处理客户端转发过来的 HTTP 请求（Path 5 relay）。
// 使用 stdlib http.Server 解析请求，完成鉴权与客户端选择后，重新分派到本机
// 或目标客户端。连接关闭（含对端 RST）时 http.Server 取消 handler 的请求 ctx，
// 目标请求随之关闭。实现 tunnel.ProxyHandler 接口。
func (p *Proxy) HandleForwardedRequest(ctx context.Context, stream *yamux.Stream, fromClientID string) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.handleForwardedHTTP(w, r, fromClientID)
	})
	pkgTunnel.ServeHTTPStream(stream, handler)
}

// handleForwardedHTTP Path 5 relay 的请求路由逻辑。
// 解析流程与公网入口共享（context.go 的 resolve），响应映射为 respondRelayHTTP；
// Path 5 二次解析是安全边界：入站上下文的路由/目标字段在此被重新解析覆盖。
func (p *Proxy) handleForwardedHTTP(w http.ResponseWriter, r *http.Request, fromClientID string) {
	// 上下文由来源客户端经 envelope 透传（#62）；读即 pop
	hop, err := pkgTunnel.PopHopContext(r.Header)
	if err != nil {
		slog.Warn("读取转发上下文失败", "type", "proxy", "from_client", fromClientID, "error", err)
		http.Error(w, "缺少代理上下文（对端可能未升级）", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	rc := &ReqContext{
		Hop:            hop,
		Style:          entryRelayHTTP,
		W:              w,
		R:              r,
		SourceClientID: fromClientID,
	}
	if re := p.resolve(rc); re != nil {
		p.respondRelayHTTP(rc, re)
		return
	}

	slog.Info("转发代理请求", append(rc.LogAttrs(), "method", r.Method,
		"path", r.URL.Path, "client_id", rc.Selection.ClientID, "target_url", rc.FinalTargetURL)...)

	if rc.Selection.ClientID == HostClientID {
		// 本机执行（Path 5 → Path 1）
		p.proxyForwardedLocal(rc)
		return
	}
	// 转发到目标客户端（Path 5 → Path 2）
	p.proxyForwardedTunnel(rc)
}

// proxyForwardedLocal 本机执行转发请求：反向代理到目标（Path 5 → Path 1）。
// 使用 httputil.ReverseProxy 委托 stdlib：请求/响应 body 流式、hop-by-hop 剥离、
// trailers、100-continue、压缩透传均由 stdlib 负责。
func (p *Proxy) proxyForwardedLocal(rc *ReqContext) {
	r := rc.R
	fullURL := strings.TrimRight(rc.Hop.TargetURL, "/") + r.URL.RequestURI()
	u, err := url.Parse(fullURL)
	if err != nil {
		slog.Error("解析目标 URL 失败", append(rc.LogAttrs(), "error", err, "url", fullURL)...)
		http.Error(rc.W, "目标 URL 无效", http.StatusBadGateway)
		return
	}
	// 归一化请求体编码：目标服务（如 Proxmox VE）可能不支持 chunked 请求体
	if err := httputil.NormalizeRequestBody(r, httputil.RequestBodyNormalizeLimit); err != nil {
		slog.Error("读取请求体失败", append(rc.LogAttrs(), "error", err)...)
		http.Error(rc.W, "读取请求体失败", http.StatusBadRequest)
		return
	}
	// 统一解析出站请求头（#46）。clientIP 留空：XFF 链已含 client A 侧追加的
	// 真实浏览器 IP，且 stdlib ReverseProxy 会自动追加本节点看到的 RemoteAddr。
	resolvedHeader, customHost := httputil.ResolveOutboundHeader(r.Header, rc.CustomHeaders, rc.App.HeaderMode, rc.Hop.TargetURL, r.Host, httputil.RequestProto(r), "")
	transport, err := p.selectDirectTransport(rc.Hop, rc.Selection.ProxyPassword, customHost)
	if err != nil {
		slog.Error("选择代理传输失败", append(rc.LogAttrs(), "error", err)...)
		http.Error(rc.W, err.Error(), http.StatusBadGateway)
		return
	}
	rp := &stdhttputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			req.URL.Path = u.Path
			req.URL.RawQuery = u.RawQuery
			// 使用统一解析后的出站请求头（#46）
			req.Header = resolvedHeader
			req.Host = customHost
			// peer 认证头（HTTPS CONNECT 隧道由 Transport.ProxyConnectHeader 携带，
			// 这里额外设置以兼容 HTTP 明文 peer 路径）
			if rc.Hop.ProxyType == "peer" && rc.Hop.ProxyAddress != "" {
				httputil.SetPeerHeadersOnRequest(req, HostClientID, rc.Hop.PeerSecret)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("本机代理请求失败", append(rc.LogAttrs(), "error", err)...)
			http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(rc.W, r)
}

// proxyForwardedTunnel 经隧道转发到目标客户端（Path 5 → Path 2）。
func (p *Proxy) proxyForwardedTunnel(rc *ReqContext) {
	// http.Transport 需要绝对 URL；请求行只用到 path+query，Host 由 forwardTunnelRoundTrip 设置
	outreq := rc.R.Clone(rc.R.Context())
	pkgTunnel.AbsoluteURL(outreq)

	resp, err := p.forwardTunnelRoundTrip(outreq, rc)
	if err != nil {
		slog.Error("转发请求执行失败", append(rc.LogAttrs(), "error", err)...)
		http.Error(rc.W, "请求目标服务失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if err := httputil.StreamResponse(rc.W, rc.R, resp); err != nil {
		slog.Warn("写回转发响应失败", append(rc.LogAttrs(), "error", err)...)
	}
}

// forwardTunnelRoundTrip 经隧道转发到目标客户端，返回响应。
// 上下文的路由字段已由共享 resolve 重新解析并覆盖（安全边界），
// 转发前 ApplyTo 重新编码给执行端（入站上下文已 pop）。
func (p *Proxy) forwardTunnelRoundTrip(req *http.Request, rc *ReqContext) (*http.Response, error) {
	conn, ok := p.hub.GetConn(rc.Selection.ClientID)
	if !ok {
		return nil, errClientGone
	}
	// 统一解析出站请求头（#46）。clientIP 留空：XFF 链已含 client A 侧追加的
	// 真实浏览器 IP，不追加隧道内部地址。
	req.Header, req.Host = httputil.ResolveOutboundHeader(req.Header, rc.CustomHeaders, rc.App.HeaderMode, rc.Hop.TargetURL, req.Host, httputil.RequestProto(req), "")
	// 重新序列化上下文（目标地址/代理策略/限流配置/日志关联），执行端读取后剥离
	if err := rc.Hop.ApplyTo(req.Header); err != nil {
		return nil, err
	}

	transport := pkgTunnel.NewHTTPTransport(conn.OpenStream)
	return transport.RoundTrip(req)
}

// HandleForwardedWSOpen 处理客户端转发过来的 WebSocket 请求（Path 5 relay）。
// 读取原生 HTTP/1.1 升级请求，鉴权并重新分派到本机或目标客户端，回写 101 后双向桥接。
// 解析流程与公网入口共享，响应映射为 respondRelayWS（错误以 WS 错误帧写回）。
func (p *Proxy) HandleForwardedWSOpen(ctx context.Context, stream *yamux.Stream, fromClientID string) {
	defer stream.Close()

	br := bufio.NewReader(stream)
	req, err := http.ReadRequest(br)
	if err != nil {
		slog.Warn("读取转发 WS 升级请求失败", "type", "proxy", "from_client", fromClientID, "error", err)
		return
	}

	// 上下文由来源客户端经 envelope 透传；读即 pop，路由/目标字段重新解析覆盖
	hop, err := pkgTunnel.PopHopContext(req.Header)
	if err != nil {
		slog.Warn("读取转发 WS 上下文失败", "type", "proxy", "from_client", fromClientID, "error", err)
		pkgTunnel.WriteWSErrorResponse(stream, http.StatusBadRequest)
		return
	}
	if req.URL.Path == "" {
		req.URL.Path = "/"
	}
	rc := &ReqContext{
		Hop:            hop,
		Style:          entryRelayWS,
		R:              req,
		Stream:         stream,
		SourceClientID: fromClientID,
	}
	if re := p.resolve(rc); re != nil {
		p.respondRelayWS(rc, re)
		return
	}

	if rc.Selection.ClientID == HostClientID {
		p.forwardedWSLocal(ctx, stream, rc)
		return
	}
	p.forwardedWSRemote(ctx, stream, rc)
}

// forwardedWSLocal 本机 WS 代理：dial 目标 WS，回写 101，BridgeWS 桥接
func (p *Proxy) forwardedWSLocal(ctx context.Context, stream *yamux.Stream, rc *ReqContext) {
	req := rc.R
	wsURL := strings.TrimRight(rc.Hop.TargetURL, "/") + req.URL.Path
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	opts := httputil.BuildWSDialOptions(req.Header, req.Host)
	switch {
	case rc.Hop.AgentKeyUUID != "":
		transport, err := p.GetAgentTransport(rc.Hop.AgentKeyUUID)
		if err != nil {
			slog.Error("获取 agent Transport 失败", append(rc.LogAttrs(), "error", err, "agent_key", rc.Hop.AgentKeyUUID)...)
			pkgTunnel.WriteWSErrorResponse(stream, http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: transport}
	case rc.Hop.ProxyType == "peer" && rc.Hop.ProxyAddress != "":
		transport, err := p.GetPeerTransport(rc.Hop.ProxyAddress, HostClientID, rc.Hop.PeerSecret, req.Host, rc.Hop.ConnectTimeout, rc.Hop.ReadTimeout)
		if err != nil {
			pkgTunnel.WriteWSErrorResponse(stream, http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: transport}
	case proxydial.IsProxyConfigured(rc.Hop.ProxyType, rc.Hop.ProxyAddress):
		transport, err := p.GetSocksTransport(rc.Hop.ProxyType, rc.Hop.ProxyAddress, rc.Selection.ProxyPassword, req.Host, rc.Hop.ConnectTimeout, rc.Hop.ReadTimeout)
		if err != nil {
			pkgTunnel.WriteWSErrorResponse(stream, http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: transport}
	}

	// 连接超时按代理限制配置生效值（#47）：仅包裹拨号阶段（dialCtx），
	// 桥接阶段使用原始 ctx 不受限——否则超时到期 ws.Read(ctx) 失败导致连接被关
	dialCtx := ctx
	if rc.Hop.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, rc.Hop.ConnectTimeout)
		defer cancel()
	}
	pkgTunnel.EnsureWSSSecureTLS(opts, wsURL)
	targetWS, resp, err := websocket.Dial(dialCtx, wsURL, opts)
	if err != nil {
		// 记录实际发给目标的最末端请求（opts.HTTPHeader 是过滤+改写后的拨号头）
		sentHost := opts.Host
		if sentHost == "" {
			sentHost = httputil.HostOfURL(rc.Hop.TargetURL)
		}
		slog.Error("转发 WS 本机连接失败", append(rc.LogAttrs(),
			"error", err, "url", wsURL, "method", req.Method, "host", sentHost,
			"request_headers", httputil.HeadersFromHTTP(opts.HTTPHeader))...)
		pkgTunnel.WriteWSErrorResponse(stream, http.StatusBadGateway)
		return
	}
	defer targetWS.Close(websocket.StatusNormalClosure, "关闭")
	// 消息上限按代理限制配置生效值（#47；0=不限）
	if rc.Hop.MaxBodySize > 0 {
		targetWS.SetReadLimit(rc.Hop.MaxBodySize)
	}

	if err := pkgTunnel.WriteResponseHeader(stream, resp); err != nil {
		return
	}
	pkgTunnel.BridgeWS(ctx, stream, targetWS)
}

// forwardedWSRemote 远程 WS 代理：开新流到目标客户端，转发升级请求，双向中继。
// 上下文已由共享 resolve 重新解析并填充；转发前 ApplyTo 重新编码（入站上下文已 pop）。
func (p *Proxy) forwardedWSRemote(ctx context.Context, inStream *yamux.Stream, rc *ReqContext) {
	req := rc.R
	targetConn, ok := p.hub.GetConn(rc.Selection.ClientID)
	if !ok {
		pkgTunnel.WriteWSErrorResponse(inStream, http.StatusBadGateway)
		return
	}
	outStream, err := targetConn.OpenStream()
	if err != nil {
		pkgTunnel.WriteWSErrorResponse(inStream, http.StatusBadGateway)
		return
	}
	defer outStream.Close()

	// 重新序列化上下文并写入升级请求（目标客户端读取后剥离）
	if err := rc.Hop.ApplyTo(req.Header); err != nil {
		slog.Error("转发 WS 上下文编码失败", append(rc.LogAttrs(), "error", err)...)
		pkgTunnel.WriteWSErrorResponse(inStream, http.StatusBadGateway)
		return
	}
	if err := pkgTunnel.WriteStreamType(outStream, pkgTunnel.StreamWS); err != nil {
		pkgTunnel.WriteWSErrorResponse(inStream, http.StatusBadGateway)
		return
	}
	if err := req.Write(outStream); err != nil {
		pkgTunnel.WriteWSErrorResponse(inStream, http.StatusBadGateway)
		return
	}

	// 读取目标客户端 101 并转发到入站流
	outBR := bufio.NewReader(outStream)
	resp, err := pkgTunnel.ReadResponseHeader(outBR)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		pkgTunnel.WriteWSErrorResponse(inStream, http.StatusBadGateway)
		return
	}
	if err := pkgTunnel.WriteResponseHeader(inStream, resp); err != nil {
		return
	}

	// 双向桥接：inStream ↔ outStream（WS 帧透传，outStream 的 bufio 包装以保留缓冲字节）
	outBS := &pkgTunnel.BufioStream{BR: outBR, WC: outStream}
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(outBS, inStream)
		outBS.Close()
	}()
	io.Copy(inStream, outBS)
	inStream.Close()
	<-done
}
