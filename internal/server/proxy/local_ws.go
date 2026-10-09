package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxydial"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// proxyLocalWebSocket 本机直接代理 WebSocket 连接，不走隧道
// 使用 coder/websocket.Dial 直接连接目标并双向转发
// rc 提供目标地址、代理策略（peer/SOCKS/agent）、限流配置与日志关联字段。
func (p *Proxy) proxyLocalWebSocket(rc *ReqContext) {
	r := rc.R
	// 将 http(s) 目标 URL 转换为 ws(s)
	wsURL := strings.TrimRight(rc.Hop.TargetURL, "/") + r.URL.RequestURI()
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	proxyLogTag := rc.Hop.ProxyType
	if rc.Hop.AgentKeyUUID != "" {
		proxyLogTag = "agent:" + rc.Hop.AgentKeyUUID[:min(8, len(rc.Hop.AgentKeyUUID))]
	}
	slog.Info("本机 WebSocket 代理", append(rc.LogAttrs(), "url", wsURL, "proxy", proxyLogTag)...)

	// 统一解析出站请求头（#46）：自定义 headers 合并、Host/Origin 解析、
	// X-Forwarded-* 三件套；WS 拨号头基于解析结果构建
	resolvedHeader, host := httputil.ResolveOutboundHeader(r.Header, rc.CustomHeaders, rc.App.HeaderMode, rc.Hop.TargetURL, r.Host, httputil.RequestProto(r), httputil.RemoteIP(r))
	customHost := host
	opts := httputil.BuildWSDialOptions(resolvedHeader, customHost)

	// 设置代理（超时按上下文的代理限制配置生效值；#47）
	switch {
	case rc.Hop.AgentKeyUUID != "":
		// agent 安全代理：复用缓存的 agent Transport，Noise 握手在 DialContext 内完成
		transport, err := p.GetAgentTransport(rc.Hop.AgentKeyUUID)
		if err != nil {
			slog.Error("获取 agent Transport 失败", append(rc.LogAttrs(), "error", err, "agent_key", rc.Hop.AgentKeyUUID)...)
			http.Error(rc.W, "安全代理不可用", http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: transport}
		slog.Debug("本机 WebSocket 使用 agent 代理", append(rc.LogAttrs(), "agent_key", rc.Hop.AgentKeyUUID)...)

	case rc.Hop.ProxyType == "peer" && rc.Hop.ProxyAddress != "":
		// peer 代理：复用缓存的 Transport，SNI 由 GetPeerTransport 在创建时一次性设置
		transport, err := p.GetPeerTransport(rc.Hop.ProxyAddress, HostClientID, rc.Hop.PeerSecret, customHost, rc.Hop.ConnectTimeout, rc.Hop.ReadTimeout)
		if err != nil {
			slog.Error("peer 代理配置失败", append(rc.LogAttrs(), "error", err)...)
			http.Error(rc.W, err.Error(), http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: transport}
		slog.Debug("本机 WebSocket 使用 peer 代理", append(rc.LogAttrs(), "address", rc.Hop.ProxyAddress)...)

	case proxydial.IsProxyConfigured(rc.Hop.ProxyType, rc.Hop.ProxyAddress):
		// SOCKS5 / Shadowsocks 代理：复用缓存的 Transport
		transport, err := p.GetSocksTransport(rc.Hop.ProxyType, rc.Hop.ProxyAddress, rc.Selection.ProxyPassword, customHost, rc.Hop.ConnectTimeout, rc.Hop.ReadTimeout)
		if err != nil {
			slog.Error("创建代理拨号器失败", append(rc.LogAttrs(), "error", err)...)
			http.Error(rc.W, "创建代理拨号器失败: "+err.Error(), http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: transport}
		slog.Debug("本机 WebSocket 使用代理", append(rc.LogAttrs(), "proxy_type", rc.Hop.ProxyType, "address", rc.Hop.ProxyAddress)...)
	}

	// 升级用户连接为 WebSocket
	userWS, err := websocket.Accept(rc.W, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       httputil.ParseSubprotocolsFromHTTP(r.Header),
	})
	if err != nil {
		slog.Error("外部 WebSocket 升级失败", append(rc.LogAttrs(), "error", err)...)
		return
	}
	// 使用 sync.Once 包装 userWS.Close，确保只执行一次（语义明确，防止 defer + 提前关闭路径重复调用）
	var closeUserOnce sync.Once
	closeUserWS := func(code websocket.StatusCode, reason string) {
		closeUserOnce.Do(func() { userWS.Close(code, reason) })
	}
	defer closeUserWS(websocket.StatusNormalClosure, "关闭")

	// 连接目标 WebSocket
	ctx := r.Context()
	// 连接超时按代理限制配置生效值（#47）：仅包裹拨号阶段（dialCtx），
	// 桥接阶段使用原始 ctx 不受限——否则超时到期 ws.Read(ctx) 失败导致连接被关
	dialCtx := ctx
	if rc.Hop.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, rc.Hop.ConnectTimeout)
		defer cancel()
	}
	pkgTunnel.EnsureWSSSecureTLS(opts, wsURL)
	targetWS, dialResp, err := websocket.Dial(dialCtx, wsURL, opts)
	if err != nil {
		// 记录实际发给目标的最末端请求（opts.HTTPHeader 是过滤+改写后的拨号头）
		sentHost := customHost
		if sentHost == "" {
			sentHost = httputil.HostOfURL(rc.Hop.TargetURL)
		}
		slog.Error("连接目标 WebSocket 失败", append(rc.LogAttrs(),
			"error", err, "url", wsURL, "method", r.Method, "host", sentHost,
			"request_headers", httputil.HeadersFromHTTP(opts.HTTPHeader))...)
		// 用户侧已升级，只能优雅关闭
		closeUserWS(websocket.StatusBadGateway, "连接目标服务失败")
		return
	}
	// 捕获最末端 req/resp headers（opts.HTTPHeader 是实际发给后端的请求头，dialResp 是后端返回的握手响应头）
	if rc.HdrCapture != nil {
		rc.HdrCapture.ReqHeaders = opts.HTTPHeader.Clone()
		if dialResp != nil {
			rc.HdrCapture.RespHeaders = dialResp.Header.Clone()
		}
	}
	defer targetWS.Close(websocket.StatusNormalClosure, "关闭")

	// 设置读取限制，防止 OOM（按代理限制配置生效值，#47；0=不限）
	if rc.Hop.MaxBodySize > 0 {
		userWS.SetReadLimit(rc.Hop.MaxBodySize)
		targetWS.SetReadLimit(rc.Hop.MaxBodySize)
	}

	// 双向转发
	// goroutine 1: 目标 -> 用户
	// 目标端关闭时主动关闭 userWS，让主循环退出，避免 handler goroutine 残留
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer closeUserWS(websocket.StatusNormalClosure, "目标已关闭")
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

	// 主循环: 用户 -> 目标
	// 通过 select 监听 done，目标端关闭时立即退出
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
