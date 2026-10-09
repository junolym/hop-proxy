package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
)

// SSOHandler SSO 认证处理
type SSOHandler struct {
	db *db.DB
	// 通行密钥 handler：二次验证方式为 passkey 时复用其实例构造、用户适配器与 challenge 会话 (#76)
	wa *WebAuthnHandler
}

func newSSOHandler(database *db.DB, wa *WebAuthnHandler) *SSOHandler {
	return &SSOHandler{db: database, wa: wa}
}

// Authorize 已登录用户为指定应用授权，颁发该应用专属票据
// 请求参数：redirect - 授权后跳转的目标 URL，从中提取子域名查询应用
//
// 应用开启二次验证时为两阶段调用 (#76)：
//   - totp：首次调用不带 totp_code，返回 second_factor="totp"；前端展示验证码输入后带 totp_code 再调完成授权
//   - passkey：首次调用不带 assertion，返回 second_factor="passkey" 与断言参数（UV=required）；
//     前端完成浏览器断言后带 assertion 再调完成授权。断言仅作确认手势，授权语义（目标应用、
//     子域名、redirect）始终由本接口从 redirect 解析并校验，与 /sso 页面呈现一致。
func (h *SSOHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		slog.WarnContext(r.Context(), "未登录访问", "type", "api")
		jsonError(w, http.StatusUnauthorized, "未登录")
		return
	}

	var req struct {
		Redirect  string          `json:"redirect"`
		TOTPCode  string          `json:"totp_code"`
		Assertion json.RawMessage `json:"assertion"` // 通行密钥断言（passkey 二次验证第二阶段）
	}
	if err := decodeJSON(r, &req); err != nil || req.Redirect == "" {
		jsonError(w, http.StatusBadRequest, "缺少 redirect 参数")
		return
	}

	// 解析 redirect URL，提取子域名
	redirectURL, err := url.Parse(req.Redirect)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的 redirect URL")
		return
	}

	// 获取 proxy_domain 配置
	proxyDomain, _ := h.db.GetSetting("proxy_domain")
	if proxyDomain == "" {
		slog.ErrorContext(r.Context(), "服务未配置", "type", "api")
		jsonError(w, http.StatusInternalServerError, "服务未配置")
		return
	}

	// 从 redirect URL 的 host 中提取子域名
	host := redirectURL.Host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	host = strings.ToLower(host)
	proxyDomainLower := strings.ToLower(proxyDomain)

	// 验证 host 属于 proxy_domain
	suffix := "." + proxyDomainLower
	if !strings.HasSuffix(host, suffix) {
		jsonError(w, http.StatusForbidden, "无效的 redirect 域名")
		return
	}

	subdomain := strings.TrimSuffix(host, suffix)
	if subdomain == "" {
		jsonError(w, http.StatusForbidden, "无效的 redirect 域名")
		return
	}

	// 根据子域名查询应用
	app, err := h.db.GetAppBySubdomain(subdomain)
	if err != nil {
		jsonError(w, http.StatusForbidden, "应用不存在或已禁用")
		return
	}

	// 检查是否需要认证
	if app.AuthMethod == server.AuthMethodNone {
		jsonError(w, http.StatusBadRequest, "该应用未启用 SSO 认证")
		return
	}

	// 权限检查：根据 allowed_users 字段
	if app.AllowedUsers == server.AllowedUsersOwner || app.AllowedUsers == "" {
		// 仅应用所有者或管理员可授权
		if app.UserID != claims.UserID && !claims.IsAdmin {
			jsonError(w, http.StatusForbidden, "仅应用所有者可访问")
			return
		}
	} else if app.AllowedUsers != server.AllowedUsersAll {
		// allowed_users 为逗号分隔的用户名列表
		allowed := false
		for _, name := range strings.Split(app.AllowedUsers, ",") {
			if strings.TrimSpace(name) == claims.Username {
				allowed = true
				break
			}
		}
		// 管理员始终允许
		if !allowed && !claims.IsAdmin {
			jsonError(w, http.StatusForbidden, "您不在允许访问的用户列表中")
			return
		}
	}
	// allowed_users == "all": 所有已登录用户均可授权

	// 应用开启二次验证时按方式校验 (#76)
	switch app.SecondFactor {
	case server.SecondFactorTOTP:
		user, err := h.db.GetUserByID(claims.UserID)
		if err != nil || user == nil {
			slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
			return
		}
		if !user.TOTPEnabled {
			jsonError(w, http.StatusForbidden, "该应用要求二次验证，请先在个人设置中开启 TOTP")
			return
		}
		if req.TOTPCode == "" {
			// 第一阶段：告知前端需要 TOTP 验证
			jsonOK(w, map[string]any{
				"second_factor": server.SecondFactorTOTP,
				"app_id":        app.ID,
				"app_name":      app.Name,
			})
			return
		}
		if !totp.Validate(req.TOTPCode, user.TOTPSecret) {
			jsonError(w, http.StatusBadRequest, "验证码错误")
			return
		}
	case server.SecondFactorPasskey:
		ok := h.authorizeWithPasskey(w, r, claims.UserID, app, req.Assertion)
		if !ok {
			return
		}
	}

	// 使用应用配置的 Cookie 过期时间
	ttl := ssoSessionTTL(app.SSOCookieMaxAge)
	cookieMaxAge := ssoCookieMaxAge(app.SSOCookieMaxAge)

	// subdomain 已在上方提取（从 redirect URL），作为 cookie name 后缀与 session 绑定 (#40)
	// 来源归属当前管理登录会话（#80）：会话管理页据此展示"某会话授权了哪些应用"
	parentID := int64(0)
	if sess := getAuthSessionFromContext(r); sess != nil {
		parentID = sess.ID
	}
	token, err := h.db.CreateSSOSession(claims.UserID, app.ID, subdomain, ttl, parentID)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建票据失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建票据失败")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     db.SSOCookieName(app.ID, subdomain),
		Value:    token,
		Path:     "/",
		Domain:   "." + proxyDomain,
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	jsonOK(w, map[string]any{
		"app_id":   app.ID,
		"app_name": app.Name,
	})

	slog.InfoContext(r.Context(), "SSO 授权", "type", "api", "user_id", claims.UserID, "app_id", app.ID,
		"app_name", app.Name, "subdomain", subdomain, "second_factor", app.SecondFactor, "ip", clientIP(r))
}

// authorizeWithPasskey 处理 passkey 方式的二次验证 (#76)。
// assertion 为空为第一阶段：校验用户已注册通行密钥后发起断言（UV=required），
// 返回断言参数；非空为第二阶段：验证断言通过则返回 true，否则已写响应并返回 false。
func (h *SSOHandler) authorizeWithPasskey(w http.ResponseWriter, r *http.Request, userID int64, app *db.App, assertion json.RawMessage) bool {
	user, err := h.db.GetUserByID(userID)
	if err != nil || user == nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return false
	}

	wa, err := h.wa.waInstance()
	if err != nil {
		slog.ErrorContext(r.Context(), "构造 WebAuthn 实例失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "服务配置错误")
		return false
	}
	u, err := h.wa.buildWaUser(user)
	if err != nil {
		slog.ErrorContext(r.Context(), "加载通行密钥凭据失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "加载凭据失败")
		return false
	}

	// JSON null 反序列化后为字面量 "null" 字节，与字段缺失同等视为第一阶段
	if len(assertion) == 0 || string(assertion) == "null" {
		// 第一阶段：用户必须持有至少一个通行密钥
		if len(u.creds) == 0 {
			jsonError(w, http.StatusForbidden, "该应用要求通行密钥二次验证，请先在个人设置中注册通行密钥")
			return false
		}
		options, session, err := wa.BeginLogin(u,
			webauthn.WithUserVerification(protocol.VerificationRequired),
		)
		if err != nil {
			slog.ErrorContext(r.Context(), "SSO 二次验证：发起断言失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "发起验证失败")
			return false
		}
		h.wa.storeSession(session, user.ID, "")
		jsonOK(w, map[string]any{
			"second_factor": server.SecondFactorPasskey,
			"options":       options,
			"app_id":        app.ID,
			"app_name":      app.Name,
		})
		return false
	}

	// 第二阶段：验证断言（challenge 会话一次性，须属于当前用户）
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(assertion))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "验证响应无效")
		return false
	}
	session := h.wa.takeSession(parsed.Response.CollectedClientData.Challenge)
	if session == nil || session.userID != user.ID {
		jsonError(w, http.StatusBadRequest, "验证会话已过期，请重试")
		return false
	}
	credential, err := wa.ValidateLogin(u, session.data, parsed)
	if err != nil {
		slog.InfoContext(r.Context(), "SSO 二次验证：断言验证失败", "type", "api", "user_id", user.ID, "app_id", app.ID, "error", err, "ip", clientIP(r))
		jsonError(w, http.StatusUnauthorized, "通行密钥验证失败")
		return false
	}

	// 更新凭据使用状态（签名计数 + 备份标志 + 最后使用时间）
	_ = h.db.UpdateWebAuthnCredentialUsageByCredentialID(
		base64.RawURLEncoding.EncodeToString(credential.ID),
		credential.Authenticator.SignCount, credential.Flags.BackupState,
	)
	return true
}

// RevokeApp 撤销对某个应用的 SSO 授权。
// cookie name 含子域名后缀（#40），从 admin_domain 调用时无法精确读取具体子域名的 cookie，
// 改为按 user+app 批量标记失效（#80：标记不删行，审计保留），session 失效后 cookie 自动作废。
func (h *SSOHandler) RevokeApp(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		slog.WarnContext(r.Context(), "未登录访问", "type", "api")
		jsonError(w, http.StatusUnauthorized, "未登录")
		return
	}

	appIDStr := r.URL.Query().Get("app_id")
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil || appID == 0 {
		jsonError(w, http.StatusBadRequest, "缺少 app_id")
		return
	}

	// 批量撤销该用户在该应用上的所有 SSO session（跨子域名，#80 改为标记失效）
	h.db.RevokeUserAppSSOSessions(claims.UserID, appID)

	// cookie name 含子域名后缀无法精确清除，靠 session 失效让 cookie 作废。
	// 浏览器下次请求带旧 cookie 时，server 查 DB 不命中 → 重定向 SSO 登录。

	slog.InfoContext(r.Context(), "SSO 撤销授权", "type", "api", "user_id", claims.UserID, "app_id", appID, "ip", clientIP(r))
	jsonMsg(w, "已撤销授权")
}

// GetAppBySubdomain 根据子域名查询应用信息（用于 SSO 页面显示应用名称）
func (h *SSOHandler) GetAppBySubdomain(w http.ResponseWriter, r *http.Request) {
	subdomain := r.URL.Query().Get("subdomain")
	if subdomain == "" {
		jsonError(w, http.StatusBadRequest, "缺少 subdomain 参数")
		return
	}

	// 获取 proxy_domain 配置进行验证
	proxyDomain, _ := h.db.GetSetting("proxy_domain")
	if proxyDomain == "" {
		slog.ErrorContext(r.Context(), "服务未配置", "type", "api")
		jsonError(w, http.StatusInternalServerError, "服务未配置")
		return
	}

	// 验证子域名格式（防止路径遍历等攻击）
	subdomain = strings.ToLower(strings.TrimSpace(subdomain))
	if strings.Contains(subdomain, ".") || strings.Contains(subdomain, "/") {
		jsonError(w, http.StatusBadRequest, "无效的子域名")
		return
	}

	// 查询应用
	app, err := h.db.GetAppBySubdomain(subdomain)
	if err != nil {
		// 应用不存在
		jsonErrorWithData(w, http.StatusNotFound, "应用不存在", map[string]any{"error_code": "app_not_found"})
		return
	}

	// 检查应用是否已禁用
	if !app.Enabled {
		// 应用已禁用
		jsonErrorWithData(w, http.StatusNotFound, "应用已禁用", map[string]any{"error_code": "app_disabled"})
		return
	}

	// 只返回必要的信息
	jsonOK(w, map[string]any{
		"id":            app.ID,
		"name":          app.Name,
		"require_auth":  app.RequireAuth,
		"second_factor": app.SecondFactor, // 供 /sso 授权页预判二次验证方式 (#76)
	})
}
