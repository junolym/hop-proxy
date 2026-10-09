package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/robin/hop-proxy/internal/appcfg"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/random"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
	"github.com/robin/hop-proxy/pkg/vars"
)

// probeTimeout 单次探测超时时间
const probeTimeout = 10 * time.Second

// ProbeResult 探测结果
type ProbeResult struct {
	Status     string // db.ProbeStatusAvailable / Error / Failed / Timeout / Offline
	StatusCode int
	Detail     string
}

// ProbeApp 对指定应用发起一次可用性探测。
// 复用代理路径的客户端选择与 Transport 构造逻辑，避免不一致。
// 不会更新 app.LastUsedAt（探测与实际访问分离）。
func (p *Proxy) ProbeApp(ctx context.Context, appID int64) ProbeResult {
	app, err := p.db.GetApp(appID)
	if err != nil {
		return ProbeResult{Status: db.ProbeStatusFailed, Detail: fmt.Sprintf("加载应用失败: %v", err)}
	}
	if !app.Enabled {
		return ProbeResult{Status: db.ProbeStatusFailed, Detail: "应用已禁用"}
	}

	// 获取客户端列表（按优先级）
	clients, err := p.db.FetchClientsWithTarget(appID)
	if err != nil {
		return ProbeResult{Status: db.ProbeStatusFailed, Detail: fmt.Sprintf("加载客户端列表失败: %v", err)}
	}

	selection := p.selectClient(appID, app.LoadBalance, clients, app.TargetURL)
	if selection.ClientID == "" {
		// 无可用客户端 → 离线
		return ProbeResult{Status: db.ProbeStatusOffline}
	}

	// 构造目标 URL：selection.TargetURL + "/"
	targetURL := strings.TrimRight(selection.TargetURL, "/") + "/"

	// 构造 GET 请求（HEAD 很多后端不支持，会返回 501 Not Implemented）
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return ProbeResult{Status: db.ProbeStatusFailed, Detail: fmt.Sprintf("构造请求失败: %v", err)}
	}

	// 探测请求独立生成 request_id（#62）：探测是系统发起的请求入口之一，
	// 与真实用户请求的关联链路隔离；Path 2 经 envelope 透传给执行端日志
	hop := pkgTunnel.NewHopContext(random.RequestID())
	hop.Subdomain = app.Subdomain
	hop.AppID = appID

	// 获取自定义 headers
	customHeaders, err := p.db.GetAppCustomHeaders(appID)
	if err != nil {
		slog.Warn("探测：获取自定义 Headers 失败", append(hop.LogAttrsWithType("probe"), "error", err)...)
		customHeaders = make(map[string]string)
	}

	// 统一解析出站请求头（#46）：与代理路径发起端同一函数，按应用的
	// 请求头缺省处理模式（header_mode）模拟真实用户访问：
	//   - 自定义 headers 合并 + Host 解析（自定义优先，否则目标 URL 提取）
	//   - auto_xff：补充 X-Forwarded-Proto/Host（模拟浏览器经 nginx 访问
	//     公网子域名 https://subdomain.proxy_domain 的视角）
	//   - auto_origin：Origin 改写为 scheme://host（探测请求本身无 Origin，不构造）
	// clientIP 留空：探测无真实客户端 IP，不伪造 X-Forwarded-For
	proxyDomain, _ := p.db.GetSetting("proxy_domain")
	origHost := app.Subdomain
	if proxyDomain != "" {
		origHost = app.Subdomain + "." + proxyDomain
	}

	// 自定义 Header 值做内置变量展开（#50）：探测模拟浏览器经公网子域名
	// 访问，变量用合成值（host=子域名.代理域、proto=https、method=GET、path=/）；
	// remote_ip / user_* 无真实值，展开为空串/0。探测无子域名捕获组，captures 为空。
	probeVars := &vars.RequestVars{
		Host:      origHost,
		Subdomain: app.Subdomain,
		Proto:     "https",
		Method:    http.MethodGet,
		Path:      "/",
		AppID:     appID,
		AppName:   app.Name,
	}
	customHeaders = appcfg.ExpandCustomHeaders(customHeaders, nil, probeVars)

	req.Header, req.Host = httputil.ResolveOutboundHeader(
		http.Header{}, customHeaders, app.HeaderMode, selection.TargetURL,
		origHost, "https", "")

	// 剥离 HopProxy 内部 Cookie（与代理路径保持一致，避免泄漏到后端）
	httputil.StripHopProxyCookies(req.Header)

	// 探测使用代理限制配置生效值（#47），与真实代理路径行为一致
	cfg := p.effectiveCfg(app)
	hop.TargetURL = selection.TargetURL
	hop.ProxyType = selection.ProxyType
	hop.ProxyAddress = selection.ProxyAddress
	hop.PeerSecret = selection.PeerSecret
	hop.AgentKeyUUID = app.AgentKeyUUID
	hop.SetLimits(cfg)

	// 选择 Transport 并执行请求
	var transport http.RoundTripper
	if selection.ClientID == HostClientID {
		// Path 1：本机直接反向代理，复用 selectDirectTransport
		// （customHost 仅用于 SNI/Host 覆盖，空值表示跟随目标 URL）
		customHost := httputil.HostOf(customHeaders)
		t, err := p.selectDirectTransport(hop, selection.ProxyPassword, customHost)
		if err != nil {
			return ProbeResult{Status: db.ProbeStatusFailed, Detail: fmt.Sprintf("选择传输失败: %v", err)}
		}
		transport = t
	} else {
		// Path 2：隧道代理
		conn, ok := p.hub.GetConn(selection.ClientID)
		if !ok {
			return ProbeResult{Status: db.ProbeStatusOffline, Detail: "客户端已离线"}
		}
		// 上下文经 envelope 带外传递（与 proxyHTTP 保持一致）
		if err := hop.ApplyTo(req.Header); err != nil {
			return ProbeResult{Status: db.ProbeStatusFailed, Detail: "上下文编码失败"}
		}
		// http.Transport 要求绝对 URL（隧道请求行只用 path+query）
		// 隧道内传输用 HTTP，目标 TLS 由客户端处理（与 ServePeerForward 一致）
		req.URL.Scheme = "http"
		pkgTunnel.AbsoluteURL(req)
		transport = pkgTunnel.NewHTTPTransport(conn.OpenStream)
	}

	// 捕获发往后端的请求头（Path 2 含 X-Hop-* 带外头，日志展示时剥离），
	// 与代理路径的 hdrCapture 排查手段对齐
	hdrCapture := &httputil.HeaderCapture{}

	client := &http.Client{
		Transport: httputil.NewHeaderCaptureTransport(transport, hdrCapture),
		Timeout:   probeTimeout,
		// 不跟随重定向：3xx 也算探测成功（目标可达），
		// 避免跟随到登录页等地址后拿到非预期状态码
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		// 区分超时与其他失败
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return ProbeResult{Status: db.ProbeStatusTimeout, Detail: err.Error()}
		}
		// 隧道开流失败视为客户端离线
		if errors.Is(err, pkgTunnel.ErrTunnelOpen) {
			return ProbeResult{Status: db.ProbeStatusOffline, Detail: "客户端不在线"}
		}
		return ProbeResult{Status: db.ProbeStatusFailed, Detail: err.Error()}
	}
	// 读取响应 body：前 256 字节作为失败原因片段（5xx 时写入探测状态展示），
	// 其余丢弃并统计响应大小
	limited := io.LimitReader(resp.Body, 256)
	bodySnippet, _ := io.ReadAll(limited)
	rest, _ := io.Copy(io.Discard, resp.Body)
	size := int64(len(bodySnippet)) + rest
	_ = resp.Body.Close()

	slog.Debug("应用探测完成", append(hop.LogAttrsWithType("probe"),
		"client_id", selection.ClientID,
		"status_code", resp.StatusCode,
	)...)

	// 探测请求详情 debug 日志：与代理路径的"代理请求详情"对齐，
	// 打印发往后端的最末端 req/resp headers（X-Hop-* 带外头剥离后）
	detailAttrs := append(hop.LogAttrsWithType("probe"),
		"method", http.MethodGet,
		"path", "/",
		"status", resp.StatusCode,
		"size", httputil.FormatSize(int(size)),
	)
	if hdrCapture.ReqHeaders != nil {
		sentHeader := hdrCapture.ReqHeaders.Clone()
		httputil.StripHopProxyHeaders(sentHeader)
		detailAttrs = append(detailAttrs, "req_headers", httputil.HeadersFromHTTP(sentHeader))
	}
	if hdrCapture.RespHeaders != nil {
		detailAttrs = append(detailAttrs, "resp_headers", httputil.HeadersFromHTTP(hdrCapture.RespHeaders))
	}
	slog.Debug("探测请求详情", detailAttrs...)

	// 分类响应状态
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 500:
		// 2xx/3xx/4xx → 可用
		return ProbeResult{Status: db.ProbeStatusAvailable, StatusCode: resp.StatusCode}
	case resp.StatusCode >= 500:
		// 5xx → 错误；peer 认证失败时优先给出配置错位的定位详情 (#67)
		detail := p.peerAuthMismatchDetail(resp, selection, app.UserID)
		if detail == "" {
			detail = strings.TrimSpace(string(bodySnippet))
			if detail == "" {
				detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
			} else {
				detail = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, detail)
			}
		}
		return ProbeResult{Status: db.ProbeStatusError, StatusCode: resp.StatusCode, Detail: detail}
	default:
		// 其他罕见状态码归为可用（如 1xx 信息响应）
		return ProbeResult{Status: db.ProbeStatusAvailable, StatusCode: resp.StatusCode}
	}
}

// peerAuthMismatchDetail 解析 peer 执行端回传的对端密钥指纹（X-Hop-Peer-Secret-FP），
// 与下发给执行端的密钥指纹比对：不一致说明代理配置的 target_client_id 指向的
// 客户端记录 ≠ 实际监听 proxy_address 的客户端（常见于目标客户端删除重建后配置
// 仍指向旧记录），自动定位地址上实际的客户端并给出修正指引 (#67)。
// 非该场景（无指纹/指纹一致/非 peer 路径）返回空串，由调用方回退通用详情。
func (p *Proxy) peerAuthMismatchDetail(resp *http.Response, selection ClientSelection, userID int64) string {
	fp := pkgTunnel.PeerSecretFP(resp.Header)
	if fp == "" || selection.ProxyType != "peer" || selection.PeerSecret == "" {
		return ""
	}
	if httputil.PeerSecretFingerprint(selection.PeerSecret) == fp {
		// 指纹一致仍被拒：时间戳/签名等其他原因，不属于配置错位
		return ""
	}

	// 定位地址上实际的客户端：在该用户客户端中按指纹匹配
	actualID, actualName, err := p.db.FindClientByPeerSecretFP(userID, fp)
	if err != nil {
		slog.Warn("探测：按密钥指纹定位客户端失败", "type", "probe", "error", err)
		return fmt.Sprintf("peer 密钥不匹配（对端密钥指纹 %s）", fp)
	}
	if actualID == "" {
		return fmt.Sprintf("peer 密钥不匹配（对端密钥指纹 %s 不属于本用户任何客户端，地址可能指向其他客户端）", fp)
	}

	// 配置目标客户端（旧记录可能已被删除重建，名称取不到时仅展示 ID）
	target := selection.ProxyTargetClient
	if tc, err := p.db.GetClient(target); err == nil {
		target = fmt.Sprintf("%s(%s)", tc.Name, tc.ID)
	} else {
		target = fmt.Sprintf("%s（记录不存在，可能已删除重建）", target)
	}
	return fmt.Sprintf("peer 密钥不匹配：代理配置目标客户端 %s，地址实际客户端 %s(%s)，请修正代理配置的目标客户端",
		target, actualName, actualID)
}
