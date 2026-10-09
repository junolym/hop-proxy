package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/password"
)

// SettingsHandler 站点设置处理
type SettingsHandler struct {
	db *db.DB
}

func newSettingsHandler(database *db.DB) *SettingsHandler {
	return &SettingsHandler{db: database}
}

// Get 获取站点设置（所有用户可读）
func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.GetAllSettings()
	if err != nil {
		slog.ErrorContext(r.Context(), "获取设置失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取设置失败")
		return
	}
	// 不返回敏感信息
	delete(settings, "jwt_secret")
	jsonOK(w, settings)
}

// UserSettingsHandler 用户设置处理
type UserSettingsHandler struct {
	db *db.DB
}

func newUserSettingsHandler(database *db.DB) *UserSettingsHandler {
	return &UserSettingsHandler{db: database}
}

// Get 获取用户设置
func (h *UserSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	jsonOK(w, map[string]any{
		"auto_disable_days":  user.AutoDisableDays,
		"temp_login_enabled": user.TempLoginEnabled,
		"temp_login_pin_set": user.TempLoginPIN != "",
		"totp_secret_set":    user.TOTPSecret != "",
		"totp_enabled":       user.TOTPEnabled,
		"quick_login":        user.QuickLogin,
		"app_api_enabled":    user.AppAPIEnabled,
		"app_api_token_set":  user.AppAPIToken != "",
		"app_api_token":      user.AppAPIToken,
	})
}

// Update 更新用户设置
func (h *UserSettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	var req struct {
		AutoDisableDays  *int    `json:"auto_disable_days"`
		TempLoginEnable  *bool   `json:"temp_login_enabled"`
		TempLoginPIN     *string `json:"temp_login_pin"` // nil=不改；空字符串=不改；非空=更新
		QuickLogin       *bool   `json:"quick_login"`
		AppAPIEnabled    *bool   `json:"app_api_enabled"`     // nil=不改；true=启用（首次自动生成 token）；false=禁用并清空 token
		ResetAppAPIToken *bool   `json:"reset_app_api_token"` // true=重置 token（自动启用），返回新 token
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	// 更新自动禁用天数
	if req.AutoDisableDays != nil {
		days := *req.AutoDisableDays
		if days < 0 || days > 365 {
			jsonError(w, http.StatusBadRequest, "自动禁用天数必须在 0-365 之间")
			return
		}
		if err := h.db.UpdateAutoDisableDays(claims.UserID, days); err != nil {
			slog.ErrorContext(r.Context(), "更新设置失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "更新设置失败")
			return
		}
	}

	// 更新快速登录开关
	if req.QuickLogin != nil {
		if err := h.db.UpdateQuickLogin(claims.UserID, *req.QuickLogin); err != nil {
			slog.ErrorContext(r.Context(), "更新快速登录设置失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "更新快速登录设置失败")
			return
		}
	}

	// 更新临时登录开关与 PIN
	if req.TempLoginEnable != nil || req.TempLoginPIN != nil {
		// 取当前用户状态
		user, err := h.db.GetUserByID(claims.UserID)
		if err != nil {
			slog.ErrorContext(r.Context(), "获取用户信息失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
			return
		}

		enabled := user.TempLoginEnabled
		if req.TempLoginEnable != nil {
			enabled = *req.TempLoginEnable
		}

		// 启用临时登录时必须已有 totp_secret
		if enabled && user.TOTPSecret == "" {
			jsonError(w, http.StatusBadRequest, "请先绑定 TOTP 密钥后再启用临时登录")
			return
		}

		// PIN 校验：仅当本次启用或修改 PIN 时强制要求非空
		pin := ""
		if req.TempLoginPIN != nil {
			pin = strings.TrimSpace(*req.TempLoginPIN)
			if pin != "" && !isValidPIN(pin) {
				jsonError(w, http.StatusBadRequest, "PIN 必须为 6 位数字")
				return
			}
		}
		// 启用时未提供 PIN，沿用现有 PIN
		if enabled && pin == "" && user.TempLoginPIN == "" {
			jsonError(w, http.StatusBadRequest, "首次启用临时登录需要设置 PIN")
			return
		}

		// 非空 PIN 用 bcrypt 哈希存储；空 PIN 表示沿用现有值，传空字符串给 db
		storedPin := ""
		if pin != "" {
			hashed, err := password.HashPassword(pin)
			if err != nil {
				slog.ErrorContext(r.Context(), "PIN 加密失败", "type", "api", "error", err)
				jsonError(w, http.StatusInternalServerError, "PIN 加密失败")
				return
			}
			storedPin = hashed
		}

		if err := h.db.UpdateTempLogin(claims.UserID, enabled, storedPin); err != nil {
			slog.ErrorContext(r.Context(), "更新临时登录设置失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "更新临时登录设置失败")
			return
		}
	}

	// 更新应用 API 开关
	if req.AppAPIEnabled != nil {
		token, err := h.db.UpdateAppAPI(claims.UserID, *req.AppAPIEnabled)
		if err != nil {
			slog.ErrorContext(r.Context(), "更新应用 API 设置失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "更新应用 API 设置失败")
			return
		}
		// 首次启用且生成了新 token，附加到响应（前端用于一次性展示）
		if token != "" {
			jsonOK(w, map[string]any{
				"message":       "设置已更新",
				"app_api_token": token,
			})
			return
		}
	}

	// 重置应用 API token
	if req.ResetAppAPIToken != nil && *req.ResetAppAPIToken {
		token, err := h.db.ResetAppAPIToken(claims.UserID)
		if err != nil {
			slog.ErrorContext(r.Context(), "重置应用 API token 失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "重置应用 API token 失败")
			return
		}
		jsonOK(w, map[string]any{
			"message":       "token 已重置",
			"app_api_token": token,
		})
		return
	}

	slog.InfoContext(r.Context(), "用户设置更新", "type", "api", "user_id", claims.UserID)
	jsonMsg(w, "设置已更新")
}

// isValidPIN 校验 PIN 为 6 位数字
func isValidPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
