package localproxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/robin/hop-proxy/internal/appcfg"
	"github.com/robin/hop-proxy/internal/client/agenttransport"
	"github.com/robin/hop-proxy/internal/client/apps"
	"github.com/robin/hop-proxy/internal/client/tunnel"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/random"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
	"github.com/robin/hop-proxy/pkg/vars"
)

// appAccessReportThrottle 客户端上报节流间隔（与服务端 last_used 节流对齐）
const appAccessReportThrottle = 60 * time.Second

// peerAuthRefreshThrottle peer 认证失败后应用缓存刷新节流间隔：
// 防止并发失败请求触发刷新风暴 (#67)
const peerAuthRefreshThrottle = 10 * time.Second

// LocalProxy 客户端本地 HTTP 代理
type LocalProxy struct {
	appManager   *apps.Manager
	sessionCache *SessionCache
	peerManager  *PeerManager
	agentManager *agenttransport.Manager
	client       *tunnel.Client
	tunnelOnline func() bool
	clientID     string // 本客户端 ID（用于 peer 签名）
	peerSecret   string // 本客户端 peer 密钥（用于验证其他客户端的 peer 连接）

	// 本地代理访问上报节流：同一 app 60 秒内只上报一次，避免高 QPS 下开流开销
	appAccessMu sync.Mutex
	appAccess   map[int64]time.Time

	// peer 认证失败自愈节流 (#67)：记录上次触发应用缓存刷新的时间
	authFailMu        sync.Mutex
	authFailRefreshAt time.Time
}

// NewLocalProxyWithAgent 创建本地代理，注入外部 agenttransport.Manager 与 PeerManager
// （与 cmd/client 共享同一实例：Path 4 本地代理与 Path 6 隧道执行器共用 peer 隧道池，#58）
func NewLocalProxyWithAgent(appManager *apps.Manager, client *tunnel.Client, tunnelOnline func() bool, clientID, peerSecret string, agentMgr *agenttransport.Manager, peerMgr *PeerManager) *LocalProxy {
	lp := &LocalProxy{
		appManager:   appManager,
		sessionCache: NewSessionCache(),
		peerManager:  peerMgr,
		agentManager: agentMgr,
		client:       client,
		tunnelOnline: tunnelOnline,
		clientID:     clientID,
		peerSecret:   peerSecret,
		appAccess:    make(map[int64]time.Time),
	}
	// peer 认证失败自愈接线 (#67)：密钥不匹配时刷新应用缓存同步对端新密钥。
	// Path 4（本地代理）与 Path 6（隧道执行器）共用同一 peer 隧道池，统一从此处收口
	peerMgr.SetAuthFailureHandler(lp.handlePeerAuthFailure)
	return lp
}

// handlePeerAuthFailure peer 认证失败（密钥不匹配）自愈 (#67)：
// 说明本地缓存的对端 peer 密钥已过期（对端重启后重新生成），
// 节流触发应用列表刷新，使后续请求携带新密钥重建 peer 隧道。
func (p *LocalProxy) handlePeerAuthFailure() {
	p.authFailMu.Lock()
	if time.Since(p.authFailRefreshAt) < peerAuthRefreshThrottle {
		p.authFailMu.Unlock()
		return
	}
	p.authFailRefreshAt = time.Now()
	p.authFailMu.Unlock()

	go func() {
		if err := p.appManager.Refresh(); err != nil {
			slog.Warn("peer 认证失败后刷新应用列表失败", "type", "proxy", "error", err)
			return
		}
		slog.Info("peer 认证失败，已刷新应用列表以同步对端密钥", "type", "proxy")
	}()
}

// ServeHTTP 处理 HTTP 请求
func (p *LocalProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// /ws-peer 端点：WebSocket peer 隧道
	if r.URL.Path == "/ws-peer" {
		p.handleWSPeer(w, r)
		return
	}

	// 生成全链路请求关联 ID（#62）：本地代理是内网用户请求的最外层入口，
	// 仅在此处生成；后续 Path 3/4/5 各分支在同一 hop 上维护并经 envelope 传递
	hop := pkgTunnel.NewHopContext(random.RequestID())

	// SSE 请求剔除 Accept-Encoding，防止目标服务对 SSE 响应做 gzip/br 压缩导致
	// 流式数据被缓冲（块压缩与 SSE 流式语义天然冲突）。非 SSE 请求保留压缩协商。
	httputil.StripAcceptEncodingForSSE(r)

	proxyDomain, _ := p.appManager.GetDomains()

	// 提取子域名
	subdomain := httputil.ExtractSubdomain(r.Host, proxyDomain)
	if subdomain == "" {
		http.Error(w, "无法解析子域名", http.StatusBadRequest)
		return
	}
	// 子域名保持请求提取值（模糊匹配场景下与注册模式不同，保留用户实际访问的域名
	// 更有助于排查；SSO cookie 构造与 session 归属校验均依赖此值，#40）
	hop.Subdomain = subdomain

	// 查找本地应用
	app, captures := p.appManager.GetApp(subdomain)
	if app == nil {
		// 应用不在本客户端，转发到服务端（hop 仅携带 RequestID/提取的子域名，
		// 路由与目标字段由服务端 relay 重新解析填充）
		slog.Info("应用不在本客户端，转发到服务端", hop.LogAttrs()...)
		if httputil.IsWebSocketUpgrade(r) {
			p.forwardWSToServer(w, r, hop)
		} else {
			p.forwardToServer(w, r, hop)
		}
		return
	}
	// 命中应用：AppID 供日志与下游关联
	hop.AppID = app.ID

	slog.Debug("本地代理请求", append(hop.LogAttrs(),
		"user_id", userIDFromContext(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
	)...)

	// 应用路由规则：覆盖认证方式和目标地址
	// 来源客户端 = 本地代理自身（p.clientID，#70）：仅命中"来源客户端=本客户端"的规则
	effectiveAuthMethod := app.AuthMethod
	routeAuthOverride := db.FindRouteAuthOverride(app.Routes, r.Method, r.URL.Path, p.clientID)
	if routeAuthOverride != "" {
		effectiveAuthMethod = routeAuthOverride
	}

	// 路径豁免（#68）：命中豁免列表则跳过所有认证（含路由规则覆盖），
	// db.IsPathExempt 为 server/client 两侧收口的唯一实现，语义与服务端一致
	var userID int64
	var userName string
	if db.IsPathExempt(r.URL.Path, app.ExemptPaths) {
		slog.Debug("路径豁免 SSO 认证", append(hop.LogAttrsWithType("auth"), "path", r.URL.Path)...)
		userID = -1 // guest，与 auth=none 语义一致
	} else {
		// 按有效认证方式分类处理认证，认证成功后把 user_id 塞入 context 供后续日志使用
		// userID 语义：-1=guest（免认证），0=system 哨兵（未鉴权业务日志），>0=实际用户
		// userName 为鉴权用户名，供内置变量 ${user_name} 展开 (#50)
		switch effectiveAuthMethod {
		case "none":
			// 无需认证：标记为 guest（-1），与 system（0）区分
			userID = -1
		case "token":
			// 仅接受票据认证（不接受 SSO Cookie，不做 SSO 重定向）
			if uid, name, ok := p.checkTokenAuth(w, r, app, hop); !ok {
				return
			} else {
				userID, userName = uid, name
			}
		case "sso_token":
			// SSO + 票据混合模式：票据优先，SSO 回退
			if uid, name, ok := p.checkSSOTokenAuth(w, r, app, hop); !ok {
				return
			} else {
				userID, userName = uid, name
			}
		default:
			// sso / sso_owner / sso_all 及其他值：仅 SSO 认证（不支持票据）
			if uid, name, ok := p.checkAuth(w, r, app, hop); !ok {
				return
			} else {
				userID, userName = uid, name
			}
		}
	}
	// 把 userID 塞入 context，后续代理日志可用
	r = r.WithContext(context.WithValue(r.Context(), userIDKey{}, userID))

	// 构造请求级内置变量（#50）：鉴权后统一收口，供跳转/目标地址/自定义 Header 展开。
	// guest（-1）与其他未鉴权语义统一为 0
	reqVars := vars.FromRequest(r, subdomain)
	reqVars.AppID = app.ID
	reqVars.AppName = app.Name
	if userID > 0 {
		reqVars.UserID = userID
		reqVars.UserName = userName
	}

	// 路由规则目标地址覆盖（来源客户端 = 本地代理自身，#70）
	// 应用级/路由级目标地址统一做捕获组 + 内置变量展开 (#50)
	routeTarget := ""
	if matchedRoute := db.MatchRoute(app.Routes, r.Method, r.URL.Path, p.clientID); matchedRoute != nil {
		routeTarget = matchedRoute.TargetURL
		// 路由规则路径改写 (#53)：鉴权/跳转均按原路径匹配完成，此处改写转发路径；
		// OriginalPath 随上下文传递供 peer 执行端（Path 4）日志关联
		hop.OriginalPath = db.ApplyRoutePathRewrite(matchedRoute, r, subdomain, app.ID)
	}
	finalTargetURL := appcfg.ResolveTargetURL(app.TargetURL, routeTarget, captures, reqVars)
	if routeTarget != "" {
		slog.Debug("路由规则覆盖目标地址", append(hop.LogAttrs(), "original", app.TargetURL, "override", finalTargetURL)...)
	}

	// 自定义 Header 值做捕获组 + 内置变量展开 (#50)
	// app 来自 GetApp 的深拷贝，覆盖 CustomHeaders 不影响缓存
	app.CustomHeaders = appcfg.ExpandCustomHeaders(app.CustomHeaders, captures, reqVars)

	// SSO 应用首页禁用浏览器缓存：浏览器缓存首页后，SSO 会话失效时不再回源，
	// 后续 API 调用被 SSO 拦截导致应用层无响应 (#35)
	// 鉴权通过后设置；鉴权失败已由 checkAuth/checkSSOTokenAuth 返回 302/401
	if r.URL.Path == "/" {
		switch effectiveAuthMethod {
		case "sso", "sso_token", "sso_owner", "sso_all":
			w.Header().Set("Cache-Control", "no-store")
		}
	}

	// 鉴权通过后剥离 HopProxy 内部 Cookie（hopproxy_session / hopproxy_sso_* / hopproxy_csrf 等），
	// 防止这些 Cookie 转发到后端应用造成信息泄漏 (#31)。
	// 注意：forwardToServer/forwardWSToServer 在 app==nil 分支已提前 return，
	// 不会走到这里；服务端 relay 会自行鉴权并剥离 Cookie。
	httputil.StripHopProxyCookies(r.Header)

	// 跳转路径规则（鉴权后、代理前；WS 升级跳过）
	if !httputil.IsWebSocketUpgrade(r) {
		// 捕获组 + 内置变量先替换到 redirect_target，再走路径正则展开 (#50)
		if target, code, ok := db.MatchRedirect(appcfg.ExpandRedirects(app.Redirects, captures, reqVars), r.URL.Path, r.URL.RawQuery); ok {
			slog.Info("本地代理跳转路径命中", append(hop.LogAttrs(),
				"user_id", userIDFromContext(r.Context()),
				"path", r.URL.Path, "target", target, "status", code,
			)...)
			http.Redirect(w, r, target, code)
			return
		}
	}

	// 处理 WebSocket 升级
	if httputil.IsWebSocketUpgrade(r) {
		// app 来自 GetApp 的深拷贝，可直接覆盖 TargetURL 应用路由规则
		app.TargetURL = finalTargetURL
		p.proxyLocalWebSocket(w, r, app, hop)
		return
	}

	// 代理 HTTP 请求
	app.TargetURL = finalTargetURL
	p.proxyLocalHTTP(w, r, app, hop)
}

// reportAppAccess 异步上报本地代理访问到服务端（Path 3/4）。
// 节流：同一 app 60 秒内只上报一次，避免高 QPS 下每请求开流。
// 失败只 slog.Warn，不影响主请求路径。
func (p *LocalProxy) reportAppAccess(appID int64, userID int64, method, path string) {
	p.appAccessMu.Lock()
	if last, ok := p.appAccess[appID]; ok && time.Since(last) < appAccessReportThrottle {
		p.appAccessMu.Unlock()
		return
	}
	p.appAccess[appID] = time.Now()
	p.appAccessMu.Unlock()

	go func() {
		stream, err := p.client.OpenStream()
		if err != nil {
			slog.Warn("上报应用访问失败（开流）", "type", "proxy", "app_id", appID, "error", err)
			return
		}
		defer stream.Close()

		body, _ := json.Marshal(struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		}{method, path})

		if err := pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
			Action: pkgTunnel.ActionReportAppAccess,
			AppID:  appID,
			UserID: userID,
			Body:   body,
		}); err != nil {
			slog.Warn("上报应用访问失败（写入）", "type", "proxy", "app_id", appID, "error", err)
		}
	}()
}
