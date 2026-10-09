package proxy

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/libp2p/go-yamux/v4"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// WSProxy 管理客户端侧的 WebSocket 代理连接
type WSProxy struct {
	clientID       string
	peerOpenStream PeerTunnelOpenStream
	agentTransport pkgTunnel.AgentTransportProvider
}

// NewWSProxy 创建 WebSocket 代理管理器
func NewWSProxy(clientID string, peerOpenStream PeerTunnelOpenStream, agentTransport pkgTunnel.AgentTransportProvider) *WSProxy {
	return &WSProxy{clientID: clientID, peerOpenStream: peerOpenStream, agentTransport: agentTransport}
}

// HandleWSStream 处理来自服务端的 WS 代理请求（通过 yamux 流）。
// 读取原生 HTTP/1.1 升级请求（ReadProxyRequest 内部 pop 上下文）：
// peer 类型经 peer 隧道转发（Path 6，转发前重编码上下文），否则本机直连 dial 目标。
func (p *WSProxy) HandleWSStream(ctx context.Context, stream *yamux.Stream) {
	defer stream.Close()

	req, hopPtr, err := pkgTunnel.ReadProxyRequest(stream)
	if err != nil {
		slog.Warn("WS 读取升级请求失败", "type", "proxy", "error", err)
		return
	}
	hop := *hopPtr

	slog.Info("代理 WebSocket 连接", append(hop.LogAttrs(), "url", req.URL.String())...)

	if hop.ProxyType == "peer" && hop.ProxyAddress != "" && p.peerOpenStream != nil {
		// Path 6：经 peer 隧道转发
		p.handlePeerWSStream(ctx, stream, req, hop)
		return
	}
	pkgTunnel.ServeDirectWSWithTransport(ctx, stream, req, hop, p.agentTransport)
}

// handlePeerWSStream 通过 peer 隧道转发 WS 升级请求 (#58：peerOpenStream 接线后启用)。
// hop 已 pop；转发前必须 ApplyTo 重编码给 peer 执行端。
func (p *WSProxy) handlePeerWSStream(ctx context.Context, mainStream *yamux.Stream, req *http.Request, hop pkgTunnel.HopContext) {
	peerStream, err := p.peerOpenStream(hop.ProxyAddress, hop.PeerSecret)
	if err != nil {
		slog.Error("peer 隧道流打开失败", append(hop.LogAttrs(), "error", err)...)
		pkgTunnel.WriteWSErrorResponse(mainStream, 502)
		return
	}
	defer peerStream.Close()

	if err := pkgTunnel.WriteStreamType(peerStream, pkgTunnel.StreamWS); err != nil {
		pkgTunnel.WriteWSErrorResponse(mainStream, 502)
		return
	}
	// 重新序列化上下文给下一跳（peer 执行端）
	if err := hop.ApplyTo(req.Header); err != nil {
		slog.Error("peer 转发上下文编码失败", append(hop.LogAttrs(), "error", err)...)
		pkgTunnel.WriteWSErrorResponse(mainStream, 502)
		return
	}
	if err := req.Write(peerStream); err != nil {
		pkgTunnel.WriteWSErrorResponse(mainStream, 502)
		return
	}

	br := bufio.NewReader(peerStream)
	resp, err := pkgTunnel.ReadResponseHeader(br)
	if err != nil || resp.StatusCode != 101 {
		pkgTunnel.WriteWSErrorResponse(mainStream, 502)
		return
	}
	if err := pkgTunnel.WriteResponseHeader(mainStream, resp); err != nil {
		return
	}
	bs := &pkgTunnel.BufioStream{BR: br, WC: peerStream}
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(bs, mainStream)
		bs.Close()
	}()
	io.Copy(mainStream, bs)
	mainStream.Close()
	<-done
}
