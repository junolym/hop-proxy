package proxy

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/internal/appcfg"
	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxycfg"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
	"github.com/robin/hop-proxy/pkg/vars"
)

// entryStyle 入口风格：决定响应映射与行为开关。
// 服务端三条入口流水线（公网 ServeHTTP / Path 5 HTTP relay / Path 5 WS relay）
// 共用同一 resolve 流程，差异集中在此枚举与各 respondXxx 映射表。
type entryStyle int

const (
	entryPublic    entryStyle = iota // 公网入口：防枚举 SSO 跳转、路径豁免、首页 no-store、主备重试
	entryRelayHTTP                   // Path 5 HTTP relay：来源客户端认证、防环、http 响应
	entryRelayWS                     // Path 5 WS relay：同上，错误以 WS 错误帧写回
)

// resolveStage 解析/鉴权失败阶段
type resolveStage int

const (
	stageConfigMissing    resolveStage = iota // 服务未配置（仅公网 500）
	stageUnknownSubdomain                     // 未知子域名
	stageAppNotFound                          // 应用不存在/已禁用
	stageNoClients                            // 应用未关联客户端
	stageNoClientOnline                       // 所有客户端均不在线
	stageLoopback                             // 转发环回（仅 relay）
	stageUnauthorized                         // 需认证（401）
	stageSSORedirect                          // 需跳转 SSO（302；公网由 DoSSORedirect 构造，relay 带 ssoURL）
	stageBadRequest                           // 请求构造失败（400，relay SSO URL 构造失败等）
	stageResponded                            // 响应已写出（跳转命中/认证函数已写），调用方直接返回
)

// resolveErr 共享解析流程的失败：携带阶段与响应所需数据，
// 由各入口的 respond 映射为具体 HTTP/WS 响应
type resolveErr struct {
	stage  resolveStage
	detail string // 附加详情（响应正文或日志说明）
	ssoURL string // stageSSORedirect：跳转目标（仅 entryRelayHTTP 使用）
}

func (e *resolveErr) Error() string {
	if e.detail != "" {
		return e.detail
	}
	return "代理请求解析失败"
}

// ReqContext 服务端代理请求的运行态上下文。
// 内嵌 wire 层 HopContext（跨节点字段，出站 ApplyTo 下发）；运行态字段由共享
// resolve 流程填充。发起端函数统一接收本对象，替代原十余个散参数。
type ReqContext struct {
	Hop pkgTunnel.HopContext // wire 层上下文

	Style entryStyle

	W      http.ResponseWriter // relayWS 为 nil
	R      *http.Request
	Stream *yamux.Stream // 仅 relayWS

	SourceClientID string // #70 来源客户端：公网为空；relay = fromClientID

	ProxyDomain   string
	Subdomain     string
	Res           *db.AppResolve
	App           *db.App
	AppID         int64
	Routes        []db.AppRoute
	EffectiveAuth string // 认证方式（含路由级覆盖）
	AuthUserID    int64
	ReqVars       *vars.RequestVars

	Selection      ClientSelection
	Cfg            map[string]string
	CustomHeaders  map[string]string
	FinalTargetURL string

	Recorder   *httputil.StatusRecorder // 仅公网入口（代理完成日志）
	HdrCapture *httputil.HeaderCapture  // 仅公网入口（最末端 req/resp headers 捕获）
}

// LogAttrs 代理链路日志公共字段（type/request_id/subdomain/app_id/original_path）
func (rc *ReqContext) LogAttrs() []any { return rc.Hop.LogAttrs() }

// LogAttrsWithType 同 LogAttrs，覆盖日志类型（如 auth）
func (rc *ReqContext) LogAttrsWithType(typ string) []any { return rc.Hop.LogAttrsWithType(typ) }

// resolve 共享解析流程：子域名 → 应用/客户端/配置 → 鉴权 → 跳转规则 →
// 路由改写 → 选客户端/目标/自定义 headers。
// 行为差异（全部由 Style 控制，不得顺手统一）：
//   - 路径豁免（#68）、防枚举 SSO 跳转、首页 no-store、主备重试：仅 entryPublic
//   - 来源客户端认证（headers map）与防环：仅 relay（Path 5 二次解析是安全边界，
//     入站上下文的路由/目标字段在此被重新解析覆盖）
func (p *Proxy) resolve(rc *ReqContext) *resolveErr {
	if re := p.resolveApp(rc); re != nil {
		return re
	}
	if re := p.authenticate(rc); re != nil {
		return re
	}
	rc.ReqVars = p.buildRequestVars(rc.R, rc.Subdomain, rc.App, rc.AuthUserID)

	// SSO 应用首页禁用浏览器缓存：浏览器缓存首页后，SSO 会话失效时不再回源，
	// 后续 API 调用被 SSO 拦截导致应用层无响应 (#35)。仅公网入口。
	if rc.Style == entryPublic && rc.R.URL.Path == "/" {
		switch rc.EffectiveAuth {
		case server.AuthMethodSSO, server.AuthMethodSSOToken,
			server.AuthMethodSSOOwner, server.AuthMethodSSOAll:
			rc.W.Header().Set("Cache-Control", "no-store")
		}
	}

	// 跳转路径规则（鉴权后、代理前；WS 升级跳过）
	if !httputil.IsWebSocketUpgrade(rc.R) {
		if redirects, _ := p.db.GetActiveRedirectsForApp(rc.AppID); len(redirects) > 0 {
			// 捕获组 + 内置变量先替换到 redirect_target，再走路径正则展开 (#50)
			expanded := appcfg.ExpandRedirects(redirects, rc.Res.Captures, rc.ReqVars)
			if target, code, ok := db.MatchRedirect(expanded, rc.R.URL.Path, rc.R.URL.RawQuery); ok {
				msg := "跳转路径命中"
				if rc.Style != entryPublic {
					msg = "转发跳转路径命中"
				}
				slog.Info(msg, append(rc.LogAttrs(), "path", rc.R.URL.Path, "target", target, "status", code)...)
				http.Redirect(rc.W, rc.R, target, code)
				return &resolveErr{stage: stageResponded}
			}
		}
	}

	return p.resolveTarget(rc)
}

// resolveApp 解析子域名、命中的应用与客户端列表、代理配置与认证方式覆盖
func (p *Proxy) resolveApp(rc *ReqContext) *resolveErr {
	proxyDomain, err := p.db.GetSetting("proxy_domain")
	if err != nil {
		if rc.Style == entryPublic {
			slog.Warn("代理错误", append(rc.LogAttrs(), "method", rc.R.Method, "path", rc.R.URL.Path, "host", rc.R.Host, "status", http.StatusInternalServerError, "error", "服务未配置")...)
			return &resolveErr{stage: stageConfigMissing}
		}
		// relay：忽略读取失败，子域名解析自然失败 → 404
		proxyDomain = ""
	}
	rc.ProxyDomain = proxyDomain

	subdomain := ExtractSubdomain(rc.R.Host, proxyDomain)
	if subdomain == "" {
		slog.Warn("代理错误", append(rc.LogAttrs(), "method", rc.R.Method, "path", rc.R.URL.Path, "host", rc.R.Host, "from_client", rc.SourceClientID, "status", http.StatusNotFound, "error", "未知的子域名")...)
		return &resolveErr{stage: stageUnknownSubdomain}
	}
	rc.Subdomain = subdomain
	rc.Hop.Subdomain = subdomain

	res, err := p.db.GetAppClientsWithTarget(subdomain)
	if err != nil {
		if rc.Style == entryPublic {
			// 子域名不存在或应用已禁用时，统一跳转到 SSO 登录页，
			// 这样扫描器无法区分有效和无效的子域名
			return &resolveErr{stage: stageSSORedirect}
		}
		slog.Warn("代理错误", append(rc.LogAttrs(), "method", rc.R.Method, "path", rc.R.URL.Path, "from_client", rc.SourceClientID, "status", http.StatusNotFound, "error", "应用不存在或已禁用")...)
		return &resolveErr{stage: stageAppNotFound, detail: "应用不存在或已禁用"}
	}
	if len(res.Clients) == 0 {
		slog.Warn("代理错误", append(rc.LogAttrs(), "method", rc.R.Method, "path", rc.R.URL.Path, "from_client", rc.SourceClientID, "status", http.StatusServiceUnavailable, "error", "应用未关联客户端")...)
		return &resolveErr{stage: stageNoClients}
	}
	rc.Res = res
	rc.AppID = res.AppID
	rc.Hop.AppID = res.AppID

	app, err := p.db.GetApp(res.AppID)
	if err != nil {
		msg := "应用不存在或已禁用"
		if rc.Style != entryPublic {
			msg = "应用不存在"
		}
		slog.Warn("代理错误", append(rc.LogAttrs(), "method", rc.R.Method, "path", rc.R.URL.Path, "from_client", rc.SourceClientID, "status", http.StatusNotFound, "error", msg)...)
		return &resolveErr{stage: stageAppNotFound, detail: msg}
	}
	rc.App = app

	// 代理限制配置生效值（#47）：全局默认 + 应用级覆盖合并
	rc.Cfg = p.effectiveCfg(app)
	// 限制请求 Body 大小（按配置生效值，0=不限）；WS 升级请求无 body
	if rc.Style != entryRelayWS {
		if maxBody := proxycfg.SizeOf(rc.Cfg, "client_max_body_size"); maxBody > 0 {
			rc.R.Body = http.MaxBytesReader(rc.W, rc.R.Body, maxBody)
		}
	}

	// 路由规则（按优先级排序，用于覆盖认证方式和目标地址）
	routes, _ := p.db.GetActiveRoutesForApp(res.AppID)
	rc.Routes = routes

	// 认证方式覆盖：来源客户端取本跳实体（公网空串=无来源；relay=fromClientID，#70）
	rc.EffectiveAuth = app.AuthMethod
	if override := db.FindRouteAuthOverride(routes, rc.R.Method, rc.R.URL.Path, rc.SourceClientID); override != "" {
		rc.EffectiveAuth = override
	}
	return nil
}

// authenticate 按入口风格执行认证。
// 公网：路径豁免（#68）→ 调用 sso.go 的 CheckXxx（失败时响应已写出）；
// relay：headers map 认证（CheckAccessTokenFromHeaders/checkSSOCookieFromHeaders），
// 失败返回 stageUnauthorized / stageSSORedirect 由 respond 写出。
func (p *Proxy) authenticate(rc *ReqContext) *resolveErr {
	// 路径豁免（仅公网，#68）：命中则跳过所有认证（含路由规则覆盖）
	if rc.Style == entryPublic && db.IsPathExempt(rc.R.URL.Path, rc.App.ExemptPaths) {
		slog.Debug("路径豁免 SSO 认证", append(rc.LogAttrsWithType("auth"), "path", rc.R.URL.Path)...)
		return nil
	}

	if rc.Style == entryPublic {
		switch {
		case rc.EffectiveAuth == server.AuthMethodToken:
			// 仅票据模式：不接受 SSO Cookie，只检查 Authorization header
			if uid, ok := CheckTokenAuth(rc.W, rc.R, p.db, rc.App, rc.Hop); !ok {
				return &resolveErr{stage: stageResponded}
			} else {
				rc.AuthUserID = uid
			}
		case rc.EffectiveAuth == server.AuthMethodSSOToken:
			// SSO + 票据混合模式：票据优先，SSO 回退
			if uid, ok := CheckSSOTokenAuth(rc.W, rc.R, p.db, rc.App, rc.Subdomain, rc.Hop); !ok {
				return &resolveErr{stage: stageResponded}
			} else {
				rc.AuthUserID = uid
			}
		case rc.EffectiveAuth == server.AuthMethodSSO ||
			rc.EffectiveAuth == server.AuthMethodSSOOwner ||
			rc.EffectiveAuth == server.AuthMethodSSOAll:
			// 仅 SSO 模式：只检查 SSO Cookie，不支持票据
			if uid, ok := CheckSSOAuth(rc.W, rc.R, p.db, rc.App, rc.Subdomain, rc.Hop); !ok {
				return &resolveErr{stage: stageResponded}
			} else {
				rc.AuthUserID = uid
			}
		}
		return nil
	}

	// relay（HTTP/WS）：从 headers map 认证（跨隧道请求无 http.ResponseWriter 语义）
	hdrMap := httputil.HeadersFromHTTP(rc.R.Header)
	switch {
	case rc.EffectiveAuth == server.AuthMethodToken:
		// 仅票据模式：不接受 SSO Cookie
		uid, ok := CheckAccessTokenFromHeaders(hdrMap, p.db, rc.App, rc.SourceClientID)
		if !ok {
			slog.Warn("代理错误", append(rc.LogAttrsWithType("auth"), "method", rc.R.Method, "path", rc.R.URL.Path, "from_client", rc.SourceClientID, "status", http.StatusUnauthorized, "error", "需要提供访问票据")...)
			return &resolveErr{stage: stageUnauthorized, detail: "需要提供访问票据"}
		}
		rc.AuthUserID = uid
	case rc.EffectiveAuth == server.AuthMethodSSOToken:
		// 混合模式：票据优先，SSO 回退
		if uid, ok := CheckAccessTokenFromHeaders(hdrMap, p.db, rc.App, rc.SourceClientID); ok {
			rc.AuthUserID = uid
		} else if uid, ok := checkSSOCookieFromHeaders(hdrMap, p.db, rc.App, rc.Subdomain); ok {
			rc.AuthUserID = uid
		} else {
			return p.relaySSOFallback(rc, hdrMap)
		}
	case rc.EffectiveAuth != server.AuthMethodNone:
		// 仅 SSO 模式：只查 SSO Cookie，不支持票据
		if uid, ok := checkSSOCookieFromHeaders(hdrMap, p.db, rc.App, rc.Subdomain); ok {
			rc.AuthUserID = uid
		} else {
			return p.relaySSOFallback(rc, hdrMap)
		}
	}
	return nil
}

// relaySSOFallback relay 认证失败时的响应决策：浏览器（Accept: text/html）→
// 302 SSO 跳转；API 客户端 → 401（含 WWW-Authenticate）。WS relay 不做区别，
// 统一 401 错误帧（不构造 SSO URL）。
func (p *Proxy) relaySSOFallback(rc *ReqContext, hdrMap map[string]string) *resolveErr {
	if rc.Style == entryRelayWS {
		return &resolveErr{stage: stageUnauthorized, detail: "需要认证"}
	}
	if strings.Contains(hdrMap["Accept"], "text/html") {
		info := GetSSORedirectInfoFromMsg(rc.R.Host, rc.R.URL.Path, hdrMap, p.db)
		ssoURL, err := info.BuildSSORedirectURL()
		if err != nil {
			slog.Warn("代理错误", append(rc.LogAttrsWithType("auth"), "method", rc.R.Method, "path", rc.R.URL.Path, "from_client", rc.SourceClientID, "status", http.StatusBadRequest, "error", err)...)
			return &resolveErr{stage: stageBadRequest, detail: err.Error()}
		}
		return &resolveErr{stage: stageSSORedirect, ssoURL: ssoURL}
	}
	slog.Warn("代理错误", append(rc.LogAttrsWithType("auth"), "method", rc.R.Method, "path", rc.R.URL.Path, "from_client", rc.SourceClientID, "status", http.StatusUnauthorized, "error", "需要认证")...)
	return &resolveErr{stage: stageUnauthorized, detail: "需要认证"}
}

// resolveTarget 选择客户端、解析目标地址与自定义 headers、填充出站上下文
func (p *Proxy) resolveTarget(rc *ReqContext) *resolveErr {
	selection := p.selectClient(rc.AppID, rc.Res.LoadBalance, rc.Res.Clients, rc.Res.AppTargetURL)
	if selection.ClientID == "" {
		slog.Warn("代理错误", append(rc.LogAttrs(), "method", rc.R.Method, "path", rc.R.URL.Path, "from_client", rc.SourceClientID, "status", http.StatusBadGateway, "error", "所有客户端均不在线")...)
		return &resolveErr{stage: stageNoClientOnline}
	}
	rc.Selection = selection

	// 防环：若目标客户端就是来源客户端，直接拒绝（避免 Path 5 转发回自身形成死循环）
	if rc.SourceClientID != "" && selection.ClientID == rc.SourceClientID {
		slog.Warn("转发请求检测到环回，拒绝", append(rc.LogAttrs(), "from_client", rc.SourceClientID)...)
		return &resolveErr{stage: stageLoopback}
	}

	// 应用级/路由级目标地址统一做捕获组 + 内置变量展开 (#50)；
	// 路由规则路径改写 (#53)：鉴权/跳转均按原路径匹配完成，此处改写转发路径，
	// OriginalPath 随上下文传递供下游执行端日志关联
	routeTarget := ""
	if matched := db.MatchRoute(rc.Routes, rc.R.Method, rc.R.URL.Path, rc.SourceClientID); matched != nil {
		routeTarget = matched.TargetURL
		rc.Hop.OriginalPath = db.ApplyRoutePathRewrite(matched, rc.R, rc.Subdomain, rc.AppID)
	}
	rc.FinalTargetURL = appcfg.ResolveTargetURL(selection.TargetURL, routeTarget, rc.Res.Captures, rc.ReqVars)
	if rc.Style == entryPublic && routeTarget != "" {
		slog.Debug("路由规则覆盖目标地址", append(rc.LogAttrs(), "original", selection.TargetURL, "override", rc.FinalTargetURL)...)
	}

	// 更新应用最近使用时间（节流：同一 app 60 秒内只写一次 DB）
	p.throttledUpdateAppLastUsed(rc.AppID)

	// 获取自定义 Headers（捕获组 + 内置变量展开，#50）
	customHeaders, err := p.db.GetAppCustomHeaders(rc.AppID)
	if err != nil {
		if rc.Style == entryPublic {
			slog.Warn("获取自定义 Headers 失败", append(rc.LogAttrs(), "error", err)...)
		}
		customHeaders = make(map[string]string)
	}
	rc.CustomHeaders = appcfg.ExpandCustomHeaders(customHeaders, rc.Res.Captures, rc.ReqVars)

	// 鉴权后剥离 HopProxy 内部 Cookie（hopproxy_session / hopproxy_sso_* / hopproxy_csrf 等），
	// 防止这些 Cookie 转发到后端应用造成信息泄漏 (#31)
	httputil.StripHopProxyCookies(rc.R.Header)

	// 填充出站上下文：目标地址、代理策略、密钥与限流配置（#47/#52/#67）
	rc.Hop.TargetURL = rc.FinalTargetURL
	rc.Hop.ProxyType = selection.ProxyType
	rc.Hop.ProxyAddress = selection.ProxyAddress
	rc.Hop.PeerSecret = selection.PeerSecret
	rc.Hop.AgentKeyUUID = rc.Res.AgentKeyUUID
	rc.Hop.SetLimits(rc.Cfg)
	return nil
}

// respondPublic 公网入口（ServeHTTP）的失败响应映射
func (p *Proxy) respondPublic(rc *ReqContext, re *resolveErr) {
	switch re.stage {
	case stageConfigMissing:
		http.Error(rc.Recorder, "服务未配置", http.StatusInternalServerError)
	case stageUnknownSubdomain:
		http.Error(rc.Recorder, "未知的子域名", http.StatusNotFound)
	case stageAppNotFound:
		http.Error(rc.Recorder, "应用不存在或已禁用", http.StatusNotFound)
	case stageNoClients:
		http.Error(rc.Recorder, "应用未关联客户端", http.StatusServiceUnavailable)
	case stageNoClientOnline:
		http.Error(rc.Recorder, "所有客户端均不在线", http.StatusBadGateway)
	case stageSSORedirect:
		// 子域名不存在/应用禁用同样走 SSO 跳转（防枚举）
		DoSSORedirect(rc.W, rc.R, p.db, rc.Hop)
	case stageResponded, stageLoopback, stageUnauthorized, stageBadRequest:
		// 响应已写出，或公网流程不会出现该阶段
	}
}

// respondRelayHTTP Path 5 HTTP relay 的失败响应映射
func (p *Proxy) respondRelayHTTP(rc *ReqContext, re *resolveErr) {
	switch re.stage {
	case stageUnknownSubdomain:
		http.Error(rc.W, "未知的子域名", http.StatusNotFound)
	case stageAppNotFound:
		http.Error(rc.W, re.detail, http.StatusNotFound)
	case stageNoClients:
		http.Error(rc.W, "应用未关联客户端", http.StatusServiceUnavailable)
	case stageNoClientOnline:
		http.Error(rc.W, "所有客户端均不在线", http.StatusBadGateway)
	case stageLoopback:
		http.Error(rc.W, "检测到转发环回", http.StatusBadGateway)
	case stageUnauthorized:
		rc.W.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
		http.Error(rc.W, re.detail, http.StatusUnauthorized)
	case stageSSORedirect:
		http.Redirect(rc.W, rc.R, re.ssoURL, http.StatusFound)
	case stageBadRequest:
		http.Error(rc.W, re.detail, http.StatusBadRequest)
	case stageConfigMissing, stageResponded:
		// relay 不出现；响应已写出
	}
}

// respondRelayWS Path 5 WS relay 的失败响应映射（统一 WS 错误帧）
func (p *Proxy) respondRelayWS(rc *ReqContext, re *resolveErr) {
	status := http.StatusBadGateway
	switch re.stage {
	case stageUnknownSubdomain, stageAppNotFound, stageNoClients:
		status = http.StatusNotFound
	case stageUnauthorized, stageSSORedirect:
		// WS relay 不做 SSO 跳转（浏览器升级请求无法安全重定向），统一 401
		status = http.StatusUnauthorized
	case stageNoClientOnline, stageLoopback, stageBadRequest:
		status = http.StatusBadGateway
	case stageConfigMissing, stageResponded:
		status = http.StatusInternalServerError
	}
	pkgTunnel.WriteWSErrorResponse(rc.Stream, status)
}
