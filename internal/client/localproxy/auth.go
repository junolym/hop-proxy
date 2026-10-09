package localproxy

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/ssoutil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// userIDKey 用于在 request context 中传递认证后的 user_id
type userIDKey struct{}

// userIDFromContext 从 ctx 提取 user_id（未认证时返回 0）
func userIDFromContext(ctx context.Context) int64 {
	if v, ok := ctx.Value(userIDKey{}).(int64); ok {
		return v
	}
	return 0
}

// checkAuth 检查请求认证（仅 SSO 模式）。
// 只检查 SSO Cookie，不支持票据。未认证时直接重定向到 SSO 登录页。
// 子域名取自 hop.Subdomain（请求 host 提取值），用于构造 cookie name 与校验 session 归属 (#40)。
// 返回 (userID, userName, true) 表示认证成功；用户信息供内置变量 ${user_id}/${user_name} 展开 (#50)。
//
// ⚠️ 认证逻辑需与服务端 internal/server/proxy/sso.go CheckSSOAuth 保持一致，
// 修改此处逻辑时请同步修改对应函数。
func (p *LocalProxy) checkAuth(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) (int64, string, bool) {
	if uid, name, ok, responded := p.verifySSOCookie(w, r, app, hop); ok {
		return uid, name, true
	} else if responded {
		// verifySSOCookie 已写响应（如隧道离线 503），不得再写
		return 0, "", false
	}
	p.redirectToSSO(w, r, app, hop)
	return 0, "", false
}

// checkSSOTokenAuth 检查请求认证（SSO + 票据混合模式）。
// 认证优先级：访问票据（Authorization header）优先于 SSO Cookie。
//   - 携带有效票据 → 放行
//   - 携带无效票据 → 401（不回退 SSO）
//   - 无票据 → 检查 SSO Cookie，失效/缺失则要求认证：
//     浏览器（Accept: text/html）→ 302 重定向到 SSO 登录页；
//     API 客户端 → 401 + WWW-Authenticate
//
// subdomain 取自 hop.Subdomain（请求 host 提取值），用于构造 cookie name 与校验 session 归属 (#40)。
// 返回 (userID, userName, true) 表示认证成功；用户信息供内置变量 ${user_id}/${user_name} 展开 (#50)。
//
// ⚠️ 认证优先级顺序需与服务端 internal/server/proxy/sso.go CheckSSOTokenAuth 保持一致，
// 修改此处逻辑时请同步修改对应函数。
func (p *LocalProxy) checkSSOTokenAuth(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) (int64, string, bool) {
	// 1. 优先检查访问票据（Authorization header）——票据优先于 SSO
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		token := ""
		authHeaderLower := strings.ToLower(authHeader)
		if strings.HasPrefix(authHeaderLower, "bearer ") {
			token = strings.TrimSpace(authHeader[7:])
		} else if strings.HasPrefix(authHeaderLower, "basic ") {
			_, password, ok := p.parseBasicAuth(authHeader)
			if ok {
				token = password
			}
		}

		if token != "" {
			// 检查本地缓存
			if uid, name, ok := p.sessionCache.Get(app.ID, token, p.tunnelOnline()); ok {
				return uid, name, true
			}
			// 通过隧道验证票据
			if p.tunnelOnline() {
				valid, uid, name, _ := p.verifySession(app.ID, token, hop.Subdomain)
				if valid {
					p.sessionCache.Set(app.ID, token, uid, name, 1*time.Minute)
					return uid, name, true
				}
			}
		}
		// 提供了 Authorization 但认证失败，返回 401
		w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
		http.Error(w, "访问票据无效或已过期", http.StatusUnauthorized)
		return 0, "", false
	}

	// 2. 无票据，检查 SSO Cookie
	if uid, name, ok, responded := p.verifySSOCookie(w, r, app, hop); ok {
		return uid, name, true
	} else if responded {
		// verifySSOCookie 已写响应（如隧道离线 503），不得再写
		return 0, "", false
	}

	// 3. 既无有效票据也无有效 SSO Cookie，要求认证
	p.requireSSOAuth(w, r, app, hop)
	return 0, "", false
}

// verifySSOCookie 验证 SSO Cookie（缓存 + 隧道验证）。
// 子域名取自 hop.Subdomain（请求 host 提取值），用于构造 cookie name 与校验 session 归属 (#40)。
// 返回 (userID, userName, true, false) 表示认证成功；用户信息供内置变量 ${user_id}/${user_name} 展开 (#50)。
// 返回 responded=true 表示已写响应（隧道离线时写 503），调用方不得再写响应，
// 否则会触发 net/http 的 superfluous WriteHeader 告警。
func (p *LocalProxy) verifySSOCookie(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) (int64, string, bool, bool) {
	cookieName := db.SSOCookieName(app.ID, hop.Subdomain)
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return 0, "", false, false
	}
	token := cookie.Value

	// 检查本地缓存
	if uid, name, ok := p.sessionCache.Get(app.ID, token, p.tunnelOnline()); ok {
		return uid, name, true, false
	}

	// 隧道离线时，允许已验证过的 Session 继续
	if !p.tunnelOnline() {
		slog.Warn("隧道离线，无法验证 Session", hop.LogAttrsWithType("auth")...)
		http.Error(w, "隧道离线，无法验证认证", http.StatusServiceUnavailable)
		return 0, "", false, true
	}

	// 通过隧道验证 Session（含子域名绑定校验）
	valid, userID, userName, verifyErr := p.verifySession(app.ID, token, hop.Subdomain)
	if verifyErr != nil || !valid {
		return 0, "", false, false
	}

	// 缓存验证结果（1 分钟）
	p.sessionCache.Set(app.ID, token, userID, userName, 1*time.Minute)
	slog.Debug("Session 验证成功", append(hop.LogAttrsWithType("auth"), "user_id", userID)...)
	return userID, userName, true, false
}

// requireSSOAuth 要求 SSO 认证：浏览器请求重定向到 SSO 登录页，
// API 客户端（Accept 不含 text/html）返回 401 + WWW-Authenticate 以便其提供票据。
func (p *LocalProxy) requireSSOAuth(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		p.redirectToSSO(w, r, app, hop)
		return
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
	http.Error(w, "需要认证", http.StatusUnauthorized)
}

// parseBasicAuth 解析 Basic Auth
func (p *LocalProxy) parseBasicAuth(authHeader string) (username, password string, ok bool) {
	if len(authHeader) < 6 || !strings.EqualFold(authHeader[:6], "Basic ") {
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authHeader[6:]))
	if err != nil {
		return
	}
	s := string(decoded)
	colonIdx := strings.IndexByte(s, ':')
	if colonIdx < 0 {
		return
	}
	return s[:colonIdx], s[colonIdx+1:], true
}

// checkTokenAuth 检查请求是否携带有效的访问票据（仅票据模式）。
// 不检查 SSO Cookie，不做 SSO 重定向。
// 子域名取自 hop.Subdomain，隧道验证时传给服务端做子域名绑定校验 (#40)。
// 返回 (userID, userName, true) 表示认证成功 (#50)。
func (p *LocalProxy) checkTokenAuth(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) (int64, string, bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
		http.Error(w, "需要提供访问票据", http.StatusUnauthorized)
		return 0, "", false
	}

	// 提取 token 值
	token := ""
	authHeaderLower := strings.ToLower(authHeader)
	if strings.HasPrefix(authHeaderLower, "bearer ") {
		token = strings.TrimSpace(authHeader[7:])
	} else if strings.HasPrefix(authHeaderLower, "basic ") {
		_, password, ok := p.parseBasicAuth(authHeader)
		if ok {
			token = password
		}
	}

	if token == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
		http.Error(w, "访问票据格式无效", http.StatusUnauthorized)
		return 0, "", false
	}

	// 通过隧道验证票据（复用 verifySession，服务端已支持 SSO Session fallback 到 access token 验证）
	valid, userID, userName, verifyErr := p.verifySession(app.ID, token, hop.Subdomain)
	if verifyErr != nil {
		slog.Warn("验证访问票据失败", append(hop.LogAttrsWithType("auth"), "error", verifyErr)...)
		w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
		http.Error(w, "验证票据失败", http.StatusUnauthorized)
		return 0, "", false
	}
	if !valid {
		w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
		http.Error(w, "访问票据无效或已过期", http.StatusUnauthorized)
		return 0, "", false
	}

	return userID, userName, true
}

// verifySession 通过隧道验证 SSO Session。
// subdomain 传递给服务端，校验 session.Subdomain == subdomain 防伪造 cookie name (#40)。
// 返回的 userName 由服务端查询填充，供内置变量 ${user_name} 展开 (#50)。
// 使用 read deadline 防止服务端不响应时永久阻塞。
func (p *LocalProxy) verifySession(appID int64, token string, subdomain string) (bool, int64, string, error) {
	stream, err := p.client.OpenStream()
	if err != nil {
		return false, 0, "", err
	}
	defer stream.Close()

	// 设置 read deadline，防止服务端不响应时永久阻塞
	_ = stream.SetReadDeadline(time.Now().Add(peerAuthTimeout))
	defer stream.SetReadDeadline(time.Time{})

	pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
		Action:    pkgTunnel.ActionVerifySession,
		AppID:     appID,
		Subdomain: subdomain,
		Body:      []byte(token),
	})

	resp, err := pkgTunnel.ReadCtrlMessage(stream)
	if err != nil {
		return false, 0, "", err
	}
	return resp.Success, resp.UserID, resp.UserName, nil
}

// redirectToSSO 重定向到服务端 SSO
func (p *LocalProxy) redirectToSSO(w http.ResponseWriter, r *http.Request, app *db.ClientAppInfo, hop pkgTunnel.HopContext) {
	_, adminDomain := p.appManager.GetDomains()
	proxyDomain, _ := p.appManager.GetDomains()
	if adminDomain == "" {
		http.Error(w, "SSO 未配置", http.StatusInternalServerError)
		return
	}

	// 使用共享工具提取协议和主机
	scheme := ssoutil.ExtractScheme(r.TLS != nil, r.Header.Get("X-Forwarded-Proto"))
	host := ssoutil.ExtractHost(r.Host, r.Header.Get("X-Forwarded-Host"))

	slog.Info("SSO 重定向 (本地代理)", append(hop.LogAttrsWithType("auth"),
		"host", host,
		"scheme", scheme,
		"X-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"),
		"requestURI", r.URL.RequestURI(),
	)...)

	ssoURL, err := ssoutil.BuildSSORedirectURL(adminDomain, proxyDomain, scheme, host, r.URL.RequestURI())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Debug("SSO 重定向 URL", append(hop.LogAttrsWithType("auth"), "ssoURL", ssoURL)...)
	http.Redirect(w, r, ssoURL, http.StatusFound)
}
