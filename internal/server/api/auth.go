package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/api/ratelimit"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/password"
)

// AuthHandler 认证处理
type AuthHandler struct {
	db      *db.DB
	limiter *ratelimit.RateLimiter
}

func newAuthHandler(database *db.DB) *AuthHandler {
	return &AuthHandler{db: database, limiter: ratelimit.New(server.LoginMaxAttempts, time.Duration(server.LoginWindowSecs)*time.Second)}
}

// clientIP 从请求中提取客户端 IP（已移至 pkg/httputil）
var clientIP = httputil.ClientIP

// Login 用户登录
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)

	// 检查是否被限流
	if secs := h.limiter.IsBlocked(ip); secs > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", secs))
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("登录尝试过于频繁，请 %d 秒后重试", secs))
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	if req.Username == "" || req.Password == "" {
		jsonError(w, http.StatusBadRequest, "用户名和密码不能为空")
		return
	}

	// 查找用户
	user, err := h.db.GetUserByUsername(req.Username)
	if err != nil {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "登录失败", "type", "api", "username", req.Username, "ip", ip, "reason", "user_not_found")
		jsonError(w, http.StatusUnauthorized, server.ErrInvalidCredentials)
		return
	}

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "登录失败", "type", "api", "user_id", user.ID, "username", req.Username, "ip", ip, "reason", "wrong_password")
		jsonError(w, http.StatusUnauthorized, server.ErrInvalidCredentials)
		return
	}

	// 密码验证成功，检查是否需要 TOTP 二次验证
	if user.TOTPEnabled {
		// 生成临时 token（TOTP 待验证状态）
		tempToken, err := h.generateTOTPPendingToken(user)
		if err != nil {
			slog.ErrorContext(r.Context(), "生成临时令牌失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "生成临时令牌失败")
			return
		}

		// 设置临时 Cookie（有效期 5 分钟）
		http.SetCookie(w, &http.Cookie{
			Name:     server.CookieTOTPPending,
			Value:    tempToken,
			Path:     "/",
			MaxAge:   server.TOTPPendingTTL, // 5 分钟
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		jsonOK(w, map[string]any{
			"totp_required": true,
			"username":      user.Username,
		})
		return
	}

	// 登录成功，清零限流
	h.limiter.Reset(ip)
	slog.InfoContext(r.Context(), "登录成功", "type", "api", "user_id", user.ID, "username", user.Username, "ip", ip)

	// 生成 JWT 并设置 httpOnly Cookie
	if err := issueSessionCookie(w, r, h.db, user, db.SessionSourcePassword); err != nil {
		slog.ErrorContext(r.Context(), "生成令牌失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "生成令牌失败")
		return
	}

	jsonOK(w, map[string]bool{"ok": true})
}

// issueSessionCookie 为用户签发管理 session：创建登录会话记录（#80）+ 生成 JWT 并写入
// httpOnly Cookie，防止 XSS 盗取。密码登录、TOTP 二次验证、通行密钥登录（#71）共用。
// source 标识登录方式（db.SessionSourcePassword / db.SessionSourcePasskey）。
func issueSessionCookie(w http.ResponseWriter, r *http.Request, database *db.DB, user *db.User, source string) error {
	secret, err := database.GetSetting("jwt_secret")
	if err != nil {
		return err
	}

	ttlStr, _ := database.GetSetting("session_ttl")
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		ttl = 24 * time.Hour
	}

	// 创建登录会话记录（#80）：sid 写入 JWT，服务端可校验有效性并支持强制下线
	session, err := database.CreateAuthSession(user.ID, source, "", clientIP(r), r.UserAgent(), ttl)
	if err != nil {
		return err
	}

	claims := &UserClaims{
		UserID:    user.ID,
		Username:  user.Username,
		IsAdmin:   user.IsAdmin,
		Role:      user.Role,
		SessionID: session.SID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return err
	}

	cookie := &http.Cookie{
		Name:     server.CookieSession,
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	// 会话级模式（#77）：不设 MaxAge，Cookie 只存内存、关闭浏览器即失效；
	// session_ttl 语义不变，仍作为 JWT 服务端有效期上限。缺省/未知值按持久处理。
	if mode, err := database.GetSetting("session_cookie_mode"); err != nil || mode != server.SessionCookieSession {
		cookie.MaxAge = int(ttl.Seconds())
	}
	http.SetCookie(w, cookie)
	return nil
}

// Me 获取当前用户信息
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}
	jsonOK(w, map[string]any{
		"id":       claims.UserID,
		"username": claims.Username,
		"is_admin": claims.IsAdmin,
		"role":     claims.Role,
	})
}

// LoginOptions 登录页可见的登录方式开关（公开端点，#71）。
// 管理员在系统设置控制「临时登录」「扫码登录」入口是否显示，默认关闭。
func (h *AuthHandler) LoginOptions(w http.ResponseWriter, r *http.Request) {
	showTemp, _ := h.db.GetSetting("show_temp_login")
	showQR, _ := h.db.GetSetting("show_qr_login")
	jsonOK(w, map[string]bool{
		"show_temp_login": showTemp == "true",
		"show_qr_login":   showQR == "true",
	})
}

// ChangePassword 修改密码
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	if req.OldPassword == "" || req.NewPassword == "" {
		jsonError(w, http.StatusBadRequest, "旧密码和新密码不能为空")
		return
	}
	if len(req.NewPassword) < 6 {
		jsonError(w, http.StatusBadRequest, server.ErrPasswordTooShort)
		return
	}

	// 验证旧密码
	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
		jsonError(w, http.StatusBadRequest, "旧密码错误")
		return
	}

	// 更新密码
	hash, err := password.HashPassword(req.NewPassword)
	if err != nil {
		slog.ErrorContext(r.Context(), "密码加密失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := h.db.UpdatePassword(claims.UserID, hash); err != nil {
		slog.ErrorContext(r.Context(), "更新密码失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新密码失败")
		return
	}

	slog.InfoContext(r.Context(), "密码修改", "type", "api", "user_id", claims.UserID)
	jsonMsg(w, "密码修改成功")
}

// Logout 退出登录，清除 Session Cookie 及所有 SSO 票据。
// SSO cookie name 含子域名后缀 (#40)，无法精确清除浏览器中的所有 SSO cookie，
// 改为通过 MarkUserSSOSessionsRevoked 标记失效 DB session——下次请求带旧 cookie 时
// 查 DB 不命中 → 重定向登录。登录会话本身标记 logged_out（#80：标记不删行，审计保留）。
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims != nil {
		// 标记当前登录会话退出 + 失效该用户的所有应用授权（cookie 靠 session 失效作废）
		h.db.EndAuthSession(claims.SessionID, db.SessionStatusLoggedOut)
		h.db.MarkUserSSOSessionsRevoked(claims.UserID)

		slog.InfoContext(r.Context(), "退出登录", "type", "api", "user_id", claims.UserID, "username", claims.Username, "ip", clientIP(r))
	}

	// 清除管理端 Session Cookie
	http.SetCookie(w, &http.Cookie{
		Name:     server.CookieSession,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	slog.InfoContext(r.Context(), "用户退出登录", "type", "api", "user_id", claims.UserID)
	jsonMsg(w, "已退出登录")
}

// generateTOTPPendingToken 生成 TOTP 待验证状态的临时 token
func (h *AuthHandler) generateTOTPPendingToken(user *db.User) (string, error) {
	secret, err := h.db.GetSetting("jwt_secret")
	if err != nil {
		return "", err
	}

	claims := &UserClaims{
		UserID:      user.ID,
		Username:    user.Username,
		IsAdmin:     user.IsAdmin,
		Role:        user.Role,
		TOTPPending: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)), // 临时 token 有效期 5 分钟
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// VerifyTOTP 验证 TOTP 码并完成登录
func (h *AuthHandler) VerifyTOTP(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)

	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	if req.Code == "" {
		jsonError(w, http.StatusBadRequest, "验证码不能为空")
		return
	}

	// 从 Cookie 获取临时 token
	cookie, err := r.Cookie(server.CookieTOTPPending)
	if err != nil || cookie.Value == "" {
		jsonError(w, http.StatusUnauthorized, "请先完成密码验证")
		return
	}

	// 解析临时 token
	secret, err := h.db.GetSetting("jwt_secret")
	if err != nil {
		slog.ErrorContext(r.Context(), "获取 JWT 密钥失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取 JWT 密钥失败")
		return
	}

	claims := &UserClaims{}
	token, err := jwt.ParseWithClaims(cookie.Value, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("不支持的签名方法: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		jsonError(w, http.StatusUnauthorized, "临时令牌无效或已过期")
		return
	}

	// 验证是否为 TOTP 待验证状态
	if !claims.TOTPPending {
		jsonError(w, http.StatusBadRequest, "令牌状态错误")
		return
	}

	// 获取用户的 TOTP 密钥
	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	// 验证 TOTP 码
	if !totp.Validate(req.Code, user.TOTPSecret) {
		slog.InfoContext(r.Context(), "TOTP 验证失败", "type", "api", "user_id", user.ID, "username", user.Username, "ip", ip)
		jsonError(w, http.StatusBadRequest, "验证码错误")
		return
	}

	// TOTP 验证成功，清除临时 Cookie
	http.SetCookie(w, &http.Cookie{
		Name:     server.CookieTOTPPending,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// 生成正式 JWT 并设置 Session Cookie
	if err := issueSessionCookie(w, r, h.db, user, db.SessionSourcePassword); err != nil {
		slog.ErrorContext(r.Context(), "生成令牌失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "生成令牌失败")
		return
	}

	h.limiter.Reset(ip) // 清零限流
	slog.InfoContext(r.Context(), "登录成功", "type", "api", "user_id", user.ID, "username", user.Username, "totp", true, "ip", ip)
	jsonOK(w, map[string]bool{"ok": true})
}
