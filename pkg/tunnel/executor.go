package tunnel

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	stdhttputil "net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/pkg/httputil"
)

// ReadProxyRequest 从流读取原生 HTTP/1.1 代理请求，pop 出 X-Hop-Ctx 上下文，
// 重建完整目标 URL。供 WS 执行端（主隧道 / peer 隧道）复用。
//
// 返回的 req 已设置 req.URL 为完整目标 URL、req.RequestURI 清空（可直接用于
// client.Do 或 req.Write），req.Host 保留发起端设置的值（customHost 或目标 host）。
// req.Body 流式读取自入站流（bufio 包装，body 字节不会丢失）。
//
// 上下文头在此一次完成"取值 + 剥离"（PopHopContext）；需要继续转发到下一跳的
// 调用方（Path 6 中继）必须用返回的 hop 重新 ApplyTo 出站请求。
func ReadProxyRequest(stream io.Reader) (*http.Request, *HopContext, error) {
	br := bufio.NewReader(stream)
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, nil, err
	}
	hop, err := PopHopContext(req.Header)
	if err != nil {
		return nil, nil, err
	}
	if hop.TargetURL == "" {
		return nil, nil, errors.New("缺少目标地址（X-Hop-Ctx.TargetURL）")
	}
	fullURL := strings.TrimRight(hop.TargetURL, "/") + req.URL.RequestURI()
	u, perr := url.Parse(fullURL)
	if perr != nil {
		return nil, nil, perr
	}
	req.URL = u
	req.RequestURI = ""
	return req, &hop, nil
}

// notifyCloseConn 包装 net.Conn，在 Close() 时通知 closed 通道。
// 用于等待 http.Server 完成对流的处理（http.Server 处理完请求后关闭连接）。
type notifyCloseConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *notifyCloseConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// oneConnListener 只吐出一条连接的 listener，供 http.Server.Serve 在单条 yamux 流上
// 处理一次 HTTP 请求。首个 Accept 返回流，之后阻塞直到流被关闭，再返回 io.EOF
// 让 Serve 退出——确保 ServeHTTPStream 等到请求处理完成才返回。
type oneConnListener struct {
	conn     net.Conn
	addr     net.Addr
	released bool
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if !l.released {
		l.released = true
		return l.conn, nil
	}
	// 等待连接（http.Server 的 conn goroutine）关闭
	if nc, ok := l.conn.(*notifyCloseConn); ok {
		<-nc.closed
	}
	return nil, io.EOF
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.addr }

// ServeHTTPStream 在一条 yamux 流上执行一次 HTTP 请求（执行端）。
// stdlib http.Server 负责请求解析与响应写回；连接关闭（含对端 RST）时
// http.Server 自动取消 handler 的请求 ctx，把取消沿链传播到目标。
// 阻塞直到请求处理完成（流被 http.Server 关闭），保证调用方的 defer stream.Close()
// 不会与处理中的请求竞争。
func ServeHTTPStream(stream *yamux.Stream, handler http.Handler) {
	wrapped := &notifyCloseConn{Conn: stream, closed: make(chan struct{})}
	srv := &http.Server{Handler: handler}
	_ = srv.Serve(&oneConnListener{conn: wrapped, addr: stream.LocalAddr()})
}

// AgentTransportProvider 由 client 侧 agenttransport.Manager 实现，用于获取 agent Transport。
// 放在 pkg/tunnel 是为了让执行端在直连分支能按 agent_key_uuid 切换 Transport。
type AgentTransportProvider interface {
	GetTransport(agentKeyUUID string) (http.RoundTripper, error)
}

// ServeHTTPDirectWithTransport 执行端直连分支，支持 agent Transport 注入。
// hop 由入口 PopHopContext 得到（envelope 已剥离，本函数不再触碰 X-Hop 头）；
// hop.AgentKeyUUID 为空或 agentTransport 为 nil 时走默认直连 Transport（DirectTransportFor）。
func ServeHTTPDirectWithTransport(w http.ResponseWriter, r *http.Request, hop HopContext, agentTransport AgentTransportProvider) {
	logCtx := hop.LogAttrs()

	fullURL := strings.TrimRight(hop.TargetURL, "/") + r.URL.RequestURI()
	u, err := url.Parse(fullURL)
	if err != nil {
		slog.Error("解析目标 URL 失败", append(logCtx, "error", err, "url", fullURL)...)
		http.Error(w, "目标 URL 无效", http.StatusBadGateway)
		return
	}
	// 归一化请求体编码：目标服务（如 Proxmox VE）可能不支持 chunked 请求体
	if err := httputil.NormalizeRequestBody(r, httputil.RequestBodyNormalizeLimit); err != nil {
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}

	var transport http.RoundTripper
	if hop.AgentKeyUUID != "" && agentTransport != nil {
		t, err := agentTransport.GetTransport(hop.AgentKeyUUID)
		if err != nil {
			slog.Error("获取 agent Transport 失败", append(logCtx, "error", err, "agent_key", hop.AgentKeyUUID)...)
			http.Error(w, "安全代理不可用", http.StatusBadGateway)
			return
		}
		transport = t
	} else {
		// 出站连接/读超时按上下文限流配置生效（#47）
		transport = httputil.DirectTransportFor(hop.TargetURL, r.Host, hop.ConnectTimeout, hop.ReadTimeout)
	}

	rp := &stdhttputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			req.URL.Path = u.Path
			req.URL.RawQuery = u.RawQuery
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("执行端反向代理失败", append(logCtx, "error", err)...)
			http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// ServePeerForward 执行端 handler 的 peer 分支（Path 6）：经 peer 隧道转发到目标客户端。
// hop 由入口 PopHopContext 得到；转发前必须 ApplyTo 重新编码——上下文"读即 pop"，
// 不重编码则 peer 执行端（B）拿不到目标地址；B 侧再 pop 消费。
func ServePeerForward(w http.ResponseWriter, r *http.Request, hop HopContext, peerOpenStream func() (*yamux.Stream, error)) {
	logCtx := hop.LogAttrs()

	transport := NewHTTPTransport(peerOpenStream)
	// http.Transport 需要绝对 URL；请求行只用到 path+query，Host 沿用 r.Host
	outreq := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = "http"
	if u.Host == "" {
		u.Host = r.Host
	}
	outreq.URL = &u
	outreq.RequestURI = ""
	// 重新序列化上下文给下一跳（peer 执行端）
	if err := hop.ApplyTo(outreq.Header); err != nil {
		http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		return
	}

	resp, err := transport.RoundTrip(outreq)
	if err != nil {
		slog.Error("peer 转发请求失败", append(logCtx, "error", err)...)
		// peer 认证失败时把对端密钥指纹透传给服务端（X-Hop-Peer-Secret-FP 带外
		// 响应头，写回浏览器前由 StreamResponse 剥离），供服务端比对下发的密钥、
		// 定位代理配置指向了错误的客户端记录 (#67)。pkg/tunnel 不能 import
		// internal（PeerAuthError 定义在 internal/client/localproxy），经接口断言解耦
		var fpProvider interface{ PeerSecretFP() string }
		if errors.As(err, &fpProvider) {
			SetPeerSecretFP(w.Header(), fpProvider.PeerSecretFP())
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		http.Error(w, "请求目标服务失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if err := httputil.StreamResponse(w, r, resp); err != nil {
		slog.Warn("peer 隧道写回失败", append(logCtx, "error", err)...)
	}
}

// ServeDirectWSWithTransport 执行端 WS 直连分支，支持 agent Transport 注入。
// hop 由入口 ReadProxyRequest（内部 PopHopContext）得到，envelope 已剥离，
// 不会随 WS 拨号发给目标。
func ServeDirectWSWithTransport(ctx context.Context, stream io.ReadWriteCloser, req *http.Request, hop HopContext, agentTransport AgentTransportProvider) {
	logCtx := hop.LogAttrs()

	wsURL := strings.TrimRight(hop.TargetURL, "/") + req.URL.RequestURI()
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	// 出站请求头已由发起端 ResolveOutboundHeader 统一解析（#46），此处透传
	opts := httputil.BuildWSDialOptions(req.Header, req.Host)

	// agent Transport 注入
	if hop.AgentKeyUUID != "" && agentTransport != nil {
		t, err := agentTransport.GetTransport(hop.AgentKeyUUID)
		if err != nil {
			slog.Error("获取 agent Transport 失败", append(logCtx, "error", err, "agent_key", hop.AgentKeyUUID)...)
			WriteWSErrorResponse(stream, http.StatusBadGateway)
			return
		}
		opts.HTTPClient = &http.Client{Transport: t}
	}

	EnsureWSSSecureTLS(opts, wsURL)
	// 连接超时按上下文限流配置生效（#47）：仅包裹拨号阶段（dialCtx），
	// 桥接阶段使用原始 ctx 不受限——否则超时到期 ws.Read(ctx) 失败导致连接被关
	dialCtx := ctx
	if hop.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, hop.ConnectTimeout)
		defer cancel()
	}
	targetWS, resp, err := websocket.Dial(dialCtx, wsURL, opts)
	if err != nil {
		// 记录实际发给目标的最末端请求（opts 是过滤+改写后的拨号选项，非入站请求）
		sentHost := opts.Host
		if sentHost == "" {
			sentHost = httputil.HostOfURL(hop.TargetURL)
		}
		slog.Error("WS 连接目标失败", append(logCtx,
			"error", err,
			"url", wsURL,
			"method", req.Method,
			"host", sentHost,
			"request_headers", httputil.HeadersFromHTTP(opts.HTTPHeader),
		)...)
		WriteWSErrorResponse(stream, http.StatusBadGateway)
		return
	}
	defer targetWS.Close(websocket.StatusNormalClosure, "关闭")
	// 消息上限按上下文限流配置生效（#47）
	if hop.MaxBodySize > 0 {
		targetWS.SetReadLimit(hop.MaxBodySize)
	}
	if err := WriteResponseHeader(stream, resp); err != nil {
		return
	}
	BridgeWS(ctx, stream, targetWS)
}
