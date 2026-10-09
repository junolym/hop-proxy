package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/api/ratelimit"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/password"
)

// TempLoginHandler 临时登录（PIN + TOTP token）处理。
// 用户已启用临时登录时，可在登录页通过 用户名 + PIN + TOTP token 一次性换取某应用的 SSO Cookie，
// 不签发管理 session。
type TempLoginHandler struct {
	db      *db.DB
	limiter *ratelimit.RateLimiter

	// 已使用 token 的短期记录：key = userID:token，value = 使用时间
	// 每个 TOTP token 在 TTL 内仅可使用一次，避免同一 token 同时换多个应用。
	usedMu sync.Mutex
	used   map[string]time.Time
}

// tempTokenUsedTTL 已使用 token 记录保留时长。
// 大于 TOTP 默认 30s 周期，覆盖相邻两个 period 的重放窗口。
const tempTokenUsedTTL = 2 * time.Minute

func newTempLoginHandler(database *db.DB) *TempLoginHandler {
	h := &TempLoginHandler{
		db:      database,
		limiter: ratelimit.New(server.LoginMaxAttempts, time.Duration(server.LoginWindowSecs)*time.Second),
		used:    make(map[string]time.Time),
	}
	go h.cleanupUsedLoop()
	return h
}

// cleanupUsedLoop 后台定期清理已使用 token 记录，避免 map 无界增长
func (h *TempLoginHandler) cleanupUsedLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.usedMu.Lock()
		now := time.Now()
		for k, t := range h.used {
			if now.Sub(t) > tempTokenUsedTTL {
				delete(h.used, k)
			}
		}
		h.usedMu.Unlock()
	}
}

// Login 临时登录：验证 PIN + TOTP token + redirect，通过后下发 SSO cookie 并跳转
//
// 请求体: { username, pin, token, redirect }
//   - username: 用户名
//   - pin: 6 位数字 PIN
//   - token: 6 位数字 TOTP 验证码（与二次验证共用同一 secret）
//   - redirect: 目标应用 URL（如 https://ai.app.example.com/）
//
// 验证流程：用户存在 → 临时登录已启用 → PIN 匹配 → totp_secret 存在 →
// token 未在窗口内使用 → TOTP 验证通过 → redirect 合法 → 应用 SSO → allowed_users 权限
//
// 安全：所有认证失败（用户不存在、未启用、PIN 错、token 错、无密钥）统一返回
// 401 + 模糊错误「用户名、PIN 或验证码错误」，不暴露系统状态；
// 限流复用与登录共享的 IP 限流器；token 单次使用防止重放。
func (h *TempLoginHandler) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)

	if secs := h.limiter.IsBlocked(ip); secs > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", secs))
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("尝试过于频繁，请 %d 秒后重试", secs))
		return
	}

	var req struct {
		Username string `json:"username"`
		PIN      string `json:"pin"`
		Token    string `json:"token"`
		Redirect string `json:"redirect"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	if req.Username == "" || req.PIN == "" || req.Token == "" || req.Redirect == "" {
		jsonError(w, http.StatusBadRequest, "参数不能为空")
		return
	}

	// 查找用户
	user, err := h.db.GetUserByUsername(req.Username)
	if err != nil {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "username", req.Username, "ip", ip, "reason", "user_not_found")
		jsonError(w, http.StatusUnauthorized, "用户名、PIN 或验证码错误")
		return
	}

	// 校验临时登录已启用
	if !user.TempLoginEnabled {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "temp_login_disabled")
		jsonError(w, http.StatusUnauthorized, "用户名、PIN 或验证码错误")
		return
	}

	// 校验 PIN（bcrypt 验证）
	if err := password.VerifyPassword(user.TempLoginPIN, req.PIN); err != nil {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "wrong_pin")
		jsonError(w, http.StatusUnauthorized, "用户名、PIN 或验证码错误")
		return
	}

	// 校验 totp_secret 存在
	if user.TOTPSecret == "" {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "no_totp_secret")
		jsonError(w, http.StatusUnauthorized, "用户名、PIN 或验证码错误")
		return
	}

	// 校验 token 未在窗口内使用过（单次使用）
	// 注意：此处先标记检查，但 token 已使用的错误是针对正常用户的友好提示
	// （PIN 和 token 都正确，只是 token 被用过了），不应模糊化
	usedKey := fmt.Sprintf("%d:%s", user.ID, req.Token)
	h.usedMu.Lock()
	if _, used := h.used[usedKey]; used {
		h.usedMu.Unlock()
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "token_already_used")
		jsonError(w, http.StatusBadRequest, "该验证码已使用，请等待下一个验证码")
		return
	}
	h.usedMu.Unlock()

	// 验证 TOTP token（默认 30s 周期，前后各 1 个 period 容差）
	if !totp.Validate(req.Token, user.TOTPSecret) {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "wrong_token")
		jsonError(w, http.StatusUnauthorized, "用户名、PIN 或验证码错误")
		return
	}

	// 解析 redirect 并验证应用
	app, subdomain, ok := parseAppRedirect(h.db, req.Redirect)
	if !ok {
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "invalid_redirect", "redirect", req.Redirect)
		jsonError(w, http.StatusBadRequest, "无效的跳转地址")
		return
	}

	// 应用必须启用 SSO 类认证
	if app.AuthMethod != server.AuthMethodSSO &&
		app.AuthMethod != server.AuthMethodSSOToken &&
		app.AuthMethod != server.AuthMethodSSOOwner &&
		app.AuthMethod != server.AuthMethodSSOAll {
		jsonError(w, http.StatusBadRequest, "该应用未启用 SSO 认证")
		return
	}

	// AllowedUsers 权限校验（与 sso.go Authorize 一致）
	if !userAllowedForApp(user, app) {
		slog.InfoContext(r.Context(), "临时登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "not_allowed", "app_id", app.ID)
		jsonError(w, http.StatusForbidden, "您不在允许访问的用户列表中")
		return
	}

	// 标记 token 已使用（在所有校验通过后才标记，避免因后续错误导致 token 被无效消耗）
	h.usedMu.Lock()
	h.used[usedKey] = time.Now()
	h.usedMu.Unlock()

	// 创建 SSO session（使用临时登录用户的 user_id）
	// subdomain 作为 cookie name 后缀与 session 绑定 (#40)
	ttl := ssoSessionTTL(app.SSOCookieMaxAge)
	cookieMaxAge := ssoCookieMaxAge(app.SSOCookieMaxAge)
	// 来源会话（#80）：临时登录为一次性单应用会话，独立记录来源与设备信息
	sess, err := h.db.CreateAuthSession(user.ID, db.SessionSourceTempLogin, "", ip, r.UserAgent(), ttl)
	if err != nil {
		slog.ErrorContext(r.Context(), "临时登录：创建会话记录失败", "type", "api", "error", err, "user_id", user.ID)
		jsonError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	ssoToken, err := h.db.CreateSSOSession(user.ID, app.ID, subdomain, ttl, sess.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "临时登录：创建 SSO session 失败", "type", "api", "error", err, "app_id", app.ID, "user_id", user.ID)
		jsonError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}

	proxyDomain, _ := h.db.GetSetting("proxy_domain")
	http.SetCookie(w, &http.Cookie{
		Name:     db.SSOCookieName(app.ID, subdomain),
		Value:    ssoToken,
		Path:     "/",
		Domain:   "." + proxyDomain,
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	h.limiter.Reset(ip)
	slog.InfoContext(r.Context(), "临时登录成功", "type", "api", "user_id", user.ID, "username", user.Username,
		"app_id", app.ID, "app_name", app.Name, "subdomain", subdomain, "ip", ip)

	jsonOK(w, map[string]any{
		"ok":       true,
		"redirect": req.Redirect,
	})
}

// parseAppRedirect 解析 redirect URL 并校验属于 proxy_domain，返回应用与具体子域名。
// 返回的 subdomain 用于 cookie name 后缀与 session 绑定 (#40)。
// 临时登录与扫码登录（#71）共用。
func parseAppRedirect(database *db.DB, redirect string) (*db.App, string, bool) {
	subdomain, ok := extractRedirectSubdomain(database, redirect)
	if !ok {
		return nil, "", false
	}

	app, err := database.GetAppBySubdomain(subdomain)
	if err != nil || !app.Enabled {
		return nil, "", false
	}
	return app, subdomain, true
}

// extractRedirectSubdomain 解析 redirect URL，提取属于 proxy_domain 的子域名。
// 仅做语法级校验（URL 可解析 + host 是 proxy_domain 的子域名），不查应用存在性——
// 供扫码登录创建阶段（免认证）使用，避免向未登录用户泄漏子域名是否有效 (#74)。
func extractRedirectSubdomain(database *db.DB, redirect string) (string, bool) {
	u, err := url.Parse(redirect)
	if err != nil {
		return "", false
	}

	proxyDomain, _ := database.GetSetting("proxy_domain")
	if proxyDomain == "" {
		return "", false
	}

	host := u.Host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	host = strings.ToLower(host)
	suffix := "." + strings.ToLower(proxyDomain)
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	subdomain := strings.TrimSuffix(host, suffix)
	if subdomain == "" {
		return "", false
	}
	return subdomain, true
}

// userAllowedForApp 检查用户对该应用的 allowed_users 权限。
// 逻辑与 sso.go Authorize、proxy/sso.go validateSSOCookie 一致。
func userAllowedForApp(user *db.User, app *db.App) bool {
	if app.AllowedUsers == server.AllowedUsersOwner || app.AllowedUsers == "" {
		if app.UserID != user.ID && !user.IsAdmin {
			return false
		}
	} else if app.AllowedUsers != server.AllowedUsersAll {
		allowed := false
		for _, name := range strings.Split(app.AllowedUsers, ",") {
			if strings.TrimSpace(name) == user.Username {
				allowed = true
				break
			}
		}
		if !allowed && !user.IsAdmin {
			return false
		}
	}
	return true
}
