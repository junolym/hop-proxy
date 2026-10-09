package proxy

import (
	"errors"
	"log/slog"

	"github.com/robin/hop-proxy/pkg/httputil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// errClientGone 客户端在请求过程中离线
var errClientGone = errors.New("客户端不在线")

// proxyHTTP 代理 HTTP 请求（通过 yamux 流转发到客户端，Path 2/5/6）。
// 使用 http.Transport（DialContext 开 yamux 流）+ 原生 HTTP/1.1 over yamux：
// 请求/响应 body 流式、trailers、多值头、压缩透传、取消传播均由 stdlib 负责。
// rc 提供目标客户端（Selection）、目标地址与代理策略（Hop）、出站头配置与捕获。
func (p *Proxy) proxyHTTP(rc *ReqContext) error {
	conn, ok := p.hub.GetConn(rc.Selection.ClientID)
	if !ok {
		return errClientGone
	}

	// 构建出站请求（克隆入站请求，保留流式 body）
	outreq := rc.R.Clone(rc.R.Context())
	// 统一解析出站请求头（#46）：剥离入站 X-Hop-* → 合并自定义 headers →
	// Host/Origin 解析 → X-Forwarded-* 三件套（按应用请求头缺省处理模式）
	outreq.Header, outreq.Host = httputil.ResolveOutboundHeader(rc.R.Header, rc.CustomHeaders, rc.App.HeaderMode, rc.Hop.TargetURL, rc.R.Host, httputil.RequestProto(rc.R), httputil.RemoteIP(rc.R))
	httputil.RemoveHopHeaders(outreq.Header)
	// 上下文经 envelope 带外传递（目标地址/代理策略/限流配置/日志关联），执行端读后剥离
	if err := rc.Hop.ApplyTo(outreq.Header); err != nil {
		return err
	}
	// http.Transport 要求绝对 URL（scheme+host），隧道请求行只用 path+query
	pkgTunnel.AbsoluteURL(outreq)

	transport := pkgTunnel.NewHTTPTransport(conn.OpenStream)

	// 捕获发送给执行端的请求头（剥离 X-Hop-* 带外头；执行端 ReverseProxy 转发时才会发给后端）
	if rc.HdrCapture != nil {
		sentHeader := outreq.Header.Clone()
		httputil.StripHopProxyHeaders(sentHeader)
		rc.HdrCapture.ReqHeaders = sentHeader
	}
	resp, err := transport.RoundTrip(outreq)
	if err != nil {
		if errors.Is(err, pkgTunnel.ErrTunnelOpen) {
			return errClientGone
		}
		slog.Error("隧道代理请求失败", append(rc.LogAttrs(), "error", err, "client_id", rc.Selection.ClientID)...)
		return err
	}
	defer resp.Body.Close()
	if rc.HdrCapture != nil {
		rc.HdrCapture.RespHeaders = resp.Header.Clone()
	}

	// peer 认证失败指纹比对 (#67)：执行端回传对端实际密钥指纹（X-Hop-Peer-Secret-FP，
	// 写回浏览器前由 StreamResponse 剥离），与下发密钥不一致说明代理配置的
	// target_client_id 指向了错误的客户端记录（常见于目标客户端删除重建后未改配置）
	if rc.Hop.ProxyType == "peer" && rc.Hop.PeerSecret != "" {
		if fp := pkgTunnel.PeerSecretFP(resp.Header); fp != "" && httputil.PeerSecretFingerprint(rc.Hop.PeerSecret) != fp {
			slog.Warn("peer 密钥不匹配：代理配置的目标客户端与地址实际客户端不一致", append(rc.LogAttrs(),
				"proxy_address", rc.Hop.ProxyAddress,
				"sent_secret_fp", httputil.PeerSecretFingerprint(rc.Hop.PeerSecret),
				"actual_secret_fp", fp,
			)...)
		}
	}

	if err := httputil.StreamResponse(rc.W, rc.R, resp); err != nil {
		slog.Warn("写回浏览器失败", append(rc.LogAttrs(), "error", err)...)
	}
	return nil
}
