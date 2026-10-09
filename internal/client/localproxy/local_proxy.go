package localproxy

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	stdhttputil "net/http/httputil"
	"net/url"
	"strings"

	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxycfg"
	"github.com/robin/hop-proxy/pkg/proxydial"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// appCfg 解析服务端应用同步下发的代理配置最终生效值（#47）。
// 服务端已合并全局默认与应用级覆盖；解析失败时回退全默认值。
func appCfg(app *db.ClientAppInfo) map[string]string {
	return proxycfg.Merge("", app.ProxyConfig)
}

// proxyLocalHTTP 本地代理 HTTP 请求（Path 3 直连 / Path 4 peer）
func (p *LocalProxy) proxyLocalHTTP(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) {
	// 异步上报访问到服务端（节流 60s，不阻塞主请求路径）
	p.reportAppAccess(app.ID, userIDFromContext(r.Context()), r.Method, r.URL.Path)

	// 代理限制配置生效值（#47）：body 上限与出站超时按此生效
	cfg := appCfg(app)
	// 限制请求 Body 大小，防止 OOM（按配置生效值，0=不限）
	if maxBody := proxycfg.SizeOf(cfg, "client_max_body_size"); maxBody > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	}

	// 归一化请求体编码：目标服务（如 Proxmox VE）可能不支持 chunked 请求体
	if err := httputil.NormalizeRequestBody(r, httputil.RequestBodyNormalizeLimit); err != nil {
		slog.Error("读取请求体失败", append(hop.LogAttrs(), "error", err)...)
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}

	// peer 代理：通过 peer 隧道转发（http.Transport 流式处理 body）
	if app.ProxyType == "peer" && app.ProxyAddress != "" {
		p.proxyViaPeerTunnel(w, r, app, hop)
		return
	}

	// 直连路径：使用 httputil.ReverseProxy 委托 stdlib 处理
	// 请求/响应 body 流式、hop-by-hop 剥离、trailers、100-continue、压缩透传均由 stdlib 负责
	fullURL := strings.TrimRight(app.TargetURL, "/") + r.URL.RequestURI()
	u, err := url.Parse(fullURL)
	if err != nil {
		slog.Error("解析目标 URL 失败", append(hop.LogAttrs(), "error", err, "url", fullURL)...)
		http.Error(w, "目标 URL 无效", http.StatusBadGateway)
		return
	}
	proxyLogTag := "direct"
	if app.AgentKeyUUID != "" {
		proxyLogTag = "agent:" + app.AgentKeyUUID[:min(8, len(app.AgentKeyUUID))]
	}
	slog.Info("本地代理", append(hop.LogAttrs(),
		"user_id", userIDFromContext(r.Context()),
		"method", r.Method,
		"url", fullURL,
		"proxy", proxyLogTag,
	)...)

	// 统一解析出站请求头（#46）：自定义 headers 合并、Host/Origin 解析、
	// X-Forwarded-* 三件套。clientIP 留空：stdlib ReverseProxy 自动追加
	// RemoteAddr 到 X-Forwarded-For，避免链尾重复。
	resolvedHeader, customHost := httputil.ResolveOutboundHeader(r.Header, app.CustomHeaders, app.HeaderMode, app.TargetURL, r.Host, httputil.RequestProto(r), "")

	// 选择 Transport：agent → SOCKS → 直连（超时按代理配置生效值，#47）
	connectTimeout := proxycfg.DurationOf(cfg, "proxy_connect_timeout")
	readTimeout := proxycfg.DurationOf(cfg, "proxy_read_timeout")
	var transport http.RoundTripper
	switch {
	case app.AgentKeyUUID != "":
		t, err := p.agentManager.GetTransport(app.AgentKeyUUID)
		if err != nil {
			slog.Error("获取 agent Transport 失败", append(hop.LogAttrs(), "error", err, "agent_key", app.AgentKeyUUID)...)
			http.Error(w, "安全代理不可用", http.StatusBadGateway)
			return
		}
		transport = t
	case app.ProxyType != "" && app.ProxyAddress != "":
		// SOCKS5/Shadowsocks 出口代理
		dialer, err := proxydial.NewDialerSimple(app.ProxyType, app.ProxyAddress, app.ProxyPassword, connectTimeout)
		if err != nil {
			slog.Error("创建代理拨号器失败", append(hop.LogAttrs(), "error", err)...)
			http.Error(w, "创建代理拨号器失败: "+err.Error(), http.StatusBadGateway)
			return
		}
		transport = &http.Transport{
			Dial:                  dialer.Dial,
			DisableCompression:    true,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			ResponseHeaderTimeout: readTimeout,
		}
	default:
		transport = httputil.DirectTransportFor(app.TargetURL, customHost, connectTimeout, readTimeout)
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
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("本地代理请求失败", append(hop.LogAttrs(),
				"user_id", userIDFromContext(r.Context()),
				"error", err,
			)...)
			http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		},
	}

	kw := httputil.NewSSEKeepaliveResponseWriter(w)
	// 必须 defer：若 ServeHTTP 内部 panic，非 defer 的 Stop 会被跳过，
	// 泄漏的 keepalive 计时器会在请求结束后写入已回收的 ResponseWriter，
	// 在 timer goroutine 内触发 SIGSEGV 直接杀死进程（#54）
	defer kw.Stop()
	rp.ServeHTTP(kw, r)
}

// proxyLocalWebSocket 本地代理 WebSocket 连接（Path 3 直连）
func (p *LocalProxy) proxyLocalWebSocket(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) {
	// 异步上报访问到服务端（节流 60s，不阻塞主请求路径）
	p.reportAppAccess(app.ID, userIDFromContext(r.Context()), r.Method, r.URL.Path)

	// peer 代理：通过 Peer 隧道转发 WebSocket
	if app.ProxyType == "peer" && app.ProxyAddress != "" {
		p.proxyLocalWebSocketViaPeer(w, r, app, hop)
		return
	}

	// 将 http(s) 目标 URL 转换为 ws(s)
	wsURL := strings.TrimRight(app.TargetURL, "/") + r.URL.RequestURI()
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	slog.Info("本地 WebSocket 代理", append(hop.LogAttrs(),
		"user_id", userIDFromContext(r.Context()),
		"url", wsURL,
		"proxy", app.ProxyType,
	)...)

	// 统一解析出站请求头（#46）：自定义 headers 合并、Host/Origin 解析、
	// X-Forwarded-* 三件套；WS 拨号头基于解析结果构建
	resolvedHeader, customHost := httputil.ResolveOutboundHeader(r.Header, app.CustomHeaders, app.HeaderMode, app.TargetURL, r.Host, httputil.RequestProto(r), httputil.RemoteIP(r))
	opts := httputil.BuildWSDialOptions(resolvedHeader, customHost)

	// 代理限制配置生效值（#47）：连接超时与消息上限按此生效
	cfg := appCfg(app)
	connectTimeout := proxycfg.DurationOf(cfg, "proxy_connect_timeout")

	// 处理 agent / SOCKS5 / Shadowsocks 代理
	switch {
	case app.AgentKeyUUID != "":
		transport, err := p.agentManager.GetTransport(app.AgentKeyUUID)
		if err != nil {
			slog.Error("获取 agent Transport 失败", append(hop.LogAttrs(),
				"error", err, "agent_key", app.AgentKeyUUID,
			)...)
			http.Error(w, "安全代理不可用", http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: transport}
		slog.Debug("本地 WebSocket 使用 agent 代理", append(hop.LogAttrs(),
			"agent_key", app.AgentKeyUUID,
		)...)
	case proxydial.IsProxyConfigured(app.ProxyType, app.ProxyAddress):
		dialer, err := proxydial.NewDialerSimple(app.ProxyType, app.ProxyAddress, app.ProxyPassword, connectTimeout)
		if err != nil {
			slog.Error("创建代理拨号器失败", append(hop.LogAttrs(),
				"error", err,
			)...)
			http.Error(w, "创建代理拨号器失败: "+err.Error(), http.StatusBadGateway)
			return
		}
		transport := &http.Transport{
			Dial:               dialer.Dial,
			DisableCompression: true,
		}
		// wss:// 且设置了自定义 Host 时配置 SNI
		if strings.HasPrefix(wsURL, "wss://") && customHost != "" {
			transport.TLSClientConfig = &tls.Config{
				ServerName:         customHost,
				InsecureSkipVerify: true,
			}
		}
		opts.HTTPClient = &http.Client{Transport: transport}
		slog.Debug("本地 WebSocket 使用代理", append(hop.LogAttrs(),
			"proxy_type", app.ProxyType, "address", app.ProxyAddress,
		)...)
	}

	// 升级用户连接为 WebSocket
	userWS, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       httputil.ParseSubprotocolsFromHTTP(r.Header),
	})
	if err != nil {
		slog.Error("用户 WebSocket 升级失败", append(hop.LogAttrs(), "error", err)...)
		return
	}
	defer userWS.Close(websocket.StatusNormalClosure, "关闭")

	// 连接目标 WebSocket
	ctx := r.Context()
	// 连接超时按代理限制配置生效值（#47）：仅包裹拨号阶段（dialCtx），
	// 桥接阶段使用原始 ctx 不受限——否则超时到期 ws.Read(ctx) 失败导致连接被关
	dialCtx := ctx
	if connectTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, connectTimeout)
		defer cancel()
	}
	pkgTunnel.EnsureWSSSecureTLS(opts, wsURL)
	targetWS, _, err := websocket.Dial(dialCtx, wsURL, opts)
	if err != nil {
		// 记录实际发给目标的最末端请求（opts.HTTPHeader 是过滤+改写后的拨号头）
		sentHost := customHost
		if sentHost == "" {
			sentHost = httputil.HostOfURL(app.TargetURL)
		}
		slog.Error("连接目标 WebSocket 失败", append(hop.LogAttrs(),
			"error", err, "url", wsURL, "method", r.Method, "host", sentHost,
			"request_headers", httputil.HeadersFromHTTP(opts.HTTPHeader))...)
		userWS.Close(websocket.StatusBadGateway, "连接目标服务失败")
		return
	}
	defer targetWS.Close(websocket.StatusNormalClosure, "关闭")

	// 设置读取限制，防止 OOM（按代理限制配置生效值，#47；0=不限）
	if maxBody := proxycfg.SizeOf(cfg, "client_max_body_size"); maxBody > 0 {
		userWS.SetReadLimit(maxBody)
		targetWS.SetReadLimit(maxBody)
	}

	// 双向转发
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer userWS.Close(websocket.StatusNormalClosure, "目标已关闭")
		for {
			msgType, data, readErr := targetWS.Read(ctx)
			if readErr != nil {
				return
			}
			if writeErr := userWS.Write(ctx, msgType, data); writeErr != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		default:
		}
		msgType, data, readErr := userWS.Read(ctx)
		if readErr != nil {
			return
		}
		if writeErr := targetWS.Write(ctx, msgType, data); writeErr != nil {
			return
		}
	}
}
