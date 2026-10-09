package api

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
)

// TOTPHandler TOTP 处理器
type TOTPHandler struct {
	db *db.DB
}

func newTOTPHandler(database *db.DB) *TOTPHandler {
	return &TOTPHandler{db: database}
}

// Setup 生成 TOTP 密钥和二维码。
// - totp_secret 为空时：生成新密钥并保存（首次依赖）
// - totp_secret 已存在时：使用现有密钥生成二维码（不重新生成，避免让用户重复扫码）
// 已启用 totp_enabled 的用户也可调用（用于为临时登录获取二维码）
func (h *TOTPHandler) Setup(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	// 获取用户信息
	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	// 获取站点名称作为 Issuer
	siteName, _ := h.db.GetSetting("site_name")
	if siteName == "" {
		siteName = "HopProxy"
	}

	var key *otp.Key
	if user.TOTPSecret != "" {
		// 已有密钥：从 secret 重建 key 用于生成二维码（不重新生成）
		key, err = otp.NewKeyFromURL(buildTOTPUri(siteName, user.Username, user.TOTPSecret))
		if err != nil {
			slog.ErrorContext(r.Context(), "重建密钥失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "重建密钥失败")
			return
		}
	} else {
		// 首次：生成新密钥
		key, err = totp.Generate(totp.GenerateOpts{
			Issuer:      siteName,
			AccountName: user.Username,
			SecretSize:  32,
			Digits:      otp.DigitsSix,
			Algorithm:   otp.AlgorithmSHA1,
		})
		if err != nil {
			slog.ErrorContext(r.Context(), "生成密钥失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "生成密钥失败")
			return
		}
		if err := h.db.UpdateTOTPSecret(user.ID, key.Secret()); err != nil {
			slog.ErrorContext(r.Context(), "保存密钥失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "保存密钥失败")
			return
		}
	}

	// 生成二维码图片
	img, err := key.Image(200, 200)
	if err != nil {
		slog.ErrorContext(r.Context(), "生成二维码失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "生成二维码失败")
		return
	}

	// 将图片编码为 base64
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		slog.ErrorContext(r.Context(), "编码二维码失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "编码二维码失败")
		return
	}
	qrBase64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	jsonOK(w, map[string]string{
		"secret":    key.Secret(),
		"qr_base64": qrBase64,
		"uri":       key.URL(),
	})
}

// buildTOTPUri 拼接 otpauth URI（用于从已有 secret 重建 key）
func buildTOTPUri(issuer, account, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6",
		url.PathEscape(issuer), url.PathEscape(account), secret, url.PathEscape(issuer))
}

// Enable 验证 TOTP 码并启用二次验证
func (h *TOTPHandler) Enable(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

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

	// 获取用户信息
	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	// 已启用 TOTP 的用户不能再次启用
	if user.TOTPEnabled {
		jsonError(w, http.StatusBadRequest, "已启用二次验证")
		return
	}

	// 验证 TOTP 码
	if user.TOTPSecret == "" {
		jsonError(w, http.StatusBadRequest, "请先生成二次验证密钥")
		return
	}

	valid := totp.Validate(req.Code, user.TOTPSecret)
	if !valid {
		jsonError(w, http.StatusBadRequest, "验证码错误")
		return
	}

	// 启用 TOTP
	if err := h.db.EnableTOTP(user.ID); err != nil {
		slog.ErrorContext(r.Context(), "启用二次验证失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "启用二次验证失败")
		return
	}

	slog.InfoContext(r.Context(), "TOTP 启用", "type", "api", "user_id", user.ID, "username", user.Username, "ip", clientIP(r))
	jsonMsg(w, "二次验证已启用")
}

// Disable 禁用 TOTP（需要验证密码或 TOTP 码）
func (h *TOTPHandler) Disable(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	var req struct {
		Password string `json:"password"` // 密码验证
		Code     string `json:"code"`     // TOTP 码验证（二选一）
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	// 获取用户信息
	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	// 未启用 TOTP 的用户不能禁用
	if !user.TOTPEnabled {
		jsonError(w, http.StatusBadRequest, "未启用二次验证")
		return
	}

	// 验证方式：密码或 TOTP 码二选一
	verified := false

	if req.Password != "" {
		// 验证密码
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err == nil {
			verified = true
		}
	} else if req.Code != "" {
		// 验证 TOTP 码
		if totp.Validate(req.Code, user.TOTPSecret) {
			verified = true
		}
	}

	if !verified {
		jsonError(w, http.StatusBadRequest, "密码或验证码错误")
		return
	}

	// 禁用 TOTP
	if err := h.db.DisableTOTP(user.ID); err != nil {
		slog.ErrorContext(r.Context(), "禁用二次验证失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "禁用二次验证失败")
		return
	}

	slog.InfoContext(r.Context(), "TOTP 禁用", "type", "api", "user_id", user.ID, "username", user.Username, "ip", clientIP(r))
	jsonMsg(w, "二次验证已禁用")
}

// Status 获取当前用户的 TOTP 状态
func (h *TOTPHandler) Status(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	// 获取用户信息
	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	jsonOK(w, map[string]bool{
		"totp_enabled": user.TOTPEnabled,
	})
}

// Reset 重置 TOTP 密钥（独立功能，不影响 totp_enabled / temp_login_enabled 状态）。
// 重置后需要重新调用 Setup 生成新密钥并重新绑定认证器。
// 验证方式：密码或当前验证码二选一（与 Disable 一致）。
// 安全提示：若 totp_enabled=true 重置后会导致二次验证失效（无法登录），
//
//	若 temp_login_enabled=true 重置后会导致临时登录失效。
func (h *TOTPHandler) Reset(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	if user.TOTPSecret == "" {
		jsonError(w, http.StatusBadRequest, "无 TOTP 密钥可重置")
		return
	}

	// 验证方式：密码或 TOTP 码二选一
	verified := false
	if req.Password != "" {
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err == nil {
			verified = true
		}
	} else if req.Code != "" {
		if totp.Validate(req.Code, user.TOTPSecret) {
			verified = true
		}
	}
	if !verified {
		jsonError(w, http.StatusBadRequest, "密码或验证码错误")
		return
	}

	// 重置密钥
	if err := h.db.ResetTOTPSecret(user.ID); err != nil {
		slog.ErrorContext(r.Context(), "重置密钥失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "重置密钥失败")
		return
	}

	slog.InfoContext(r.Context(), "TOTP 重置", "type", "api", "user_id", user.ID, "username", user.Username, "ip", clientIP(r))
	jsonMsg(w, "TOTP 密钥已重置，请重新绑定")
}
