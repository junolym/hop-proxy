package proxy

import (
	"log/slog"
	"net/http"
	stdhttputil "net/http/httputil"
	"net/url"
	"strings"

	"github.com/robin/hop-proxy/pkg/httputil"
)

// proxyLocalHTTP 本机直接反向代理（Path 1：服务端 __host__），不走隧道。
// 使用 httputil.ReverseProxy 委托给 stdlib 处理：请求/响应 body 流式、hop-by-hop 剥离、
// trailers、100-continue、压缩透传均由 stdlib 负责。SSE 响应自动注入 keepalive。
// rc 提供目标地址、代理策略（peer/SOCKS/agent）、限流配置与日志关联字段。
func (p *Proxy) proxyLocalHTTP(rc *ReqContext) {
	fullURL := strings.TrimRight(rc.Hop.TargetURL, "/") + rc.R.URL.RequestURI()
	u, err := url.Parse(fullURL)
	if err != nil {
		slog.Error("解析目标 URL 失败", append(rc.LogAttrs(), "error", err, "url", fullURL)...)
		http.Error(rc.W, "目标 URL 无效", http.StatusBadGateway)
		return
	}
	proxyLogTag := rc.Hop.ProxyType
	if rc.Hop.AgentKeyUUID != "" {
		proxyLogTag = "agent:" + rc.Hop.AgentKeyUUID[:min(8, len(rc.Hop.AgentKeyUUID))]
	}
	slog.Info("本机代理", append(rc.LogAttrs(), "method", rc.R.Method, "url", fullURL, "proxy", proxyLogTag)...)

	// 归一化请求体编码：目标服务（如 Proxmox VE）可能不支持 chunked 请求体
	if err := httputil.NormalizeRequestBody(rc.R, httputil.RequestBodyNormalizeLimit); err != nil {
		slog.Error("读取请求体失败", append(rc.LogAttrs(), "error", err)...)
		http.Error(rc.W, "读取请求体失败", http.StatusBadRequest)
		return
	}

	// 统一解析出站请求头（#46）：自定义 headers 合并、Host/Origin 解析、
	// X-Forwarded-* 三件套。clientIP 留空：stdlib ReverseProxy 自动追加
	// RemoteAddr 到 X-Forwarded-For，避免链尾重复。
	// customHost 同时用于 SNI 与出站 Host 头。
	resolvedHeader, customHost := httputil.ResolveOutboundHeader(rc.R.Header, rc.CustomHeaders, rc.App.HeaderMode, rc.Hop.TargetURL, rc.R.Host, httputil.RequestProto(rc.R), "")

	transport, err := p.selectDirectTransport(rc.Hop, rc.Selection.ProxyPassword, customHost)
	if err != nil {
		slog.Error("选择代理传输失败", append(rc.LogAttrs(), "error", err)...)
		http.Error(rc.W, err.Error(), http.StatusBadGateway)
		return
	}
	// 包装 transport 捕获最末端 req/resp headers
	if rc.HdrCapture != nil {
		transport = httputil.NewHeaderCaptureTransport(transport, rc.HdrCapture)
	}

	rp := &stdhttputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // 每次写入后立即 flush，SSE/流式响应不被缓冲
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

	kw := httputil.NewSSEKeepaliveResponseWriter(rc.W)
	// 必须 defer：若 ServeHTTP 内部 panic（会被 recoveryHandler 恢复），
	// 非 defer 的 Stop 会被跳过，泄漏的 keepalive 计时器会在请求结束后
	// 写入已回收的 ResponseWriter，在 timer goroutine 内触发 SIGSEGV
	// 直接杀死整个进程（#54）
	defer kw.Stop()
	rp.ServeHTTP(kw, rc.R)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
