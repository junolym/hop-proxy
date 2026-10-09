package localproxy

import (
	"log/slog"
	"net/http"

	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// proxyViaPeerTunnel 通过 peer yamux 隧道发送 HTTP 请求（客户端 A 侧，Path 4）
// 使用 http.Transport（DialContext 开 peer 流）+ 原生 HTTP/1.1，请求/响应 body 流式。
// hop 中的路由字段（TargetURL/代理策略/密钥）在此填齐并经 envelope 传给 peer 执行端。
func (p *LocalProxy) proxyViaPeerTunnel(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) {
	peerTunnel, err := p.peerManager.GetTunnel(app.ProxyAddress, app.ProxyAddress, app.ProxySecret, p.clientID)
	if err != nil {
		slog.Error("获取 peer 隧道失败", append(hop.LogAttrs(), "error", err, "address", app.ProxyAddress)...)
		http.Error(w, "连接 peer 客户端失败", http.StatusBadGateway)
		return
	}

	openStream := func() (*yamux.Stream, error) {
		st, oerr := peerTunnel.OpenStream()
		if oerr != nil {
			// 尝试重连一次
			peerTunnel, err = p.peerManager.ReconnectTunnel(app.ProxyAddress, app.ProxyAddress, app.ProxySecret, p.clientID)
			if err != nil {
				return nil, err
			}
			return peerTunnel.OpenStream()
		}
		return st, nil
	}

	// 构建出站请求（克隆入站请求，保留流式 body）
	outreq := r.Clone(r.Context())
	// 统一解析出站请求头（#46）：剥离入站 X-Hop-* → 合并自定义 headers →
	// Host/Origin 解析 → X-Forwarded-* 三件套
	outreq.Header, outreq.Host = httputil.ResolveOutboundHeader(r.Header, app.CustomHeaders, app.HeaderMode, app.TargetURL, r.Host, httputil.RequestProto(r), httputil.RemoteIP(r))
	httputil.RemoveHopHeaders(outreq.Header)
	// 构造出站上下文并经 envelope 传递（#47 限流 / #52 agent 密钥 / #53 原始路径）
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
	// http.Transport 要求绝对 URL（scheme+host），隧道请求行只用 path+query
	pkgTunnel.AbsoluteURL(outreq)

	transport := pkgTunnel.NewHTTPTransport(openStream)
	resp, err := transport.RoundTrip(outreq)
	if err != nil {
		slog.Error("peer 隧道请求失败", append(hop.LogAttrs(), "error", err)...)
		http.Error(w, "peer 隧道不可用", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if err := httputil.StreamResponse(w, r, resp); err != nil {
		slog.Warn("peer 隧道写回浏览器失败", append(hop.LogAttrs(), "error", err)...)
	}
}

// ClosePeerTunnels 关闭所有 peer 隧道
func (p *LocalProxy) ClosePeerTunnels() {
	p.peerManager.CloseAll()
}

// Close 关闭本地代理及其所有子资源
func (p *LocalProxy) Close() {
	p.peerManager.CloseAll()
}
