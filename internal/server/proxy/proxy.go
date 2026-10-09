// Package proxy 处理子域名请求的代理转发。
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/tunnel"
	"github.com/robin/hop-proxy/pkg/agentcrypto"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxycfg"
	"github.com/robin/hop-proxy/pkg/proxydial"
	"github.com/robin/hop-proxy/pkg/random"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
	"github.com/robin/hop-proxy/pkg/vars"
)

// HostClientID 表示服务端本机作为客户端的特殊 ID
const HostClientID = "__host__"

// streamBufSize 流式读取缓冲区大小
// 32KB：确保每次 WebSocket 写入快速完成，减少大块写入阻塞 ctrlCh（心跳 pong）的时间
const streamBufSize = 32 * 1024

// effectiveCfg 计算应用请求的代理限制配置最终生效值（#47）：
// settings 表全局 proxy_config + 应用级 proxy_config 覆盖合并。
func (p *Proxy) effectiveCfg(app *db.App) map[string]string {
	global, err := p.db.GetSetting("proxy_config")
	if err != nil {
		global = ""
	}
	appCfg := ""
	if app != nil {
		appCfg = app.ProxyConfig
	}
	return proxycfg.Merge(global, appCfg)
}

// Proxy 代理请求处理器
type Proxy struct {
	db        *db.DB
	hub       *tunnel.Hub
	rrCounter atomic.Uint64 // round robin 计数器

	// peer Transport 缓存：key 为 "proxyAddr|serverName"，复用 TCP 连接池
	peerTransportMu    sync.RWMutex
	peerTransportCache map[string]*peerTransportEntry
	done               chan struct{} // 用于停止清理 goroutine

	// SOCKS5/Shadowsocks 出口 Transport 缓存：key 为 "proxyType|proxyAddress|proxyPassword|serverName"
	// 复用 TCP 连接池，避免每请求新建 dialer + Transport 导致握手开销与 fd 堆积
	socksTransportMu    sync.RWMutex
	socksTransportCache map[string]*peerTransportEntry

	// agent Transport 缓存：key 为 agentKeyUUID，复用 TCP 连接池
	agentTransportMu    sync.RWMutex
	agentTransportCache map[string]*peerTransportEntry

	// 应用最近使用时间节流：同一 app 60 秒内只写一次 DB
	appLastUsedMu sync.Mutex
	appLastUsed   map[int64]time.Time
}

// peerTransportEntry 带最后访问时间的 Transport 缓存条目
type peerTransportEntry struct {
	transport    *http.Transport
	lastAccessAt time.Time
}

// 清理参数常量
const (
	peerTransportCleanupInterval = 5 * time.Minute  // 清理检查间隔
	peerTransportMaxIdle         = 30 * time.Minute // 最大空闲时间，超过则淘汰
)

// NewProxy 创建代理处理器
func NewProxy(database *db.DB, hub *tunnel.Hub) *Proxy {
	p := &Proxy{
		db:                  database,
		hub:                 hub,
		peerTransportCache:  make(map[string]*peerTransportEntry),
		socksTransportCache: make(map[string]*peerTransportEntry),
		agentTransportCache: make(map[string]*peerTransportEntry),
		done:                make(chan struct{}),
		appLastUsed:         make(map[int64]time.Time),
	}
	p.startCleanupGoroutine()
	return p
}

// GetPeerTransport 获取缓存的 peer Transport（按 proxyAddr|secret|serverName|connectMs 复用 TCP 连接池）。
// serverName 用于设置 TLS SNI，为空表示不设置自定义 SNI（Go 使用 URL hostname）。
// connectTimeout / readTimeout 为出站代理配置生效值（#47，缓存 key 含超时值）。
// 缓存 key 必须包含密钥（与 SOCKS 缓存含密码对齐，#67）：peer 密钥在对端重启后会
// 轮换，key 不含密钥时旧 entry 会永久复用创建时冻结的旧密钥（周期访问刷新
// lastAccessAt 使空闲淘汰失效），密钥不匹配的告警将持续到进程重启
func (p *Proxy) GetPeerTransport(proxyAddr, clientID, secret, serverName string, connectTimeout, readTimeout time.Duration) (*http.Transport, error) {
	cacheKey := fmt.Sprintf("%s|%s|%s|%d|%d", proxyAddr, secret, serverName, connectTimeout.Milliseconds(), readTimeout.Milliseconds())
	p.peerTransportMu.RLock()
	_, ok := p.peerTransportCache[cacheKey]
	p.peerTransportMu.RUnlock()
	if ok {
		// 写锁下重新查找：cleanupIdleTransports 可能在 RUnlock→Lock 间淘汰该 key
		// 必须返回重新查到的 entry，否则返回的 Transport 会脱离缓存追踪，空闲连接永不被清理
		p.peerTransportMu.Lock()
		if e, exists := p.peerTransportCache[cacheKey]; exists {
			e.lastAccessAt = time.Now()
			p.peerTransportMu.Unlock()
			return e.transport, nil
		}
		p.peerTransportMu.Unlock()
		// entry 已被淘汰，fall through 到创建新 Transport
	}

	p.peerTransportMu.Lock()
	defer p.peerTransportMu.Unlock()
	// double-check
	if e, exists := p.peerTransportCache[cacheKey]; exists {
		e.lastAccessAt = time.Now()
		return e.transport, nil
	}

	transport, err := httputil.NewPeerTransport(proxyAddr, clientID, secret, connectTimeout)
	if err != nil {
		return nil, err
	}
	transport.ResponseHeaderTimeout = readTimeout
	// 在创建时一次性设置 TLSClientConfig，避免调用方对共享 Transport 原地修改导致数据竞争
	if serverName != "" {
		transport.TLSClientConfig = &tls.Config{ServerName: serverName, InsecureSkipVerify: true}
	}
	p.peerTransportCache[cacheKey] = &peerTransportEntry{
		transport:    transport,
		lastAccessAt: time.Now(),
	}
	return transport, nil
}

// GetAgentTransport 获取缓存的 agent Transport（按 agentKeyUUID 复用）
// Transport 的 DialContext 执行 Noise_XX 握手，然后按 scheme 决定是否 TLS。
func (p *Proxy) GetAgentTransport(agentKeyUUID string) (*http.Transport, error) {
	// 快速路径：缓存命中
	p.agentTransportMu.RLock()
	if e, ok := p.agentTransportCache[agentKeyUUID]; ok {
		e.lastAccessAt = time.Now()
		p.agentTransportMu.RUnlock()
		return e.transport, nil
	}
	p.agentTransportMu.RUnlock()

	p.agentTransportMu.Lock()
	defer p.agentTransportMu.Unlock()
	// double-check
	if e, exists := p.agentTransportCache[agentKeyUUID]; exists {
		e.lastAccessAt = time.Now()
		return e.transport, nil
	}

	// 从 DB 加载私钥
	agentKey, err := p.db.GetAgentKeyByUUID(agentKeyUUID)
	if err != nil {
		return nil, fmt.Errorf("加载 agent 密钥失败: %w", err)
	}
	staticKey, err := agentcrypto.LoadPrivateKey(agentKey.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("加载 agent 私钥失败: %w", err)
	}

	transport := &http.Transport{
		DisableCompression: true,
		// agent Transport 的 DialContext：TCP 连接 → Noise 握手 → 返回加密 net.Conn
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{}
			rawConn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, fmt.Errorf("连接 agent 失败: %w", err)
			}
			noiseConn, err := agentcrypto.DialAndHandshake(rawConn, staticKey)
			if err != nil {
				rawConn.Close()
				return nil, fmt.Errorf("Noise 握手失败: %w", err)
			}
			return noiseConn, nil
		},
		// agent 后端若为 https，在 Noise 隧道内再走 TLS（InsecureSkipVerify 因为内网自签）
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		IdleConnTimeout:   30 * time.Second,
		ForceAttemptHTTP2: false,
	}
	p.agentTransportCache[agentKeyUUID] = &peerTransportEntry{
		transport:    transport,
		lastAccessAt: time.Now(),
	}
	return transport, nil
}

// EvictPeerTransport 主动淘汰指定 proxyAddr 的所有 peer Transport 缓存
// 缓存 key 为 "proxyAddr|serverName|connectMs|readMs" 复合 key，按前缀匹配删除
// 供客户端离线、应用删除等场景调用
func (p *Proxy) EvictPeerTransport(proxyAddr string) {
	prefix := proxyAddr + "|"
	p.peerTransportMu.Lock()
	defer p.peerTransportMu.Unlock()
	for key, entry := range p.peerTransportCache {
		if strings.HasPrefix(key, prefix) {
			entry.transport.CloseIdleConnections()
			delete(p.peerTransportCache, key)
		}
	}
	slog.Debug("淘汰 peer Transport 缓存", "type", "proxy", "proxy_addr", proxyAddr)
}

// GetSocksTransport 获取或创建 SOCKS5/Shadowsocks 出口的缓存 Transport
// serverName 用于设置 TLS SNI，为空表示不设置自定义 SNI（由 http.Transport 按 URL hostname 动态设置）
// 缓存 key 为 "proxyType|proxyAddress|proxyPassword|serverName|connectMs|readMs"，密码不同视为不同出口配置
// connectTimeout / readTimeout 为出站代理配置生效值（#47，缓存 key 含超时值）
func (p *Proxy) GetSocksTransport(proxyType, proxyAddress, proxyPassword, serverName string, connectTimeout, readTimeout time.Duration) (*http.Transport, error) {
	cacheKey := fmt.Sprintf("%s|%s|%s|%s|%d|%d", proxyType, proxyAddress, proxyPassword, serverName, connectTimeout.Milliseconds(), readTimeout.Milliseconds())

	p.socksTransportMu.RLock()
	_, ok := p.socksTransportCache[cacheKey]
	p.socksTransportMu.RUnlock()
	if ok {
		// 写锁下重新查找：cleanupIdleTransports 可能在 RUnlock→Lock 间淘汰该 key
		p.socksTransportMu.Lock()
		if e, exists := p.socksTransportCache[cacheKey]; exists {
			e.lastAccessAt = time.Now()
			p.socksTransportMu.Unlock()
			return e.transport, nil
		}
		p.socksTransportMu.Unlock()
		// entry 已被淘汰，fall through 到创建新 Transport
	}

	p.socksTransportMu.Lock()
	defer p.socksTransportMu.Unlock()
	// double-check
	if e, exists := p.socksTransportCache[cacheKey]; exists {
		e.lastAccessAt = time.Now()
		return e.transport, nil
	}

	dialer, err := proxydial.NewDialerSimple(proxyType, proxyAddress, proxyPassword, connectTimeout)
	if err != nil {
		return nil, err
	}
	// 统一设置 TLSClientConfig：InsecureSkipVerify=true 兼容自签证书场景
	// serverName 非空时显式设置 SNI（与原 WS 路径行为一致）；为空时由 http.Transport 按 URL hostname 动态设置
	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	if serverName != "" {
		tlsConfig.ServerName = serverName
	}
	transport := &http.Transport{
		Dial:                  dialer.Dial,
		DisableCompression:    true,
		TLSClientConfig:       tlsConfig,
		ResponseHeaderTimeout: readTimeout,
	}
	p.socksTransportCache[cacheKey] = &peerTransportEntry{
		transport:    transport,
		lastAccessAt: time.Now(),
	}
	return transport, nil
}

// EvictSocksTransport 主动淘汰指定 proxyAddress 的所有 SOCKS5/Shadowsocks Transport 缓存
// 缓存 key 为 "proxyType|proxyAddress|proxyPassword|serverName" 复合 key，按 proxyAddress 前缀匹配删除
// 供代理配置更新/删除等场景调用
func (p *Proxy) EvictSocksTransport(proxyAddress string) {
	prefix := "|" + proxyAddress + "|"
	p.socksTransportMu.Lock()
	defer p.socksTransportMu.Unlock()
	for key, entry := range p.socksTransportCache {
		// key 格式 "proxyType|proxyAddress|proxyPassword|serverName"，匹配 "|proxyAddress|"
		if strings.Contains(key, prefix) {
			entry.transport.CloseIdleConnections()
			delete(p.socksTransportCache, key)
		}
	}
	slog.Debug("淘汰 SOCKS5/SS Transport 缓存", "type", "proxy", "proxy_addr", proxyAddress)
}

// startCleanupGoroutine 启动定期清理 goroutine，淘汰超时未使用的 Transport
func (p *Proxy) startCleanupGoroutine() {
	go func() {
		ticker := time.NewTicker(peerTransportCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-ticker.C:
				p.cleanupIdleTransports()
			}
		}
	}()
}

// cleanupIdleTransports 清理超过最大空闲时间的 Transport 缓存项
func (p *Proxy) cleanupIdleTransports() {
	now := time.Now()
	p.peerTransportMu.Lock()
	for addr, entry := range p.peerTransportCache {
		if now.Sub(entry.lastAccessAt) > peerTransportMaxIdle {
			entry.transport.CloseIdleConnections()
			delete(p.peerTransportCache, addr)
			slog.Debug("清理空闲 peer Transport 缓存", "type", "proxy", "proxy_addr", addr, "idle_duration", now.Sub(entry.lastAccessAt).Round(time.Second))
		}
	}
	p.peerTransportMu.Unlock()

	p.socksTransportMu.Lock()
	for key, entry := range p.socksTransportCache {
		if now.Sub(entry.lastAccessAt) > peerTransportMaxIdle {
			entry.transport.CloseIdleConnections()
			delete(p.socksTransportCache, key)
			slog.Debug("清理空闲 SOCKS5/SS Transport 缓存", "type", "proxy", "cache_key", key, "idle_duration", now.Sub(entry.lastAccessAt).Round(time.Second))
		}
	}
	p.socksTransportMu.Unlock()
}

// appLastUsedThrottleInterval 应用最近使用时间的节流间隔
const appLastUsedThrottleInterval = 60 * time.Second

// throttledUpdateAppLastUsed 节流更新应用最近使用时间（同一 app 60 秒内只写一次）。
// 节流检查在调用方 goroutine 内同步完成，只在需要写 DB 时启动 goroutine，
// 避免高 QPS 下每个请求都启动 goroutine 只为立即退出。
func (p *Proxy) throttledUpdateAppLastUsed(appID int64) {
	p.appLastUsedMu.Lock()
	if last, ok := p.appLastUsed[appID]; ok && time.Since(last) < appLastUsedThrottleInterval {
		p.appLastUsedMu.Unlock()
		return
	}
	p.appLastUsed[appID] = time.Now()
	p.appLastUsedMu.Unlock()

	go func() {
		if err := p.db.UpdateAppLastUsed(appID); err != nil {
			slog.Warn("更新应用使用时间失败", "type", "proxy", "app_id", appID, "error", err)
		}
	}()
}

// ForgetAppLastUsed 清理指定 app 的节流记录（App 删除时调用）
// 避免节流 map 随 App 创建-删除历史无界增长
func (p *Proxy) ForgetAppLastUsed(appID int64) {
	p.appLastUsedMu.Lock()
	delete(p.appLastUsed, appID)
	p.appLastUsedMu.Unlock()
}

// ReportAppAccess 处理客户端本地代理访问上报（Path 3 直连 / Path 4 peer）。
// 异步节流更新 last_used_at（60s 内同一 app 只写一次 DB），并通过 slog 记录访问，
// 让本地代理访问也能在服务端一处集中查看。实现 tunnel.ProxyHandler 接口。
func (p *Proxy) ReportAppAccess(fromClientID string, appID, userID int64, method, path string) {
	p.throttledUpdateAppLastUsed(appID)
	// userID=0 表示匿名访问（auth_method=none → guest）；>0 表示实际用户
	slog.Info("本地代理访问", "type", "proxy", "client_id", fromClientID, "app_id", appID,
		"user_id", userID, "method", method, "path", path)
}

// selectDirectTransport 为直连路径（Path 1/3）选择 RoundTripper。
// agent → 缓存的 agent Transport（Noise_KN 握手）；peer → 缓存的 peer Transport；
// SOCKS5/SS → 缓存的出口 Transport；否则按目标协议与自定义 Host 选 SNI/default Transport。
// proxyPassword 为 SOCKS/SS 出口密码（不跨隧道，故不在上下文中）。
// customHost 用于 TLS SNI（HTTPS + 自定义 Host 的虚拟主机场景）。
// 超时取自上下文（#47，ServeHTTP/Probe 已 SetLimits 填入生效值）。
func (p *Proxy) selectDirectTransport(hop pkgTunnel.HopContext, proxyPassword, customHost string) (http.RoundTripper, error) {
	switch {
	case hop.AgentKeyUUID != "":
		return p.GetAgentTransport(hop.AgentKeyUUID)
	case hop.ProxyType == "peer" && hop.ProxyAddress != "":
		return p.GetPeerTransport(hop.ProxyAddress, HostClientID, hop.PeerSecret, customHost, hop.ConnectTimeout, hop.ReadTimeout)
	case proxydial.IsProxyConfigured(hop.ProxyType, hop.ProxyAddress):
		return p.GetSocksTransport(hop.ProxyType, hop.ProxyAddress, proxyPassword, customHost, hop.ConnectTimeout, hop.ReadTimeout)
	default:
		return httputil.DirectTransportFor(hop.TargetURL, customHost, hop.ConnectTimeout, hop.ReadTimeout), nil
	}
}

// Close 关闭 Proxy，停止后台清理 goroutine 并释放所有 Transport 资源
func (p *Proxy) Close() {
	// 停止清理 goroutine
	close(p.done)

	// 释放所有 peer Transport 的空闲连接
	p.peerTransportMu.Lock()
	for addr, entry := range p.peerTransportCache {
		entry.transport.CloseIdleConnections()
		delete(p.peerTransportCache, addr)
	}
	p.peerTransportMu.Unlock()

	// 释放所有 SOCKS5/SS Transport 的空闲连接
	p.socksTransportMu.Lock()
	for key, entry := range p.socksTransportCache {
		entry.transport.CloseIdleConnections()
		delete(p.socksTransportCache, key)
	}
	p.socksTransportMu.Unlock()

	// 释放所有 agent Transport 的空闲连接
	p.agentTransportMu.Lock()
	for key, entry := range p.agentTransportCache {
		entry.transport.CloseIdleConnections()
		delete(p.agentTransportCache, key)
	}
	p.agentTransportMu.Unlock()

	slog.Info("Proxy 已关闭，已释放所有出口 Transport 缓存", "type", "proxy")
}

// ServeHTTP 处理公网代理请求（Path 1/2/5/6 起点）。
// 解析流程与 Path 5 relay 共享（context.go 的 resolve），响应映射为 respondPublic。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 包装 ResponseWriter，捕获 status code 和 bytes written，用于代理完成日志
	rec := httputil.NewStatusRecorder(w)
	rc := &ReqContext{
		// 生成全链路请求关联 ID（#62）：公网入口是用户请求的最外层，
		// 仅在此处生成（覆盖可能伪造的入站上下文），其余各跳经同一上下文维护
		Hop:        pkgTunnel.NewHopContext(random.RequestID()),
		Style:      entryPublic,
		W:          rec,
		R:          r,
		Recorder:   rec,
		HdrCapture: &httputil.HeaderCapture{},
	}
	defer func() {
		p.logProxyComplete(rc)
	}()

	// SSE 请求剔除 Accept-Encoding，防止目标服务对 SSE 响应做 gzip/br 压缩导致
	// 流式数据被缓冲（块压缩与 SSE 流式语义天然冲突）。非 SSE 请求保留压缩协商。
	httputil.StripAcceptEncodingForSSE(r)

	if re := p.resolve(rc); re != nil {
		p.respondPublic(rc, re)
		return
	}

	// 记录代理信息
	proxyInfo := ""
	if rc.Selection.ProxyType != "" {
		proxyInfo = " via " + rc.Selection.ProxyType + " proxy"
	}
	slog.Debug("代理请求", append(rc.LogAttrs(),
		"method", r.Method, "path", r.URL.Path,
		"client_id", rc.Selection.ClientID, "target_url", rc.Selection.TargetURL,
		"load_balance", rc.Res.LoadBalance, "proxy", proxyInfo,
	)...)

	// 本机客户端：直接本地反向代理
	if rc.Selection.ClientID == HostClientID {
		if httputil.IsWebSocketUpgrade(r) {
			p.proxyLocalWebSocket(rc)
		} else {
			p.proxyLocalHTTP(rc)
		}
		return
	}

	// 处理 WebSocket 升级
	if httputil.IsWebSocketUpgrade(r) {
		p.proxyWebSocket(rc)
		return
	}

	// 隧道代理请求（主备模式下客户端离线时重试一次）。
	// 请求 body 流式透传，仅在 OpenStream 失败（客户端离线，body 未消费）时重试。
	err := p.proxyHTTP(rc)
	if errors.Is(err, errClientGone) && !rc.Res.LoadBalance {
		// 主备模式：尝试选择下一个客户端重试
		retry := p.selectClient(rc.AppID, false, rc.Res.Clients, rc.Res.AppTargetURL)
		if retry.ClientID != "" && retry.ClientID != rc.Selection.ClientID {
			slog.Info("客户端掉线，尝试重试", append(rc.LogAttrs(), "from", rc.Selection.ClientID, "to", retry.ClientID)...)
			err = p.proxyHTTP(rc.withSelection(retry))
		}
	}
	if err != nil {
		slog.Warn("代理错误", append(rc.LogAttrs(), "method", r.Method, "path", r.URL.Path, "status", http.StatusBadGateway, "error", "客户端不在线")...)
		http.Error(rc.Recorder, "客户端不在线", http.StatusBadGateway)
	}
}

// withSelection 返回替换目标客户端后的上下文副本（主备重试用：
// 目标地址直接用关联级 target_url，与 proxyHTTP 发起端一致）。
func (rc *ReqContext) withSelection(sel ClientSelection) *ReqContext {
	retry := *rc
	retry.Selection = sel
	retry.Hop.TargetURL = sel.TargetURL
	retry.Hop.ProxyType = sel.ProxyType
	retry.Hop.ProxyAddress = sel.ProxyAddress
	retry.Hop.PeerSecret = sel.PeerSecret
	return &retry
}

// logProxyComplete 输出代理完成日志，包含 status 和 size
// - status 2xx/3xx → info；4xx/5xx → warning
// - size 友好格式化（>1KB 显示 K）
// - warning：补充打印最末端 req/resp headers（直接发送给后端服务的）
// - 成功：额外输出一条 debug 日志，打印最末端 req/resp headers
func (p *Proxy) logProxyComplete(rc *ReqContext) {
	status := rc.Recorder.Status()
	size := rc.Recorder.BytesWritten()
	sizeStr := httputil.FormatSize(size)
	baseAttrs := append(rc.LogAttrs(),
		"method", rc.R.Method,
		"path", rc.R.URL.Path,
		"status", status,
		"size", sizeStr,
	)
	hdrCapture := rc.HdrCapture
	if status >= 400 {
		// warning：补充打印最末端 req/resp headers 便于排查
		attrs := append([]any{}, baseAttrs...)
		if hdrCapture != nil {
			if hdrCapture.ReqHeaders != nil {
				attrs = append(attrs, "req_headers", httputil.HeadersFromHTTP(hdrCapture.ReqHeaders))
			}
			if hdrCapture.RespHeaders != nil {
				attrs = append(attrs, "resp_headers", httputil.HeadersFromHTTP(hdrCapture.RespHeaders))
			}
		}
		slog.Warn("代理完成", attrs...)
	} else {
		slog.Info("代理完成", baseAttrs...)
		// 成功时额外输出 debug 日志：打印最末端 req/resp headers
		if hdrCapture != nil && (hdrCapture.ReqHeaders != nil || hdrCapture.RespHeaders != nil) {
			debugAttrs := append([]any{}, baseAttrs...)
			if hdrCapture.ReqHeaders != nil {
				debugAttrs = append(debugAttrs, "req_headers", httputil.HeadersFromHTTP(hdrCapture.ReqHeaders))
			}
			if hdrCapture.RespHeaders != nil {
				debugAttrs = append(debugAttrs, "resp_headers", httputil.HeadersFromHTTP(hdrCapture.RespHeaders))
			}
			slog.Debug("代理请求详情", debugAttrs...)
		}
	}
}

// extractCookie 从 Cookie 头中提取指定名称的值
func extractCookie(cookieHeader, name string) string {
	cookies := strings.Split(cookieHeader, ";")
	for _, c := range cookies {
		c = strings.TrimSpace(c)
		if strings.HasPrefix(c, name+"=") {
			return strings.TrimPrefix(c, name+"=")
		}
	}
	return ""
}

// buildRequestVars 构造请求级内置变量（#50）：请求上下文 + 应用信息 + 鉴权用户。
// userID > 0 时查 DB 补充用户名（代理路径本就每请求多次 DB 访问，一次主键查询开销可忽略）。
func (p *Proxy) buildRequestVars(r *http.Request, subdomain string, app *db.App, userID int64) *vars.RequestVars {
	v := vars.FromRequest(r, subdomain)
	if app != nil {
		v.AppID = app.ID
		v.AppName = app.Name
	}
	v.UserID = userID
	if userID > 0 {
		if user, err := p.db.GetUserByID(userID); err == nil {
			v.UserName = user.Username
		}
	}
	return v
}
