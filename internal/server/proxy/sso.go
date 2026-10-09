package proxy

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/ssoutil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// SSORedirectInfo 包含构建 SSO 重定向 URL 所需的信息（兼容旧代码）
type SSORedirectInfo = ssoutil.RedirectInfo

// GetSSORedirectInfo 从 HTTP 请求中提取 SSO 重定向所需信息
func GetSSORedirectInfo(r *http.Request, database *db.DB) *SSORedirectInfo {
	adminDomain, _ := database.GetSetting("admin_domain")
	proxyDomain, _ := database.GetSetting("proxy_domain")

	scheme := ssoutil.ExtractScheme(r.TLS != nil, r.Header.Get("X-Forwarded-Proto"))
	host := ssoutil.ExtractHost(r.Host, r.Header.Get("X-Forwarded-Host"))

	return &SSORedirectInfo{
		AdminDomain: adminDomain,
		ProxyDomain: proxyDomain,
		Scheme:      scheme,
		Host:        host,
		RequestURI:  r.RequestURI,
	}
}

// GetSSORedirectInfoFromMsg 从隧道消息中提取 SSO 重定向所需信息
func GetSSORedirectInfoFromMsg(host, path string, headers map[string]string, database *db.DB) *SSORedirectInfo {
	adminDomain, _ := database.GetSetting("admin_domain")
	proxyDomain, _ := database.GetSetting("proxy_domain")

	scheme := ssoutil.ExtractScheme(false, headers["X-Forwarded-Proto"])
	// 对于隧道消息，host 已经是原始主机名

	return &SSORedirectInfo{
		AdminDomain: adminDomain,
		ProxyDomain: proxyDomain,
		Scheme:      scheme,
		Host:        host,
		RequestURI:  path,
	}
}

// checkAccessToken 检查请求中的访问票据是否有效
// fromClientID: 请求来源客户端 ID，空字符串表示服务端直接处理
// 返回 (userID, true) 表示票据有效，可放行；userID 供内置变量 ${user_id} 使用 (#50)
func checkAccessToken(authHeader string, database *db.DB, app *db.App, fromClientID string) (int64, bool) {
	if authHeader == "" {
		return 0, false
	}

	var tokenValue string
	authHeaderLower := strings.ToLower(authHeader)

	if strings.HasPrefix(authHeaderLower, "bearer ") {
		tokenValue = authHeader[7:]
	} else if strings.HasPrefix(authHeaderLower, "basic ") {
		username, password, ok := parseBasicAuth(authHeader)
		if !ok || strings.ToLower(username) != "token" {
			return 0, false
		}
		tokenValue = password
	} else {
		return 0, false
	}

	tokenValue = strings.TrimSpace(tokenValue)
	if tokenValue == "" {
		return 0, false
	}

	ticket, err := database.GetAccessTokenByValue(tokenValue)
	if err != nil {
		return 0, false
	}

	// 使用统一的校验逻辑
	if !ticket.Verify(app.ID, fromClientID) {
		return 0, false
	}

	// 票据有效，异步更新最近使用时间
	go database.TouchAccessToken(ticket.ID)
	return ticket.UserID, true
}

// parseBasicAuth 解析 "Basic <base64>" 格式的 Authorization header
func parseBasicAuth(authHeader string) (username, password string, ok bool) {
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

// CheckTokenAuth 检查代理请求是否携带有效的访问票据（仅票据模式）。
// 不检查 SSO Cookie，不做 SSO 重定向。
// 无效/无 Token 时返回 401 + WWW-Authenticate header。
// 返回 (userID, true) 表示认证通过；userID 供内置变量 ${user_id} 使用 (#50)
func CheckTokenAuth(w http.ResponseWriter, r *http.Request, database *db.DB, app *db.App, hop pkgTunnel.HopContext) (int64, bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if uid, ok := checkAccessToken(authHeader, database, app, ""); ok {
			return uid, true
		}
	}

	// 无效或无 Token，返回 401
	w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
	http.Error(w, "需要提供访问票据", http.StatusUnauthorized)
	return 0, false
}

// CheckSSOAuth 检查代理请求是否已通过该应用的 SSO 认证（仅 SSO 模式）。
// 只检查 SSO Cookie，不支持票据。未认证时直接 302 重定向到 SSO 登录页。
// subdomain 为请求 host 提取的具体子域名，用于构造 cookie name 与校验 session 归属 (#40)。
// 返回 (userID, true) 表示认证通过；userID 供内置变量 ${user_id} 使用 (#50)
//
// ⚠️ 认证逻辑需与客户端 internal/client/localproxy/auth.go checkAuth 保持一致，
// 修改此处逻辑时请同步修改对应函数。
func CheckSSOAuth(w http.ResponseWriter, r *http.Request, database *db.DB, app *db.App, subdomain string, hop pkgTunnel.HopContext) (int64, bool) {
	if uid, ok := validateSSOCookie(r, database, app, subdomain); ok {
		return uid, true
	}
	DoSSORedirect(w, r, database, hop)
	return 0, false
}

// CheckSSOTokenAuth 检查代理请求的认证（SSO + 票据混合模式）。
// 认证优先级：访问票据（Authorization header）优先于 SSO Cookie。
//   - 携带有效票据 → 放行（不再要求 SSO）
//   - 携带无效票据 → 401（票据优先，结果权威，不回退 SSO）
//   - 无票据 → 检查 SSO Cookie，失效/缺失则要求认证：
//     浏览器（Accept: text/html）→ 302 重定向到 SSO 登录页；
//     API 客户端 → 401 + WWW-Authenticate（以便其提供票据）
//
// subdomain 为请求 host 提取的具体子域名，用于构造 cookie name 与校验 session 归属 (#40)。
// 返回 (userID, true) 表示认证通过；userID 供内置变量 ${user_id} 使用 (#50)
//
// ⚠️ 认证优先级顺序需与客户端 internal/client/localproxy/auth.go checkSSOTokenAuth 保持一致，
// 修改此处逻辑时请同步修改对应函数。
func CheckSSOTokenAuth(w http.ResponseWriter, r *http.Request, database *db.DB, app *db.App, subdomain string, hop pkgTunnel.HopContext) (int64, bool) {
	// 1. 优先检查访问票据（Bearer / Basic Auth）
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if uid, ok := checkAccessToken(authHeader, database, app, ""); ok {
			return uid, true
		}
		// 提供了 Authorization 但认证失败，返回 401
		w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
		http.Error(w, "访问票据无效或已过期", http.StatusUnauthorized)
		return 0, false
	}

	// 2. 无票据，检查 SSO Cookie
	if uid, ok := validateSSOCookie(r, database, app, subdomain); ok {
		return uid, true
	}

	// 3. 既无有效票据也无有效 SSO Cookie，要求认证
	requireSSOAuth(w, r, database, subdomain, hop)
	return 0, false
}

// validateSSOCookie 验证请求中的 SSO Cookie 是否有效（含 allowed_users 权限检查 + 子域名绑定校验）。
// subdomain 为请求 host 提取的具体子域名，必须与 session.Subdomain 一致，否则视为伪造 (#40)。
// 不处理响应写入，由调用方决定未认证时的响应方式。
// 返回 (userID, true) 表示认证通过；userID 供内置变量 ${user_id} 使用 (#50)
func validateSSOCookie(r *http.Request, database *db.DB, app *db.App, subdomain string) (int64, bool) {
	cookieName := db.SSOCookieName(app.ID, subdomain)
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return 0, false
	}

	session, sessionErr := database.GetSSOSession(cookie.Value, app.ID)
	if sessionErr != nil {
		return 0, false
	}

	// 子域名绑定校验：session.Subdomain 必须与当前请求子域名一致，
	// 防止伪造 cookie name 跨子域串号 (#40)
	if session.Subdomain != subdomain {
		return 0, false
	}

	// 根据 allowed_users 判断权限
	if app.AllowedUsers == server.AllowedUsersOwner || app.AllowedUsers == "" {
		if session.UserID != app.UserID {
			user, userErr := database.GetUserByID(session.UserID)
			if userErr != nil || !user.IsAdmin {
				return 0, false
			}
		}
	} else if app.AllowedUsers != server.AllowedUsersAll {
		user, userErr := database.GetUserByID(session.UserID)
		if userErr != nil {
			return 0, false
		}
		allowed := false
		for _, name := range strings.Split(app.AllowedUsers, ",") {
			if strings.TrimSpace(name) == user.Username {
				allowed = true
				break
			}
		}
		if !allowed && !user.IsAdmin {
			return 0, false
		}
	}

	// 该授权最近使用时间（#80）：60s 节流更新，首次使用立即写入
	database.TouchSSOSessionRequest(session.Token)
	return session.UserID, true
}

// requireSSOAuth 要求 SSO 认证：浏览器请求重定向到 SSO 登录页，
// API 客户端（Accept 不含 text/html）返回 401 + WWW-Authenticate 以便其提供票据。
func requireSSOAuth(w http.ResponseWriter, r *http.Request, database *db.DB, subdomain string, hop pkgTunnel.HopContext) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		DoSSORedirect(w, r, database, hop)
		return
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="HopProxy Access Token"`)
	http.Error(w, "需要认证", http.StatusUnauthorized)
}

// CheckAccessTokenFromHeaders 从 headers map 中检查访问票据（用于隧道消息）
// fromClientID 为请求来源的客户端 ID
// 返回 (userID, true) 表示认证通过；userID 供内置变量 ${user_id} 使用 (#50)
func CheckAccessTokenFromHeaders(headers map[string]string, database *db.DB, app *db.App, fromClientID string) (int64, bool) {
	var authHeader string
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") {
			authHeader = v
			break
		}
	}
	return checkAccessToken(authHeader, database, app, fromClientID)
}

// checkSSOCookieFromHeaders 从 headers map 中检查 SSO Cookie 是否有效（用于 relay 路径）。
// subdomain 为请求 host 提取的具体子域名，用于构造 cookie name 与校验 session 归属 (#40)。
// 不检查 allowed_users（relay 路径只验证 session 存在性 + 子域名绑定）。
// 返回 (userID, true) 表示认证通过；userID 供内置变量 ${user_id} 使用 (#50)
func checkSSOCookieFromHeaders(headers map[string]string, database *db.DB, app *db.App, subdomain string) (int64, bool) {
	cookieHeader := headers["Cookie"]
	if cookieHeader == "" {
		return 0, false
	}
	cookieName := db.SSOCookieName(app.ID, subdomain)
	token := extractCookie(cookieHeader, cookieName)
	if token == "" {
		return 0, false
	}
	session, err := database.GetSSOSession(token, app.ID)
	if err != nil {
		return 0, false
	}
	// 子域名绑定校验 (#40)
	if session.Subdomain != subdomain {
		return 0, false
	}
	// 该授权最近使用时间（#80）：Path 5 relay 经服务端的成功使用同样计入
	database.TouchSSOSessionRequest(session.Token)
	return session.UserID, true
}

// DoSSORedirect 执行 SSO 重定向（从 HTTP 请求）。
// 日志字段取自 hop（request_id/subdomain/app_id，应用不存在或已禁用时 AppID=0，#72）。
func DoSSORedirect(w http.ResponseWriter, r *http.Request, database *db.DB, hop pkgTunnel.HopContext) {
	info := GetSSORedirectInfo(r, database)

	slog.Info("SSO 重定向", append(hop.LogAttrsWithType("auth"),
		"host", info.Host,
		"scheme", info.Scheme,
		"X-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"),
		"X-Forwarded-Host", r.Header.Get("X-Forwarded-Host"),
		"requestURI", info.RequestURI,
	)...)

	ssoURL, err := info.BuildSSORedirectURL()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Debug("SSO 重定向 URL", append(hop.LogAttrsWithType("auth"), "ssoURL", ssoURL)...)
	http.Redirect(w, r, ssoURL, http.StatusFound)
}

// clientIP 从请求中提取客户端 IP（已移至 pkg/httputil）
var clientIP = httputil.ClientIP
