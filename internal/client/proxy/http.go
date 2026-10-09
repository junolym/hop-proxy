// Package proxy 执行客户端侧的 HTTP/WS 代理请求（yamux 流版本）。
package proxy

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/libp2p/go-yamux/v4"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// PeerTunnelOpenStream 打开一条 peer 隧道流用于代理请求 (#58)。
// proxyAddress/peerSecret 来自 X-Hop-Ctx 上下文，由具体实现（cmd/client 注入的
// PeerManager 封装）建立/复用到目标客户端的 peer 隧道。
type PeerTunnelOpenStream func(proxyAddress, peerSecret string) (*yamux.Stream, error)

// HandleProxyStream 处理来自服务端的 HTTP 代理请求（通过 yamux 流）。
// 使用 stdlib http.Server 解析请求：peer 类型经 peer 隧道转发（Path 6），
// 否则本机直连执行（Path 2/5）。连接关闭（含对端 RST）时 http.Server 取消
// handler 的请求 ctx，目标请求随之关闭。
// agentTransport 非 nil 时，hop.AgentKeyUUID 非空的请求走 agent Transport。
func HandleProxyStream(ctx context.Context, stream *yamux.Stream, clientID string, peerOpenStream PeerTunnelOpenStream, agentTransport pkgTunnel.AgentTransportProvider) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读即 pop：取上下文并剥离，后续全部在 hop 中维护
		hop, err := pkgTunnel.PopHopContext(r.Header)
		if err != nil {
			slog.Warn("读取代理上下文失败", "type", "proxy", "client_id", clientID, "error", err)
			http.Error(w, "缺少代理上下文", http.StatusBadRequest)
			return
		}
		if hop.TargetURL == "" {
			http.Error(w, "缺少目标地址", http.StatusBadRequest)
			return
		}
		slog.Debug("代理 HTTP 请求", append(hop.LogAttrs(), "method", r.Method, "url", r.URL.Path)...)

		if hop.ProxyType == "peer" && hop.ProxyAddress != "" && peerOpenStream != nil {
			// Path 6：经 peer 隧道转发到目标客户端（ServePeerForward 内部重编码上下文）
			pkgTunnel.ServePeerForward(w, r, hop, func() (*yamux.Stream, error) {
				return peerOpenStream(hop.ProxyAddress, hop.PeerSecret)
			})
			return
		}
		// Path 2/5：本机直连执行（agent Transport 由 ServeHTTPDirectWithTransport 内部按 hop 判断）
		pkgTunnel.ServeHTTPDirectWithTransport(w, r, hop, agentTransport)
	})
	pkgTunnel.ServeHTTPStream(stream, handler)
}
