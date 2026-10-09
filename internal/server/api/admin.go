package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/password"
	"github.com/robin/hop-proxy/pkg/proxycfg"
	"github.com/robin/hop-proxy/pkg/random"
)

// AdminHandler 管理后台处理
type AdminHandler struct {
	db *db.DB
}

func newAdminHandler(database *db.DB) *AdminHandler {
	return &AdminHandler{db: database}
}

// ListUsers 用户列表
func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.db.ListUsers()
	if err != nil {
		slog.ErrorContext(r.Context(), "获取用户列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户列表失败")
		return
	}
	if users == nil {
		users = []db.User{}
	}
	jsonOK(w, users)
}

// CreateUser 创建用户（不允许创建管理员）
func (h *AdminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"` // user 或 guest，默认 user
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		jsonError(w, http.StatusBadRequest, "用户名和密码不能为空")
		return
	}
	if len(req.Password) < 6 {
		jsonError(w, http.StatusBadRequest, server.ErrPasswordTooShort)
		return
	}

	// 角色校验：只允许 user 或 guest
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "guest" {
		jsonError(w, http.StatusBadRequest, "无效的用户角色")
		return
	}

	exists, _ := h.db.UserExists(req.Username)
	if exists {
		jsonError(w, http.StatusBadRequest, "用户名已存在")
		return
	}

	hash, err := password.HashPassword(req.Password)
	if err != nil {
		slog.ErrorContext(r.Context(), "密码加密失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "密码加密失败")
		return
	}

	// 新用户永远不是管理员
	user, err := h.db.CreateUser(req.Username, hash, false, role)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建用户失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建用户失败")
		return
	}

	slog.InfoContext(r.Context(), "用户创建", "type", "api", "admin_id", getUserFromContext(r).UserID, "user_id", getUserFromContext(r).UserID, "username", user.Username)
	jsonOK(w, user)
}

// UpdateUser 编辑用户（不允许修改管理员状态）
func (h *AdminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的用户 ID")
		return
	}

	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"` // user 或 guest
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	user, err := h.db.GetUserByID(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "用户不存在")
		return
	}

	// 不允许修改管理员的信息
	if user.IsAdmin {
		slog.WarnContext(r.Context(), "权限拒绝：修改管理员", "type", "api")
		jsonError(w, http.StatusForbidden, "不允许修改管理员")
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = user.Username
	}

	// 检查用户名唯一性
	exists, _ := h.db.UserExistsExcluding(username, id)
	if exists {
		jsonError(w, http.StatusBadRequest, "用户名已存在")
		return
	}

	if err := h.db.UpdateUser(id, username); err != nil {
		slog.ErrorContext(r.Context(), "更新用户失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新用户失败")
		return
	}

	// 如果请求了修改角色
	if req.Role != "" {
		role := strings.TrimSpace(req.Role)
		if role != "user" && role != "guest" {
			jsonError(w, http.StatusBadRequest, "无效的用户角色")
			return
		}
		if err := h.db.UpdateUserRole(id, role); err != nil {
			slog.ErrorContext(r.Context(), "更新用户角色失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "更新用户角色失败")
			return
		}
	}

	slog.InfoContext(r.Context(), "用户更新", "type", "api", "admin_id", getUserFromContext(r).UserID, "user_id", id)
	jsonMsg(w, "更新成功")
}

// DeleteUser 删除用户
func (h *AdminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的用户 ID")
		return
	}

	if id == claims.UserID {
		jsonError(w, http.StatusBadRequest, "不能删除自己")
		return
	}

	if err := h.db.DeleteUser(id); err != nil {
		slog.ErrorContext(r.Context(), "删除用户失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "删除用户失败")
		return
	}

	// 清除该用户的通行密钥凭据（SSO 会话随用户级联删除）
	h.db.DeleteUserWebAuthnCredentials(id)
	slog.InfoContext(r.Context(), "用户删除", "type", "api", "admin_id", getUserFromContext(r).UserID, "user_id", id)

	jsonMsg(w, "删除成功")
}

// ResetPassword 管理员重置用户密码
func (h *AdminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的用户 ID")
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	if len(req.Password) < 6 {
		jsonError(w, http.StatusBadRequest, server.ErrPasswordTooShort)
		return
	}

	hash, err := password.HashPassword(req.Password)
	if err != nil {
		slog.ErrorContext(r.Context(), "密码加密失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "密码加密失败")
		return
	}

	if err := h.db.UpdatePassword(id, hash); err != nil {
		slog.ErrorContext(r.Context(), "重置密码失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "重置密码失败")
		return
	}

	slog.InfoContext(r.Context(), "密码重置", "type", "api", "admin_id", getUserFromContext(r).UserID, "user_id", id)
	jsonMsg(w, "密码已重置")
}

// UpdateSettings 更新站点设置（管理员专用）
func (h *AdminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SiteName           *string `json:"site_name"`
		AdminDomain        *string `json:"admin_domain"`
		ProxyDomain        *string `json:"proxy_domain"`
		SessionTTL         *string `json:"session_ttl"`
		SessionCookieMode  *string `json:"session_cookie_mode"`  // 管理端会话 Cookie 模式：persistent/session（#77）
		ProxyConfig        *string `json:"proxy_config"`         // 代理限制配置全局默认值（多行 key: value 文本，#47）
		ShowTempLogin      *bool   `json:"show_temp_login"`      // 登录页显示「临时登录」入口（#71，默认关）
		ShowQRLogin        *bool   `json:"show_qr_login"`        // 登录页显示「扫码登录」入口（#71，默认关）
		AuditRetentionDays *int    `json:"audit_retention_days"` // 会话保留期（天，0=永久，#80，默认 90）
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	if req.SiteName != nil {
		h.db.SetSetting("site_name", *req.SiteName)
	}
	if req.AdminDomain != nil {
		h.db.SetSetting("admin_domain", *req.AdminDomain)
	}
	if req.ProxyDomain != nil {
		h.db.SetSetting("proxy_domain", *req.ProxyDomain)
	}
	if req.SessionTTL != nil {
		h.db.SetSetting("session_ttl", *req.SessionTTL)
	}
	if req.SessionCookieMode != nil {
		// 白名单校验，防止无效值落库导致签发行为不可预期（#77）
		if *req.SessionCookieMode != server.SessionCookiePersistent && *req.SessionCookieMode != server.SessionCookieSession {
			jsonError(w, http.StatusBadRequest, "session_cookie_mode 必须为 persistent 或 session")
			return
		}
		h.db.SetSetting("session_cookie_mode", *req.SessionCookieMode)
	}
	if req.ShowTempLogin != nil {
		settingBool(h.db, "show_temp_login", *req.ShowTempLogin)
	}
	if req.ShowQRLogin != nil {
		settingBool(h.db, "show_qr_login", *req.ShowQRLogin)
	}
	if req.AuditRetentionDays != nil {
		if *req.AuditRetentionDays < 0 {
			jsonError(w, http.StatusBadRequest, "audit_retention_days 不能为负数")
			return
		}
		h.db.SetSetting("audit_retention_days", strconv.Itoa(*req.AuditRetentionDays))
	}
	if req.ProxyConfig != nil {
		// 校验通过才落库：key 是否注册、值是否符合类型，出错返回具体行（#47）
		if _, err := proxycfg.Parse(*req.ProxyConfig); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := h.db.SetSetting("proxy_config", *req.ProxyConfig); err != nil {
			slog.ErrorContext(r.Context(), "更新代理配置失败", "type", "api", "error", err)
			jsonError(w, http.StatusInternalServerError, "更新代理配置失败")
			return
		}
	}

	slog.InfoContext(r.Context(), "系统设置更新", "type", "api", "admin_id", getUserFromContext(r).UserID)
	jsonMsg(w, "设置已更新")
}

// settingBool 布尔设置项落库（"true"/"false" 字符串存储）
func settingBool(database *db.DB, key string, value bool) {
	v := "false"
	if value {
		v = "true"
	}
	database.SetSetting(key, v)
}

// ResetJWTSecret 重置 JWT 密钥（管理员专用）
func (h *AdminHandler) ResetJWTSecret(w http.ResponseWriter, r *http.Request) {
	newSecret := random.Hex(32)
	if err := h.db.SetSetting("jwt_secret", newSecret); err != nil {
		slog.ErrorContext(r.Context(), "JWT 密钥重置失败", "type", "api", "admin_id", getUserFromContext(r).UserID, "error", err)
		jsonError(w, http.StatusInternalServerError, "重置密钥失败")
		return
	}

	// 全部管理会话随密钥失效，会话记录同步标记（#80），避免残留"有效"的幽灵会话
	if err := h.db.MarkAllAuthSessionsRevoked(); err != nil {
		slog.ErrorContext(r.Context(), "标记登录会话失效失败", "type", "api", "error", err)
	}

	slog.WarnContext(r.Context(), "JWT 密钥重置", "type", "api", "admin_id", getUserFromContext(r).UserID)
	jsonMsg(w, "JWT 密钥已重置，所有会话已失效")
}
